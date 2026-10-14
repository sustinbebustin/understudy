package outbox

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TimerAttempt fires when a delivery's next attempt is due. The key's ID
// is "<account>/<region>/<delivery-id>", the scope being the target's.
const TimerAttempt kernel.TimerKind = "outbox.attempt"

// PolicySource reads the stored resource policy of a delivery target: an
// SQS queue's Policy attribute, a function's permissions, a topic's Policy.
type PolicySource interface {
	// ResourcePolicy returns target's stored policy, nil when it has none.
	// ok is false when target does not exist, which skips the advisory: the
	// attempt then fails on its own.
	ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool)
}

// Options configures Bind. A nil field disables its stage.
type Options struct {
	// Faults evaluates the Delivery-phase fault rules before each attempt.
	Faults kernel.FaultStage
	// Policies supplies target policies for the access advisory.
	Policies PolicySource
}

// Outbox is the delivery engine: the kernel's DeliverSink and a Stateful
// service with one cell of pending deliveries per target scope. Build it
// with New, hand it to kernel.Options.Deliver, then Bind it to that kernel
// before Run; the kernel needs the sink before the outbox can have the
// kernel.
type Outbox struct {
	// bind serializes Bind, so e is published only once fully built.
	bind sync.Mutex
	e    atomic.Pointer[engine]
}

// engine is a bound outbox.
type engine struct {
	k        *kernel.Kernel
	cells    *store.Partitioned[aws.Scope, State]
	attempt  kernel.HandledKind
	faults   kernel.FaultStage
	policies PolicySource
	// seq is the last Entry.Seq handed out; Import raises it past every
	// loaded entry's.
	seq atomic.Uint64
	// ready runs the attempts onAttempt hands over in firing order.
	ready sequencer
}

// The outbox is the kernel's delivery sink and is snapshotted like a
// service.
var (
	_ kernel.DeliverSink = (*Outbox)(nil)
	_ kernel.Stateful    = (*Outbox)(nil)
)

// New returns an unbound outbox, for kernel.Options.Deliver.
func New() *Outbox { return &Outbox{} }

// BindError reports a second Bind; the outbox stays bound to its first
// kernel.
type BindError struct{}

func (*BindError) Error() string {
	return "outbox: Bind was called twice; build one outbox per kernel and bind it once, before kernel.Run"
}

// Bind ties the outbox to k, the kernel it was handed to as
// Options.Deliver, and registers TimerAttempt. Call it before k.Run. The
// engine is published only after TimerAttempt is registered, so no
// delivery ever sees a zero HandledKind.
func (o *Outbox) Bind(k *kernel.Kernel, opts Options) error {
	o.bind.Lock()
	defer o.bind.Unlock()
	if o.e.Load() != nil {
		return &BindError{}
	}
	e := &engine{
		k:        k,
		cells:    store.NewPartitioned(k.Gate(), codec(), newState),
		faults:   opts.Faults,
		policies: opts.Policies,
	}
	e.attempt = k.Timers().Handle(TimerAttempt, e.onAttempt)
	o.e.Store(e)
	return nil
}

// UnboundError is a delivery, load or export reaching an outbox that was
// never bound to its kernel.
type UnboundError struct{}

func (*UnboundError) Error() string {
	return "outbox: the outbox is not bound to a kernel, so it holds no deliveries; call Outbox.Bind with the kernel before kernel.Run (internal/server/wire.go)"
}

// Deliver accepts a delivery a committed write produced: kernel.DeliverSink.
func (o *Outbox) Deliver(d kernel.Delivery) error {
	_, err := o.Enqueue(d.Request, d.Cause, d.Accepted)
	return err
}

// Enqueue makes req pending, caused by cause and accepted at accepted, and
// arms its first attempt at accepted, unless a delivery the outbox took
// earlier into its FIFO lane is still pending: it is then armed when that
// one ends. The outbox takes ownership of req's slices. A request the outbox
// cannot attempt is refused with a *RequestError and nothing is queued.
func (o *Outbox) Enqueue(req delivery.Request, cause ref.Cause, accepted clock.Instant) (ids.DeliveryID, error) {
	e := o.e.Load()
	if e == nil {
		return "", &UnboundError{}
	}
	scope, err := checkRequest(req)
	if err != nil {
		return "", err
	}
	src := e.k.IDs()
	entry := Entry{
		ID:       src.DeliveryID(),
		Request:  req,
		NextAt:   accepted,
		Cause:    cause,
		Accepted: accepted,
	}
	if _, ok := req.Policy.(delivery.EventBridgeTarget); ok {
		entry.Jitter = src.Uint64(ids.KindJitter)
	}
	key := e.attempt.Key(timerID(scope, entry.ID))
	sched := e.k.Timers().Scheduler()
	// The kernel calls Deliver after the source cell unlocked and while the
	// source call is still in kernel.Write, so this is a plain cell update
	// rather than a nested kernel.Write; the timer is armed inside it, as
	// Tx.Arm would, so the scheduler agrees with the state. Seq is drawn
	// under the cell lock, so it orders the entries of a lane as they were
	// inserted.
	for {
		cell := e.cells.For(scope)
		inserted := store.Update(cell, func(st *State) (bool, bool) {
			if cell.Retired() {
				return false, false
			}
			entry.Seq = e.seq.Add(1)
			l, inLane := laneOf(&entry)
			blocked := inLane && st.laneBusy(l)
			st.Entries[entry.ID] = entry
			if !blocked {
				sched.Arm(key, accepted)
			}
			return true, true
		})
		if inserted {
			return entry.ID, nil
		}
	}
}

// RequestError is a delivery request the outbox refused; nothing was
// queued and the source call answers Internal.
type RequestError struct {
	// Field names the request member at fault, such as "Target.ARN".
	Field  string
	Reason string
	// Err is the parse error behind Reason, when there is one: an ARN that
	// does not parse in a snapshot entry, or one that names no account and
	// region.
	Err error
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("outbox: refused a delivery request: %s; the producing service built it wrong, and nothing was queued", e.problem())
}

func (e *RequestError) Unwrap() error { return e.Err }

// problem names the member at fault and why, with the underlying error.
func (e *RequestError) problem() string {
	p := e.Field + " " + e.Reason
	if e.Err != nil {
		p += ": " + e.Err.Error()
	}
	return p
}

// checkRequest returns the scope of req's target, or why the outbox cannot
// attempt req.
func checkRequest(req delivery.Request) (aws.Scope, *RequestError) {
	if !slices.Contains(delivery.Mechanisms(), req.Mechanism) {
		return aws.Scope{}, &RequestError{Field: "Mechanism", Reason: "is not a declared mechanism"}
	}
	target, ok := targetARN(req.Target)
	if !ok {
		return aws.Scope{}, &RequestError{Field: "Target", Reason: "is nil"}
	}
	if string(req.Mechanism.Target()) != target.Service {
		return aws.Scope{}, &RequestError{Field: "Target.ARN", Reason: fmt.Sprintf("%s is not a %s resource, which %s delivers to", target, req.Mechanism.Target(), req.Mechanism)}
	}
	if f, ok := req.Target.(delivery.LambdaFunction); ok && f.Invocation != delivery.InvocationEvent && f.Invocation != delivery.InvocationRequestResponse {
		return aws.Scope{}, &RequestError{Field: "Target.Invocation", Reason: "is neither Event nor RequestResponse"}
	}
	if err := checkPolicy(req.Policy); err != nil {
		return aws.Scope{}, err
	}
	if req.DLQ != nil {
		if req.DLQ.Service != "sqs" {
			return aws.Scope{}, &RequestError{Field: "DLQ", Reason: fmt.Sprintf("%s is not an SQS queue", req.DLQ)}
		}
		if _, err := scopeOf(*req.DLQ); err != nil {
			return aws.Scope{}, &RequestError{Field: "DLQ", Reason: unservedScope, Err: err}
		}
	}
	scope, err := scopeOf(target)
	if err != nil {
		return aws.Scope{}, &RequestError{Field: "Target.ARN", Reason: unservedScope, Err: err}
	}
	return scope, nil
}

// unservedScope is the RequestError reason of an ARN scopeOf refuses.
const unservedScope = "names no account and region understudy serves"

func checkPolicy(p delivery.RetryPolicy) *RequestError {
	switch p := p.(type) {
	case delivery.SNSManaged, delivery.S3Notification, delivery.None:
		return nil
	case delivery.EventBridgeTarget:
		if p.MaxAge < minEventAge || p.MaxAge > maxEventAge || p.MaxAttempts < 0 || p.MaxAttempts > maxEventRetries {
			return &RequestError{Field: "Policy", Reason: fmt.Sprintf("is EventBridgeTarget{MaxAge: %v, MaxAttempts: %d}; EventBridge allows an age of %v to %v and 0 to %d retries",
				p.MaxAge, p.MaxAttempts, minEventAge, maxEventAge, maxEventRetries)}
		}
		return nil
	}
	return &RequestError{Field: "Policy", Reason: "is nil"}
}

// targetARN returns the resource a target names.
func targetARN(t delivery.Target) (aws.ARN, bool) {
	switch t := t.(type) {
	case delivery.SQSQueue:
		return t.ARN, true
	case delivery.LambdaFunction:
		return t.ARN, true
	case delivery.SNSTopic:
		return t.ARN, true
	}
	return aws.ARN{}, false
}

// scopeError is why an ARN names no account and region understudy serves.
// It is the detail of a RequestError or a Dropped reason, which say what
// the ARN was for, so its message is the ARN and the cause alone.
type scopeError struct {
	ARN aws.ARN
	Err error
}

func (e *scopeError) Error() string { return fmt.Sprintf("%s: %v", e.ARN, e.Err) }

func (e *scopeError) Unwrap() error { return e.Err }

// scopeOf returns the account and region an ARN names (aws.ScopeOf), with
// the ARN in its error.
func scopeOf(a aws.ARN) (aws.Scope, *scopeError) {
	s, err := aws.ScopeOf(a)
	if err != nil {
		return aws.Scope{}, &scopeError{ARN: a, Err: err}
	}
	return s, nil
}

// isFIFO reports whether queue names a FIFO queue.
func isFIFO(queue aws.ARN) bool { return strings.HasSuffix(queue.Resource, ".fifo") }
