package destination

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
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

// FIFO reports whether the queue is a FIFO queue, whose name ends in
// .fifo; Lambda does not deliver to one (lambda-destinations-layers.md 2.2).
func (d Queue) FIFO() bool { return strings.HasSuffix(d.ARN.Resource, ".fifo") }

// FIFO reports whether the topic is a FIFO topic, whose name ends in
// .fifo; Lambda does not deliver to one (lambda-destinations-layers.md 2.2).
func (d Topic) FIFO() bool { return strings.HasSuffix(d.ARN.Resource, ".fifo") }

func (Queue) isDestination()    {}
func (Topic) isDestination()    {}
func (Function) isDestination() {}
func (Bus) isDestination()      {}
func (Bucket) isDestination()   {}

// Parse reads a configured destination ARN: an SQS queue, an SNS topic, a
// Lambda function with or without a qualifier, an EventBridge event bus or
// an S3 bucket (lambda-destinations-layers.md 2.2). Anything else, the
// kafka:// form of event source mappings included, is a *ParseError.
func Parse(arn string) (Destination, error) {
	if strings.HasPrefix(arn, "kafka://") {
		return nil, &ParseError{ARN: arn, Reason: "a kafka:// destination serves Kafka event source mappings only, not asynchronous invocations (lambda-destinations-layers.md 2.2)"}
	}
	a, err := aws.ParseARN(arn)
	if err != nil {
		return nil, &ParseError{ARN: arn, Reason: "it is not an ARN, arn:<partition>:<service>:<region>:<account>:<resource>"}
	}
	if a.Service == "s3" {
		return parseBucket(arn, a)
	}
	if a.Region == "" || a.Account == "" {
		return nil, &ParseError{ARN: arn, Reason: "it names no region or account, which an " + a.Service + " destination has"}
	}
	switch a.Service {
	case "sqs":
		if !plainName(a.Resource) {
			return nil, &ParseError{ARN: arn, Reason: "an SQS destination is a queue, arn:<partition>:sqs:<region>:<account>:<name>"}
		}
		return Queue{ARN: a}, nil
	case "sns":
		if !plainName(a.Resource) {
			return nil, &ParseError{ARN: arn, Reason: "an SNS destination is a topic, arn:<partition>:sns:<region>:<account>:<name>"}
		}
		return Topic{ARN: a}, nil
	case "lambda":
		return parseFunction(arn, a)
	case "events":
		if name, ok := strings.CutPrefix(a.Resource, "event-bus/"); !ok || name == "" {
			return nil, &ParseError{ARN: arn, Reason: "an EventBridge destination is an event bus, arn:<partition>:events:<region>:<account>:event-bus/<name>"}
		}
		return Bus{ARN: a}, nil
	default:
		return nil, &ParseError{ARN: arn, Reason: "Lambda delivers records to SQS, SNS, Lambda, EventBridge and S3 only, not to " + a.Service}
	}
}

// parseFunction reads a function destination, function:<name> with an
// optional :<qualifier>.
func parseFunction(arn string, a aws.ARN) (Destination, error) {
	rest, ok := strings.CutPrefix(a.Resource, "function:")
	name, qualifier, qualified := strings.Cut(rest, ":")
	if !ok || name == "" || (qualified && (qualifier == "" || strings.Contains(qualifier, ":"))) {
		return nil, &ParseError{ARN: arn, Reason: "a Lambda destination is a function, arn:<partition>:lambda:<region>:<account>:function:<name>[:<qualifier>]"}
	}
	a.Resource = "function:" + name
	return Function{ARN: a, Qualifier: qualifier}, nil
}

// parseBucket reads an S3 destination, a bucket ARN with no region,
// account or key.
func parseBucket(arn string, a aws.ARN) (Destination, error) {
	if a.Region != "" || a.Account != "" || strings.Contains(a.Resource, "/") {
		return nil, &ParseError{ARN: arn, Reason: "an S3 destination is a bucket, arn:<partition>:s3:::<name>, with no region, account or key"}
	}
	return Bucket{Name: a.Resource}, nil
}

// plainName reports whether resource is a bare queue or topic name: no
// path and no further ARN element, such as a subscription's id.
func plainName(resource string) bool {
	return resource != "" && !strings.ContainsAny(resource, ":/")
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

// ObjectKey is the S3 key of a record of function's event:
// aws/lambda/async/<function>/YYYY/MM/DD/YYYY-MM-DDTHH.MM.SS-<uuid>
// (lambda-destinations-layers.md 4.5). The date parts are at's in UTC and
// the UUID is in RFC 9562's lowercase form; AWS's zone and form are
// unrecorded (LDL-07, 11.3.5).
func ObjectKey(function string, at time.Time, id uuid.UUID) string {
	u := at.UTC()
	return "aws/lambda/async/" + function + "/" + u.Format("2006/01/02") + "/" + u.Format("2006-01-02T15.04.05") + "-" + id.String()
}

// recordInvocation is how a destination function is invoked with a record:
// asynchronously, the value of the switch
// lambda.destination.lambda-invocation-type (lambda-destinations-layers.md
// 4.3, 11.2; LDL-06).
const recordInvocation = delivery.InvocationEvent

// To is the outbox delivery of o to d, sent by s
// (lambda-destinations-layers.md 4, 11.3): one attempt, as the switch
// lambda.destination.delivery-retry says (4.6), with no dead-letter queue
// of its own, run as the execution role. A FIFO queue or topic is wrapped
// like any other, and the outbox fails the delivery (11.3.6). To refuses,
// with a *WrapError, a record Lambda never sends: a Success to S3, which
// takes on-failure records only (2.2), an S3 record without its key or a
// key for any other destination, an undeclared condition and an empty
// record, and one bound for a scope understudy does not serve (served).
func To(d Destination, s Sender, o Outgoing) (delivery.Request, error) {
	if err := o.check(d); err != nil {
		return delivery.Request{}, err
	}
	req := delivery.Request{
		Source: s.Function,
		Policy: delivery.None{},
		Sender: &delivery.LambdaSender{Role: s.Role, Carries: delivery.DestinationRecord},
	}
	// in is the ARN the record lands in; a bucket's names no scope, and the
	// record lands in the function's.
	var in aws.ARN
	switch d := d.(type) {
	case Queue:
		// No message attributes; AWSTraceHeader is the Invoke's trace header
		// (4.1, 11.3.5).
		in, req.Mechanism, req.Target = d.ARN, delivery.LambdaToSQS, delivery.SQSQueue{ARN: d.ARN, Body: string(o.Body), TraceHeader: s.Trace}
	case Topic:
		// No subject or attributes, which are unrecorded (4.2, LDL-05). A
		// record over SNS's limit goes out whole and SNS refuses it, as the
		// switch lambda.destination.sns-oversize says.
		in, req.Mechanism, req.Target = d.ARN, delivery.LambdaToSNS, delivery.SNSTopic{ARN: d.ARN, Message: string(o.Body)}
	case Function:
		in, req.Mechanism, req.Target = d.ARN, delivery.LambdaToLambda, delivery.LambdaFunction{ARN: d.ARN, Qualifier: d.Qualifier, Event: o.Body, Invocation: recordInvocation}
	case Bus:
		// The detail is the record compact throughout, its payloads included
		// (11.4, LDL-10); whether Lambda or EventBridge compacts it is not
		// visible, and understudy compacts it here.
		var detail bytes.Buffer
		if err := json.Compact(&detail, o.Body); err != nil {
			return delivery.Request{}, &WrapError{Reason: "the record is not JSON, so it cannot be an EventBridge event's detail: " + err.Error()}
		}
		// Resources are the bus, then the qualified function: the switch
		// lambda.destination.eventbridge-resources-order (4.4, LDL-10).
		in, req.Mechanism, req.Target = d.ARN, delivery.LambdaToEvents, delivery.EventBus{ARN: d.ARN, Entries: []delivery.EventEntry{{
			Source: "lambda", DetailType: o.Condition.DetailType(), Detail: detail.String(), Resources: []string{d.ARN.String(), s.Qualified},
		}}}
	case Bucket:
		in, req.Mechanism, req.Target = s.Function, delivery.LambdaToS3, delivery.S3Object{Bucket: d.Name, Key: o.ObjectKey, Body: o.Body}
	default:
		return delivery.Request{}, &WrapError{Reason: "the destination is nil"}
	}
	if err := served(in); err != nil {
		return delivery.Request{}, err
	}
	return req, nil
}

// check refuses an outgoing record Lambda never sends to d.
func (o Outgoing) check(d Destination) *WrapError {
	_, bucket := d.(Bucket)
	switch {
	case !o.Condition.declared():
		return &WrapError{Reason: "the condition is " + o.Condition.String() + ", which is no condition"}
	case len(o.Body) == 0:
		return &WrapError{Reason: "the record is empty"}
	case bucket && o.Condition == Success:
		return &WrapError{Reason: "an S3 bucket takes on-failure records only, not a Success record (lambda-destinations-layers.md 2.2)"}
	case bucket && o.ObjectKey == "":
		return &WrapError{Reason: "an S3 record has no object key; ObjectKey computes it"}
	case !bucket && o.ObjectKey != "":
		return &WrapError{Reason: "an object key is set for a destination that is not an S3 bucket"}
	}
	return nil
}

// zeroConcurrencyMessage is the ErrorMessage of the dead letter of an
// event a function with zero reserved concurrency never ran
// (aws-recorded, lambda-destinations-layers.md 5.2: LDL-04).
const zeroConcurrencyMessage = "Function not invoked. Function isconfigured with zero reserved concurrent executions"

// DeadLetter is the outbox delivery of an event that failed for good to
// the function's dead-letter queue or topic target
// (lambda-destinations-layers.md 5.2, 11.3.7): the event as invoked, byte
// for byte (finding 1), with the attributes ErrorCode Number "200" and
// ErrorMessage, the first 1 KB of the error's message, for an event the
// function ran, and RequestID for every one. An event that never ran under
// cond ZeroReservedConcurrency gets AWS's ErrorMessage and no ErrorCode
// (aws-recorded, LDL-04); what AWS sends for one that aged out unrun is
// unrecorded, so it gets RequestID alone. One attempt, as the execution
// role; a failure loses the event (5.4). It refuses, with a *WrapError, a
// target that is neither SQS nor SNS (5.1) or is in a scope understudy
// does not serve (served), and a nil inv.
func DeadLetter(target aws.ARN, s Sender, requestID ids.LambdaRequestID, cond Condition, inv Invocation, payload []byte) (delivery.Request, error) {
	var attrs []delivery.MessageAttribute
	switch inv := inv.(type) {
	case nil:
		return delivery.Request{}, &WrapError{Reason: "the invocation is nil; a dead letter says whether the function ran (Ran) or not (NeverRan)"}
	case Ran:
		// ErrorCode is the invoke's 200, not an error status
		// (test_lambda_destinations.snapshot.json:L251-L264).
		attrs = append(attrs, delivery.MessageAttribute{Name: "ErrorCode", Value: delivery.NumberAttribute{Value: "200"}})
		if msg := ErrorMessage(inv.Response.Payload); msg != "" {
			attrs = append(attrs, delivery.MessageAttribute{Name: "ErrorMessage", Value: delivery.StringAttribute{Value: msg}})
		}
	case NeverRan:
		if cond == ZeroReservedConcurrency {
			// "isconfigured" as AWS sends it (lambda-destinations-layers.md 5.2).
			attrs = append(attrs, delivery.MessageAttribute{Name: "ErrorMessage", Value: delivery.StringAttribute{Value: zeroConcurrencyMessage}})
		}
	}
	attrs = append(attrs, delivery.MessageAttribute{Name: "RequestID", Value: delivery.StringAttribute{Value: string(requestID)}})
	req := delivery.Request{
		Source: s.Function,
		Policy: delivery.None{},
		Sender: &delivery.LambdaSender{Role: s.Role, Carries: delivery.DeadLetter},
	}
	switch target.Service {
	case "sqs":
		req.Mechanism, req.Target = delivery.LambdaToSQS, delivery.SQSQueue{ARN: target, Body: string(payload), Attributes: attrs, TraceHeader: s.Trace}
	case "sns":
		// The same attributes as SNS message attributes, inferred from the
		// guide's "as-is, with additional information in attributes" for a
		// queue or topic alike (5.2, LDL-05).
		req.Mechanism, req.Target = delivery.LambdaToSNS, delivery.SNSTopic{ARN: target, Message: string(payload), Attributes: attrs}
	default:
		return delivery.Request{}, &WrapError{Reason: target.String() + " is neither an SQS queue nor an SNS topic, the only dead-letter targets (lambda-destinations-layers.md 5.1)"}
	}
	if err := served(target); err != nil {
		return delivery.Request{}, err
	}
	return req, nil
}

// served refuses a target ARN whose partition, account or region
// understudy cannot hold. The outbox refuses such a delivery too, but only
// after the write that ended the event has committed, so the event and the
// write's later deliveries would be lost without a trace; refused here, it
// is logged like any other delivery Lambda cannot build.
// DeadLetterConfig.TargetArn's pattern admits such an ARN
// (lambda-destinations-layers.md 5.1), and a snapshot can hold one.
func served(target aws.ARN) *WrapError {
	if _, err := aws.ScopeOf(target); err != nil {
		return &WrapError{Reason: target.String() + " names a partition, account or region understudy does not serve, so nothing can receive it: " + err.Error()}
	}
	return nil
}

// maxErrorMessage is "the first 1 KB of the error message": 1,024 bytes
// until LDL-03 records the cut (lambda-destinations-layers.md 5.2, 11.3.7).
const maxErrorMessage = 1024

// ErrorMessage is a dead letter's ErrorMessage attribute for a function
// error's response: the errorMessage member of the error object
// (lambda-destinations-layers.md 5.2), or the response as text when it is
// no object with a string errorMessage, cut to its first 1,024 bytes and
// moved back to a UTF-8 character boundary (11.3.7). Only the errorMessage
// case is recorded; the whole-response fallback is understudy's choice, and
// what AWS sends then is UNVERIFIED (5.2, LDL-03).
func ErrorMessage(response []byte) string {
	msg := string(response)
	var obj map[string]json.RawMessage
	if json.Unmarshal(response, &obj) == nil {
		var s *string
		if raw, ok := obj["errorMessage"]; ok && json.Unmarshal(raw, &s) == nil && s != nil {
			msg = *s
		}
	}
	if len(msg) <= maxErrorMessage {
		return msg
	}
	cut := maxErrorMessage
	// msg[cut] continuing a character means that character spans the cut;
	// it goes whole. A valid character is at most utf8.UTFMax bytes.
	for cut > maxErrorMessage-utf8.UTFMax && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut]
}

// WrapError reports a record or dead letter that cannot be delivered as
// asked.
type WrapError struct {
	Reason string
}

func (e *WrapError) Error() string {
	return "destination: cannot build the delivery: " + e.Reason
}
