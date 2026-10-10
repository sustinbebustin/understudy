package delivery

import (
	"fmt"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Request is one cross-service delivery: what a Deliver effect hands the
// outbox.
type Request struct {
	Source    aws.ARN // the topic, bucket or rule that produced the delivery
	Mechanism Mechanism
	Target    Target
	Policy    RetryPolicy
	// DLQ is where a delivery goes when its retries are exhausted or the
	// target is gone; nil drops it.
	DLQ *aws.ARN
}

// Target is where a delivery goes, carrying the payload for that target.
//
//sumtype:decl
type Target interface{ isTarget() }

// SQSQueue is a SendMessage to a queue. GroupID and DedupID are set for FIFO
// queues only.
type SQSQueue struct {
	ARN        aws.ARN
	Body       string
	Attributes []MessageAttribute
	GroupID    string
	DedupID    string
}

// LambdaFunction is an Invoke of a function. Event source mappings call
// Invoke directly and never go through the outbox.
type LambdaFunction struct {
	ARN        aws.ARN
	Qualifier  string
	Event      []byte
	Invocation InvocationType
}

// SNSTopic is a Publish to a topic.
type SNSTopic struct {
	ARN        aws.ARN
	Message    string
	Attributes []MessageAttribute
	Subject    string
}

func (SQSQueue) isTarget()       {}
func (LambdaFunction) isTarget() {}
func (SNSTopic) isTarget()       {}

// InvocationType is how a delivery invokes a function.
type InvocationType uint8

// InvocationType values.
const (
	InvocationEvent           InvocationType = iota + 1 // asynchronous, Lambda retries after acceptance
	InvocationRequestResponse                           // synchronous
)

// String returns the Lambda API name, "Event" or "RequestResponse".
func (t InvocationType) String() string {
	switch t {
	case InvocationEvent:
		return "Event"
	case InvocationRequestResponse:
		return "RequestResponse"
	default:
		return fmt.Sprintf("InvocationType(%d)", uint8(t))
	}
}

// InvocationTypeError reports text that names no delivery invocation type.
type InvocationTypeError struct {
	Text string
}

func (e *InvocationTypeError) Error() string {
	return fmt.Sprintf("%q names no delivery invocation type; known names are Event and RequestResponse", e.Text)
}

// ParseInvocationType returns the InvocationType whose Lambda API name is s.
func ParseInvocationType(s string) (InvocationType, error) {
	for t := InvocationEvent; t.valid(); t++ {
		if t.String() == s {
			return t, nil
		}
	}
	return 0, &InvocationTypeError{Text: s}
}

// MarshalText encodes the Lambda API name.
func (t InvocationType) MarshalText() ([]byte, error) {
	if !t.valid() {
		return nil, &InvocationTypeError{Text: t.String()}
	}
	return []byte(t.String()), nil
}

// UnmarshalText decodes a Lambda API name.
func (t *InvocationType) UnmarshalText(text []byte) error {
	parsed, err := ParseInvocationType(string(text))
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

// valid reports whether t is one of the declared values.
func (t InvocationType) valid() bool {
	return t >= InvocationEvent && t <= InvocationRequestResponse
}

// MessageAttribute is an SNS or SQS message attribute.
type MessageAttribute struct {
	Name  string
	Value AttributeValue
}

// AttributeValue is a typed message attribute value. The variant is the data
// type, so a Binary attribute cannot carry a string value or a Number
// attribute bytes. Label is the optional custom type label: "float" in
// "Number.float".
//
//sumtype:decl
type AttributeValue interface {
	// DataType returns the wire DataType: String, Number or Binary, followed
	// by "." and the label when there is one.
	DataType() string
	isAttributeValue()
}

// StringAttribute is a String attribute, sent as StringValue.
type StringAttribute struct {
	Label string
	Value string
}

// NumberAttribute is a Number attribute, sent as StringValue in the decimal
// text the caller gave.
type NumberAttribute struct {
	Label string
	Value string
}

// BinaryAttribute is a Binary attribute, sent as BinaryValue.
type BinaryAttribute struct {
	Label string
	Value []byte
}

// DataType returns "String" or "String.<label>".
func (a StringAttribute) DataType() string { return withLabel("String", a.Label) }

// DataType returns "Number" or "Number.<label>".
func (a NumberAttribute) DataType() string { return withLabel("Number", a.Label) }

// DataType returns "Binary" or "Binary.<label>".
func (a BinaryAttribute) DataType() string { return withLabel("Binary", a.Label) }

func (StringAttribute) isAttributeValue() {}
func (NumberAttribute) isAttributeValue() {}
func (BinaryAttribute) isAttributeValue() {}

func withLabel(base, label string) string {
	if label == "" {
		return base
	}
	return base + "." + label
}

// RetryPolicy says how the outbox retries a failed attempt.
//
//sumtype:decl
type RetryPolicy interface{ isRetryPolicy() }

// SNSManaged is SNS's AWS-managed delivery policy (sns.md 7.2).
type SNSManaged struct{}

// EventBridgeTarget is a rule target's retry policy.
type EventBridgeTarget struct {
	MaxAge time.Duration // MaximumEventAgeInSeconds
	// MaxAttempts is the retries after the first attempt, as
	// MaximumRetryAttempts (0 to 185); total attempts = MaxAttempts + 1.
	MaxAttempts int
}

// S3Notification is S3's event notification retry: a few quick retries.
type S3Notification struct{}

// None makes one attempt.
type None struct{}

func (SNSManaged) isRetryPolicy()        {}
func (EventBridgeTarget) isRetryPolicy() {}
func (S3Notification) isRetryPolicy()    {}
func (None) isRetryPolicy()              {}

// Mechanism is the closed set of delivery paths; fault rules in the Delivery
// phase match on it.
type Mechanism uint8

// Mechanism values.
const (
	SNSToSQS Mechanism = iota + 1
	SNSToLambda
	S3ToSQS
	S3ToSNS
	S3ToLambda
	EventsToLambda
	EventsToSQS
	EventsToSNS
)

// Mechanisms returns every declared mechanism in value order.
func Mechanisms() []Mechanism {
	return []Mechanism{SNSToSQS, SNSToLambda, S3ToSQS, S3ToSNS, S3ToLambda, EventsToLambda, EventsToSQS, EventsToSNS}
}

// String returns the wire name, such as "sns-to-sqs".
func (m Mechanism) String() string {
	switch m {
	case SNSToSQS:
		return "sns-to-sqs"
	case SNSToLambda:
		return "sns-to-lambda"
	case S3ToSQS:
		return "s3-to-sqs"
	case S3ToSNS:
		return "s3-to-sns"
	case S3ToLambda:
		return "s3-to-lambda"
	case EventsToLambda:
		return "events-to-lambda"
	case EventsToSQS:
		return "events-to-sqs"
	case EventsToSNS:
		return "events-to-sns"
	default:
		return fmt.Sprintf("Mechanism(%d)", uint8(m))
	}
}

// Via is the internal-call mechanism the delivery's target call runs under.
// m must be a declared Mechanism, as every constant and every value
// ParseMechanism or UnmarshalText returns is; any other m yields the zero
// Via, which ref.Via's MarshalText rejects.
func (m Mechanism) Via() ref.Via {
	switch m {
	case SNSToSQS, SNSToLambda:
		return ref.SNSDelivery
	case S3ToSQS, S3ToSNS, S3ToLambda:
		return ref.S3Notification
	case EventsToLambda, EventsToSQS, EventsToSNS:
		return ref.EventsTarget
	default:
		return 0
	}
}

// Target is the service a delivery by m lands in. m must be a declared
// Mechanism, as for Via; any other m yields "".
func (m Mechanism) Target() ref.ServiceID {
	switch m {
	case SNSToSQS, S3ToSQS, EventsToSQS:
		return "sqs"
	case SNSToLambda, S3ToLambda, EventsToLambda:
		return "lambda"
	case S3ToSNS, EventsToSNS:
		return "sns"
	default:
		return ""
	}
}

// MechanismError reports text that names no mechanism.
type MechanismError struct {
	Text string
}

func (e *MechanismError) Error() string {
	return fmt.Sprintf("%q names no delivery mechanism; known names are sns-to-sqs, sns-to-lambda, s3-to-sqs, s3-to-sns, s3-to-lambda, events-to-lambda, events-to-sqs and events-to-sns", e.Text)
}

// ParseMechanism returns the mechanism whose wire name is s.
func ParseMechanism(s string) (Mechanism, error) {
	for _, m := range Mechanisms() {
		if m.String() == s {
			return m, nil
		}
	}
	return 0, &MechanismError{Text: s}
}

// MarshalText encodes the wire name.
func (m Mechanism) MarshalText() ([]byte, error) {
	if !m.valid() {
		return nil, &MechanismError{Text: m.String()}
	}
	return []byte(m.String()), nil
}

// UnmarshalText decodes a wire name.
func (m *Mechanism) UnmarshalText(text []byte) error {
	parsed, err := ParseMechanism(string(text))
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

// valid reports whether m is one of the declared values.
func (m Mechanism) valid() bool { return m >= SNSToSQS && m <= EventsToSNS }
