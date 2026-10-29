package alarm

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/aws"
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
func StateUpdate(_ Change) (summary, data string) {
	// not implemented yet
	return "", ""
}
