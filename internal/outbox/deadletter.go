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

// reason is the journal's account of why the delivery ended, err being the
// target's last error.
func (e ending) reason(err *awserr.Error) string {
	if e == exhausted {
		return "its retries are exhausted; the last attempt failed with " + describe(err)
	}
	return "the target refused it with a client-side error, which is not retried: " + describe(err)
}

// deadLetterMessage is what the failed delivery e leaves in the queue dlq,
// err being the target's last error and end why it ended.
//
// The body is the payload the target would have received. For SNS to SQS
// that is what the subscription would have received: the envelope, or the
// raw message with its attributes as SQS attributes (sns.md 7.4). A
// function's event and a topic's message become the body. Group and
// deduplication ids carry over to a FIFO dead-letter queue. An EventBridge
// dead letter also carries the failure as message attributes
// (eventBridgeAttributes).
func deadLetterMessage(e *Entry, dlq aws.ARN, err *awserr.Error, end ending) sqsMessage {
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
	}
	if e.Request.Mechanism.Via() == ref.EventsTarget {
		m.attributes = slices.Concat(m.attributes, eventBridgeAttributes(e, err, end))
	}
	return m
}

// eventBridgeAttributes are the String attributes EventBridge adds to a
// dead letter (eb-rule-dlq): RULE_ARN, TARGET_ARN, ERROR_CODE,
// ERROR_MESSAGE, EXHAUSTED_RETRY_CONDITION and RETRY_ATTEMPTS, the retries
// made before the delivery ended. AWS documents the names, the condition
// values and the error code vocabulary, not their data type or when the
// condition is absent: understudy sends every one as a String and leaves
// the condition out when the target refused the delivery outright, since
// then no retry limit was reached.
func eventBridgeAttributes(e *Entry, err *awserr.Error, end ending) []delivery.MessageAttribute {
	target, _ := targetARN(e.Request.Target)
	message := err.Message
	if message == "" {
		message = err.ID.Shape.Name()
	}
	attrs := []delivery.MessageAttribute{
		eventBridgeAttribute("RULE_ARN", e.Request.Source.String()),
		eventBridgeAttribute("TARGET_ARN", target.String()),
		eventBridgeAttribute("ERROR_CODE", eventBridgeErrorCode(err.ID)),
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
