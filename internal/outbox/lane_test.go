package outbox

import (
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
)

// groupEntry is a pending delivery to message group g of queue, taken
// seq-th and accepted at.
func groupEntry(id, queue string, seq uint64, at clock.Instant) Entry {
	return Entry{
		ID: ids.DeliveryID(id),
		Request: delivery.Request{
			Mechanism: delivery.SNSToSQS,
			Target:    delivery.SQSQueue{ARN: aws.ARN{Partition: "aws", Service: "sqs", Region: "us-east-1", Account: "000000000000", Resource: queue}, GroupID: "g"},
			Policy:    delivery.SNSManaged{},
		},
		NextAt:   at,
		Accepted: at,
		Seq:      seq,
	}
}

// A lane's head is the delivery the outbox took first, the one Enqueue
// armed, even when a delivery taken later was accepted earlier: a reload
// then rearms the delivery that was already in flight. A message group of
// a standard queue is no lane, so each of its deliveries is armed.
func TestLaneHeadIsTheFirstTaken(t *testing.T) {
	t0 := clock.FromWire(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))
	first := groupEntry("taken-first", "orders.fifo", 1, t0.Add(time.Millisecond))
	st := newState(aws.Scope{})
	for _, e := range []Entry{
		groupEntry("taken-second", "orders.fifo", 2, t0),
		first,
		groupEntry("std-1", "orders", 3, t0),
		groupEntry("std-2", "orders", 4, t0),
	} {
		st.Entries[e.ID] = e
	}
	l, ok := laneOf(&first)
	if !ok {
		t.Fatal("a FIFO queue's message group forms no lane")
	}
	if head, ok := st.nextInLane(l); !ok || head.ID != first.ID {
		t.Errorf("nextInLane = %q, %v; want %q", head.ID, ok, first.ID)
	}
	armable := st.armable()
	armed := make([]ids.DeliveryID, 0, len(armable))
	for _, en := range armable {
		armed = append(armed, en.ID)
	}
	if want := []ids.DeliveryID{"std-1", "std-2", "taken-first"}; !slices.Equal(armed, want) {
		t.Errorf("armable = %v, want %v in acceptance order", armed, want)
	}
}
