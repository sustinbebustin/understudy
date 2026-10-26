package outbox

import (
	"slices"
	"strconv"
	"strings"

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

func (targetError) isFailure()   {}
func (endpointError) isFailure() {}

// describeFailure renders f for a journal reason.
func describeFailure(f failure) string {
	switch f := f.(type) {
	case targetError:
		return describe(f.err)
	case endpointError:
		return f.reason
	}
	return "no failure was recorded"
}

// reason is the journal's account of why the delivery ended, f being how
// its last attempt failed.
func (e ending) reason(f failure) string {
	if e == exhausted {
		return "its retries are exhausted; the last attempt failed with " + describeFailure(f)
	}
	if ep, ok := f.(endpointError); ok {
		return "the endpoint's answer is a permanent failure, which is not retried: " + ep.reason
	}
	return "the target refused it with a client-side error, which is not retried: " + describeFailure(f)
}

// deadLetterMessage is what the failed delivery e leaves in the queue dlq,
// f being how its last attempt failed and end why it ended.
//
// The body is the payload the target would have received. For SNS to SQS
// that is what the subscription would have received: the envelope, or the
// raw message with its attributes as SQS attributes (sns.md 7.4). A
// function's event, a topic's message and an HTTP endpoint's body become
// the body. The HTTP form is UNVERIFIED: sns-http-delivery.md 6.5 gives
// SQS's as the likely one (SNSH-08). Group and deduplication ids carry
// over to a FIFO dead-letter queue. An event bus target's body is the
// forwarded event, which deadLetterOf writes, since writing it can fail. An
// EventBridge dead letter also carries the failure as message attributes
// (eventBridgeAttributes).
func deadLetterMessage(e *Entry, dlq aws.ARN, f failure, end ending) sqsMessage {
	m := sqsMessage{queue: dlq}
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
		m.body = string(t.Body)
	}
	if e.Request.Mechanism.Via() == ref.EventsTarget {
		m.attributes = slices.Concat(m.attributes, eventBridgeAttributes(e, f, end))
	}
	return m
}

// deadLetterOf is deadLetterMessage with an event bus target's body: the
// forwarded event as the destination bus would have received it
// (delivery.EventEntry.Envelope), the dead letter of an EventBridge target
// being the event it failed to deliver (eb-rule-dlq.html). Enqueue takes a
// dead-letter queue for an EventBus delivery only under events-to-events
// (checkDLQ), whose one entry has an envelope (checkForwarding), so the
// error, an entry with no envelope, never comes from an accepted delivery.
func deadLetterOf(e *Entry, dlq aws.ARN, f failure, end ending) (sqsMessage, error) {
	m := deadLetterMessage(e, dlq, f, end)
	if t, ok := e.Request.Target.(delivery.EventBus); ok {
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
// then no retry limit was reached. No EventBridge mechanism delivers to an
// HTTP endpoint yet; its failure, a 429 included, is sent as
// ERROR_FROM_TARGET with its reason as the message. That code choice is
// UNVERIFIED: no research covers an EventBridge HTTP target's dead letter.
func eventBridgeAttributes(e *Entry, f failure, end ending) []delivery.MessageAttribute {
	code, message := "ERROR_FROM_TARGET", describeFailure(f)
	if te, ok := f.(targetError); ok {
		code, message = eventBridgeErrorCode(te.err.ID), te.err.Message
		if message == "" {
			message = te.err.ID.Shape.Name()
		}
	}
	attrs := []delivery.MessageAttribute{
		eventBridgeAttribute("RULE_ARN", e.Request.Source.String()),
		eventBridgeAttribute("TARGET_ARN", deliveredTo(e.Request).String()),
		eventBridgeAttribute("ERROR_CODE", code),
		eventBridgeAttribute("ERROR_MESSAGE", message),
	}
	if p, ok := e.Request.Policy.(delivery.EventBridgeTarget); ok && end == exhausted {
		attrs = append(attrs, eventBridgeAttribute("EXHAUSTED_RETRY_CONDITION", exhaustedCondition(e, p)))
	}
	return append(attrs, eventBridgeAttribute("RETRY_ATTEMPTS", strconv.Itoa(e.Attempt)))
}

func eventBridgeAttribute(name, value string) delivery.MessageAttribute {
	return delivery.MessageAttribute{Name: name, Value: delivery.StringAttribute{Value: value}}
}

// eventBridgeErrorCode maps a target error onto EventBridge's dead-letter
// error codes: THROTTLING for a throttling error or a 429, NO_RESOURCE for
// a missing queue, function or topic, NO_PERMISSIONS for an access denial,
// and ERROR_FROM_TARGET for anything else the target answered. AWS lists
// the codes, not this mapping.
func eventBridgeErrorCode(id awserr.Identity) string {
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
