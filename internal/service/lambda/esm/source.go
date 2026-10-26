package esm

import "github.com/sustinbebustin/understudy/internal/aws"

// EventSourceKind is the kind of source an event source mapping polls. SQS
// is the only kind today; the stream sources add their own variants with
// their settings.
//
//sumtype:decl
type EventSourceKind interface{ isEventSourceKind() }

// SQS is an SQS queue; the mapping's source is the queue's ARN.
type SQS struct{}

func (SQS) isEventSourceKind() {}

// SourceKindOf returns the kind of source an EventSourceArn names: SQS for
// an sqs ARN, and false for any other service, whose poller understudy
// does not have.
func SourceKindOf(source aws.ARN) (EventSourceKind, bool) {
	if source.Service == "sqs" {
		return SQS{}, true
	}
	return nil, false
}
