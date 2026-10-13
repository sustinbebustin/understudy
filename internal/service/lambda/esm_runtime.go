package lambda

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/ruler"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/store"
)

// A mapping's state machine: the control plane starts a transition, and
// the mapping settles in Enabled or Disabled, or is gone after Deleting.
// Every transition wakes the pollers' supervisor, which keeps one SQS
// poller per Enabled mapping (ARCHITECTURE.md 14.8).
//
// A poller's idle wait on its queue is a goroutine of the Execution that
// the tracker does not count, so a quiet mapping never holds a settle.
// When the queue's key is woken it arms the mapping's TimerPoll at once;
// the timer's call runs one step as tracked work (InvocationRunning):
// receive until the batch is full, then either wait for the batching
// window, by arming TimerPoll at its end, or invoke the function and
// delete what succeeded. A step that leaves more to do arms the next one
// before it ends, so settling sees no gap between them. One step runs at a
// time per mapping, so a mapping has at most one batch in flight.
//
// Decisions the architecture leaves open:
//
//   - The batching window opens when the first message of a batch is
//     received, on the emulator clock. A FIFO queue's window is ignored.
//   - A message received past the 6 MB of a batch starts the next one.
//   - Under a Frozen or Scaled clock, received messages are pinned
//     invisible with HoldVisibility until their batch's invocation
//     returns (or the mapping stops); the failed ones, and those whose
//     delete failed, are then released to their visibility timeout. A
//     Running clock pins nothing; a batch it left held is pinned at the
//     next step once the clock pins. The pin is a call after the receive,
//     so under a Scaled clock a visibility timeout shorter than the wall
//     time between the two can still end first. A pin is the live poller's
//     hold, not queue state, so SQS leaves it out of snapshots: a message
//     pinned when the snapshot was saved loads unpinned and returns after
//     its visibility timeout.
//   - A message received again while its batch waits for the window, its
//     visibility timeout over, replaces the held copy (esm.Batch.Add).
//   - A throttled invocation, or one the emulator could not run, fails the
//     whole batch, which returns to the queue with its visibility timeout.
//   - A receive that fails after earlier receives filled part of the batch
//     ends the filling: the batch goes on to its window or its invocation.
//     A receive that fails with nothing held, and a function that cannot be
//     invoked, retry after pollRetry of emulator time, doubled at each
//     failure in a row up to maxPollRetry, or sooner when the queue or the
//     mapping changes. A receive that succeeds ends the backoff. A receive
//     failure is logged when its error changes, and once when receiving
//     recovers.
//   - A filter pattern the engine cannot evaluate within its work bound
//     does not match: the message is deleted, the log says why, and a
//     filter-undecided Advisory on the mapping's ARN journals it, since AWS
//     has no such bound.
//   - The filter patterns are compiled when the poller first reads them and
//     again only when the mapping's patterns change. A stored pattern that
//     no longer compiles, which the control plane's validation rules out
//     unless a snapshot of another engine version was loaded, stops the
//     mapping: it gives back what it holds, receives nothing and logs why
//     once, until an update changes its patterns.
//   - The Invocation's parent is the step's TimerFired event, which also
//     roots the trace of the step's ReceiveMessage and DeleteMessageBatch,
//     so the trace does not reach the SendMessage of the batch's messages.
//     This falls short of ARCHITECTURE.md 10, which makes the earliest
//     message's enqueue event the parent and the rest Links: SQS keeps
//     each message's enqueue event but does not yet return it to an
//     internal receive.
//   - One batch is in flight per mapping, so a FIFO queue's message groups
//     are processed one batch at a time: stricter than AWS's one batch per
//     group, and never out of order.
//   - After a reset or load, RearmPollers reconciles the pollers. A poller
//     whose mapping's cell was replaced gives back what it held without
//     invoking it, since those messages belong to the state before; a
//     reset that leaves the Lambda state alone keeps the batches held.
//   - A mapping whose function is gone keeps its messages in the queue: its
//     poller journals a StateChange from Polling to FunctionMissing on the
//     mapping's ARN, polls nothing, and journals the way back once the
//     function exists again, which a retry finds.
//   - ScalingConfig and ProvisionedPollerConfig are stored only.

// TimerPoll runs one step of an event source mapping's SQS poller: at once
// when the queue may have a message, or when a batching window ends. Its id
// is the mapping's ARN.
const TimerPoll kernel.TimerKind = "lambda.esm-poll"

// mappingsChanged is what the pollers' supervisor waits on: a mapping
// write that starts a transition wakes it, and so must a load or reset
// that replaces mappings.
const mappingsChanged store.WaitKey = "lambda:event-source-mappings"

// The poller's journal states, recorded on the mapping's ARN.
const (
	pollerPolling         journal.ResourceState = "Polling"
	pollerFunctionMissing journal.ResourceState = "FunctionMissing"
)

// maxReceive is the most messages one ReceiveMessage returns.
const maxReceive = 10

// pollRetry is how long, in emulator time, a poller first waits before it
// tries again after a receive failed with nothing held or its function
// could not be invoked. AWS documents no interval for an SQS poller; one
// second keeps a recovered function or queue prompt without spinning.
const pollRetry = time.Second

// maxPollRetry caps a poller's backoff at the five minutes Lambda's own
// retries of throttled invocations back off to (lambda-events.md 8), so a
// queue deleted under its mapping costs one receive per five minutes of
// emulator time rather than one per second.
const maxPollRetry = 5 * time.Minute

// transition starts state under TransitionsClock and arms its settle
// timer; with final transitions the mapping stays settled. Either way the
// pollers' supervisor is woken to follow the change.
func (s *Service) transition(tx *kernel.Tx, scope aws.Scope, m *Mapping, state MappingState, now clock.Instant) {
	tx.Wake(mappingsChanged)
	if !s.clockTransitions() {
		return
	}
	m.Transition = &Transition{State: state, Settles: now.Add(settleDelay)}
	tx.Arm(s.settle, mappingARN(scope, m.UUID), m.Transition.Settles)
}

// settleMapping ends a mapping's transition that was due by due: it
// settles in Enabled or Disabled, or is gone once Deleting ends.
func (st *State) settleMapping(uuid string, due clock.Instant) bool {
	m, ok := st.Mappings[uuid]
	if !ok || m.Transition == nil || due.Before(m.Transition.Settles) {
		return false
	}
	if m.Transition.State == MappingDeleting {
		delete(st.Mappings, uuid)
		return true
	}
	m.Transition = nil
	st.Mappings[uuid] = m
	return true
}

// polled reports whether m wants a poller: it is enabled and not being
// deleted.
func (m Mapping) polled() bool {
	return m.Enabled && (m.Transition == nil || m.Transition.State != MappingDeleting)
}

// StartPollers registers TimerPoll and starts the supervisor that keeps
// one SQS poller per enabled event source mapping until Close. Call it
// once, after NewExecution.
func (x *Execution) StartPollers() {
	p := &pollers{x: x, byID: map[string]*poller{}}
	p.kind = x.k.Timers().Handle(TimerPoll, p.onPoll)
	woke := x.k.Waiters().Wait(x.ctx, mappingsChanged)
	x.wg.Go(func() { p.supervise(woke) })
}

// RearmPollers follows a reset or snapshot load that replaced the
// mappings and cleared the scheduler: a loaded mapping gets a poller, the
// poller of a mapping that is gone stops, and every poller steps at once,
// so a batching window or retry whose timer was cleared still ends. A
// poller whose mapping was replaced gives back the batch it held, which
// came from the state before. It also ends the hold Drain put on
// invocations, as the state is replaced by now. Call it after every
// service's Rearm.
func (x *Execution) RearmPollers() {
	x.running.resume()
	x.k.Waiters().Wake(mappingsChanged)
}

// pollers is the Execution's set of SQS pollers, by mapping ARN.
type pollers struct {
	x    *Execution
	kind kernel.HandledKind

	mu   sync.Mutex
	byID map[string]*poller
}

// poller is one mapping's poller.
type poller struct {
	arn aws.ARN // the mapping's ARN
	// id is arn as a string: TimerPoll's id and the key of pollers.byID.
	id    string
	scope aws.Scope
	uuid  string
	queue aws.ARN
	// queueScope is the queue's account and region, where its calls run.
	queueScope aws.Scope
	// stop ends the goroutine that waits on the queue.
	stop context.CancelFunc

	// mu also orders every arm of the poller's timer, so a request and the
	// end of a step never race to arm it.
	mu    sync.Mutex
	phase phase
	// requested is set by each request for a step and cleared when a step
	// starts. A step that ends with it set steps again at once, whether or
	// not the request's timer fired while the step ran.
	requested bool

	// held is touched only by the step that runs.
	held held
}

// phase is where a poller is between its steps.
type phase uint8

const (
	// phaseIdle waits for its timer to start a step.
	phaseIdle phase = iota
	// phaseStepping runs a step.
	phaseStepping
	// phaseRetired ended in a step that found its mapping no longer wants
	// it. No step starts on it again, even from a timer that found it
	// registered before retire removed it, so a poller that replaces it
	// under the same id never runs beside it.
	phaseRetired
)

// held is the state only the running step touches: the batch the poller
// has received and not yet invoked or given back, with its pins, and the
// step's caches and failure tracking.
type held struct {
	batch esm.Batch
	// opened is when the batch's first message was received, which opens
	// its batching window.
	opened clock.Instant
	// carry are messages received past the batch's 6 MB, for the next.
	carry []esm.Record
	// pinned are the receipt handles HoldVisibility pins, and target the
	// calls that pinned them, to release them with.
	pinned []string
	target kernel.Target
	// unavailable is why the function could not be invoked at the last
	// step, already journaled or logged; nil while it can be.
	unavailable *awserr.Error
	// receiveErr is why the last receive failed, already logged; nil once
	// a receive succeeds.
	receiveErr *awserr.Error
	// retry is the wait after the next failed step that holds nothing:
	// zero until a step fails, and again once a receive succeeds.
	retry time.Duration
	// cell is the Lambda cell the mapping was last read from, retired once
	// a reset or load replaces it.
	cell *store.Cell[State]
	// filters are the mapping's filter patterns as last compiled.
	filters compiledFilters
}

// compiledFilters are filter patterns and what compiling them gave: the
// filters, or why a pattern does not compile. The zero value is the
// compiled form of no patterns, which pass every record.
type compiledFilters struct {
	patterns []string
	filters  esm.Filters
	err      *ruler.CompileError
}

// supervise reconciles the pollers with the mappings now, and whenever
// the mappings change, until the Execution closes.
func (p *pollers) supervise(woke <-chan struct{}) {
	ctx := p.x.ctx
	for {
		p.reconcile(ctx)
		select {
		case <-woke:
			woke = p.x.k.Waiters().Wait(ctx, mappingsChanged)
		case <-ctx.Done():
			return
		}
	}
}

// reconcile starts a poller for every polled mapping that has none and
// asks every poller for a step, so each picks up its mapping's change or
// stops when its mapping no longer wants it.
func (p *pollers) reconcile(ctx context.Context) {
	wanted := p.polledMappings()
	p.mu.Lock()
	for _, w := range wanted {
		if _, ok := p.byID[w.id]; !ok {
			p.byID[w.id] = p.start(ctx, w)
		}
	}
	all := make([]*poller, 0, len(p.byID))
	for _, pl := range p.byID {
		all = append(all, pl)
	}
	p.mu.Unlock()
	for _, pl := range all {
		p.request(pl)
	}
}

// polledMappings lists the mappings of every scope that want a poller.
func (p *pollers) polledMappings() []*poller {
	var out []*poller
	for _, scope := range p.x.svc.cells.Keys(store.ScopeKeys()) {
		cell, ok := p.x.svc.cells.Lookup(scope)
		if !ok {
			continue
		}
		out = append(out, store.Read(cell, func(st *State) []*poller {
			var ps []*poller
			for uuid := range st.Mappings {
				if !st.Mappings[uuid].polled() {
					continue
				}
				if pl, ok := newPoller(scope, uuid, st.Mappings[uuid]); ok {
					ps = append(ps, pl)
				}
			}
			return ps
		})...)
	}
	return out
}

// newPoller is the poller of mapping uuid; false when its source is not a
// queue ARN this emulator can address.
func newPoller(scope aws.Scope, uuid string, m Mapping) (*poller, bool) {
	queue, err := aws.ParseARN(m.Source)
	if err != nil {
		return nil, false
	}
	queueScope, serr := aws.ScopeOf(queue)
	if serr != nil {
		return nil, false
	}
	arn := aws.ARN{Partition: scope.Partition.String(), Service: "lambda", Region: scope.Region.String(), Account: scope.Account.String(), Resource: "event-source-mapping:" + uuid}
	return &poller{arn: arn, id: arn.String(), scope: scope, uuid: uuid, queue: queue, queueScope: queueScope}, true
}

// start begins pl's wait on its queue. The first wait is registered before
// start returns, so no wake after the first step's receive is lost.
func (p *pollers) start(ctx context.Context, pl *poller) *poller {
	ctx, pl.stop = context.WithCancel(ctx)
	key := esm.QueueWaitKey(pl.queue)
	woke := p.x.k.Waiters().Wait(ctx, key)
	p.x.wg.Go(func() {
		for {
			select {
			case <-woke:
				woke = p.x.k.Waiters().Wait(ctx, key)
				p.request(pl)
			case <-ctx.Done():
				return
			}
		}
	})
	return pl
}

// request asks for a step of pl at once.
func (p *pollers) request(pl *poller) {
	now := p.x.k.Clock().Now()
	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.requested = true
	p.arm(pl, now)
}

// arm arms pl's timer at at, replacing the one armed. Call it with pl.mu
// held. The scheduler dispatches outside its own lock, so onPoll taking
// pl.mu cannot deadlock with it.
func (p *pollers) arm(pl *poller, at clock.Instant) {
	p.x.k.Timers().Scheduler().Arm(p.kind.Key(pl.id), at)
}

// onPoll is TimerPoll's handler: it starts a step of the poller as tracked
// work. While one runs the timer can only come from a request, which the
// step's finish sees and follows. A retired poller starts no step: the
// request is recovered by the poller that replaces it, which retire has
// the supervisor start when the mapping wants one.
func (p *pollers) onPoll(c *kernel.Call, key clock.TimerKey, _ clock.Instant) {
	p.mu.Lock()
	pl := p.byID[key.ID]
	p.mu.Unlock()
	if pl == nil {
		return
	}
	pl.mu.Lock()
	if pl.phase != phaseIdle {
		pl.mu.Unlock()
		return
	}
	pl.phase, pl.requested = phaseStepping, false
	pl.mu.Unlock()
	p.x.k.Go(kernel.InvocationRunning, func(ctx context.Context) {
		// A step that panics leaves the poller idle, waiting on its queue.
		var next pollNext = pollIdle{}
		defer func() { p.finish(pl, next) }()
		next = p.step(ctx, c, pl)
	})
}

// pollNext is what a poller does after a step.
//
//sumtype:decl
type pollNext interface{ isPollNext() }

// pollAgain steps again at once: the batch was invoked or messages were
// filtered out, and the queue may hold more.
type pollAgain struct{}

// pollAt steps again at at: a batching window or a transition ends.
type pollAt struct{ at clock.Instant }

// pollIdle waits for the queue to be woken.
type pollIdle struct{}

// pollStop ends the poller: its mapping no longer wants one.
type pollStop struct{}

func (pollAgain) isPollNext() {}
func (pollAt) isPollNext()    {}
func (pollIdle) isPollNext()  {}
func (pollStop) isPollNext()  {}

// finish ends a step, while it still counts as tracked work, and retires
// pl when the step stopped it.
func (p *pollers) finish(pl *poller, next pollNext) {
	if p.end(pl, next) {
		p.retire(pl)
	}
}

// end ends pl's step and arms the next one, or retires pl on pollStop,
// which it reports. A request made while the step ran overrides a later
// step or an idle wait: its timer may have fired and found the step
// running, or may still be armed and about to be moved by this arm.
func (p *pollers) end(pl *poller, next pollNext) (retired bool) {
	now := p.x.k.Clock().Now()
	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.phase = phaseIdle
	switch n := next.(type) {
	case pollStop:
		pl.phase = phaseRetired
		return true
	case pollAgain:
		p.arm(pl, now)
	case pollAt:
		if pl.requested {
			p.arm(pl, now)
		} else {
			p.arm(pl, n.at)
		}
	case pollIdle:
		if pl.requested {
			p.arm(pl, now)
		}
	}
	return false
}

// retire removes pl after a step found that its mapping no longer wants a
// poller. An update that enabled the mapping again while the step ran may
// have found pl still registered, and so started no other; its request
// for a step is lost with pl. So once pl is gone retire reads the mapping
// again and, when it is wanted, wakes the supervisor to start a new
// poller. Either this read sees the update, or the supervisor's pass that
// the update woke reads the mappings after this removal and finds pl gone.
// A request that reached pl after end retired it is lost the same way and
// recovered the same way.
func (p *pollers) retire(pl *poller) {
	p.mu.Lock()
	if p.byID[pl.id] == pl {
		delete(p.byID, pl.id)
	}
	p.mu.Unlock()
	pl.stop()
	if _, wanted := p.mapping(pl); wanted {
		p.x.k.Waiters().Wake(mappingsChanged)
	}
}

// step runs one poll cycle of pl on behalf of the timer's call c.
func (p *pollers) step(ctx context.Context, c *kernel.Call, pl *poller) pollNext {
	if p.replaced(pl) {
		p.giveBack(ctx, c, pl)
	}
	m, ok := p.mapping(pl)
	switch {
	case !ok:
		p.giveBack(ctx, c, pl)
		return pollStop{}
	case m.Transition != nil && (m.Transition.State == MappingCreating || m.Transition.State == MappingEnabling):
		return pollAt{at: m.Transition.Settles}
	}
	t, err := p.x.resolve(functionRef{scope: pl.scope, name: m.Function, qualifier: m.Qualifier})
	h := &pl.held
	if err != nil {
		p.unavailable(ctx, c, pl, err)
		return pollAt{at: p.x.k.Clock().Now().Add(h.backoff())}
	}
	p.available(ctx, c, pl)
	filters, ok := p.filters(ctx, pl, m.Filters)
	if !ok {
		p.giveBack(ctx, c, pl)
		return pollIdle{}
	}
	target := kernel.Target{Scope: pl.queueScope, As: kernel.AsExecutionRole{Role: t.role}, Via: ref.LambdaESM}
	p.pinHeld(ctx, c, pl, target)
	received, err := p.receive(ctx, c, pl, &m, filters, target)
	p.receiveResult(ctx, pl, err)
	switch {
	case h.batch.Len() > 0:
	case err != nil:
		return pollAt{at: p.x.k.Clock().Now().Add(h.backoff())}
	case received:
		return pollAgain{}
	default:
		return pollIdle{}
	}
	if closes, open := windowOpen(&m, h, p.x.k.Clock().Now()); open {
		return pollAt{at: closes}
	}
	p.invoke(ctx, c, pl, &m, t, target)
	return pollAgain{}
}

// replaced reports whether a reset or load replaced the cell of pl's
// mapping since pl last read it, so what pl holds is from the state before.
func (p *pollers) replaced(pl *poller) bool {
	prev := pl.held.cell
	pl.held.cell, _ = p.x.svc.cells.Lookup(pl.scope)
	return prev != nil && prev.Retired()
}

// mapping reads pl's mapping; false when it is gone, no longer wants a
// poller, or is another mapping under a reused id.
func (p *pollers) mapping(pl *poller) (Mapping, bool) {
	m, _ := view(p.x.svc, pl.scope, func(st *State) (Mapping, *awserr.Error) {
		return st.Mappings[pl.uuid], nil
	})
	return m, m.UUID == pl.uuid && m.Source == pl.queue.String() && m.polled()
}

// unavailable gives back what pl holds when its function cannot run. The
// poller journals that it stopped polling when the function is gone, and
// that it polls again when the function is back but cannot run yet; any
// other reason is logged. Each is reported once, not at every retry.
func (p *pollers) unavailable(ctx context.Context, c *kernel.Call, pl *poller, err *awserr.Error) {
	p.giveBack(ctx, c, pl)
	h := &pl.held
	prev := h.unavailable
	h.unavailable = err
	switch {
	case functionMissing(err) && !functionMissing(prev):
		p.record(ctx, c, pl, pollerPolling, pollerFunctionMissing)
	case functionMissing(err):
	case functionMissing(prev):
		p.record(ctx, c, pl, pollerFunctionMissing, pollerPolling)
		fallthrough
	case prev == nil || prev.Message != err.Message:
		p.x.k.Log().LogAttrs(ctx, slog.LevelInfo, "an event source mapping's function cannot be invoked; its messages stay in the queue, and its poller tries again, backing off from a second to five minutes of emulator time, or at once when the queue or the mapping changes",
			pl.attrs(slog.String("error", err.Message))...)
	}
}

// receiveResult follows the outcome of pl's receive: a failure is logged
// when its error differs from the last one's, and a success ends the
// backoff and logs once that receiving recovered.
func (p *pollers) receiveResult(ctx context.Context, pl *poller, err *awserr.Error) {
	h := &pl.held
	prev := h.receiveErr
	h.receiveErr = err
	switch {
	case err == nil:
		h.retry = 0
		if prev != nil {
			p.x.k.Log().LogAttrs(ctx, slog.LevelInfo, "an event source mapping receives from its queue again", pl.attrs()...)
		}
	case prev == nil || prev.ID.Shape != err.ID.Shape:
		p.x.k.Log().LogAttrs(ctx, slog.LevelWarn, "an event source mapping could not receive from its queue; the messages it already holds go on to their invocation, and it polls again, backing off from a second to five minutes of emulator time, or at once when the queue or the mapping changes; this is logged again only if the error changes",
			pl.attrs(slog.Int("held", h.batch.Len()), slog.String("code", string(err.ID.Shape)), slog.String("error", err.Message))...)
	}
}

// filters returns the compiled form of patterns, pl's mapping's filter
// patterns, compiling them only when they differ from the last ones. False
// when a pattern does not compile, which is logged once per change of the
// patterns.
func (p *pollers) filters(ctx context.Context, pl *poller, patterns []string) (esm.Filters, bool) {
	f := &pl.held.filters
	if !slices.Equal(f.patterns, patterns) {
		compiled, err := esm.CompileFilters(patterns)
		*f = compiledFilters{patterns: slices.Clone(patterns), filters: compiled, err: err}
		if err != nil {
			p.x.k.Log().LogAttrs(ctx, slog.LevelError, "an event source mapping's stored filter pattern does not compile, though the control plane accepted it; the mapping gives back the messages it holds and receives nothing until an UpdateEventSourceMapping changes its FilterCriteria",
				pl.attrs(slog.String("error", err.Detail))...)
		}
	}
	return f.filters, f.err == nil
}

// backoff returns how long to wait after a failed step that holds
// nothing: pollRetry after the first failure, doubled at each one in a
// row up to maxPollRetry.
func (h *held) backoff() time.Duration {
	d := max(h.retry, pollRetry)
	h.retry = min(2*d, maxPollRetry)
	return d
}

// available journals that pl polls again after its function was missing.
func (p *pollers) available(ctx context.Context, c *kernel.Call, pl *poller) {
	h := &pl.held
	if functionMissing(h.unavailable) {
		p.record(ctx, c, pl, pollerFunctionMissing, pollerPolling)
	}
	h.unavailable = nil
}

// functionMissing reports whether err says the mapping's function is gone.
func functionMissing(err *awserr.Error) bool {
	return err != nil && err.ID.Status == 404
}

// record journals pl's poller state change from to on the
// mapping's ARN.
func (p *pollers) record(ctx context.Context, c *kernel.Call, pl *poller, from, to journal.ResourceState) {
	c.Record(ctx, pl.scope, journal.StateChange{Resource: pl.arn, From: from, To: to})
}

// attrs are the log attributes that name pl, then extra.
func (pl *poller) attrs(extra ...slog.Attr) []slog.Attr {
	return append([]slog.Attr{slog.String("mapping", pl.id), slog.String("queue", pl.queue.String())}, extra...)
}
