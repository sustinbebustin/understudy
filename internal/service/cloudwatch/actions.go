package cloudwatch

import (
	"context"
	"log/slog"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch/alarm"
	"github.com/sustinbebustin/understudy/internal/serviceevent"
)

// cloudWatchPrincipal is the service principal alarm events are put as
// (cloudwatch-alarms.md 6.2).
const cloudWatchPrincipal = "cloudwatch.amazonaws.com"

// subjectOf is the alarm as payloads describe it.
func subjectOf(r alarmRef, a *Alarm) alarm.Subject {
	return alarm.Subject{
		ARN: r.arn(), Config: &a.Config, ActionsEnabled: a.ActionsEnabled,
		ConfigUpdated: a.ConfigUpdated.Wire(), MetricID: a.MetricID,
	}
}

// actionsOf is the action list a transition to v runs: OKActions,
// AlarmActions or InsufficientDataActions (cloudwatch-alarms.md 7.1).
func actionsOf(cfg *cloudwatchapi.PutMetricAlarmInput, v cloudwatchapi.StateValue) []string {
	switch v {
	case cloudwatchapi.StateValueOk:
		return cfg.OKActions
	case cloudwatchapi.StateValueAlarm:
		return cfg.AlarmActions
	case cloudwatchapi.StateValueInsufficientData:
		return cfg.InsufficientDataActions
	default:
		return nil
	}
}

// actionRuns are the actions of a transition the alarm a has made, in list
// order, when its actions are enabled (cloudwatch-alarms.md 7.1, 12.4),
// each an effect to apply in a write tx: SNS and Lambda actions are
// deliveries with no retry schedule, as CloudWatch "doesn't test or
// validate the actions" (cloudwatch-alarms.md 5.3), and every other kind
// is journaled as not run (cloudwatch-alarms.md 12.7). transition is the
// id of the call that made the change, which a FIFO deduplication id
// derives from.
func actionRuns(r alarmRef, a *Alarm, ch alarm.Change, transition string) []func(tx *kernel.Tx) {
	if !a.ActionsEnabled {
		return nil
	}
	subject := subjectOf(r, a)
	source := r.arn()
	var runs []func(tx *kernel.Tx)
	for _, arn := range actionsOf(&a.Config, ch.New.Value) {
		req := delivery.Request{Source: source, SourceAccount: r.scope.Account, Policy: delivery.None{}}
		switch act := alarm.ParseAction(arn).(type) {
		case alarm.SNSAction:
			topic := delivery.SNSTopic{ARN: act.Topic, Message: alarm.SNSMessage(subject, ch), Subject: alarm.SNSSubject(subject, ch.New.Value)}
			if strings.HasSuffix(act.Topic.Resource, ".fifo") {
				// cloudwatch.alarm.sns.fifo-group (cloudwatch-alarms.md 5.3)
				topic.GroupID = alarm.FIFOGroupID(source.String())
				topic.DedupID = alarm.FIFODedupID(source.String(), transition)
			}
			req.Mechanism, req.Target = delivery.CloudWatchToSNS, topic
			runs = append(runs, func(tx *kernel.Tx) { tx.Deliver(req) })
		case alarm.LambdaAction:
			req.Mechanism = delivery.CloudWatchToLambda
			req.Target = delivery.LambdaFunction{
				ARN: act.Function, Qualifier: act.Qualifier, Event: alarm.LambdaEvent(subject, ch), Invocation: delivery.InvocationEvent,
			}
			runs = append(runs, func(tx *kernel.Tx) { tx.Deliver(req) })
		case alarm.NotRunAction:
			adv := journal.Advisory{
				Kind: journal.AdvisoryAlarmActionNotRun, Resource: source,
				Text: act.Kind.String() + " action " + act.ARN + " is not run: " + notRunWhy(act.Kind),
			}
			runs = append(runs, func(tx *kernel.Tx) { tx.Record(r.scope, adv) })
		}
	}
	return runs
}

// notRunWhy says why an action kind understudy does not run is not run.
func notRunWhy(k alarm.NotRunKind) string {
	switch k {
	case alarm.NotRunEC2:
		return "understudy does not emulate EC2"
	case alarm.NotRunAutoScaling:
		return "understudy does not emulate EC2 Auto Scaling"
	case alarm.NotRunSystemsManager:
		return "understudy does not emulate Systems Manager OpsCenter or Incident Manager"
	case alarm.NotRunInvestigation:
		return "understudy does not emulate CloudWatch investigations"
	case alarm.NotRunUnrecognized:
		return "it is not an SNS topic or Lambda function in a valid account and Region, nor another documented action form (cloudwatch-alarms.md 7.2)"
	default:
		return "it is not an SNS topic or Lambda function in a valid account and Region, nor another documented action form (cloudwatch-alarms.md 7.2)"
	}
}

// alarmEvent is the EventBridge event an alarm change sends once its write
// has committed, put on the default bus of the alarm's scope as
// cloudwatch.amazonaws.com (cloudwatch-alarms.md 6.2).
func alarmEvent(r alarmRef, detailType, detail string) serviceevent.Event {
	return serviceevent.Event{
		Scope: r.scope, Principal: cloudWatchPrincipal, Via: ref.CloudWatchAlarm,
		Source: alarm.EventSource, DetailType: detailType, Detail: detail, Resources: []string{r.arn().String()},
	}
}

// stateChangeEvent is the "CloudWatch Alarm State Change" event of a
// transition, sent whatever ActionsEnabled is
// (cloudwatch.alarm.events.actions-disabled, CWA-14).
func stateChangeEvent(r alarmRef, a *Alarm, ch alarm.Change) serviceevent.Event {
	return alarmEvent(r, alarm.DetailTypeStateChange, alarm.StateChangeDetail(subjectOf(r, a), ch))
}

// configChangeEvent is the "CloudWatch Alarm Configuration Change" event
// of a PutMetricAlarm, DeleteAlarms, EnableAlarmActions or
// DisableAlarmActions; previous is the replaced alarm on an update and nil
// otherwise (cloudwatch-alarms.md 6.2, CWA-14).
func configChangeEvent(op alarm.ConfigOp, r alarmRef, a, previous *Alarm) serviceevent.Event {
	var prev *alarm.Subject
	if previous != nil {
		prev = new(subjectOf(r, previous))
	}
	state := alarm.StateRecord{Value: a.State.Value, Reason: a.State.Reason, Timestamp: a.State.Updated.Wire()}
	return alarmEvent(r, alarm.DetailTypeConfigChange, alarm.ConfigurationChangeDetail(op, subjectOf(r, a), prev, state))
}

// lostEvent warns of an alarm event that did not reach the default bus,
// for a kernel without EventBridge or a failed send; it is not retried.
func (s *Service) lostEvent(ctx context.Context, e *serviceevent.Event, why string) {
	s.k.Log().LogAttrs(ctx, slog.LevelWarn, "an alarm event did not reach the default event bus and is not sent again; the alarm's state and actions are unaffected",
		slog.String("alarm", strings.Join(e.Resources, ",")), slog.String("detail_type", e.DetailType), slog.String("error", why))
}
