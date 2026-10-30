package cloudwatch

import (
	"context"
	"log/slog"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch/alarm"
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

// runActions runs, in the write tx, the actions of a transition the alarm
// a has made, in list order, when its actions are enabled
// (cloudwatch-alarms.md 7.1, 12.4): SNS and Lambda actions are deliveries
// with no retry schedule, as CloudWatch "doesn't test or validate the
// actions" (cloudwatch-alarms.md 5.3), and every other kind is journaled
// as not run (cloudwatch-alarms.md 12.7). A kernel built without a
// delivery sink, as unit harnesses are, cannot deliver: the transition
// stands and the SNS and Lambda actions are logged as not delivered.
func (s *Service) runActions(tx *kernel.Tx, r alarmRef, a *Alarm, ch alarm.Change) {
	if !a.ActionsEnabled {
		return
	}
	deliver := func(req delivery.Request) {
		if s.k.Delivers() {
			tx.Deliver(req)
			return
		}
		s.k.Log().Warn("an alarm action is not delivered: this kernel has no delivery sink",
			slog.String("alarm", req.Source.String()), slog.String("mechanism", req.Mechanism.String()))
	}
	subject := subjectOf(r, a)
	source := r.arn()
	for _, arn := range actionsOf(&a.Config, ch.New.Value) {
		req := delivery.Request{Source: source, SourceAccount: r.scope.Account, Policy: delivery.None{}}
		switch act := alarm.ParseAction(arn).(type) {
		case alarm.SNSAction:
			topic := delivery.SNSTopic{ARN: act.Topic, Message: alarm.SNSMessage(subject, ch), Subject: alarm.SNSSubject(subject, ch.New.Value)}
			if strings.HasSuffix(act.Topic.Resource, ".fifo") {
				// cloudwatch.alarm.sns.fifo-group (cloudwatch-alarms.md 5.3)
				topic.GroupID = alarm.FIFOGroupID(source.String())
				topic.DedupID = alarm.FIFODedupID(source.String(), ch.New.Timestamp)
			}
			req.Mechanism, req.Target = delivery.CloudWatchToSNS, topic
			deliver(req)
		case alarm.LambdaAction:
			req.Mechanism = delivery.CloudWatchToLambda
			req.Target = delivery.LambdaFunction{
				ARN: act.Function, Qualifier: act.Qualifier, Event: alarm.LambdaEvent(subject, ch), Invocation: delivery.InvocationEvent,
			}
			deliver(req)
		case alarm.NotRunAction:
			tx.Record(r.scope, journal.Advisory{
				Kind: journal.AdvisoryAlarmActionNotRun, Resource: source,
				Text: act.Kind.String() + " action " + act.ARN + " is not run: understudy does not emulate " + notRunService(act.Kind),
			})
		}
	}
}

// notRunService names what an action kind understudy does not run would
// need.
func notRunService(k alarm.NotRunKind) string {
	switch k {
	case alarm.NotRunEC2:
		return "EC2"
	case alarm.NotRunAutoScaling:
		return "EC2 Auto Scaling"
	case alarm.NotRunSystemsManager:
		return "Systems Manager OpsCenter or Incident Manager"
	case alarm.NotRunInvestigation:
		return "CloudWatch investigations"
	case alarm.NotRunUnrecognized:
		return "the service the ARN names"
	default:
		return "the service the ARN names"
	}
}

// alarmEvent is an EventBridge event an alarm change sends once its write
// has committed (cloudwatch-alarms.md 6.2).
type alarmEvent struct {
	scope      aws.Scope
	alarm      aws.ARN
	detailType string
	detail     string
}

// stateChangeEvent is the "CloudWatch Alarm State Change" event of a
// transition, sent whatever ActionsEnabled is
// (cloudwatch.alarm.events.actions-disabled, CWA-14).
func stateChangeEvent(r alarmRef, a *Alarm, ch alarm.Change) alarmEvent {
	return alarmEvent{scope: r.scope, alarm: r.arn(), detailType: alarm.DetailTypeStateChange, detail: alarm.StateChangeDetail(subjectOf(r, a), ch)}
}

// configChangeEvent is the "CloudWatch Alarm Configuration Change" event
// of a PutMetricAlarm or DeleteAlarms; previous is the replaced alarm on
// an update and nil otherwise (cloudwatch-alarms.md 6.2).
func configChangeEvent(op alarm.ConfigOp, r alarmRef, a, previous *Alarm) alarmEvent {
	var prev *alarm.Subject
	if previous != nil {
		prev = new(subjectOf(r, previous))
	}
	state := alarm.StateRecord{Value: a.State.Value, Timestamp: a.State.Updated.Wire()}
	return alarmEvent{
		scope: r.scope, alarm: r.arn(), detailType: alarm.DetailTypeConfigChange,
		detail: alarm.ConfigurationChangeDetail(op, subjectOf(r, a), prev, state),
	}
}

// sendEvents puts events on the default bus of their alarm's scope as
// cloudwatch.amazonaws.com, through EventBridge's internal operation, on
// behalf of parent and admitted at (zero: parent's admission). It must
// run after the write that made the change, which holds no lock then. A
// kernel without EventBridge skips them; a failure is logged and not
// retried (contract 3.6).
func (s *Service) sendEvents(ctx context.Context, parent *kernel.Call, at clock.Instant, events []alarmEvent) {
	if len(events) == 0 {
		return
	}
	if _, bound := s.k.Bindings().Status(eventbridgeapi.OpPutServiceEventInternal.Descriptor()).(kernel.Bound); !bound {
		return
	}
	for i := range events {
		e := &events[i]
		t := kernel.Target{Scope: e.scope, As: kernel.AsPlatform{Name: cloudWatchPrincipal}, Via: ref.CloudWatchAlarm, At: at}
		in := &eventbridgeapi.PutServiceEventInternalInput{Source: alarm.EventSource, DetailType: e.detailType, Detail: e.detail, Resources: []string{e.alarm.String()}}
		if _, err := kernel.Invoke(ctx, s.k, parent, eventbridgeapi.OpPutServiceEventInternal, t, in); err != nil {
			s.k.Log().LogAttrs(ctx, slog.LevelWarn, "sending an alarm event to the default event bus failed; the alarm's state and actions are unaffected and the event is not sent again",
				slog.String("alarm", e.alarm.String()), slog.String("detail_type", e.detailType), slog.String("error", err.Message))
		}
	}
}
