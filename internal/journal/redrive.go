package journal

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/ids"
)

// Redrive is SQS moving messages from one queue to another inside the
// service: a receive past a source queue's maxReceiveCount moving them to
// its dead-letter queue, or a message move task's step. SQS records it with
// Tx.Record in the write that moves them, so its parent is that receive's
// call event or the step's TimerFired event (ADR 0007, amendment
// 2026-10-13). It is never a Delivery: delivery.Mechanism is the closed
// set of outbox paths, and an in-service move takes none of them.
type Redrive struct {
	From, To aws.ARN
	// Messages are the ids of the moved messages as From held them, in
	// From's order. A move task gives each moved message a new id in To.
	Messages []ids.MessageID
	Reason   RedriveReason
}

// RedriveReason is why a Redrive moved its messages.
//
//sumtype:decl
type RedriveReason interface{ isRedriveReason() }

// MaxReceiveCount is a receive that would have exceeded the source queue's
// redrive policy; Count is the policy's maxReceiveCount.
type MaxReceiveCount struct{ Count int }

// MoveTask is one step of a message move task; TaskHandle is the handle
// StartMessageMoveTask returned for it.
type MoveTask struct{ TaskHandle string }

func (MaxReceiveCount) isRedriveReason() {}
func (MoveTask) isRedriveReason()        {}
