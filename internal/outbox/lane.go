package outbox

import (
	"cmp"
	"slices"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/delivery"
)

// lane is one message group of a FIFO queue. SNS preserves the order of a
// FIFO topic's messages per subscription and group (sns.md 10), so the
// deliveries of a lane are attempted one at a time in the order the outbox
// took them (Seq): only the first pending one is armed, a retry keeps the
// lane blocked, and a terminal outcome arms the next. A group of a standard
// queue is a fair queue's tenant label and promises no order (sqs.md 8.5),
// so it forms no lane.
type lane struct {
	queue aws.ARN
	group string
}

// laneOf returns the lane of e, false when e is not a delivery to a FIFO
// message group and so is attempted on its own.
func laneOf(e *Entry) (lane, bool) {
	q, ok := e.Request.Target.(delivery.SQSQueue)
	if !ok || q.GroupID == "" || !isFIFO(q.ARN) {
		return lane{}, false
	}
	return lane{queue: q.ARN, group: q.GroupID}, true
}

// laneBusy reports whether st holds a pending delivery of l.
func (s *State) laneBusy(l lane) bool {
	for id := range s.Entries {
		en := s.Entries[id]
		if got, ok := laneOf(&en); ok && got == l {
			return true
		}
	}
	return false
}

// nextInLane returns the pending delivery of l the outbox took first,
// false when l has none.
func (s *State) nextInLane(l lane) (Entry, bool) {
	for _, en := range s.bySeq() {
		if got, ok := laneOf(en); ok && got == l {
			return *en, true
		}
	}
	return Entry{}, false
}

// armable returns, in acceptance order, the entries whose attempt timer is
// armed while they are pending: every entry outside a lane and the head of
// each lane, the entry nextInLane names.
func (s *State) armable() []*Entry {
	heads := map[lane]*Entry{}
	for _, en := range s.bySeq() {
		if l, ok := laneOf(en); ok && heads[l] == nil {
			heads[l] = en
		}
	}
	return slices.DeleteFunc(s.sorted(), func(en *Entry) bool {
		l, ok := laneOf(en)
		return ok && heads[l].ID != en.ID
	})
}

// sorted returns the entries in acceptance order: by Accepted, then by Seq.
func (s *State) sorted() []*Entry {
	out := s.entries()
	slices.SortFunc(out, func(a, b *Entry) int {
		return cmp.Or(a.Accepted.Compare(b.Accepted), cmp.Compare(a.Seq, b.Seq))
	})
	return out
}

// bySeq returns the entries in the order the outbox took them, which is
// the order Enqueue arms a lane in.
func (s *State) bySeq() []*Entry {
	out := s.entries()
	slices.SortFunc(out, func(a, b *Entry) int { return cmp.Compare(a.Seq, b.Seq) })
	return out
}

// entries returns a copy of every entry, unordered.
func (s *State) entries() []*Entry {
	out := make([]*Entry, 0, len(s.Entries))
	for id := range s.Entries {
		en := s.Entries[id]
		out = append(out, &en)
	}
	return out
}
