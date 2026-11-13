package alarm

import (
	"bytes"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
)

// ConfigurationUpdate is the HistorySummary and HistoryData of the
// ConfigurationUpdate history item of a create, update or delete:
// `Alarm "<name>" created` with {"version","type":"Create","createdAlarm"},
// `... updated` with {"version","type":"Update","updatedAlarm",
// "originalUpdatedFields"} and `... deleted` with
// {"version","type":"Delete","deletedAlarm"} (cloudwatch-alarms.md 8.3;
// CWA-14, CWA-16). s is the alarm after the change, or as it was when
// deleted; previous is the replaced alarm on an update and nil otherwise;
// state is the alarm's state, which a configuration change keeps.
//
// The model caps HistoryData at MaxHistoryData characters; how AWS fits an
// alarm object past it is unrecorded, and understudy writes it whole.
func ConfigurationUpdate(op ConfigOp, s Subject, previous *Subject, state StateRecord) (summary, data string) {
	summary = `Alarm "` + s.Config.AlarmName + `" `
	b := []byte(`{"version":"1.0","type":`)
	switch op {
	case ConfigCreate:
		summary += "created"
		b = append(b, `"Create","createdAlarm":`...)
	case ConfigUpdate:
		summary += "updated"
		b = append(b, `"Update","updatedAlarm":`...)
	case ConfigDelete:
		summary += "deleted"
		b = append(b, `"Delete","deletedAlarm":`...)
	}
	b = appendAlarmObject(b, s, state)
	if op == ConfigUpdate && previous != nil {
		b = append(b, `,"originalUpdatedFields":`...)
		b = appendOriginalFields(b, s, *previous, state)
	}
	return summary, string(append(b, '}'))
}

// alarmMember is one member of a history item's alarm object: its key and
// its JSON value, absent (ok false) when the configuration leaves it unset.
type alarmMember struct {
	key   string
	value func(s Subject, state StateRecord) (v []byte, ok bool)
}

// alarmMembers returns the alarm object's members in AWS's order:
// alarmName, alarmArn, alarmConfigurationUpdatedTimestamp, namespace,
// metricName, extendedStatistic, period, dimensions, threshold,
// comparisonOperator, evaluationPeriods, treatMissingData, stateValue,
// stateUpdatedTimestamp, actionsEnabled, alarmActions,
// insufficientDataActions, okactions (sic), a standard statistic printed
// upper-case as extendedStatistic (cloudwatch-alarms.md 8.3; CWA-14,
// CWA-16).
//
// Inferred: no recording sets alarmDescription, unit, datapointsToAlarm,
// evaluateLowSampleCountPercentile or thresholdMetricId, or leaves a
// recorded optional member unset. Each optional member appears only when
// set, the unrecorded ones each after its predecessor in the MetricAlarm
// shape. Dimensions print as {"name","value"} objects in the order sent,
// a shape no recording shows. Metrics, EvaluationCriteria,
// EvaluationWindow, EvaluationInterval and WarmUpConfiguration, whose
// forms no recording shows, are left out.
func alarmMembers() []alarmMember {
	return []alarmMember{
		{"alarmName", func(s Subject, _ StateRecord) ([]byte, bool) {
			return jsonx.AppendString(nil, s.Config.AlarmName), true
		}},
		{"alarmArn", func(s Subject, _ StateRecord) ([]byte, bool) { return jsonx.AppendString(nil, s.ARN.String()), true }},
		{"alarmDescription", func(s Subject, _ StateRecord) ([]byte, bool) { return optString(s.Config.AlarmDescription) }},
		{"alarmConfigurationUpdatedTimestamp", func(s Subject, _ StateRecord) ([]byte, bool) {
			return jsonx.AppendString(nil, FormatTimestamp(s.ConfigUpdated)), true
		}},
		{"namespace", func(s Subject, _ StateRecord) ([]byte, bool) { return optString(s.Config.Namespace) }},
		{"metricName", func(s Subject, _ StateRecord) ([]byte, bool) { return optString(s.Config.MetricName) }},
		{"extendedStatistic", func(s Subject, _ StateRecord) ([]byte, bool) {
			if c := s.Config; c.Statistic != nil {
				return jsonx.AppendString(nil, upperSnake(string(*c.Statistic))), true
			}
			return optString(s.Config.ExtendedStatistic)
		}},
		{"period", func(s Subject, _ StateRecord) ([]byte, bool) { return optInt(s.Config.Period) }},
		{"unit", func(s Subject, _ StateRecord) ([]byte, bool) {
			if s.Config.Unit == nil {
				return nil, false
			}
			return jsonx.AppendString(nil, string(*s.Config.Unit)), true
		}},
		{"dimensions", func(s Subject, _ StateRecord) ([]byte, bool) {
			b := []byte{'['}
			for i, d := range s.Config.Dimensions {
				if i > 0 {
					b = append(b, ',')
				}
				b = append(b, `{"name":`...)
				b = jsonx.AppendString(b, d.Name)
				b = append(b, `,"value":`...)
				b = jsonx.AppendString(b, d.Value)
				b = append(b, '}')
			}
			return append(b, ']'), true
		}},
		{"threshold", func(s Subject, _ StateRecord) ([]byte, bool) {
			if s.Config.Threshold == nil {
				return nil, false
			}
			return []byte(FormatNumber(*s.Config.Threshold)), true
		}},
		{"comparisonOperator", func(s Subject, _ StateRecord) ([]byte, bool) {
			if s.Config.ComparisonOperator == nil {
				return nil, false
			}
			return jsonx.AppendString(nil, string(*s.Config.ComparisonOperator)), true
		}},
		{"evaluationPeriods", func(s Subject, _ StateRecord) ([]byte, bool) { return optInt(s.Config.EvaluationPeriods) }},
		{"datapointsToAlarm", func(s Subject, _ StateRecord) ([]byte, bool) { return optInt(s.Config.DatapointsToAlarm) }},
		{"treatMissingData", func(s Subject, _ StateRecord) ([]byte, bool) { return optString(s.Config.TreatMissingData) }},
		{"evaluateLowSampleCountPercentile", func(s Subject, _ StateRecord) ([]byte, bool) {
			return optString(s.Config.EvaluateLowSampleCountPercentile)
		}},
		{"thresholdMetricId", func(s Subject, _ StateRecord) ([]byte, bool) { return optString(s.Config.ThresholdMetricId) }},
		{"stateValue", func(_ Subject, st StateRecord) ([]byte, bool) { return jsonx.AppendString(nil, string(st.Value)), true }},
		{"stateUpdatedTimestamp", func(_ Subject, st StateRecord) ([]byte, bool) {
			return jsonx.AppendString(nil, FormatTimestamp(st.Timestamp)), true
		}},
		{"actionsEnabled", func(s Subject, _ StateRecord) ([]byte, bool) { return strconv.AppendBool(nil, s.ActionsEnabled), true }},
		{"alarmActions", func(s Subject, _ StateRecord) ([]byte, bool) { return appendStrings(nil, s.Config.AlarmActions), true }},
		{"insufficientDataActions", func(s Subject, _ StateRecord) ([]byte, bool) {
			return appendStrings(nil, s.Config.InsufficientDataActions), true
		}},
		{"okactions", func(s Subject, _ StateRecord) ([]byte, bool) { return appendStrings(nil, s.Config.OKActions), true }},
	}
}

// appendAlarmObject appends the alarm object of a ConfigurationUpdate
// item: every member alarmMembers lists that s sets.
func appendAlarmObject(b []byte, s Subject, state StateRecord) []byte {
	b = append(b, '{')
	first := true
	for _, m := range alarmMembers() {
		if v, ok := m.value(s, state); ok {
			b = appendMember(b, m.key, v, first)
			first = false
		}
	}
	return append(b, '}')
}

// appendOriginalFields appends an update's originalUpdatedFields: the
// members whose value changed, with their old values in alarmMembers'
// order, alarmConfigurationUpdatedTimestamp among them (cloudwatch-alarms.md
// 8.3; CWA-14, CWA-16). A member the update sets that was unset before
// reads null (inferred: no recording adds or removes a member).
func appendOriginalFields(b []byte, s, previous Subject, state StateRecord) []byte {
	b = append(b, '{')
	first := true
	for _, m := range alarmMembers() {
		now, set := m.value(s, state)
		was, wasSet := m.value(previous, state)
		if set == wasSet && bytes.Equal(now, was) {
			continue
		}
		if !wasSet {
			was = []byte("null")
		}
		b = appendMember(b, m.key, was, first)
		first = false
	}
	return append(b, '}')
}

// appendMember appends "key":value, after a comma unless first.
func appendMember(b []byte, key string, value []byte, first bool) []byte {
	if !first {
		b = append(b, ',')
	}
	b = jsonx.AppendString(b, key)
	b = append(b, ':')
	return append(b, value...)
}

// optString is s as a JSON string, absent when nil.
func optString(s *string) ([]byte, bool) {
	if s == nil {
		return nil, false
	}
	return jsonx.AppendString(nil, *s), true
}

// optInt is n as a JSON number, absent when nil.
func optInt(n *int32) ([]byte, bool) {
	if n == nil {
		return nil, false
	}
	return strconv.AppendInt(nil, int64(*n), 10), true
}
