package ref

import "fmt"

// Via is the closed set of mechanisms that make internal calls.
type Via uint8

// Via values.
const (
	SNSDelivery    Via = iota + 1 // SNS delivering to a subscription
	LambdaESM                     // an event source mapping polling or invoking
	EventsTarget                  // EventBridge invoking a rule target
	S3Notification                // S3 delivering an event notification
	LambdaLogs                    // function output written to CloudWatch Logs
	DynamoDBTTL                   // the TTL shim deleting expired items
	Timer                         // a scheduler timer callback
	Snapshot                      // snapshot export and import
	Admin                         // the admin API
	KMSForward                    // S3, Secrets Manager, DynamoDB or CloudWatch Logs calling KMS for the caller, or on its own behalf for the caller's request
	STSAssumeRole                 // STS reading the role AssumeRole names from IAM, for the caller
	LambdaCode                    // Lambda reading a function's package from S3, for the caller
)

// String returns the wire name, such as "sns-delivery".
func (v Via) String() string {
	switch v {
	case SNSDelivery:
		return "sns-delivery"
	case LambdaESM:
		return "lambda-esm"
	case EventsTarget:
		return "events-target"
	case S3Notification:
		return "s3-notification"
	case LambdaLogs:
		return "lambda-logs"
	case DynamoDBTTL:
		return "dynamodb-ttl"
	case Timer:
		return "timer"
	case Snapshot:
		return "snapshot"
	case Admin:
		return "admin"
	case KMSForward:
		return "kms-forward"
	case STSAssumeRole:
		return "sts-assume-role"
	case LambdaCode:
		return "lambda-code"
	default:
		return fmt.Sprintf("Via(%d)", uint8(v))
	}
}

// ViaError reports text that names no Via.
type ViaError struct {
	Text string
}

func (e *ViaError) Error() string {
	return fmt.Sprintf("%q names no internal call mechanism; known names are sns-delivery, lambda-esm, events-target, s3-notification, lambda-logs, dynamodb-ttl, timer, snapshot, admin, kms-forward, sts-assume-role and lambda-code", e.Text)
}

// ParseVia returns the Via whose wire name is s.
func ParseVia(s string) (Via, error) {
	for v := SNSDelivery; v.valid(); v++ {
		if v.String() == s {
			return v, nil
		}
	}
	return 0, &ViaError{Text: s}
}

// MarshalText encodes the wire name.
func (v Via) MarshalText() ([]byte, error) {
	if !v.valid() {
		return nil, &ViaError{Text: v.String()}
	}
	return []byte(v.String()), nil
}

// UnmarshalText decodes a wire name.
func (v *Via) UnmarshalText(text []byte) error {
	parsed, err := ParseVia(string(text))
	if err != nil {
		return err
	}
	*v = parsed
	return nil
}

// valid reports whether v is one of the declared values.
func (v Via) valid() bool { return v >= SNSDelivery && v <= LambdaCode }
