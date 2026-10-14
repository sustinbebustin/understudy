package sqs

import (
	"maps"
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of State in snapshot sections.
const stateVersion = 4

// State is the SQS state of one account and region: its queues by name,
// the names deleted recently and the message move tasks. Records are
// values, replaced on change and never changed in place
// (ARCHITECTURE.md 3.5): a write changes a working copy of a queue
// (Queue.clone), which copies what it changes on first change, and stores
// it back; what a handler takes out of a cell is a clone, so it never
// aliases the cell. A dead-letter queue lives in the same account and
// region as its sources, so a redrive or a move task changes one cell.
type State struct {
	Queues map[string]Queue `json:"queues"`
	// Deleted holds when each recently deleted queue name was deleted,
	// kept only while Options enforce QueueDeletedRecently. CreateQueue
	// drops the entries older than the wait.
	Deleted map[string]clock.Instant `json:"deleted,omitempty"`
	// Tasks are the message move tasks, oldest first.
	Tasks []MoveTask `json:"tasks,omitempty"`
}

func newState(aws.Scope) State { return State{Queues: map[string]Queue{}} }

// IsZero reports a scope without queues, tombstones or tasks, which
// snapshots skip.
func (s *State) IsZero() bool {
	return len(s.Queues) == 0 && len(s.Deleted) == 0 && len(s.Tasks) == 0
}

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*State)(nil)

// Queue is one standard or FIFO queue.
type Queue struct {
	Name       string            `json:"name"`
	Created    clock.Instant     `json:"created"`
	Modified   clock.Instant     `json:"modified"`
	Attributes Attributes        `json:"attributes"`
	Tags       map[string]string `json:"tags,omitempty"`
	// FIFO is the state only FIFO queues have; nil for a standard queue.
	FIFO *FIFO `json:"fifo,omitempty"`
	// Purged is when PurgeQueue last ran, zero before the first.
	Purged clock.Instant `json:"purged,omitzero"`
	// Messages are in enqueue order, which receives follow.
	Messages []Message `json:"messages,omitempty"`
	// borrowed reports that a working copy (clone) still shares Messages'
	// backing array with the stored record; message and remove copy it
	// before the first change in place. It is never persisted.
	borrowed bool
}

// clone returns a working copy of q for a write. The copy shares q's
// messages and FIFO caches until it first changes them in place (message,
// remove, DedupCache.put, FIFO.attempts), so a write pays for copying only
// what it changes: an enqueue appends past the length q's slice sees and
// copies no message, and an empty poll copies nothing. What else the copy
// shares, the tags, the attribute policies and each message's maps and
// pointers, is never changed in place, only replaced.
func (q *Queue) clone() Queue {
	c := *q
	c.borrowed = true
	if q.FIFO != nil {
		f := *q.FIFO
		f.Dedup.lend()
		f.attemptsBorrowed = true
		c.FIFO = &f
	}
	return c
}

// message returns message i of a working copy for a change in place.
func (q *Queue) message(i int) *Message {
	q.ownMessages()
	return &q.Messages[i]
}

// ownMessages copies the messages of a working copy while they are the
// stored record's.
func (q *Queue) ownMessages() {
	if q.borrowed {
		q.Messages = slices.Clone(q.Messages)
		q.borrowed = false
	}
}

// remove takes the messages at the ascending indices at out of a working
// copy in one pass. Removing the oldest messages only reslices, which
// leaves the stored record's slice as it was; any other removal copies
// the messages first while they are the stored record's.
func (q *Queue) remove(at []int) {
	if len(at) == 0 {
		return
	}
	if at[len(at)-1] == len(at)-1 {
		if !q.borrowed {
			clear(q.Messages[:len(at)])
		}
		q.Messages = q.Messages[len(at):]
		return
	}
	q.ownMessages()
	kept, next := at[0], 0
	for i := at[0]; i < len(q.Messages); i++ {
		if next < len(at) && at[next] == i {
			next++
			continue
		}
		q.Messages[kept] = q.Messages[i]
		kept++
	}
	clear(q.Messages[kept:])
	q.Messages = q.Messages[:kept]
}

// DedupScope is the scope of FIFO deduplication (sqs.md 8.2).
type DedupScope string

// DedupScope values.
const (
	DedupQueue        DedupScope = "queue"
	DedupMessageGroup DedupScope = "messageGroup"
)

// ThroughputLimit is a FIFO queue's FifoThroughputLimit.
type ThroughputLimit string

// ThroughputLimit values.
const (
	ThroughputPerQueue          ThroughputLimit = "perQueue"
	ThroughputPerMessageGroupID ThroughputLimit = "perMessageGroupId"
)

// FIFO is what a FIFO queue adds: its FIFO attributes, the sequence
// counter, the deduplication cache and the receive attempts.
type FIFO struct {
	ContentBasedDeduplication bool            `json:"contentBasedDeduplication"`
	DeduplicationScope        DedupScope      `json:"deduplicationScope"`
	FifoThroughputLimit       ThroughputLimit `json:"fifoThroughputLimit"`
	// CreatedScope is the deduplication scope the queue was created with.
	// It decides deduplication whatever DeduplicationScope reports after a
	// SetQueueAttributes, as AWS was recorded doing (sqs.md 8.2).
	CreatedScope DedupScope `json:"createdScope"`
	// Sequence is the last SequenceNumber issued, 0 before the first.
	Sequence uint64 `json:"sequence"`
	// Dedup maps a deduplication key (dedupKey) to the message it
	// accepted, for the deduplication window.
	Dedup DedupCache `json:"dedup,omitzero"`
	// Attempts maps a ReceiveRequestAttemptId to what it received, for
	// the same window. Retries of a receive are few, so a working copy
	// copies the whole map to change it.
	Attempts map[string]Attempt `json:"attempts,omitempty"`
	// attemptsBorrowed reports that a working copy (Queue.clone) still
	// shares Attempts with the stored record. It is never persisted.
	attemptsBorrowed bool
}

// attempts returns the receive attempts of a working copy for a change,
// copying them first while they are the stored record's.
func (f *FIFO) attempts() map[string]Attempt {
	switch {
	case f.Attempts == nil:
		f.Attempts = map[string]Attempt{}
	case f.attemptsBorrowed:
		f.Attempts = maps.Clone(f.Attempts)
	}
	f.attemptsBorrowed = false
	return f.Attempts
}

// dedupWindow is how long a deduplication id and a receive request attempt
// id are remembered (sqs.md 8.2 and 8.3).
const dedupWindow = 5 * time.Minute

// Dedup is one accepted deduplication id: the message it enqueued and when
// the window for it ends.
type Dedup struct {
	MessageID ids.MessageID `json:"messageId"`
	Sequence  string        `json:"sequence"`
	Expires   clock.Instant `json:"expires"`
}

// Attempt is what one ReceiveRequestAttemptId received: a retry within
// the window returns the same messages and handles while none of them
// was deleted, received again or had its visibility changed (sqs.md 8.3).
// A visibility change forgets the attempts that hold the message.
type Attempt struct {
	Expires  clock.Instant     `json:"expires"`
	Received []AttemptReceived `json:"received"`
}

// AttemptReceived is one message an attempt received and the receipt
// handle it got.
type AttemptReceived struct {
	ID      ids.MessageID     `json:"id"`
	Receipt ids.ReceiptHandle `json:"receipt"`
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
	// FIFO is what a FIFO queue's message carries; nil on a standard queue.
	FIFO *FIFOMessage `json:"fifo,omitempty"`
	// DeadLetterSource is the queue whose redrive policy moved the message
	// here, the DeadLetterQueueSourceArn system attribute; nil for a
	// message sent here.
	DeadLetterSource *queueRef `json:"deadLetterSource,omitzero"`
	// SenderID is the SenderId system attribute: the sending principal.
	SenderID string `json:"senderId"`
	// Enqueued is the journal event of the call that sent the message, the
	// cause of what its delivery leads to, and EnqueuedTrace that event's
	// trace. A saved state without enqueuedTrace loads it as zero, and
	// MessageCauses then reports Enqueued as the trace.
	Enqueued      ref.EventID   `json:"enqueuedEventId"`
	EnqueuedTrace ref.EventID   `json:"enqueuedTrace,omitzero"`
	Sent          clock.Instant `json:"sent"`
	// VisibleAt is when the message can next be received: the end of its
	// delay before the first receive, of its visibility timeout after one.
	VisibleAt clock.Instant `json:"visibleAt"`
	// Receives is what the message's receives left; nil before the first.
	Receives *Receives `json:"receives,omitempty"`
}

// FIFOMessage is what a message on a FIFO queue adds: its deduplication id
// and its SequenceNumber.
type FIFOMessage struct {
	DedupID  string `json:"dedupId"`
	Sequence string `json:"sequence"`
}

// Receives is a received message's history: how many receives took it,
// when the first did, and the latest one while its handle still reaches
// the message.
type Receives struct {
	Count int           `json:"count"`
	First clock.Instant `json:"first"`
	// Latest is the latest receive; nil once a redrive moved the message to
	// a dead-letter queue, where no handle reaches it.
	Latest *Receive `json:"latest,omitempty"`
}

// Receive is one receive of a message: the handle it issued, when, and
// whether HoldVisibility pins the message invisible whatever VisibleAt
// says (ARCHITECTURE.md 9, ESM in-flight visibility). A pin is the live
// poller's hold, not queue state, so snapshots leave it out: a loaded
// message follows VisibleAt and gets its visibility timer back (timersOf).
type Receive struct {
	Handle ids.ReceiptHandle `json:"handle"`
	At     clock.Instant     `json:"at"`
	Pinned bool              `json:"-"`
}

// Attribute is a message attribute value; String holds String and Number
// values, Binary binary ones.
type Attribute struct {
	DataType string `json:"dataType"`
	String   string `json:"string,omitempty"`
	Binary   []byte `json:"binary,omitempty"`
}

// visible reports whether m can be received at now.
func (m *Message) visible(now clock.Instant) bool {
	return !m.pinned() && m.VisibleAt.Compare(now) <= 0
}

// expired reports whether m is past the queue's retention at now,
// measured from its enqueue (sqs.md 7.3).
func (m *Message) expired(retention time.Duration, now clock.Instant) bool {
	return m.Sent.Add(retention).Compare(now) <= 0
}

// receiveCount is ApproximateReceiveCount: 0 before the first receive.
func (m *Message) receiveCount() int {
	if m.Receives == nil {
		return 0
	}
	return m.Receives.Count
}

// firstReceived is ApproximateFirstReceiveTimestamp: zero before the
// first receive.
func (m *Message) firstReceived() clock.Instant {
	if m.Receives == nil {
		return clock.Instant{}
	}
	return m.Receives.First
}

// latest returns the latest receive while its handle reaches m.
func (m *Message) latest() (Receive, bool) {
	if m.Receives == nil || m.Receives.Latest == nil {
		return Receive{}, false
	}
	return *m.Receives.Latest, true
}

// holds reports whether handle is the handle of m's latest receive.
func (m *Message) holds(handle string) bool {
	r, ok := m.latest()
	return ok && string(r.Handle) == handle
}

func (m *Message) pinned() bool {
	r, ok := m.latest()
	return ok && r.Pinned
}

// receive records a receive at now that issued handle. Like every change
// below, it replaces m.Receives rather than changing what it points at,
// which the stored record may share.
func (m *Message) receive(handle ids.ReceiptHandle, now clock.Instant) {
	r := Receives{First: now}
	if m.Receives != nil {
		r = *m.Receives
	}
	r.Count++
	r.Latest = &Receive{Handle: handle, At: now}
	m.Receives = &r
}

// pin pins m invisible, or releases it, on its latest receive.
func (m *Message) pin(pinned bool) {
	latest, ok := m.latest()
	if !ok {
		return
	}
	latest.Pinned = pinned
	r := *m.Receives
	r.Latest = &latest
	m.Receives = &r
}

// dropHandle forgets m's latest receive, keeping the count and the first
// receive, as a redrive does.
func (m *Message) dropHandle() {
	if m.Receives != nil {
		m.Receives = &Receives{Count: m.Receives.Count, First: m.Receives.First}
	}
}

// TaskState is where a message move task is in its lifecycle (sqs.md 9.3):
// only a running task has a next step, only a failed one a reason.
// understudy cancels at once, so a task is never seen CANCELLING.
//
//sumtype:decl
type TaskState interface {
	isTaskState()
	// status is the Status ListMessageMoveTasks reports.
	status() string
}

// TaskRunning is a task still moving messages; it moves its next ones at
// Next.
type TaskRunning struct {
	Next clock.Instant `json:"next"`
}

// TaskCompleted is a task that moved what it set out to, or found nothing
// left to move.
type TaskCompleted struct{}

// TaskCancelled is a task CancelMessageMoveTask stopped.
type TaskCancelled struct{}

// TaskFailed is a task stopped by an error whose code is Reason, its
// FailureReason.
type TaskFailed struct {
	Reason string `json:"reason"`
}

func (TaskRunning) isTaskState()   {}
func (TaskCompleted) isTaskState() {}
func (TaskCancelled) isTaskState() {}
func (TaskFailed) isTaskState()    {}

func (TaskRunning) status() string   { return "RUNNING" }
func (TaskCompleted) status() string { return "COMPLETED" }
func (TaskCancelled) status() string { return "CANCELLED" }
func (TaskFailed) status() string    { return "FAILED" }

// MoveTarget is where a message move task moves messages.
//
//sumtype:decl
type MoveTarget interface{ isMoveTarget() }

// ToSources moves each message back to the queue it was dead-lettered
// from, the task without a DestinationArn.
type ToSources struct{}

// ToQueue moves every message to Queue, the DestinationArn.
type ToQueue struct {
	Queue queueRef `json:"arn"`
}

func (ToSources) isMoveTarget() {}
func (ToQueue) isMoveTarget()   {}

// MoveTask is one message move task from a dead-letter queue.
type MoveTask struct {
	ID string `json:"id"`
	// Source is the dead-letter queue the task moves messages out of.
	Source queueRef   `json:"source"`
	Target MoveTarget `json:"target"`
	// Rate is MaxNumberOfMessagesPerSecond; nil when the caller left it to
	// SQS, which moves everything in one step.
	Rate    *int32        `json:"rate,omitempty"`
	State   TaskState     `json:"state"`
	Moved   int64         `json:"moved"`
	ToMove  int64         `json:"toMove"`
	Started clock.Instant `json:"started"`
}

// running reports whether t still moves messages.
func (t *MoveTask) running() bool {
	_, ok := t.State.(TaskRunning)
	return ok
}

// timerID is the id of t's sqs.move-task timer: timerID of its source
// queue and its id.
func (t *MoveTask) timerID() string {
	return timerID(t.Source, t.ID)
}

// codec is the State codec: jsonx with the TaskState and MoveTarget
// unions. The tables are static, so an error building one is a bug in it;
// the codec then fails every section with it.
func codec() store.Codec {
	states, err := jsonx.NewUnion("sqs.TaskState",
		jsonx.Variant[TaskState, TaskRunning]("running"),
		jsonx.Variant[TaskState, TaskCompleted]("completed"),
		jsonx.Variant[TaskState, TaskCancelled]("cancelled"),
		jsonx.Variant[TaskState, TaskFailed]("failed"),
	)
	if err != nil {
		return failingCodec(err)
	}
	targets, err := jsonx.NewUnion("sqs.MoveTarget",
		jsonx.Variant[MoveTarget, ToSources]("sources"),
		jsonx.Variant[MoveTarget, ToQueue]("queue"),
	)
	if err != nil {
		return failingCodec(err)
	}
	c := jsonx.NewCodec(states, targets)
	return store.Codec{Marshal: c.Marshal, Unmarshal: c.Unmarshal}
}

func failingCodec(err error) store.Codec {
	return store.Codec{
		Marshal:   func(any) ([]byte, error) { return nil, err },
		Unmarshal: func([]byte, any) error { return err },
	}
}
