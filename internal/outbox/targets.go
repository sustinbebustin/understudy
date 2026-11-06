package outbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/egress"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// send makes the attempt's target call and returns its outcome; false when
// the kernel stopped first. Every target but an HTTP endpoint is called
// through the kernel.
func (a *attempt) send(ctx context.Context, t delivery.Target) (journal.DeliveryOutcome, bool) {
	switch t := t.(type) {
	case delivery.SQSQueue:
		return a.end(ctx, a.sendSQS(ctx, a.as(a.scope), sqsMessage{
			queue: t.ARN, body: t.Body, attributes: t.Attributes, groupID: t.GroupID, dedupID: t.DedupID, traceHeader: t.TraceHeader,
		}))
	case delivery.LambdaFunction:
		return a.end(ctx, a.invokeLambda(ctx, t))
	case delivery.SNSTopic:
		return a.end(ctx, a.publishSNS(ctx, t))
	case delivery.EventBus:
		return a.end(ctx, putEvents(ctx, a.e.k, a.c, a.as(a.scope), t))
	case delivery.StateMachine:
		return a.end(ctx, startExecution(ctx, a.e.k, a.c, a.as(a.scope), t))
	case delivery.S3Object:
		if a.entry.Request.Mechanism == delivery.LambdaToS3 {
			// Lambda's execution role needs s3:ListBucket beside s3:PutObject
			// (lambda-destinations-layers.md 2.5): the bucket is checked
			// first, and a failed check puts nothing.
			if err := headBucket(ctx, a.e.k, a.c, a.as(a.scope), t.Bucket); err != nil {
				return a.end(ctx, err)
			}
		}
		return a.end(ctx, putObject(ctx, a.e.k, a.c, a.as(a.scope), t))
	case delivery.HTTPEndpoint:
		elapsed := a.e.k.Clock().Stopwatch()
		r := postHTTP(ctx, a.e.egress, t)
		a.took = elapsed()
		return a.endHTTP(ctx, r, a.took)
	}
	return a.end(ctx, awserr.Internalf(&RequestError{Field: "Target", Reason: "is nil"}))
}

// putEvents is the target call of an attempt of t: one events:PutEvents
// through the kernel k, as the call c makes it, run as kt, with one entry
// per entry of t in order. EventBusName takes the bus's ARN
// (https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutEventsRequestEntry.html).
// A new event is sent without Time, so the call's own time stamps it. A
// forwarded event (events-to-events, which carries exactly one entry) is
// sent with its own time, and its envelope rides the call's context
// (delivery.WithForwarded), since the wire input has no id, account or
// region member: the receiving bus keeps all four (eventbridge-buses.md
// 10.4.12).
//
// PutEvents reports a failed entry in its answer, not as an error
// (https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutEvents.html),
// so an answer with failed entries is the attempt's error, classed by the
// first failed entry's code (entryError). A delivery carries one entry
// (checkEventBus), so that class is the whole attempt's, and a retry puts
// no entry twice.
func putEvents(ctx context.Context, k *kernel.Kernel, c *kernel.Call, kt kernel.Target, t delivery.EventBus) *awserr.Error {
	bus := t.ARN.String()
	in := &eventbridgeapi.PutEventsInput{Entries: make([]eventbridgeapi.PutEventsRequestEntry, len(t.Entries))}
	for i, e := range t.Entries {
		in.Entries[i] = eventbridgeapi.PutEventsRequestEntry{
			Source: &e.Source, DetailType: &e.DetailType, Detail: &e.Detail,
			Resources: slices.Clone(e.Resources), EventBusName: &bus,
		}
		if e.Forwarded != nil {
			at := e.Forwarded.Time
			in.Entries[i].Time = &at
		}
	}
	call := ctx
	if len(t.Entries) == 1 && t.Entries[0].Forwarded != nil {
		call = delivery.WithForwarded(ctx, *t.Entries[0].Forwarded)
	}
	out, err := kernel.Invoke(call, k, c, eventbridgeapi.OpPutEvents, kt, in)
	if err != nil {
		return err
	}
	if out.FailedEntryCount == 0 {
		return nil
	}
	reason := strconv.Itoa(int(out.FailedEntryCount)) + " of " + strconv.Itoa(len(t.Entries)) + " entries failed"
	code := ""
	if i := slices.IndexFunc(out.Entries, func(e eventbridgeapi.PutEventsResultEntry) bool { return e.ErrorCode != nil }); i >= 0 {
		code = deref(out.Entries[i].ErrorCode)
		reason += "; the first with " + code + ": " + deref(out.Entries[i].ErrorMessage)
	}
	return entryError(code, reason)
}

// entryError is the attempt's error for a PutEvents answer whose first
// failed entry has code, reason describing it.
//
// The codes AWS lists as non-retryable
// (https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutEventsResultEntry.html)
// fail the delivery outright: no error shape models an entry code, so
// ValidationException, which is not retried, stands in and reason names
// the code. NotImplemented, the code understudy's PutEvents gives an entry
// for a bus it does not deliver to (internal/service/events putevents.go,
// foreignRegion), is unimplemented, so the delivery is dropped as a 501
// is. Any other code, the listed retryable InternalFailure and
// ThrottlingException among them, is an InternalFailure, which the outbox
// retries.
func entryError(code, reason string) *awserr.Error {
	switch code {
	case "AccessDeniedException", "InvalidAccountIdException", "InvalidArgument", "MalformedDetail",
		"RedactionFailure", "NotAuthorizedForSourceException", "NotAuthorizedForDetailTypeException":
		return eventbridgeapi.ErrValidationException(reason)
	case "NotImplemented":
		return &awserr.Error{ID: awserr.NotImplemented(), Message: reason, Origin: awserr.Unimplemented{Op: eventbridgeapi.OpPutEvents.Descriptor().ID}}
	}
	return &awserr.Error{ID: awserr.InternalFailure(), Message: reason, Origin: awserr.Generic{}}
}

// deref is *p, or "" for nil.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// startExecution is the target call of an attempt of t: one
// states:StartExecution through the kernel k, as the call c makes it, run
// as kt, of t's state machine with its input and execution name
// (scheduler.md 6.4).
func startExecution(ctx context.Context, k *kernel.Kernel, c *kernel.Call, kt kernel.Target, t delivery.StateMachine) *awserr.Error {
	in := &sfnapi.StartExecutionInput{StateMachineArn: t.ARN.String(), Name: &t.Name, Input: &t.Input}
	_, err := kernel.Invoke(ctx, k, c, sfnapi.OpStartExecution, kt, in)
	return err
}

// putObject is the target call of an attempt of t: one s3:PutObject
// through the kernel k, as the call c makes it, run as kt, of t's body
// with its length.
func putObject(ctx context.Context, k *kernel.Kernel, c *kernel.Call, kt kernel.Target, t delivery.S3Object) *awserr.Error {
	length := int64(len(t.Body))
	in := &s3api.PutObjectInput{Bucket: t.Bucket, Key: t.Key, Body: objectBody(t.Body), ContentLength: &length}
	_, err := kernel.Invoke(ctx, k, c, s3api.OpPutObject, kt, in)
	return err
}

// headBucket is one s3:HeadBucket of bucket through the kernel k, as the
// call c makes it, run as kt; its IAM action is s3:ListBucket.
func headBucket(ctx context.Context, k *kernel.Kernel, c *kernel.Call, kt kernel.Target, bucket string) *awserr.Error {
	_, err := kernel.Invoke(ctx, k, c, s3api.OpHeadBucket, kt, &s3api.HeadBucketInput{Bucket: bucket})
	return err
}

// objectBody is a PutObject body held in memory: a smithy.Body of known
// length with no trailers.
type objectBody []byte

func (b objectBody) Open() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }

func (b objectBody) Len() (int64, bool) { return int64(len(b)), true }

func (objectBody) Trailer(string) (string, bool) { return "", false }

// postHTTP POSTs t's body to its URL with its headers through client, the
// one outbound dialer, and classifies the answer under
// sns.http.status-classes, the reading sns-http-delivery.md 6.1
// recommends: 2xx accepted; 429, 5xx and a timed-out exchange retried; any
// other status permanent (statusResult). Two of those classes are
// UNVERIFIED there: a connection-level failure (refused, reset, DNS or
// TLS) is retried under the server-side reading (SNSH-24), and 3xx and 4xx
// other than 429 are permanent (SNSH-05). A redirect is not followed: its
// 3xx is the answer (sns.http.redirects). A request client refuses, or any
// request when client is nil, is refused (7.3).
func postHTTP(ctx context.Context, client *egress.Client, t delivery.HTTPEndpoint) httpResult {
	if client == nil {
		return httpRefused{reason: "outbound HTTP delivery is off: no allow-list enables it, so nothing was sent to " + endpointOf(t)}
	}
	header := make(http.Header, len(t.Headers))
	for _, h := range t.Headers {
		header[h.Name] = append(header[h.Name], h.Value)
	}
	u := t.URL
	res, err := client.Send(ctx, egress.Request{Method: http.MethodPost, URL: &u, Header: header, Body: t.Body})
	if re, ok := errors.AsType[*egress.RefusedError](err); ok {
		return httpRefused{reason: re.Error()}
	}
	if err != nil {
		return httpRetry{reason: err.Error()}
	}
	return statusResult(res.Status)
}

// statusResult classes an answer of status s, the endpoint's or one a
// fault rule injected, as postHTTP does.
func statusResult(s int) httpResult {
	switch {
	case s >= 200 && s < 300:
		return httpAccepted{status: s}
	case s == http.StatusTooManyRequests || s >= 500:
		return httpRetry{status: s, reason: statusText(s)}
	default:
		return httpPermanent{status: s}
	}
}

// exchangeOf is the journal's account of an HTTP attempt that ended as r
// after took of wall time.
func exchangeOf(r httpResult, took time.Duration) *journal.HTTPExchange {
	x := &journal.HTTPExchange{Duration: took}
	switch r := r.(type) {
	case httpAccepted:
		x.Status, x.Class = r.status, journal.HTTPSuccess
	case httpRetry:
		x.Status, x.Class = r.status, journal.HTTPRetryable
	case httpPermanent:
		x.Status, x.Class = r.status, journal.HTTPPermanent
	case httpRefused:
		x.Class = journal.HTTPRefused
	}
	return x
}

// statusText names an HTTP status for a journal reason: "HTTP 503 Service
// Unavailable".
func statusText(status int) string {
	text := "HTTP " + strconv.Itoa(status)
	if name := http.StatusText(status); name != "" {
		text += " " + name
	}
	return text
}

// endpointOf is the journal's Endpoint of a delivery to t: an HTTP
// endpoint's HostPort; "" for every other target.
func endpointOf(t delivery.Target) string {
	h, ok := t.(delivery.HTTPEndpoint)
	if !ok {
		return ""
	}
	return h.HostPort()
}

// httpResult is how an HTTPEndpoint attempt ended.
//
//sumtype:decl
type httpResult interface{ isHTTPResult() }

// httpAccepted is a 2xx answer: the delivery is done.
type httpAccepted struct{ status int }

// httpRetry is a 429 or 5xx answer, or an exchange that failed or timed
// out, whose status is 0: retried under the delivery's policy.
type httpRetry struct {
	status int
	reason string
}

// httpPermanent is a 1xx, 3xx or other 4xx answer: not retried.
type httpPermanent struct{ status int }

// httpRefused is a request egress refused, or one with no client to send
// it: never retried or dead-lettered (journal.Refused).
type httpRefused struct{ reason string }

func (httpAccepted) isHTTPResult()  {}
func (httpRetry) isHTTPResult()     {}
func (httpPermanent) isHTTPResult() {}
func (httpRefused) isHTTPResult()   {}

// operationOf is the operation a delivery to t calls; its IAM actions are
// what the target's resource policy must allow. It is nil for an HTTP
// endpoint, which no AWS operation reaches, and for a nil target.
func operationOf(t delivery.Target) *registry.Operation {
	switch t.(type) {
	case delivery.SQSQueue:
		return sqsapi.OpSendMessage.Descriptor()
	case delivery.LambdaFunction:
		return lambdaapi.OpInvoke.Descriptor()
	case delivery.SNSTopic:
		return snsapi.OpPublish.Descriptor()
	case delivery.EventBus:
		return eventbridgeapi.OpPutEvents.Descriptor()
	case delivery.StateMachine:
		return sfnapi.OpStartExecution.Descriptor()
	case delivery.S3Object:
		return s3api.OpPutObject.Descriptor()
	case delivery.HTTPEndpoint:
		return nil
	}
	return nil
}

// invokeLambda invokes the function with the delivery's event, with the
// invocation type the target names (Event for SNS, S3 and EventBridge).
func (a *attempt) invokeLambda(ctx context.Context, f delivery.LambdaFunction) *awserr.Error {
	invocation := lambdaapi.InvocationType(f.Invocation.String())
	in := &lambdaapi.InvokeInput{FunctionName: f.ARN.String(), InvocationType: &invocation, Payload: f.Event}
	if f.Qualifier != "" {
		in.Qualifier = &f.Qualifier
	}
	_, err := kernel.Invoke(ctx, a.e.k, a.c, lambdaapi.OpInvoke, a.as(a.scope), in)
	return err
}

// publishSNS publishes the delivery's message to the topic.
func (a *attempt) publishSNS(ctx context.Context, t delivery.SNSTopic) *awserr.Error {
	topic := t.ARN.String()
	in := &snsapi.PublishInput{TopicArn: &topic, Message: t.Message}
	if t.Subject != "" {
		in.Subject = &t.Subject
	}
	if t.GroupID != "" {
		in.MessageGroupId = &t.GroupID
	}
	if t.DedupID != "" {
		in.MessageDeduplicationId = &t.DedupID
	}
	if len(t.Attributes) > 0 {
		in.MessageAttributes = make(map[string]snsapi.MessageAttributeValue, len(t.Attributes))
		for _, attr := range t.Attributes {
			in.MessageAttributes[attr.Name] = snsAttribute(attr.Value)
		}
	}
	_, err := kernel.Invoke(ctx, a.e.k, a.c, snsapi.OpPublish, a.as(a.scope), in)
	return err
}

// snsAttribute is a message attribute as SNS takes it.
func snsAttribute(v delivery.AttributeValue) snsapi.MessageAttributeValue {
	str, bin := attributeValues(v)
	return snsapi.MessageAttributeValue{DataType: v.DataType(), StringValue: str, BinaryValue: bin}
}

// attributeValues splits v into the StringValue and BinaryValue members
// that SNS and SQS message attributes share; the one v does not use is nil.
func attributeValues(v delivery.AttributeValue) (str *string, bin []byte) {
	switch v := v.(type) {
	case delivery.StringAttribute:
		return &v.Value, nil
	case delivery.NumberAttribute:
		return &v.Value, nil
	case delivery.BinaryAttribute:
		return nil, v.Value
	}
	return nil, nil
}

// sqsMessage is a SendMessage of a delivery or of its dead letter.
type sqsMessage struct {
	queue      aws.ARN
	body       string
	attributes []delivery.MessageAttribute
	// groupID and dedupID are set for FIFO queues only.
	groupID, dedupID string
	// traceHeader is sent as the AWSTraceHeader message system attribute
	// when not empty.
	traceHeader string
}

// sendSQS sends m to its queue, as and in the scope t names.
func (a *attempt) sendSQS(ctx context.Context, t kernel.Target, m sqsMessage) *awserr.Error {
	in := &sqsapi.SendMessageInput{QueueUrl: queueURL(m.queue), MessageBody: m.body}
	if len(m.attributes) > 0 {
		in.MessageAttributes = make(map[string]sqsapi.MessageAttributeValue, len(m.attributes))
		for _, attr := range m.attributes {
			in.MessageAttributes[attr.Name] = sqsAttribute(attr.Value)
		}
	}
	if m.groupID != "" {
		in.MessageGroupId = &m.groupID
	}
	if m.dedupID != "" {
		in.MessageDeduplicationId = &m.dedupID
	}
	if m.traceHeader != "" {
		// AWSTraceHeader's data type is String (API_SendMessage.html).
		in.MessageSystemAttributes = map[sqsapi.MessageSystemAttributeNameForSends]sqsapi.MessageSystemAttributeValue{
			sqsapi.MessageSystemAttributeNameForSendsAWSTraceHeader: {DataType: "String", StringValue: &m.traceHeader},
		}
	}
	_, err := kernel.Invoke(ctx, a.e.k, a.c, sqsapi.OpSendMessage, t, in)
	return err
}

// queueURL is the AWS form of the queue's URL. SQS reads the account and
// name from the path and ignores the host.
func queueURL(queue aws.ARN) string {
	return "https://sqs." + queue.Region + ".amazonaws.com/" + queue.Account + "/" + queue.Resource
}

// sqsAttribute is an SNS or S3 message attribute as SQS takes it.
func sqsAttribute(v delivery.AttributeValue) sqsapi.MessageAttributeValue {
	str, bin := attributeValues(v)
	return sqsapi.MessageAttributeValue{DataType: v.DataType(), StringValue: str, BinaryValue: bin}
}
