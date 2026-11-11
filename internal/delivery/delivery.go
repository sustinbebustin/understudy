package delivery

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Request is one cross-service delivery: what a Deliver effect hands the
// outbox.
type Request struct {
	Source aws.ARN // the topic, bucket or rule that produced the delivery
	// SourceAccount is the aws:SourceAccount the delivering service sends
	// with each call of the delivery, zero when it sends none. The producer
	// decides: S3 sends the bucket owner's account, which a bucket ARN does
	// not name, and EventBridge sends none to Lambda
	// (iam-enforcement-surfaces.md 8.1).
	SourceAccount aws.AccountID
	Mechanism     Mechanism
	Target        Target
	Policy        RetryPolicy
	// DLQ is where a delivery goes when its retries are exhausted or the
	// target is gone; nil drops it.
	DLQ *aws.ARN
	// DLQMessage is the dead letter the producer built, which the outbox
	// sends to DLQ as it is instead of deriving one from Target; nil for
	// every delivery but SNSToLambda's with a DLQ, whose dead letter is the
	// SQS notification envelope only SNS can write (sns.md 7.4).
	DLQMessage *DeadLetterMessage
	// Sender is set for the Lambda* and ESM* mechanisms and nil for every
	// other: Lambda delivers an asynchronous invocation's record or dead
	// letter (lambda-destinations-layers.md 2.5, 11.3), and an event source
	// mapping's on-failure record (lambda-stream-sources.md 5.6), as the
	// function's execution role.
	Sender *LambdaSender
	// As is the principal the Scheduler* mechanisms deliver as, the
	// schedule's execution role (scheduler.md 14.4.4), and that
	// EventsToEvents delivers as when the event bus target has a RoleArn
	// (iam-enforcement-surfaces.md 8.4); nil for every other delivery, whose
	// principal is its service principal, or, for the Lambda* and ESM*
	// mechanisms, Sender's role.
	As Principal
	// Invocation is set for the Scheduler* mechanisms and nil for every
	// other: the outbox renders the target's payload from it before each
	// attempt (scheduler.md 14.4.2).
	Invocation *SchedulerInvocation
}

// DeadLetterMessage is a dead letter its producer built: the SQS message
// body and attributes, and the group and deduplication ids a FIFO
// dead-letter queue takes.
type DeadLetterMessage struct {
	Body       string
	Attributes []MessageAttribute
	GroupID    string
	DedupID    string
}

// LambdaSender is a Lambda* delivery's sender: the execution role it runs
// as and what it carries.
type LambdaSender struct {
	// Role is the function's execution role,
	// arn:<partition>:iam::<account>:role/<path><name>.
	Role aws.ARN
	// Carries is what the delivery is, which names the metric AWS counts
	// when it fails.
	Carries LambdaPayload
}

// LambdaPayload is what a Lambda* delivery carries.
type LambdaPayload uint8

// LambdaPayload values.
const (
	// DestinationRecord is an invocation record for an on-success or
	// on-failure destination; a failure counts DestinationDeliveryFailures
	// (lambda-destinations-layers.md 4.6).
	DestinationRecord LambdaPayload = iota + 1
	// DeadLetter is a failed event for the function's dead-letter queue or
	// topic; a failure counts DeadLetterErrors
	// (lambda-destinations-layers.md 5.4).
	DeadLetter
)

// String returns the wire name, "destination-record" or "dead-letter".
func (p LambdaPayload) String() string {
	switch p {
	case DestinationRecord:
		return "destination-record"
	case DeadLetter:
		return "dead-letter"
	default:
		return fmt.Sprintf("LambdaPayload(%d)", uint8(p))
	}
}

// Metric is the CloudWatch metric Lambda counts when a delivery carrying p
// fails for good; "" for an undeclared p.
func (p LambdaPayload) Metric() string {
	switch p {
	case DestinationRecord:
		return "DestinationDeliveryFailures"
	case DeadLetter:
		return "DeadLetterErrors"
	default:
		return ""
	}
}

// LambdaPayloadError reports text that names no Lambda delivery payload.
type LambdaPayloadError struct {
	Text string
}

func (e *LambdaPayloadError) Error() string {
	return fmt.Sprintf("%q names no Lambda delivery payload; known names are destination-record and dead-letter", e.Text)
}

// MarshalText encodes the wire name.
func (p LambdaPayload) MarshalText() ([]byte, error) {
	if p != DestinationRecord && p != DeadLetter {
		return nil, &LambdaPayloadError{Text: p.String()}
	}
	return []byte(p.String()), nil
}

// UnmarshalText decodes a wire name.
func (p *LambdaPayload) UnmarshalText(text []byte) error {
	for _, v := range []LambdaPayload{DestinationRecord, DeadLetter} {
		if v.String() == string(text) {
			*p = v
			return nil
		}
	}
	return &LambdaPayloadError{Text: string(text)}
}

// EventBridgeDeniedCode is the ERROR_CODE of an EventBridge dead letter
// whose target refused the delivery on its policy, the value of switch
// events.delivery.denied-code: NO_PERMISSIONS is among the codes AWS lists,
// and which one a policy refusal produces is UNVERIFIED
// (iam-enforcement-surfaces.md 8.4, IAM-ENF-22).
const EventBridgeDeniedCode = "NO_PERMISSIONS"

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
	// TraceHeader is sent as the AWSTraceHeader message system attribute
	// when not empty (lambda-destinations-layers.md 4.1, 11.3).
	TraceHeader string
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
	// GroupID and DedupID are the MessageGroupId and
	// MessageDeduplicationId of a Publish to a FIFO topic, empty for a
	// standard one. Only CloudWatchToSNS sets them
	// (cloudwatch-alarms.md 5.3).
	GroupID string
	DedupID string
}

// EventBus is a PutEvents to an event bus. No mechanism delivers to one
// yet; the producers that need one add theirs (ADR 0015 amendment).
type EventBus struct {
	// ARN is the bus: arn:<partition>:events:<region>:<account>:event-bus/<name>.
	ARN aws.ARN
	// Entries are sent in order. The outbox takes exactly one, so a retry
	// of a failed entry puts no other entry twice.
	Entries []EventEntry
}

// EventEntry is one PutEvents entry of an EventBus delivery.
type EventEntry struct {
	Source     string
	DetailType string
	// Detail is the event's detail, JSON object text.
	Detail    string
	Resources []string
	// Forwarded is set when an event bus target forwards an event that was
	// already put; nil for an event a producer puts as new.
	Forwarded *Forwarded
}

// Forwarded is the envelope an event keeps when an event bus target
// forwards it to another bus (eventbridge-buses.md 10.4.12). Its presence
// marks the event's one hop: the receiving bus sends it to no further bus
// (eventbridge-buses.md 10.4.13).
type Forwarded struct {
	// ID is the event's id: the original in the same region, a new one
	// across regions.
	ID string
	// Account is the account that first put the event, the sender's.
	Account string
	// Region is the region the event was first put in.
	Region string
	// Time is the event's time.
	Time time.Time
}

// StateMachine is a StartExecution of a Step Functions state machine
// (scheduler.md 6.4). Only SchedulerToStates delivers to one.
type StateMachine struct {
	// ARN is the state machine, or one of its versions or aliases:
	// arn:<partition>:states:<region>:<account>:stateMachine:<name>[:<version or alias>].
	ARN aws.ARN
	// Input is the execution's input, JSON text.
	Input string
	// Name is the execution's name, chosen by the producer.
	Name string
}

// S3Object is a PutObject. No mechanism delivers to one yet.
type S3Object struct {
	Bucket string
	Key    string
	Body   []byte
}

// HTTPEndpoint is a POST to an http or https URL, sent through
// internal/egress, the only package that dials out. SNSToHTTP delivers to
// it.
type HTTPEndpoint struct {
	URL url.URL
	// Kind is what the POST carries (sns-http-delivery.md 8.2.1).
	Kind HTTPKind
	// Headers are sent with their names as given, the values of one name
	// in order; net/http writes the names sorted.
	Headers []HTTPHeader
	Body    []byte
	// DeadLetterAttributes are sent as SQS message attributes with the dead
	// letter only, never to the endpoint: a raw notification's message
	// attributes (sns.http.dlq-body, sns-http-delivery.md 6.5). Nil for
	// every other delivery.
	DeadLetterAttributes []MessageAttribute
}

// HostPort is the host and port t's URL names, the port defaulted to 80
// for http and 443 for https, never its path, query or userinfo: what the
// journal records as an HTTP delivery's Endpoint.
func (t HTTPEndpoint) HostPort() string {
	port := t.URL.Port()
	switch {
	case port != "":
	case t.URL.Scheme == "https":
		port = "443"
	default:
		port = "80"
	}
	return net.JoinHostPort(t.URL.Hostname(), port)
}

// HTTPKind is what an HTTPEndpoint delivery carries
// (sns-http-delivery.md 8.2.1).
type HTTPKind uint8

// HTTPKind values.
const (
	HTTPNotification HTTPKind = iota + 1
	HTTPSubscriptionConfirmation
	HTTPUnsubscribeConfirmation
)

// String returns the x-amz-sns-message-type value, such as "Notification"
// (sns-http-delivery.md 3.2).
func (k HTTPKind) String() string {
	switch k {
	case HTTPNotification:
		return "Notification"
	case HTTPSubscriptionConfirmation:
		return "SubscriptionConfirmation"
	case HTTPUnsubscribeConfirmation:
		return "UnsubscribeConfirmation"
	default:
		return fmt.Sprintf("HTTPKind(%d)", uint8(k))
	}
}

// Valid reports whether k is one of the declared kinds.
func (k HTTPKind) Valid() bool {
	return k >= HTTPNotification && k <= HTTPUnsubscribeConfirmation
}

// HTTPKindError reports text that names no HTTP delivery kind.
type HTTPKindError struct {
	Text string
}

func (e *HTTPKindError) Error() string {
	return fmt.Sprintf("%q names no HTTP delivery kind; known names are Notification, SubscriptionConfirmation and UnsubscribeConfirmation", e.Text)
}

// ParseHTTPKind returns the HTTPKind whose name is s, compared exactly.
func ParseHTTPKind(s string) (HTTPKind, error) {
	for k := HTTPNotification; k.Valid(); k++ {
		if k.String() == s {
			return k, nil
		}
	}
	return 0, &HTTPKindError{Text: s}
}

// MarshalText encodes the kind's name.
func (k HTTPKind) MarshalText() ([]byte, error) {
	if !k.Valid() {
		return nil, &HTTPKindError{Text: k.String()}
	}
	return []byte(k.String()), nil
}

// UnmarshalText decodes a kind's name.
func (k *HTTPKind) UnmarshalText(text []byte) error {
	parsed, err := ParseHTTPKind(string(text))
	if err != nil {
		return err
	}
	*k = parsed
	return nil
}

// HTTPHeader is one request header of an HTTPEndpoint delivery.
type HTTPHeader struct {
	Name, Value string
}

func (SQSQueue) isTarget()       {}
func (LambdaFunction) isTarget() {}
func (SNSTopic) isTarget()       {}
func (EventBus) isTarget()       {}
func (S3Object) isTarget()       {}
func (HTTPEndpoint) isTarget()   {}
func (StateMachine) isTarget()   {}

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

// SNSHTTP is an SNS http or https subscription's resolved delivery policy
// (sns-http-delivery.md 6.2 to 6.4, 8.2.1).
type SNSHTTP struct {
	// Retry counts per phase, in phase order; their sum is numRetries.
	NoDelay, MinDelay, Backoff, MaxDelay int
	MinDelayTarget, MaxDelayTarget       time.Duration
	// BackoffFunction is kept for when a recording settles the curves;
	// under sns.http.backoff-curves every function steps linearly.
	BackoffFunction BackoffFunction
	// MaxReceivesPerSecond is throttlePolicy.maxReceivesPerSecond; 0 for
	// none.
	MaxReceivesPerSecond int
}

// Retries is the number of retries after the first attempt, the sum of the
// phase counts, so a delivery makes 1 + Retries attempts
// (sns-http-delivery.md 6.3).
func (p SNSHTTP) Retries() int { return p.NoDelay + p.MinDelay + p.Backoff + p.MaxDelay }

// Delay is how long after the failed attempt ended a retry, numbered from
// 1, is due; false when the number is below 1 or above Retries. The phases
// run in order: no delay, minDelayTarget, the backoff phase, maxDelayTarget
// (sns-http-delivery.md 6.3). The backoff phase steps linearly from
// minDelayTarget to maxDelayTarget, truncated to the millisecond, whatever
// BackoffFunction says, and a single backoff retry waits minDelayTarget
// (sns.http.backoff-curves); no delay is jittered (sns.http.retry-jitter).
func (p SNSHTTP) Delay(retry int) (time.Duration, bool) {
	noDelayEnd := p.NoDelay
	minDelayEnd := noDelayEnd + p.MinDelay
	backoffEnd := minDelayEnd + p.Backoff
	switch {
	case retry < 1 || retry > p.Retries():
		return 0, false
	case retry <= noDelayEnd:
		return 0, true
	case retry <= minDelayEnd:
		return p.MinDelayTarget, true
	case retry <= backoffEnd:
		if p.Backoff == 1 {
			return p.MinDelayTarget, true
		}
		step := int64(retry - minDelayEnd - 1)
		span := int64(p.MaxDelayTarget - p.MinDelayTarget)
		return (p.MinDelayTarget + time.Duration(span*step/int64(p.Backoff-1))).Truncate(time.Millisecond), true
	default:
		return p.MaxDelayTarget, true
	}
}

// PaceInterval is the least time between the starts of two attempts to
// the subscription, 1/maxReceivesPerSecond in whole nanoseconds, 0 without
// throttling (sns-http-delivery.md 6.4).
func (p SNSHTTP) PaceInterval() time.Duration {
	if p.MaxReceivesPerSecond <= 0 {
		return 0
	}
	return time.Second / time.Duration(p.MaxReceivesPerSecond)
}

func (SNSManaged) isRetryPolicy()        {}
func (EventBridgeTarget) isRetryPolicy() {}
func (S3Notification) isRetryPolicy()    {}
func (None) isRetryPolicy()              {}
func (SNSHTTP) isRetryPolicy()           {}

// BackoffFunction is a healthyRetryPolicy's backoffFunction (sns.md 7.3).
type BackoffFunction uint8

// BackoffFunction values.
const (
	BackoffLinear BackoffFunction = iota + 1
	BackoffArithmetic
	BackoffGeometric
	BackoffExponential
)

// String returns the function's name, such as "linear".
func (f BackoffFunction) String() string {
	switch f {
	case BackoffLinear:
		return "linear"
	case BackoffArithmetic:
		return "arithmetic"
	case BackoffGeometric:
		return "geometric"
	case BackoffExponential:
		return "exponential"
	default:
		return fmt.Sprintf("BackoffFunction(%d)", uint8(f))
	}
}

// Valid reports whether f is one of the declared functions.
func (f BackoffFunction) Valid() bool {
	return f >= BackoffLinear && f <= BackoffExponential
}

// BackoffFunctionError reports text that names no backoff function.
type BackoffFunctionError struct {
	Text string
}

func (e *BackoffFunctionError) Error() string {
	return fmt.Sprintf("%q names no backoff function; known names are linear, arithmetic, geometric and exponential", e.Text)
}

// ParseBackoffFunction returns the BackoffFunction named s, compared
// exactly: the lower-case names only.
func ParseBackoffFunction(s string) (BackoffFunction, error) {
	for f := BackoffLinear; f.Valid(); f++ {
		if f.String() == s {
			return f, nil
		}
	}
	return 0, &BackoffFunctionError{Text: s}
}

// MarshalText encodes the function's name.
func (f BackoffFunction) MarshalText() ([]byte, error) {
	if !f.Valid() {
		return nil, &BackoffFunctionError{Text: f.String()}
	}
	return []byte(f.String()), nil
}

// UnmarshalText decodes a function's name.
func (f *BackoffFunction) UnmarshalText(text []byte) error {
	parsed, err := ParseBackoffFunction(string(text))
	if err != nil {
		return err
	}
	*f = parsed
	return nil
}

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
	EventsToEvents
	SNSToHTTP
	// The Lambda mechanisms deliver an asynchronous invocation's record to
	// a destination, or its event to the function's dead-letter queue or
	// topic (the first two), as the function's execution role
	// (lambda-destinations-layers.md 11.3).
	LambdaToSQS
	LambdaToSNS
	LambdaToLambda
	LambdaToEvents
	LambdaToS3
	// ESMToSQS and ESMToSNS send an event source mapping's on-failure
	// record to its destination queue or topic, as the function's execution
	// role (lambda-stream-sources.md 5.6).
	ESMToSQS
	ESMToSNS
	// CloudWatchToSNS and CloudWatchToLambda run a metric alarm's SNS and
	// Lambda actions on a state transition, as cloudwatch.amazonaws.com and
	// lambda.alarms.cloudwatch.amazonaws.com (cloudwatch-alarms.md 12.4).
	CloudWatchToSNS
	CloudWatchToLambda
	// The Scheduler mechanisms invoke a schedule's templated target as its
	// execution role (scheduler.md 14.4.3, 14.4.4); Request.As and
	// Request.Invocation are set.
	SchedulerToLambda
	SchedulerToSQS
	SchedulerToEvents
	SchedulerToSNS
	SchedulerToStates

	// endMechanism follows the last value and is no mechanism: Mechanisms,
	// valid and the MechanismError text count up to it, so a new value is
	// one line above it plus its String, Via and Target cases.
	endMechanism
)

// Mechanisms returns every declared mechanism in value order.
func Mechanisms() []Mechanism {
	all := make([]Mechanism, 0, endMechanism-SNSToSQS)
	for m := SNSToSQS; m < endMechanism; m++ {
		all = append(all, m)
	}
	return all
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
	case EventsToEvents:
		return "events-to-events"
	case SNSToHTTP:
		return "sns-to-http"
	case LambdaToSQS:
		return "lambda-to-sqs"
	case LambdaToSNS:
		return "lambda-to-sns"
	case LambdaToLambda:
		return "lambda-to-lambda"
	case LambdaToEvents:
		return "lambda-to-events"
	case LambdaToS3:
		return "lambda-to-s3"
	case ESMToSQS:
		return "esm-to-sqs"
	case ESMToSNS:
		return "esm-to-sns"
	case CloudWatchToSNS:
		return "cloudwatch-to-sns"
	case CloudWatchToLambda:
		return "cloudwatch-to-lambda"
	case SchedulerToLambda:
		return "scheduler-to-lambda"
	case SchedulerToSQS:
		return "scheduler-to-sqs"
	case SchedulerToEvents:
		return "scheduler-to-events"
	case SchedulerToSNS:
		return "scheduler-to-sns"
	case SchedulerToStates:
		return "scheduler-to-states"
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
	case SNSToSQS, SNSToLambda, SNSToHTTP:
		return ref.SNSDelivery
	case S3ToSQS, S3ToSNS, S3ToLambda:
		return ref.S3Notification
	case EventsToLambda, EventsToSQS, EventsToSNS, EventsToEvents:
		return ref.EventsTarget
	case LambdaToSQS, LambdaToSNS, LambdaToLambda, LambdaToEvents, LambdaToS3:
		return ref.LambdaDestination
	case ESMToSQS, ESMToSNS:
		return ref.LambdaESM
	case CloudWatchToSNS, CloudWatchToLambda:
		return ref.CloudWatchAlarm
	case SchedulerToLambda, SchedulerToSQS, SchedulerToEvents, SchedulerToSNS, SchedulerToStates:
		return ref.SchedulerTarget
	default:
		return 0
	}
}

// Target is the service a delivery by m lands in. m must be a declared
// Mechanism, as for Via; any other m yields "".
func (m Mechanism) Target() ref.ServiceID {
	switch m {
	case SNSToSQS, S3ToSQS, EventsToSQS, LambdaToSQS, ESMToSQS, SchedulerToSQS:
		return "sqs"
	case SNSToLambda, S3ToLambda, EventsToLambda, LambdaToLambda, CloudWatchToLambda, SchedulerToLambda:
		return "lambda"
	case S3ToSNS, EventsToSNS, LambdaToSNS, ESMToSNS, CloudWatchToSNS, SchedulerToSNS:
		return "sns"
	case EventsToEvents, LambdaToEvents, SchedulerToEvents:
		return "eventbridge"
	case LambdaToS3:
		return "s3"
	case SchedulerToStates:
		return "sfn"
	case SNSToHTTP:
		// An HTTP endpoint lands in no service.
		return ""
	default:
		return ""
	}
}

// MechanismError reports text that names no mechanism.
type MechanismError struct {
	Text string
}

func (e *MechanismError) Error() string {
	return fmt.Sprintf("%q names no delivery mechanism; known names are %s", e.Text, knownMechanisms())
}

// knownMechanisms lists every mechanism's wire name in value order, the
// last after "and".
func knownMechanisms() string {
	all := Mechanisms()
	names := make([]string, len(all))
	for i, m := range all {
		names[i] = m.String()
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
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
func (m Mechanism) valid() bool { return m >= SNSToSQS && m < endMechanism }
