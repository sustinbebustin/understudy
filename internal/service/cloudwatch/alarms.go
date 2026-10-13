package cloudwatch

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// DescribeAlarms' page size when MaxRecords is absent. Inferred: the
// DescribeAlarms documentation gives MaxRecords a range of 1 to 100 and no
// default.
const defaultMaxRecords = 50

// initialReason is the StateReason of a new alarm (AWS-recorded).
const initialReason = "Unchecked: Initial alarm creation"

// bindAlarms binds the metric alarm operations.
func (s *Service) bindAlarms(b cloudwatchapi.Binder) {
	b.PutMetricAlarm(s.putMetricAlarm,
		registry.Partial{Note: "stored and echoed; alarms are never evaluated, so their state changes only through SetAlarmState, and their actions never run"},
		kernel.WithCheck(checkPutMetricAlarm),
		alarmsNamed(func(in *cloudwatchapi.PutMetricAlarmInput) []string { return []string{in.AlarmName} }))
	b.DescribeAlarms(s.describeAlarms,
		registry.Partial{Note: "metric alarms only: composite and log alarms are never stored, so ChildrenOfAlarmName and ParentsOfAlarmName select nothing"},
		kernel.WithCheck(checkDescribeAlarms))
	b.DeleteAlarms(s.deleteAlarms, registry.Full{},
		alarmsNamed(func(in *cloudwatchapi.DeleteAlarmsInput) []string { return in.AlarmNames }))
	b.EnableAlarmActions(s.enableAlarmActions,
		registry.StoredOnly{Note: "ActionsEnabled is stored and echoed; alarm actions never run"},
		alarmsNamed(func(in *cloudwatchapi.EnableAlarmActionsInput) []string { return in.AlarmNames }))
	b.DisableAlarmActions(s.disableAlarmActions,
		registry.StoredOnly{Note: "ActionsEnabled is stored and echoed; alarm actions never run"},
		alarmsNamed(func(in *cloudwatchapi.DisableAlarmActionsInput) []string { return in.AlarmNames }))
	b.SetAlarmState(s.setAlarmState,
		registry.Partial{Note: "the state is set and echoed; no action runs and no evaluation follows"},
		kernel.WithValidator(validateSetAlarmState),
		kernel.WithCheck(checkSetAlarmState),
		alarmsNamed(func(in *cloudwatchapi.SetAlarmStateInput) []string { return []string{in.AlarmName} }))
}

// alarmRef locates an alarm: its scope and name.
type alarmRef struct {
	scope aws.Scope
	name  string
}

// arn is the alarm's ARN, arn:aws:cloudwatch:<region>:<account>:alarm:<name>
// (AWS-recorded).
func (a alarmRef) arn() aws.ARN {
	return aws.ARN{
		Partition: a.scope.Partition.String(),
		Service:   "cloudwatch",
		Region:    a.scope.Region.String(),
		Account:   a.scope.Account.String(),
		Resource:  "alarm:" + a.name,
	}
}

// named is the alarm a name names, in the caller's scope.
func named(c *kernel.Call, name string) alarmRef { return alarmRef{scope: c.Scope(), name: name} }

// alarmsNamed names the alarms an input addresses as the call's resources,
// before fault rules run.
func alarmsNamed[In any](namesOf func(*In) []string) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		names := namesOf(in)
		out := make([]aws.ARN, len(names))
		for i, n := range names {
			out[i] = named(c, n).arn()
		}
		return out
	})
}

// putMetricAlarm creates an alarm in its initial state or replaces the
// configuration of an existing one, which keeps its state and tags: AWS
// ignores Tags on an update.
func (s *Service) putMetricAlarm(_ context.Context, c *kernel.Call, in *cloudwatchapi.PutMetricAlarmInput) (*cloudwatchapi.PutMetricAlarmOutput, *awserr.Error) {
	cfg := *in
	cfg.Tags, cfg.ActionsEnabled = nil, nil
	enabled := in.ActionsEnabled == nil || *in.ActionsEnabled
	tags := tagMap(nil, in.Tags)
	initial := initialStateValue(in)
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *State, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		a, ok := st.Alarms[in.AlarmName]
		if !ok {
			a = Alarm{
				State: AlarmState{Value: initial, Reason: initialReason, Updated: now, Transitioned: now},
				Tags:  tags,
			}
		}
		a.Config, a.ActionsEnabled, a.ConfigUpdated = cfg, enabled, now
		st.Alarms[in.AlarmName] = a
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &cloudwatchapi.PutMetricAlarmOutput{}, nil
}

// initialStateValue is a new alarm's state: OK for a PromQL alarm and
// INSUFFICIENT_DATA for the others, as AWS documents. Its reason is
// initialReason either way (inferred for a PromQL alarm: no recording
// shows one).
func initialStateValue(in *cloudwatchapi.PutMetricAlarmInput) cloudwatchapi.StateValue {
	if kind, err := alarmKindOf(in); err == nil && kind == promQLAlarm {
		return cloudwatchapi.StateValueOk
	}
	return cloudwatchapi.StateValueInsufficientData
}

// describeAlarms lists the scope's metric alarms by name, one page at a
// time. Both lists are always present, empty when nothing matches, as AWS
// records; a name that matches no alarm is not an error.
func (s *Service) describeAlarms(_ context.Context, c *kernel.Call, in *cloudwatchapi.DescribeAlarmsInput) (*cloudwatchapi.DescribeAlarmsOutput, *awserr.Error) {
	out := &cloudwatchapi.DescribeAlarmsOutput{MetricAlarms: []cloudwatchapi.MetricAlarm{}, CompositeAlarms: []cloudwatchapi.CompositeAlarm{}}
	metric := len(in.AlarmTypes) == 0 || slices.Contains(in.AlarmTypes, cloudwatchapi.AlarmTypeMetricAlarm)
	if !metric || in.ChildrenOfAlarmName != nil || in.ParentsOfAlarmName != nil {
		return out, nil
	}
	scope := c.Scope()
	var alarms []cloudwatchapi.MetricAlarm
	if cell, ok := s.cells.Lookup(scope); ok {
		alarms = store.Read(cell, func(st *State) []cloudwatchapi.MetricAlarm {
			var sel []cloudwatchapi.MetricAlarm
			for _, name := range slices.Sorted(maps.Keys(st.Alarms)) {
				a := st.Alarms[name]
				if describes(in, name, &a) {
					sel = append(sel, renderAlarm(alarmRef{scope: scope, name: name}, &a))
				}
			}
			return sel
		})
	}
	limit := defaultMaxRecords
	if in.MaxRecords != nil {
		limit = int(*in.MaxRecords)
	}
	page, next, ok := pageOf(alarms, func(a cloudwatchapi.MetricAlarm) string { return *a.AlarmName }, in.NextToken, limit)
	if !ok {
		return nil, errInvalidNextToken()
	}
	out.MetricAlarms = append(out.MetricAlarms, page...)
	out.NextToken = next
	return out, nil
}

// describes reports whether DescribeAlarms' filters select the alarm.
func describes(in *cloudwatchapi.DescribeAlarmsInput, name string, a *Alarm) bool {
	switch {
	case len(in.AlarmNames) > 0 && !slices.Contains(in.AlarmNames, name):
		return false
	case in.AlarmNamePrefix != nil && !strings.HasPrefix(name, *in.AlarmNamePrefix):
		return false
	case in.StateValue != nil && *in.StateValue != a.State.Value:
		return false
	case in.ActionPrefix != nil:
		return slices.ContainsFunc(slices.Concat(a.Config.OKActions, a.Config.AlarmActions, a.Config.InsufficientDataActions),
			func(action string) bool { return strings.HasPrefix(action, *in.ActionPrefix) })
	}
	return true
}

// renderAlarm is the MetricAlarm DescribeAlarms returns: the configuration
// as sent, the three action lists and Dimensions always present (empty
// when unset), and the synthesized ARN, state and timestamps (AWS-recorded
// minimal alarm, secrets-logs-events-alarms.md 5.1). Records are never
// written in place, so the output may share their values.
func renderAlarm(ref alarmRef, a *Alarm) cloudwatchapi.MetricAlarm {
	cfg := &a.Config
	return cloudwatchapi.MetricAlarm{
		AlarmName:                          new(ref.name),
		AlarmArn:                           new(ref.arn().String()),
		AlarmDescription:                   cfg.AlarmDescription,
		AlarmConfigurationUpdatedTimestamp: new(a.ConfigUpdated.Wire()),
		ActionsEnabled:                     new(a.ActionsEnabled),
		OKActions:                          orEmpty(cfg.OKActions),
		AlarmActions:                       orEmpty(cfg.AlarmActions),
		InsufficientDataActions:            orEmpty(cfg.InsufficientDataActions),
		StateValue:                         new(a.State.Value),
		StateReason:                        new(a.State.Reason),
		StateReasonData:                    a.State.ReasonData,
		StateUpdatedTimestamp:              new(a.State.Updated.Wire()),
		StateTransitionedTimestamp:         new(a.State.Transitioned.Wire()),
		MetricName:                         cfg.MetricName,
		Namespace:                          cfg.Namespace,
		Statistic:                          cfg.Statistic,
		ExtendedStatistic:                  cfg.ExtendedStatistic,
		Dimensions:                         orEmpty(cfg.Dimensions),
		Period:                             cfg.Period,
		Unit:                               cfg.Unit,
		EvaluationPeriods:                  cfg.EvaluationPeriods,
		DatapointsToAlarm:                  cfg.DatapointsToAlarm,
		Threshold:                          cfg.Threshold,
		ComparisonOperator:                 cfg.ComparisonOperator,
		TreatMissingData:                   cfg.TreatMissingData,
		EvaluateLowSampleCountPercentile:   cfg.EvaluateLowSampleCountPercentile,
		Metrics:                            echoQueries(cfg.Metrics),
		ThresholdMetricId:                  cfg.ThresholdMetricId,
		EvaluationWindow:                   cfg.EvaluationWindow,
		WarmUpConfiguration:                cfg.WarmUpConfiguration,
		EvaluationCriteria:                 cfg.EvaluationCriteria,
		EvaluationInterval:                 cfg.EvaluationInterval,
	}
}

// orEmpty is a list AWS always sends: empty, never absent.
func orEmpty[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// echoQueries is an alarm's Metrics as sent, with empty lists read as
// absent: a snapshot stores an absent list as an empty one, and an echo
// must not change across a save and load.
func echoQueries(qs []cloudwatchapi.MetricDataQuery) []cloudwatchapi.MetricDataQuery {
	if len(qs) == 0 {
		return nil
	}
	out := slices.Clone(qs)
	for i := range out {
		if st := out[i].MetricStat; st != nil && len(st.Metric.Dimensions) == 0 {
			ms := *st
			ms.Metric.Dimensions = nil
			out[i].MetricStat = &ms
		}
	}
	return out
}

// deleteAlarms deletes the named alarms with their tags. Names that match
// no alarm are skipped, as AWS documents, so deleting a missing alarm
// succeeds (gap-G1.md 4).
func (s *Service) deleteAlarms(_ context.Context, c *kernel.Call, in *cloudwatchapi.DeleteAlarmsInput) (*cloudwatchapi.DeleteAlarmsOutput, *awserr.Error) {
	err := s.eachAlarm(c, in.AlarmNames, func(st *State, name string, _ Alarm) {
		delete(st.Alarms, name)
	})
	if err != nil {
		return nil, err
	}
	return &cloudwatchapi.DeleteAlarmsOutput{}, nil
}

func (s *Service) enableAlarmActions(_ context.Context, c *kernel.Call, in *cloudwatchapi.EnableAlarmActionsInput) (*cloudwatchapi.EnableAlarmActionsOutput, *awserr.Error) {
	if err := s.setActionsEnabled(c, in.AlarmNames, true); err != nil {
		return nil, err
	}
	return &cloudwatchapi.EnableAlarmActionsOutput{}, nil
}

func (s *Service) disableAlarmActions(_ context.Context, c *kernel.Call, in *cloudwatchapi.DisableAlarmActionsInput) (*cloudwatchapi.DisableAlarmActionsOutput, *awserr.Error) {
	if err := s.setActionsEnabled(c, in.AlarmNames, false); err != nil {
		return nil, err
	}
	return &cloudwatchapi.DisableAlarmActionsOutput{}, nil
}

// setActionsEnabled sets ActionsEnabled on the named alarms; names that
// match no alarm are skipped, as DeleteAlarms skips them (inferred: the
// documentation is silent).
func (s *Service) setActionsEnabled(c *kernel.Call, names []string, enabled bool) *awserr.Error {
	return s.eachAlarm(c, names, func(st *State, name string, a Alarm) {
		a.ActionsEnabled = enabled
		st.Alarms[name] = a
	})
}

// eachAlarm runs fn on every named alarm that exists, under the scope's
// lock.
func (s *Service) eachAlarm(c *kernel.Call, names []string, fn func(st *State, name string, a Alarm)) *awserr.Error {
	if _, ok := s.cells.Lookup(c.Scope()); !ok {
		return nil
	}
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		found := false
		for _, name := range names {
			if a, ok := st.Alarms[name]; ok {
				fn(st, name, a)
				found = true
			}
		}
		if !found {
			tx.Unchanged()
		}
		return struct{}{}, nil
	})
	return err
}

// setAlarmState sets an alarm's state and reason. Only a change of value
// moves StateTransitionedTimestamp.
func (s *Service) setAlarmState(_ context.Context, c *kernel.Call, in *cloudwatchapi.SetAlarmStateInput) (*cloudwatchapi.SetAlarmStateOutput, *awserr.Error) {
	if _, ok := s.cells.Lookup(c.Scope()); !ok {
		return nil, errAlarmNotFound()
	}
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *State, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		a, ok := st.Alarms[in.AlarmName]
		if !ok {
			return struct{}{}, errAlarmNotFound()
		}
		next := AlarmState{Value: in.StateValue, Reason: in.StateReason, ReasonData: in.StateReasonData, Updated: now, Transitioned: a.State.Transitioned}
		if in.StateValue != a.State.Value {
			next.Transitioned = now
		}
		a.State = next
		st.Alarms[in.AlarmName] = a
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &cloudwatchapi.SetAlarmStateOutput{}, nil
}
