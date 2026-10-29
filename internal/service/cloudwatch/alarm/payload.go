package alarm

import (
	"encoding/json"
	"time"
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
func SNSSubject(_ Subject, _ cloudwatchapi.StateValue) string {
	// not implemented yet
	return ""
}

// SNSMessage is the notification body JSON in the schema's field order
// (cloudwatch.alarm.sns.unset-fields; cloudwatch-alarms.md 5.1).
func SNSMessage(_ Subject, _ Change) string {
	// not implemented yet
	return ""
}

// RegionName is a region code's long name as the notification Subject and
// Region carry it, such as "US East (N. Virginia)" for us-east-1; ok is
// false for a code the table does not hold (cloudwatch-alarms.md 5.2).
func RegionName(_ string) (name string, ok bool) {
	// not implemented yet
	return "", false
}

// FIFOGroupID is the MessageGroupId of a notification to a FIFO topic:
// lower-case hex SHA-256 of the alarm ARN (cloudwatch.alarm.sns.fifo-group;
// cloudwatch-alarms.md 5.3).
func FIFOGroupID(_ string) string {
	// not implemented yet
	return ""
}

// FIFODedupID is the MessageDeduplicationId of a notification to a FIFO
// topic: lower-case hex SHA-256 of the alarm ARN followed by the
// StateChangeTime as FormatTimestamp prints it
// (cloudwatch.alarm.sns.fifo-group; cloudwatch-alarms.md 5.3).
func FIFODedupID(_ string, _ time.Time) string {
	// not implemented yet
	return ""
}

// LambdaEvent is the Lambda action's event JSON (cloudwatch-alarms.md 6.1).
func LambdaEvent(_ Subject, _ Change) []byte {
	// not implemented yet
	return nil
}

// StateChangeDetail is the detail JSON of a "CloudWatch Alarm State
// Change" event (cloudwatch-alarms.md 6.2).
func StateChangeDetail(_ Subject, _ Change) string {
	// not implemented yet
	return ""
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
func (ConfigOp) String() string {
	// not implemented yet
	return ""
}

// ConfigurationChangeDetail is the detail JSON of a "CloudWatch Alarm
// Configuration Change" event: previous is the replaced configuration on
// an update and nil otherwise; state is the alarm's state at the change
// (cloudwatch.alarm.events.metric-id; cloudwatch-alarms.md 6.2).
func ConfigurationChangeDetail(_ ConfigOp, _ Subject, _ *Subject, _ StateRecord) string {
	// not implemented yet
	return ""
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
