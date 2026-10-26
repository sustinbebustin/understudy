package destination

import (
	"time"
	"uuid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
)

// Destination is a configured destination ARN, parsed
// (lambda-destinations-layers.md 2.2).
//
//sumtype:decl
type Destination interface{ isDestination() }

// Queue is an SQS queue, arn:<partition>:sqs:<region>:<account>:<name>.
type Queue struct{ ARN aws.ARN }

// Topic is an SNS topic, arn:<partition>:sns:<region>:<account>:<name>.
type Topic struct{ ARN aws.ARN }

// Function is a Lambda function: ARN is unqualified (resource
// function:<name>) and Qualifier is "" or the alias or version the
// configured ARN names.
type Function struct {
	ARN       aws.ARN
	Qualifier string
}

// Bus is an EventBridge event bus,
// arn:<partition>:events:<region>:<account>:event-bus/<name>.
type Bus struct{ ARN aws.ARN }

// Bucket is an S3 bucket, arn:<partition>:s3:::<name>, an on-failure
// destination only.
type Bucket struct{ Name string }

// FIFO reports whether the queue is a FIFO queue, which Lambda does not
// deliver to (lambda-destinations-layers.md 2.2). Not implemented yet: it
// returns false.
func (Queue) FIFO() bool { return false }

// FIFO reports whether the topic is a FIFO topic, which Lambda does not
// deliver to (lambda-destinations-layers.md 2.2). Not implemented yet: it
// returns false.
func (Topic) FIFO() bool { return false }

func (Queue) isDestination()    {}
func (Topic) isDestination()    {}
func (Function) isDestination() {}
func (Bus) isDestination()      {}
func (Bucket) isDestination()   {}

// Parse reads a configured destination ARN. Not implemented yet: it always
// returns a *ParseError.
func Parse(arn string) (Destination, error) {
	return nil, &ParseError{ARN: arn, Reason: "parsing destinations is not implemented yet"}
}

// ParseError reports a destination ARN that names no destination Lambda
// delivers to.
type ParseError struct {
	ARN    string
	Reason string
}

func (e *ParseError) Error() string {
	return "destination: " + e.ARN + " is not a Lambda destination: " + e.Reason
}

// Sender says who sends a function's records and dead letters.
type Sender struct {
	// Function is the function, unqualified: each delivery's Source. Its
	// name keys S3 objects.
	Function aws.ARN
	// Qualified is the record's functionArn, EventBridge's second resource.
	Qualified string
	// Role is the version's execution role, which Lambda delivers as
	// (lambda-destinations-layers.md 2.5).
	Role aws.ARN
	// Trace is the Invoke call's X-Amzn-Trace-Id, "" when it had none.
	Trace string
}

// Outgoing is one built record on its way to one destination.
type Outgoing struct {
	Condition Condition
	// Body is the record, Built.JSON.
	Body []byte
	// ObjectKey is the S3 key (ObjectKey) for a Bucket, "" for every other
	// destination.
	ObjectKey string
}

// ObjectKey is the S3 key of a record function's event sends at
// (lambda-destinations-layers.md 4.5). Not implemented yet: it returns "".
func ObjectKey(_ string, _ time.Time, _ uuid.UUID) string { return "" }

// To is the outbox delivery of o to d, sent by s
// (lambda-destinations-layers.md 4, 11.3). Not implemented yet: it always
// returns a *WrapError.
func To(_ Destination, _ Sender, _ Outgoing) (delivery.Request, error) {
	return delivery.Request{}, &WrapError{Reason: "wrapping records is not implemented yet"}
}

// DeadLetter is the outbox delivery of an event that failed for good to
// the function's dead-letter queue or topic target
// (lambda-destinations-layers.md 5.2, 11.3). Not implemented yet: it always
// returns a *WrapError.
func DeadLetter(_ aws.ARN, _ Sender, _ ids.LambdaRequestID, _ Invocation, _ []byte) (delivery.Request, error) {
	return delivery.Request{}, &WrapError{Reason: "dead letters are not implemented yet"}
}

// ErrorMessage is a dead letter's ErrorMessage attribute for a function
// error's response (lambda-destinations-layers.md 5.2). Not implemented
// yet: it returns "".
func ErrorMessage(_ []byte) string { return "" }

// WrapError reports a record or dead letter that cannot be delivered as
// asked.
type WrapError struct {
	Reason string
}

func (e *WrapError) Error() string {
	return "destination: cannot build the delivery: " + e.Reason
}
