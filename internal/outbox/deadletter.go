package outbox

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// ending is why a failed delivery is not attempted again.
type ending uint8

const (
	// refused: the target failed it with a client-side error, which is not
	// retried (sns.md 7.1, eb-rule-dlq).
	refused ending = iota + 1
	// exhausted: it failed with server-side errors until its retry policy
	// allowed no more attempts.
	exhausted
)

// failure is how the last attempt of a delivery that ends undelivered
// failed.
//
//sumtype:decl
type failure interface{ isFailure() }

// targetError is the error a target call answered.
type targetError struct{ err *awserr.Error }

// endpointError is an HTTP endpoint's failing answer or a failed exchange
// with it, which is no AWS error: reason says which, such as "HTTP 503
// Service Unavailable".
type endpointError struct{ reason string }

// senderRefusal is a delivery its sender refused before calling the
// target: a Scheduler invocation whose execution role could not be
// assumed or whose role session may not decrypt with the schedule's key,
// or an event bus target whose RoleArn could not be assumed
// (invocation.go). code is the dead letter's ERROR_CODE and message the
// Authorizer's denial.
type senderRefusal struct{ code, message string }

func (targetError) isFailure()   {}
func (endpointError) isFailure() {}
func (senderRefusal) isFailure() {}

// describeFailure renders f for a journal reason.
func describeFailure(f failure) string {
	switch f := f.(type) {
	case targetError:
		return describe(f.err)
	case endpointError:
		return f.reason
	case senderRefusal:
		return f.code + ": " + f.message
	}
	return "no failure was recorded"
}

// reason is the journal's account of why the delivery ended, f being how
// its last attempt failed.
func (e ending) reason(f failure) string {
	if e == exhausted {
		return "its retries are exhausted; the last attempt failed with " + describeFailure(f)
	}
	return "the target refused it with a client-side error, which is not retried: " + describeFailure(f)
}

// deadLetterMessage is what the failed delivery e leaves in the queue dlq,
// f being how its last attempt failed and end why it ended.
//
// A dead letter its producer built (Request.DLQMessage, SNS to Lambda's
// SQS envelope, sns.md 7.4) is sent as it is, its group and deduplication
// ids to a FIFO dead-letter queue only. Otherwise the body is the payload
// the target would have received. For SNS to SQS
// that is what the subscription would have received: the envelope, or the
// raw message with its attributes as SQS attributes (sns.md 7.4). A
// function's event and a topic's message become the body. An HTTP
// endpoint's dead letter takes SQS's form too (sns.http.dlq-body as-sqs):
// its body, with a raw notification's message attributes, which the POST
// never carried, as SQS attributes; that form is UNVERIFIED, and
// sns-http-delivery.md 6.5 gives it as the likely one (SNSH-08). No error
// attributes are added (8.8). Group and deduplication ids carry over to a
// FIFO dead-letter queue. An event bus target's body is the forwarded
// event, which deadLetterOf writes, since writing it can fail. An
// EventBridge dead letter also carries the failure as message attributes
// (eventBridgeAttributes). A Scheduler dead letter has its own form,
// which deadLetterOf writes (schedulerDeadLetter).
func deadLetterMessage(e *Entry, dlq aws.ARN, f failure, end ending) sqsMessage {
	m := sqsMessage{queue: dlq}
	if dm := e.Request.DLQMessage; dm != nil {
		m.body, m.attributes = dm.Body, dm.Attributes
		if isFIFO(dlq) {
			m.groupID, m.dedupID = dm.GroupID, dm.DedupID
		}
		return m
	}
	switch t := e.Request.Target.(type) {
	case delivery.SQSQueue:
		m.body, m.attributes = t.Body, t.Attributes
		if isFIFO(dlq) {
			m.groupID, m.dedupID = t.GroupID, t.DedupID
		}
	case delivery.LambdaFunction:
		m.body = string(t.Event)
	case delivery.SNSTopic:
		m.body, m.attributes = t.Message, t.Attributes
	case delivery.EventBus:
		// deadLetterOf writes the body.
	case delivery.S3Object:
		// Enqueue refuses a dead-letter queue for this delivery (checkDLQ):
		// the form of its dead letter is not researched.
	case delivery.HTTPEndpoint:
		m.body, m.attributes = string(t.Body), t.DeadLetterAttributes
	case delivery.StateMachine:
		// Only SchedulerToStates delivers to one, and its dead letter is
		// schedulerDeadLetter's.
		m.body = t.Input
	}
	if e.Request.Mechanism.Via() == ref.EventsTarget {
		m.attributes = slices.Concat(m.attributes, eventBridgeAttributes(e, f, end))
	}
	return m
}

// deadLetterOf is deadLetterMessage with an event bus target's body: the
// forwarded event as the destination bus would have received it
// (delivery.EventEntry.Envelope), the dead letter of an EventBridge target
// being the event it failed to deliver (eb-rule-dlq.html). The body is
// inferred: no recording shows a bus target's dead letter, and for a bus in
// another region it carries the id drawn for that region, which AWS
// assigns on arrival (eventbridge-buses.md 6.4) and so may never assign to
// an event that did not arrive (EBB-08). Enqueue takes a
// dead-letter queue for an EventBus delivery only under events-to-events
// (checkDLQ), whose one entry has an envelope (checkForwarding), so the
// error, an entry with no envelope, never comes from an accepted delivery.
// A Scheduler bus target's dead letter is its payload instead
// (schedulerDeadLetter).
func deadLetterOf(e *Entry, dlq aws.ARN, f failure, end ending) (sqsMessage, error) {
	if inv := e.Request.Invocation; inv != nil {
		return schedulerDeadLetter(e, inv, dlq, f, end)
	}
	m := deadLetterMessage(e, dlq, f, end)
	if t, ok := e.Request.Target.(delivery.EventBus); ok && e.Request.Invocation == nil {
		body, err := t.Entries[0].Envelope()
		if err != nil {
			return sqsMessage{}, err
		}
		m.body = string(body)
	}
	return m, nil
}

// eventBridgeAttributes are the String attributes EventBridge adds to a
// dead letter (eb-rule-dlq): RULE_ARN, TARGET_ARN, ERROR_CODE,
// ERROR_MESSAGE, EXHAUSTED_RETRY_CONDITION and RETRY_ATTEMPTS, the retries
// made before the delivery ended. AWS documents the names, the condition
// values and the error code vocabulary, not their data type or when the
// condition is absent: understudy sends every one as a String and leaves
// the condition out when the target refused the delivery outright, since
// then no retry limit was reached. A delivery refused before its target
// call, an event bus target whose RoleArn could not be assumed, carries
// the refusal's code, FAILED_TO_ASSUME_ROLE. No EventBridge mechanism delivers to an
// HTTP endpoint yet; its failure, a 429 included, is sent as
// ERROR_FROM_TARGET with its reason as the message. That code choice is
// UNVERIFIED: no research covers an EventBridge HTTP target's dead letter.
func eventBridgeAttributes(e *Entry, f failure, end ending) []delivery.MessageAttribute {
	code, message := "ERROR_FROM_TARGET", describeFailure(f)
	switch f := f.(type) {
	case targetError:
		code, message = eventBridgeErrorCode(f.err), errorMessage(f.err)
	case senderRefusal:
		code, message = f.code, f.message
	case endpointError:
	}
	attrs := []delivery.MessageAttribute{
		stringAttribute("RULE_ARN", e.Request.Source.String()),
		stringAttribute("TARGET_ARN", deliveredTo(e.Request).String()),
		stringAttribute("ERROR_CODE", code),
		stringAttribute("ERROR_MESSAGE", message),
	}
	if p, ok := e.Request.Policy.(delivery.EventBridgeTarget); ok && end == exhausted {
		attrs = append(attrs, stringAttribute("EXHAUSTED_RETRY_CONDITION", exhaustedCondition(e, p)))
	}
	return append(attrs, stringAttribute("RETRY_ATTEMPTS", strconv.Itoa(e.Attempt)))
}

func stringAttribute(name, value string) delivery.MessageAttribute {
	return delivery.MessageAttribute{Name: name, Value: delivery.StringAttribute{Value: value}}
}

// errorMessage is a target error's message, its shape name when it has
// none.
func errorMessage(err *awserr.Error) string {
	if err.Message == "" {
		return err.ID.Shape.Name()
	}
	return err.Message
}

// schedulerDeadLetterLimit is the size a Scheduler dead letter's body is
// cut to, SQS's 256 KiB: Scheduler "truncates the payload you specify in
// the Input attribute" when the dead-letter event exceeds the SQS maximum
// (aws-doc monitoring-cloudwatch; scheduler.md 7.2, 14.5.3).
const schedulerDeadLetterLimit = 262_144

// schedulerDeadLetter is the dead letter of the Scheduler invocation inv
// in e (scheduler.md 7.2, 14.5.3), f being how its last attempt failed and
// end why it ended. Its body is schedulerDeadLetterBody. The String
// attributes are those SCH-08 recorded (2026-11-03), in name order:
// CONTAINS_INVALID_PAYLOAD false, the target's error, EXECUTION_ID,
// EXHAUSTED_RETRY_CONDITION only when retries ran out (absent for the
// permanent errors recorded, as the aws-doc reserves it for retryable
// ones), IS_PAYLOAD_TRUNCATED, RETRY_ATTEMPTS, SCHEDULED_TIME,
// SCHEDULE_ARN and TARGET_ARN, the target as configured.
// CONTAINS_INVALID_PAYLOAD is never true here: what makes AWS set it is
// unrecorded.
func schedulerDeadLetter(e *Entry, inv *delivery.SchedulerInvocation, dlq aws.ARN, f failure, end ending) (sqsMessage, error) {
	body, truncated, err := schedulerDeadLetterBody(e.Request.Target, inv.Payload(e.Attempt+1))
	if err != nil {
		return sqsMessage{}, err
	}
	code, message := schedulerError(f)
	attrs := []delivery.MessageAttribute{
		stringAttribute("CONTAINS_INVALID_PAYLOAD", "false"),
		stringAttribute("ERROR_CODE", code),
		stringAttribute("ERROR_MESSAGE", message),
		stringAttribute("EXECUTION_ID", inv.ExecutionID),
	}
	if p, ok := e.Request.Policy.(delivery.EventBridgeTarget); ok && end == exhausted {
		attrs = append(attrs, stringAttribute("EXHAUSTED_RETRY_CONDITION", exhaustedCondition(e, p)))
	}
	attrs = append(attrs,
		stringAttribute("IS_PAYLOAD_TRUNCATED", strconv.FormatBool(truncated)),
		stringAttribute("RETRY_ATTEMPTS", strconv.Itoa(e.Attempt)),
		stringAttribute("SCHEDULED_TIME", inv.Scheduled.UTC().Format("2006-01-02T15:04:05Z")),
		stringAttribute("SCHEDULE_ARN", inv.ScheduleARN.String()),
		stringAttribute("TARGET_ARN", inv.TargetARN),
	)
	return sqsMessage{queue: dlq, body: body, attributes: attrs}, nil
}

// schedulerDeadLetterBody is a Scheduler dead letter's body for the failed
// attempt's payload, and whether the payload was cut. For an SQS target it
// is the SendMessage request Scheduler built, {"MessageBody", then
// "MessageGroupId" for a FIFO queue, then "QueueUrl"}, compact (SCH-08
// 08-recv-dlq-*, recorded 2026-11-03; scheduler.md 7.2); for any other
// target it is the payload, a bus target's being its entry's Detail, never
// an envelope. UNVERIFIED (SCH-08 recorded SQS targets only): the other
// targets' bodies are inferred. A body over schedulerDeadLetterLimit has
// its payload cut at a whole UTF-8 character until the body fits, which
// SQS accepts, and IS_PAYLOAD_TRUNCATED says so.
func schedulerDeadLetterBody(target delivery.Target, payload string) (body string, cut bool, err error) {
	q, ok := target.(delivery.SQSQueue)
	if !ok {
		body, cut = truncateUTF8(payload, schedulerDeadLetterLimit)
		return body, cut, nil
	}
	body, err = sqsRequestBody(payload, q)
	for err == nil && len(body) > schedulerDeadLetterLimit && payload != "" {
		payload, _ = truncateUTF8(payload, max(0, len(payload)-(len(body)-schedulerDeadLetterLimit)))
		body, err = sqsRequestBody(payload, q)
		cut = true
	}
	return body, cut, err
}

// sqsRequestBody is the SendMessage request a Scheduler SQS target sends
// with body: <, > and & are written as they are, as AWS's dead letters
// carry them (encoding/json escapes them by default).
func sqsRequestBody(body string, q delivery.SQSQueue) (string, error) {
	var out strings.Builder
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(struct {
		MessageBody    string `json:"MessageBody"`
		MessageGroupID string `json:"MessageGroupId,omitempty"`
		QueueURL       string `json:"QueueUrl"`
	}{body, q.GroupID, queueURL(q.ARN)}); err != nil {
		return "", fmt.Errorf("outbox: writing a Scheduler dead letter's SendMessage request: %w", err)
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}

// schedulerError is a Scheduler dead letter's ERROR_CODE and
// ERROR_MESSAGE: the target's own code in the form its clients see (the
// awsQuery code, AWS.SimpleQueueService.NonExistentQueue for a missing
// queue; the shape name otherwise) and its message (scheduler.md 7.2,
// 14.5.3). An endpoint failure got no AWS answer; no Scheduler mechanism
// delivers to an endpoint (checkTarget), and it would read as Scheduler's
// own failure, AWS.Scheduler.InternalServerError (aws-doc
// configuring-schedule-dlq).
func schedulerError(f failure) (code, message string) {
	switch f := f.(type) {
	case targetError:
		code = f.err.ID.QueryCode
		if code == "" {
			code = f.err.ID.Shape.Name()
		}
		return code, errorMessage(f.err)
	case endpointError:
		return "AWS.Scheduler.InternalServerError", f.reason
	case senderRefusal:
		return f.code, f.message
	}
	return "AWS.Scheduler.InternalServerError", describeFailure(f)
}

// truncateUTF8 is s cut to at most limit bytes at a character boundary,
// and whether it was cut.
func truncateUTF8(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// eventBridgeErrorCode maps a target error onto EventBridge's dead-letter
// error codes: delivery.EventBridgeDeniedCode for a refusal by the
// target's policy, THROTTLING for a throttling error or a 429, NO_RESOURCE
// for a missing queue, function or topic, NO_PERMISSIONS for any other
// access denial, and ERROR_FROM_TARGET for anything else the target
// answered. AWS lists the codes, not this mapping.
func eventBridgeErrorCode(err *awserr.Error) string {
	if denied(err) {
		return delivery.EventBridgeDeniedCode
	}
	id := err.ID
	name := id.Shape.Name()
	switch {
	case id.Status == 429 || strings.Contains(name, "Throttl"):
		return "THROTTLING"
	case id.Status == 404 || name == "QueueDoesNotExist":
		return "NO_RESOURCE"
	case id.Status == 403 || strings.HasPrefix(name, "AccessDenied") || name == "AuthorizationErrorException":
		return "NO_PERMISSIONS"
	}
	return "ERROR_FROM_TARGET"
}
