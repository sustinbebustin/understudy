package alarm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
)

// The EventBridge source and detail types of alarm events
// (cloudwatch-alarms.md 6.2).
const (
	EventSource            = "aws.cloudwatch"
	DetailTypeStateChange  = "CloudWatch Alarm State Change"
	DetailTypeConfigChange = "CloudWatch Alarm Configuration Change"
)

// Subject is the alarm a payload describes.
type Subject struct {
	// ARN is arn:<partition>:cloudwatch:<region>:<account>:alarm:<name>;
	// payloads read the account and region from it.
	ARN aws.ARN
	// Config is the stored configuration (cloudwatch.Alarm.Config).
	Config         *cloudwatchapi.PutMetricAlarmInput
	ActionsEnabled bool
	ConfigUpdated  time.Time
	// MetricID is the generated id of the configuration's one metric in
	// Lambda and EventBridge payloads, new on each PutMetricAlarm
	// (cloudwatch.alarm.events.metric-id).
	MetricID string
}

// StateRecord is one side of a state change.
type StateRecord struct {
	Value      cloudwatchapi.StateValue
	Reason     string
	ReasonData *string
	Timestamp  time.Time
}

// Change is one state change, from Old to New.
type Change struct {
	Old, New StateRecord
}

// SNSSubject is the notification's Subject: <NEW STATE>: "<name>" in
// <region long name>, at most 100 characters, the name cut and followed by
// "..." inside the quotes when longer (cloudwatch.alarm.sns.subject;
// cloudwatch-alarms.md 5.2).
func SNSSubject(s Subject, to cloudwatchapi.StateValue) string {
	const maxSubject = 100
	region := regionLabel(s.ARN.Region)
	name := s.Config.AlarmName
	subject := func(name string) string { return string(to) + `: "` + name + `" in ` + region }
	full := subject(name)
	if len(full) <= maxSubject {
		return full
	}
	return subject(cut(name, len(full)-maxSubject+len("...")) + "...")
}

// SNSMessage is the notification body JSON in the schema's field order
// (cloudwatch.alarm.sns.unset-fields; cloudwatch-alarms.md 5.1). It
// carries no reason data (cloudwatch-alarms.md 11). The Trigger is the
// single-metric form; for an alarm on Metrics or an ExtendedStatistic its
// metric fields print null, a shape no source records (CWA-11).
func SNSMessage(s Subject, ch Change) string {
	c := s.Config
	b := []byte(`{"AlarmName":`)
	b = jsonx.AppendString(b, c.AlarmName)
	b = append(b, `,"AlarmDescription":`...)
	b = appendOptString(b, c.AlarmDescription)
	b = append(b, `,"AWSAccountId":`...)
	b = jsonx.AppendString(b, s.ARN.Account)
	b = append(b, `,"AlarmConfigurationUpdatedTimestamp":`...)
	b = jsonx.AppendString(b, FormatTimestamp(s.ConfigUpdated))
	b = append(b, `,"NewStateValue":`...)
	b = jsonx.AppendString(b, string(ch.New.Value))
	b = append(b, `,"NewStateReason":`...)
	b = jsonx.AppendString(b, ch.New.Reason)
	b = append(b, `,"StateChangeTime":`...)
	b = jsonx.AppendString(b, FormatTimestamp(ch.New.Timestamp))
	b = append(b, `,"Region":`...)
	b = jsonx.AppendString(b, regionLabel(s.ARN.Region))
	b = append(b, `,"AlarmArn":`...)
	b = jsonx.AppendString(b, s.ARN.String())
	b = append(b, `,"OldStateValue":`...)
	b = jsonx.AppendString(b, string(ch.Old.Value))
	b = append(b, `,"OKActions":`...)
	b = appendStrings(b, c.OKActions)
	b = append(b, `,"AlarmActions":`...)
	b = appendStrings(b, c.AlarmActions)
	b = append(b, `,"InsufficientDataActions":`...)
	b = appendStrings(b, c.InsufficientDataActions)
	b = append(b, `,"Trigger":`...)
	b = appendTrigger(b, c)
	b = append(b, `,"ActionExecutedAfterMuteWindow":false}`...)
	return string(b)
}

// appendTrigger appends the notification's Trigger object
// (cloudwatch-alarms.md 5.1).
func appendTrigger(b []byte, c *cloudwatchapi.PutMetricAlarmInput) []byte {
	b = append(b, `{"MetricName":`...)
	b = appendOptString(b, c.MetricName)
	b = append(b, `,"Namespace":`...)
	b = appendOptString(b, c.Namespace)
	b = append(b, `,"StatisticType":"Statistic","Statistic":`...)
	if c.Statistic != nil {
		b = jsonx.AppendString(b, upperSnake(string(*c.Statistic)))
	} else {
		b = append(b, "null"...)
	}
	b = append(b, `,"Unit":`...)
	if c.Unit != nil {
		b = jsonx.AppendString(b, string(*c.Unit))
	} else {
		b = append(b, "null"...)
	}
	b = append(b, `,"Dimensions":[`...)
	for i, d := range c.Dimensions {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"value":`...)
		b = jsonx.AppendString(b, d.Value)
		b = append(b, `,"name":`...)
		b = jsonx.AppendString(b, d.Name)
		b = append(b, '}')
	}
	b = append(b, `],"Period":`...)
	b = appendOptInt(b, c.Period)
	b = append(b, `,"EvaluationPeriods":`...)
	b = appendOptInt(b, c.EvaluationPeriods)
	if c.DatapointsToAlarm != nil {
		b = append(b, `,"DatapointsToAlarm":`...)
		b = appendOptInt(b, c.DatapointsToAlarm)
	}
	b = append(b, `,"ComparisonOperator":`...)
	if c.ComparisonOperator != nil {
		b = jsonx.AppendString(b, string(*c.ComparisonOperator))
	} else {
		b = append(b, "null"...)
	}
	b = append(b, `,"Threshold":`...)
	if c.Threshold != nil {
		b = append(b, FormatNumber(*c.Threshold)...)
	} else {
		b = append(b, "null"...)
	}
	b = append(b, `,"TreatMissingData":`...)
	b = jsonx.AppendString(b, deref(c.TreatMissingData))
	b = append(b, `,"EvaluateLowSampleCountPercentile":`...)
	b = jsonx.AppendString(b, deref(c.EvaluateLowSampleCountPercentile))
	return append(b, '}')
}

// regionLabel is the region's long name, or the code when the table does
// not hold it (cloudwatch.alarm.sns.subject).
func regionLabel(code string) string {
	name, _ := RegionName(code)
	return name
}

// RegionName is a region code's long name as the notification Subject and
// Region carry it, such as "US East (N. Virginia)" for us-east-1; ok is
// false for a code the table does not hold (cloudwatch-alarms.md 5.2).
// The names are the General Reference's
// (https://docs.aws.amazon.com/general/latest/gr/rande.html).
func RegionName(code string) (name string, ok bool) {
	name, ok = map[string]string{
		"us-east-1":      "US East (N. Virginia)",
		"us-east-2":      "US East (Ohio)",
		"us-west-1":      "US West (N. California)",
		"us-west-2":      "US West (Oregon)",
		"af-south-1":     "Africa (Cape Town)",
		"ap-east-1":      "Asia Pacific (Hong Kong)",
		"ap-south-1":     "Asia Pacific (Mumbai)",
		"ap-south-2":     "Asia Pacific (Hyderabad)",
		"ap-southeast-1": "Asia Pacific (Singapore)",
		"ap-southeast-2": "Asia Pacific (Sydney)",
		"ap-southeast-3": "Asia Pacific (Jakarta)",
		"ap-southeast-4": "Asia Pacific (Melbourne)",
		"ap-northeast-1": "Asia Pacific (Tokyo)",
		"ap-northeast-2": "Asia Pacific (Seoul)",
		"ap-northeast-3": "Asia Pacific (Osaka)",
		"ca-central-1":   "Canada (Central)",
		"eu-central-1":   "Europe (Frankfurt)",
		"eu-central-2":   "Europe (Zurich)",
		"eu-west-1":      "Europe (Ireland)",
		"eu-west-2":      "Europe (London)",
		"eu-west-3":      "Europe (Paris)",
		"eu-south-1":     "Europe (Milan)",
		"eu-south-2":     "Europe (Spain)",
		"eu-north-1":     "Europe (Stockholm)",
		"il-central-1":   "Israel (Tel Aviv)",
		"me-south-1":     "Middle East (Bahrain)",
		"me-central-1":   "Middle East (UAE)",
		"sa-east-1":      "South America (São Paulo)",
	}[code]
	if !ok {
		return code, false
	}
	return name, true
}

// FIFOGroupID is the MessageGroupId of a notification to a FIFO topic:
// lower-case hex SHA-256 of the alarm ARN (cloudwatch.alarm.sns.fifo-group;
// cloudwatch-alarms.md 5.3).
func FIFOGroupID(alarmARN string) string {
	sum := sha256.Sum256([]byte(alarmARN))
	return hex.EncodeToString(sum[:])
}

// FIFODedupID is the MessageDeduplicationId of a notification to a FIFO
// topic: lower-case hex SHA-256 of the alarm ARN followed by the
// StateChangeTime as FormatTimestamp prints it
// (cloudwatch.alarm.sns.fifo-group; cloudwatch-alarms.md 5.3).
func FIFODedupID(alarmARN string, changeTime time.Time) string {
	return FIFOGroupID(alarmARN + FormatTimestamp(changeTime))
}

// MetricID is the generated id of the metric of an alarm configured at
// configured, replacing the configuration whose id was previous ("" for a
// new alarm): UUID-shaped, as AWS's examples show it, and new for each
// configuration (cloudwatch.alarm.events.metric-id; cloudwatch-alarms.md
// 6.2). AWS's derivation is unknown; understudy hashes the alarm ARN, the
// configuration instant and the previous id, so the id is reproducible
// without drawing from a seeded id stream and differs from the previous
// one even for two updates at one instant.
func MetricID(alarmARN string, configured time.Time, previous string) string {
	sum := sha256.Sum256([]byte(alarmARN + "\n" + configured.UTC().Format(time.RFC3339Nano) + "\n" + previous))
	h := hex.EncodeToString(sum[:16])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// LambdaEvent is the Lambda action's event JSON (cloudwatch-alarms.md 6.1).
func LambdaEvent(s Subject, ch Change) []byte {
	b := []byte(`{"source":`)
	b = jsonx.AppendString(b, EventSource)
	b = append(b, `,"alarmArn":`...)
	b = jsonx.AppendString(b, s.ARN.String())
	b = append(b, `,"accountId":`...)
	b = jsonx.AppendString(b, s.ARN.Account)
	b = append(b, `,"time":`...)
	b = jsonx.AppendString(b, FormatTimestamp(ch.New.Timestamp))
	b = append(b, `,"region":`...)
	b = jsonx.AppendString(b, s.ARN.Region)
	b = append(b, `,"alarmData":{"alarmName":`...)
	b = jsonx.AppendString(b, s.Config.AlarmName)
	b = append(b, `,"state":`...)
	b = appendState(b, ch.New)
	b = append(b, `,"previousState":`...)
	b = appendState(b, ch.Old)
	b = append(b, `,"configuration":`...)
	b = appendStateConfiguration(b, s)
	return append(b, "}}"...)
}

// StateChangeDetail is the detail JSON of a "CloudWatch Alarm State
// Change" event (cloudwatch-alarms.md 6.2). It never carries muteDetail,
// since mute rules are unbound, nor alarmTags, since tag propagation is
// not emulated.
func StateChangeDetail(s Subject, ch Change) string {
	b := []byte(`{"alarmName":`)
	b = jsonx.AppendString(b, s.Config.AlarmName)
	b = append(b, `,"configuration":`...)
	b = appendStateConfiguration(b, s)
	b = append(b, `,"previousState":`...)
	b = appendState(b, ch.Old)
	b = append(b, `,"state":`...)
	b = appendState(b, ch.New)
	return string(append(b, '}'))
}

// appendState appends a state as the Lambda event and the state change
// detail carry it: reasonData is the StateReasonData string, present only
// with one (cloudwatch-alarms.md 6.1, 6.2).
func appendState(b []byte, r StateRecord) []byte {
	b = append(b, `{"value":`...)
	b = jsonx.AppendString(b, string(r.Value))
	b = append(b, `,"reason":`...)
	b = jsonx.AppendString(b, r.Reason)
	if r.ReasonData != nil {
		b = append(b, `,"reasonData":`...)
		b = jsonx.AppendString(b, *r.ReasonData)
	}
	b = append(b, `,"timestamp":`...)
	b = jsonx.AppendString(b, FormatTimestamp(r.Timestamp))
	return append(b, '}')
}

// appendStateConfiguration appends the {description?, metrics} object of
// the Lambda event and the state change detail; description is absent
// without an AlarmDescription (cloudwatch.alarm.events.metric-id, CWA-15).
func appendStateConfiguration(b []byte, s Subject) []byte {
	b = append(b, '{')
	if s.Config.AlarmDescription != nil {
		b = append(b, `"description":`...)
		b = jsonx.AppendString(b, *s.Config.AlarmDescription)
		b = append(b, ',')
	}
	b = append(b, `"metrics":`...)
	b = appendMetrics(b, s)
	return append(b, '}')
}

// appendMetrics appends the configuration's metrics array: a single-metric
// alarm is one element with the generated MetricID (cloudwatch-alarms.md
// 6.1); an alarm on Metrics lists its queries by their own ids.
func appendMetrics(b []byte, s Subject) []byte {
	c := s.Config
	if len(c.Metrics) > 0 {
		b = append(b, '[')
		for i, q := range c.Metrics {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendQuery(b, q)
		}
		return append(b, ']')
	}
	b = append(b, `[{"id":`...)
	b = jsonx.AppendString(b, s.MetricID)
	b = append(b, `,"metricStat":`...)
	b = appendMetricStat(b, deref(c.Namespace), deref(c.MetricName), c.Dimensions, c.Period, statName(c), c.Unit)
	return append(b, `,"returnData":true}]`...)
}

// statName is the alarm's statistic as a metricStat's stat: the Statistic,
// else the ExtendedStatistic.
func statName(c *cloudwatchapi.PutMetricAlarmInput) string {
	if c.Statistic != nil {
		return string(*c.Statistic)
	}
	return deref(c.ExtendedStatistic)
}

// appendQuery appends one MetricDataQuery of an alarm on Metrics.
func appendQuery(b []byte, q cloudwatchapi.MetricDataQuery) []byte {
	b = append(b, `{"id":`...)
	b = jsonx.AppendString(b, q.Id)
	if q.Expression != nil {
		b = append(b, `,"expression":`...)
		b = jsonx.AppendString(b, *q.Expression)
	}
	if q.Label != nil {
		b = append(b, `,"label":`...)
		b = jsonx.AppendString(b, *q.Label)
	}
	if m := q.MetricStat; m != nil {
		b = append(b, `,"metricStat":`...)
		b = appendMetricStat(b, deref(m.Metric.Namespace), deref(m.Metric.MetricName), m.Metric.Dimensions, &m.Period, m.Stat, m.Unit)
	}
	b = append(b, `,"returnData":`...)
	b = strconv.AppendBool(b, q.ReturnData == nil || *q.ReturnData)
	return append(b, '}')
}

// appendMetricStat appends {metric: {namespace, name, dimensions}, period,
// stat, unit?}, dimensions as a name-to-value object.
func appendMetricStat(b []byte, namespace, name string, dims []cloudwatchapi.Dimension, period *int32, stat string, unit *cloudwatchapi.StandardUnit) []byte {
	b = append(b, `{"metric":{"namespace":`...)
	b = jsonx.AppendString(b, namespace)
	b = append(b, `,"name":`...)
	b = jsonx.AppendString(b, name)
	b = append(b, `,"dimensions":{`...)
	for i, d := range dims {
		if i > 0 {
			b = append(b, ',')
		}
		b = jsonx.AppendString(b, d.Name)
		b = append(b, ':')
		b = jsonx.AppendString(b, d.Value)
	}
	b = append(b, `}},"period":`...)
	b = appendOptInt(b, period)
	b = append(b, `,"stat":`...)
	b = jsonx.AppendString(b, stat)
	if unit != nil {
		b = append(b, `,"unit":`...)
		b = jsonx.AppendString(b, string(*unit))
	}
	return append(b, '}')
}

// ConfigOp is the operation of a configuration change event.
type ConfigOp uint8

// ConfigOp values.
const (
	ConfigCreate ConfigOp = iota + 1
	ConfigUpdate
	ConfigDelete
)

// String is the detail's operation: "create", "update" or "delete".
func (op ConfigOp) String() string {
	switch op {
	case ConfigCreate:
		return "create"
	case ConfigUpdate:
		return "update"
	case ConfigDelete:
		return "delete"
	default:
		return ""
	}
}

// ConfigurationChangeDetail is the detail JSON of a "CloudWatch Alarm
// Configuration Change" event: previous is the replaced configuration on
// an update and nil otherwise; state is the alarm's state at the change
// (cloudwatch.alarm.events.metric-id; cloudwatch-alarms.md 6.2). Create and
// delete carry the update example's configuration fields (CWA-15).
func ConfigurationChangeDetail(op ConfigOp, s Subject, previous *Subject, state StateRecord) string {
	b := []byte(`{"alarmName":`)
	b = jsonx.AppendString(b, s.Config.AlarmName)
	b = append(b, `,"operation":`...)
	b = jsonx.AppendString(b, op.String())
	b = append(b, `,"state":{"value":`...)
	b = jsonx.AppendString(b, string(state.Value))
	b = append(b, `,"timestamp":`...)
	b = jsonx.AppendString(b, FormatTimestamp(state.Timestamp))
	b = append(b, `},"configuration":`...)
	b = appendConfiguration(b, s)
	if op == ConfigUpdate && previous != nil {
		b = append(b, `,"previousConfiguration":`...)
		b = appendConfiguration(b, *previous)
	}
	return string(append(b, '}'))
}

// appendConfiguration appends a configuration object of a configuration
// change event, numbers printed by FormatConfigNumber
// (cloudwatch.alarm.number-format; cloudwatch-alarms.md 6.2).
func appendConfiguration(b []byte, s Subject) []byte {
	c := s.Config
	b = append(b, `{"evaluationPeriods":`...)
	b = appendOptInt(b, c.EvaluationPeriods)
	if c.Threshold != nil {
		b = append(b, `,"threshold":`...)
		b = append(b, FormatConfigNumber(*c.Threshold)...)
	}
	if c.ComparisonOperator != nil {
		b = append(b, `,"comparisonOperator":`...)
		b = jsonx.AppendString(b, string(*c.ComparisonOperator))
	}
	if c.TreatMissingData != nil {
		b = append(b, `,"treatMissingData":`...)
		b = jsonx.AppendString(b, *c.TreatMissingData)
	}
	b = append(b, `,"metrics":`...)
	b = appendMetrics(b, s)
	b = append(b, `,"alarmName":`...)
	b = jsonx.AppendString(b, c.AlarmName)
	if c.AlarmDescription != nil {
		b = append(b, `,"description":`...)
		b = jsonx.AppendString(b, *c.AlarmDescription)
	}
	b = append(b, `,"actionsEnabled":`...)
	b = strconv.AppendBool(b, s.ActionsEnabled)
	b = append(b, `,"timestamp":`...)
	b = jsonx.AppendString(b, FormatTimestamp(s.ConfigUpdated))
	b = append(b, `,"okActions":`...)
	b = appendStrings(b, c.OKActions)
	b = append(b, `,"alarmActions":`...)
	b = appendStrings(b, c.AlarmActions)
	b = append(b, `,"insufficientDataActions":`...)
	b = appendStrings(b, c.InsufficientDataActions)
	return append(b, '}')
}

// appendOptString appends s as a JSON string, or null when nil.
func appendOptString(b []byte, s *string) []byte {
	if s == nil {
		return append(b, "null"...)
	}
	return jsonx.AppendString(b, *s)
}

// appendOptInt appends n as a JSON number, or null when nil.
func appendOptInt(b []byte, n *int32) []byte {
	if n == nil {
		return append(b, "null"...)
	}
	return strconv.AppendInt(b, int64(*n), 10)
}

// appendStrings appends ss as a JSON array of strings, [] when empty.
func appendStrings(b []byte, ss []string) []byte {
	b = append(b, '[')
	for i, s := range ss {
		if i > 0 {
			b = append(b, ',')
		}
		b = jsonx.AppendString(b, s)
	}
	return append(b, ']')
}

// deref is *s, or "" when nil.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// upperSnake is a statistic as the notification's Trigger prints it:
// SampleCount -> SAMPLE_COUNT (cloudwatch-alarms.md 5.1).
func upperSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// StateUpdate is the HistorySummary and HistoryData of a StateUpdate
// history item: "Alarm updated from <old> to <new>" and the
// {"version","oldState","newState"} JSON, stateReasonData nested as an
// object and absent without one (cloudwatch-alarms.md 8.2).
//
// The model caps HistoryData at MaxHistoryData characters
// (cloudwatch-alarms.md 12.5); how AWS fits a change whose two reasons and
// reason data exceed it is unrecorded (CWA-10). understudy drops the old
// state's stateReasonData, then the new state's, then shortens the longer
// reason, so the item stays one JSON object.
func StateUpdate(ch Change) (summary, data string) {
	summary = "Alarm updated from " + string(ch.Old.Value) + " to " + string(ch.New.Value)
	item := historyData{Version: "1.0", OldState: historyStateOf(ch.Old), NewState: historyStateOf(ch.New)}
	for {
		data = item.encode()
		if len(data) <= MaxHistoryData {
			return summary, data
		}
		excess := len(data) - MaxHistoryData
		switch {
		case item.OldState.StateReasonData != nil:
			item.OldState.StateReasonData = nil
		case item.NewState.StateReasonData != nil:
			item.NewState.StateReasonData = nil
		case len(item.OldState.StateReason) >= len(item.NewState.StateReason):
			item.OldState.StateReason = cut(item.OldState.StateReason, excess)
		default:
			item.NewState.StateReason = cut(item.NewState.StateReason, excess)
		}
	}
}

// MaxHistoryData is the longest HistoryData AWS stores (model
// com.amazonaws.cloudwatch#HistoryData).
const MaxHistoryData = 4095

// historyData is HistoryData's JSON (cloudwatch-alarms.md 8.2).
type historyData struct {
	Version            string
	OldState, NewState historyState
}

type historyState struct {
	StateValue  cloudwatchapi.StateValue
	StateReason string
	// StateReasonData is the state's reason data, nested as an object with
	// its bytes kept; nil without one (cloudwatch-alarms.md 8.2).
	StateReasonData *string
}

// historyStateOf is one side of a change in HistoryData. Reason data that
// is not JSON, which SetAlarmState refuses and evaluation never writes,
// is left out rather than nested.
func historyStateOf(r StateRecord) historyState {
	s := historyState{StateValue: r.Value, StateReason: r.Reason}
	if r.ReasonData != nil && json.Valid([]byte(*r.ReasonData)) {
		s.StateReasonData = r.ReasonData
	}
	return s
}

// encode is the compact JSON of d, its keys in AWS's order.
func (d historyData) encode() string {
	b := []byte(`{"version":`)
	b = jsonx.AppendString(b, d.Version)
	b = append(b, `,"oldState":`...)
	b = d.OldState.append(b)
	b = append(b, `,"newState":`...)
	b = d.NewState.append(b)
	return string(append(b, '}'))
}

func (s historyState) append(b []byte) []byte {
	b = append(b, `{"stateValue":`...)
	b = jsonx.AppendString(b, string(s.StateValue))
	b = append(b, `,"stateReason":`...)
	b = jsonx.AppendString(b, s.StateReason)
	if s.StateReasonData != nil {
		b = append(b, `,"stateReasonData":`...)
		b = append(b, *s.StateReasonData...)
	}
	return append(b, '}')
}

// cut drops at least n bytes from the end of s, at a rune boundary.
func cut(s string, n int) string {
	end := max(len(s)-n, 0)
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}
