package sqs

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// stateVersion is the schema version of State in snapshot sections.
const stateVersion = 1

// State is the SQS state of one account and region: its queues by name.
// Records are values, replaced on change and never mutated in place, so a
// value read out of a cell never aliases the cell.
type State struct {
	Queues map[string]Queue `json:"queues"`
}

func newState(aws.Scope) State { return State{Queues: map[string]Queue{}} }

// IsZero reports a scope without queues, which snapshots skip.
func (s *State) IsZero() bool { return len(s.Queues) == 0 }

// Queue is one standard queue.
type Queue struct {
	Name       string            `json:"name"`
	Created    clock.Instant     `json:"created"`
	Modified   clock.Instant     `json:"modified"`
	Attributes Attributes        `json:"attributes"`
	Tags       map[string]string `json:"tags,omitempty"`
	// Messages are in enqueue order, which receives follow.
	Messages []Message `json:"messages,omitempty"`
}

// Message is one message in a queue.
type Message struct {
	ID   ids.MessageID `json:"id"`
	Body string        `json:"body"`
	// BodyMD5 is the hex MD5 of the body, as MD5OfMessageBody and MD5OfBody
	// carry it.
	BodyMD5    string               `json:"bodyMD5"`
	Attributes map[string]Attribute `json:"attributes,omitempty"`
	// System holds the system attributes the sender set (AWSTraceHeader).
	System  map[string]Attribute `json:"system,omitempty"`
	GroupID string               `json:"groupId,omitempty"`
	// SenderID is the SenderId system attribute: the sending principal.
	SenderID string `json:"senderId"`
	// Enqueued is the journal event of the call that sent the message, the
	// cause of what its delivery leads to.
	Enqueued ref.EventID   `json:"enqueuedEventId"`
	Sent     clock.Instant `json:"sent"`
	// VisibleAt is when the message can next be received: the end of its
	// delay before the first receive, of its visibility timeout after one.
	VisibleAt     clock.Instant `json:"visibleAt"`
	ReceiveCount  int           `json:"receiveCount"`
	FirstReceived clock.Instant `json:"firstReceived"`
	// Receipt is the handle of the latest receive, empty before the first.
	Receipt ids.ReceiptHandle `json:"receipt,omitempty"`
}

// Attribute is a message attribute value; String holds String and Number
// values, Binary binary ones.
type Attribute struct {
	DataType string `json:"dataType"`
	String   string `json:"string,omitempty"`
	Binary   []byte `json:"binary,omitempty"`
}

// visible reports whether m can be received at now.
func (m *Message) visible(now clock.Instant) bool { return m.VisibleAt.Compare(now) <= 0 }
