package outbox

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/egress"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TimerAttempt fires when a delivery's next attempt is due. The key's ID
// is "<account>/<region>/<delivery-id>", the scope being the target's.
const TimerAttempt kernel.TimerKind = "outbox.attempt"

// PolicySource reads the stored resource policy of a delivery target: an
// SQS queue's Policy attribute, a function's permissions, a topic's Policy.
// A target it does not find (ok false) skips the advisory: the attempt then
// fails on its own.
type PolicySource = authz.PolicyProvider

// Options configures Bind. A nil field disables its stage.
type Options struct {
	// Faults evaluates the Delivery-phase fault rules before each attempt.
	Faults kernel.FaultStage
	// Policies supplies target policies for the access advisory.
	Policies PolicySource
	// Egress sends HTTPEndpoint deliveries; nil refuses each one, which is
	// the default until a producer enables outbound delivery.
	Egress *egress.Client
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
	egress   *egress.Client
	// seq is the last Entry.Seq handed out; Import raises it past every
	// loaded entry's.
	seq atomic.Uint64
	// ready runs the attempts onAttempt hands over in firing order.
	ready sequencer
	// pace spaces the attempts of throttled SNS HTTP subscriptions.
	pace pacer
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
		egress:   opts.Egress,
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

// checkRequest returns the scope req's delivery lands in, or why the
// outbox cannot attempt req.
func checkRequest(req delivery.Request) (aws.Scope, *RequestError) {
	if !slices.Contains(delivery.Mechanisms(), req.Mechanism) {
		return aws.Scope{}, &RequestError{Field: "Mechanism", Reason: "is not a declared mechanism"}
	}
	in, field, err := checkTarget(req)
	if err != nil {
		return aws.Scope{}, err
	}
	if err := checkScheduler(req); err != nil {
		return aws.Scope{}, err
	}
	if err := checkPolicy(req.Mechanism, req.Policy); err != nil {
		return aws.Scope{}, err
	}
	if err := checkSender(req); err != nil {
		return aws.Scope{}, err
	}
	if err := checkDLQ(req); err != nil {
		return aws.Scope{}, err
	}
	scope, serr := scopeOf(in)
	if serr != nil {
		return aws.Scope{}, &RequestError{Field: field, Reason: unservedScope, Err: serr}
	}
	return scope, nil
}

// checkScheduler refuses a Scheduler delivery (scheduler.md 14.4.3): not
// implemented yet, the outbox neither renders its payload from
// Request.Invocation nor runs it as Request.As, so it attempts none rather
// than deliver one as a service principal.
func checkScheduler(req delivery.Request) *RequestError {
	if req.Mechanism.Via() != ref.SchedulerTarget {
		return nil
	}
	return &RequestError{Field: "Mechanism", Reason: "is " + req.Mechanism.String() + ", a Scheduler delivery, which the outbox does not attempt: not implemented yet"}
}

// checkSender checks req's sender: set for the Lambda mechanisms, which
// deliver an asynchronous invocation's record or dead letter as the
// function's execution role (lambda-destinations-layers.md 2.5, 11.3.2),
// and for the ESM mechanisms, which send an event source mapping's
// on-failure record as that role (lambda-stream-sources.md 5.6); nil for
// every other mechanism. A dead letter goes to a queue or topic only (5.1),
// and Lambda neither retries nor dead-letters its own deliveries: a failed
// one is counted in a metric (4.6, 5.4).
func checkSender(req delivery.Request) *RequestError {
	m, s := req.Mechanism, req.Sender
	lambda := m.Via() == ref.LambdaDestination || m.Via() == ref.LambdaESM
	switch {
	case !lambda && s == nil:
		return nil
	case !lambda:
		return &RequestError{Field: "Sender", Reason: "is set, but only the Lambda mechanisms run as an execution role, and this delivery is " + m.String()}
	case s == nil:
		return &RequestError{Field: "Sender", Reason: "is nil, but a " + m.String() + " delivery runs as the function's execution role"}
	}
	name, isRole := strings.CutPrefix(s.Role.Resource, "role/")
	if _, err := aws.ParseAccountID(s.Role.Account); s.Role.Service != "iam" || !isRole || name == "" || err != nil {
		return &RequestError{Field: "Sender.Role", Reason: fmt.Sprintf("%s is not an IAM role, arn:<partition>:iam::<account>:role/<path><name>", s.Role)}
	}
	if _, err := s.Carries.MarshalText(); err != nil {
		return &RequestError{Field: "Sender.Carries", Reason: "is not a declared Lambda delivery payload", Err: err}
	}
	switch req.Target.(type) {
	case delivery.SQSQueue, delivery.SNSTopic:
	case delivery.LambdaFunction, delivery.EventBus, delivery.S3Object, delivery.HTTPEndpoint:
		if s.Carries == delivery.DeadLetter {
			return &RequestError{Field: "Sender.Carries", Reason: "is a dead letter, which goes to an SQS queue or SNS topic only (lambda-destinations-layers.md 5.1), not to this " + m.String() + " target"}
		}
	}
	if req.DLQ != nil {
		return &RequestError{Field: "DLQ", Reason: "is set, but Lambda dead-letters none of its own deliveries: a failed one counts in " + s.Carries.Metric() + " (lambda-destinations-layers.md 4.6, 5.4)"}
	}
	if _, ok := req.Policy.(delivery.None); !ok {
		return &RequestError{Field: "Policy", Reason: fmt.Sprintf("is %T, but a Lambda delivery makes one attempt, delivery.None (lambda-destinations-layers.md 4.6, 5.4)", req.Policy)}
	}
	return nil
}

// checkDLQ checks req's dead-letter queue, if it has one: an SQS queue in
// a scope understudy serves, for a target whose dead letter has a known
// form. An event bus target's dead letter is the forwarded event, as for
// any EventBridge target (eb-rule-dlq.html, iam-enforcement-surfaces.md
// 8.4); no research gives the form of an S3Object dead letter or of an
// EventBus one under another mechanism, so those refuse a queue until
// their producer researches it. An HTTP control message, a
// SubscriptionConfirmation or UnsubscribeConfirmation, never goes to one
// (sns.http.control-dlq, sns-http-delivery.md 6.5, 8.2).
func checkDLQ(req delivery.Request) *RequestError {
	if req.DLQ == nil {
		return nil
	}
	switch t := req.Target.(type) {
	case delivery.EventBus:
		if req.Mechanism != delivery.EventsToEvents {
			return &RequestError{Field: "DLQ", Reason: "is set, but an EventBus delivery under " + req.Mechanism.String() + " has no dead-letter format yet; only " + delivery.EventsToEvents.String() + " has one"}
		}
	case delivery.S3Object:
		return &RequestError{Field: "DLQ", Reason: "is set, but an S3Object delivery has no dead-letter format yet"}
	case delivery.HTTPEndpoint:
		// An undeclared kind is checkTarget's refusal.
		if t.Kind == delivery.HTTPSubscriptionConfirmation || t.Kind == delivery.HTTPUnsubscribeConfirmation {
			return &RequestError{Field: "DLQ", Reason: "control messages never go to a dead-letter queue (sns.http.control-dlq)"}
		}
	case delivery.SQSQueue, delivery.LambdaFunction, delivery.SNSTopic:
	}
	if req.DLQ.Service != "sqs" {
		return &RequestError{Field: "DLQ", Reason: fmt.Sprintf("%s is not an SQS queue", req.DLQ)}
	}
	if _, err := scopeOf(*req.DLQ); err != nil {
		return &RequestError{Field: "DLQ", Reason: unservedScope, Err: err}
	}
	return nil
}

// checkTarget checks req's target, and that req's mechanism delivers to
// its kind, and returns the ARN whose account and region the delivery
// lands in with the request field that names it: the target's own ARN, or
// the source's for a target that names none (an S3 object, an HTTP
// endpoint).
func checkTarget(req delivery.Request) (aws.ARN, string, *RequestError) {
	m := req.Mechanism
	if _, isHTTP := req.Target.(delivery.HTTPEndpoint); m.Target() == "" && !isHTTP && req.Target != nil {
		// A mechanism that lands in no service delivers to an HTTP endpoint
		// and nothing else.
		return aws.ARN{}, "", &RequestError{Field: "Target", Reason: fmt.Sprintf("is a %T target, which %s does not deliver to; it delivers to a delivery.HTTPEndpoint only", req.Target, m)}
	}
	switch t := req.Target.(type) {
	case delivery.SQSQueue:
		return arnTarget(m, t.ARN)
	case delivery.SNSTopic:
		return arnTarget(m, t.ARN)
	case delivery.LambdaFunction:
		if _, _, err := arnTarget(m, t.ARN); err != nil {
			return aws.ARN{}, "", err
		}
		if t.Invocation != delivery.InvocationEvent && t.Invocation != delivery.InvocationRequestResponse {
			return aws.ARN{}, "", &RequestError{Field: "Target.Invocation", Reason: "is neither Event nor RequestResponse"}
		}
		return t.ARN, "Target.ARN", nil
	case delivery.EventBus:
		if err := checkEventBus(t); err != nil {
			return aws.ARN{}, "", err
		}
		if err := checkForwarding(m, t.Entries[0]); err != nil {
			return aws.ARN{}, "", err
		}
		return t.ARN, "Target.ARN", nil
	case delivery.S3Object:
		if err := deliversTo(m, "S3Object", "s3"); err != nil {
			return aws.ARN{}, "", err
		}
		return req.Source, "Source", checkS3Object(t)
	case delivery.HTTPEndpoint:
		if err := deliversTo(m, "HTTPEndpoint", ""); err != nil {
			return aws.ARN{}, "", err
		}
		if !t.Kind.Valid() {
			return aws.ARN{}, "", &RequestError{Field: "Target.Kind", Reason: fmt.Sprintf("is %v; an HTTP delivery carries a Notification, a SubscriptionConfirmation or an UnsubscribeConfirmation", t.Kind)}
		}
		return req.Source, "Source", checkHTTPEndpoint(t)
	}
	return aws.ARN{}, "", &RequestError{Field: "Target", Reason: "is nil"}
}

// checkEventBus checks that t names an event bus and holds one entry.
// PutEvents takes up to 10 and fails each on its own
// (https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutEvents.html),
// but a retry of the call would put the entries that succeeded again, so a
// producer enqueues one delivery per entry and each is retried or dropped
// by its own entry's code (entryError).
func checkEventBus(t delivery.EventBus) *RequestError {
	name, isBus := strings.CutPrefix(t.ARN.Resource, "event-bus/")
	if t.ARN.Service != "events" || !isBus || name == "" {
		return &RequestError{Field: "Target.ARN", Reason: t.ARN.String() + " is not an event bus, arn:<partition>:events:<region>:<account>:event-bus/<name>"}
	}
	if n := len(t.Entries); n != 1 {
		return &RequestError{Field: "Target.Entries", Reason: fmt.Sprintf("has %d entries; an EventBus delivery carries exactly 1, so a retry puts no entry twice", n)}
	}
	return nil
}

// checkForwarding checks that m delivers the EventBus entry e. Only
// events-to-events, an event bus target's delivery, forwards an event that
// was already put, and it forwards nothing else: its entry carries the
// event's envelope (eventbridge-buses.md 10.4.12), whose detail is one
// JSON object (MalformedDetail, secrets-logs-events-alarms.md 4.7). The
// envelope is checked before the mechanism, so a Forwarded under another
// mechanism is named as such rather than as a target m does not deliver to.
func checkForwarding(m delivery.Mechanism, e delivery.EventEntry) *RequestError {
	forwards := m == delivery.EventsToEvents
	switch {
	case forwards && e.Forwarded == nil:
		return &RequestError{Field: "Target.Entries[0].Forwarded", Reason: "is nil, but an " + m.String() + " delivery forwards an event that was already put, with its id, account, region and time"}
	case !forwards && e.Forwarded != nil:
		return &RequestError{Field: "Target.Entries[0].Forwarded", Reason: "is set, but only " + delivery.EventsToEvents.String() + " forwards an event, and this delivery is " + m.String()}
	}
	if err := deliversTo(m, "EventBus", "eventbridge"); err != nil {
		return err
	}
	if forwards {
		if _, err := e.Envelope(); err != nil {
			return &RequestError{Field: "Target.Entries[0].Detail", Reason: "is not the detail of a forwarded event", Err: err}
		}
	}
	return nil
}

// checkS3Object checks that t names a bucket and a key.
func checkS3Object(t delivery.S3Object) *RequestError {
	if t.Bucket == "" {
		return &RequestError{Field: "Target.Bucket", Reason: "is empty"}
	}
	if t.Key == "" {
		return &RequestError{Field: "Target.Key", Reason: "is empty"}
	}
	return nil
}

// checkHTTPEndpoint checks that t's URL is http or https and names a host,
// and that no header of t frames the message.
func checkHTTPEndpoint(t delivery.HTTPEndpoint) *RequestError {
	if t.URL.Scheme != "http" && t.URL.Scheme != "https" {
		return &RequestError{Field: "Target.URL", Reason: fmt.Sprintf("has the scheme %q; an HTTP endpoint is http or https", t.URL.Scheme)}
	}
	if t.URL.Hostname() == "" {
		return &RequestError{Field: "Target.URL", Reason: "names no host"}
	}
	for i, h := range t.Headers {
		if framingHeader(h.Name) {
			return &RequestError{Field: fmt.Sprintf("Target.Headers[%d]", i), Reason: fmt.Sprintf("is %q, which frames the request; egress writes it", h.Name)}
		}
	}
	return nil
}

// framingHeader reports whether name, in any case, is a header egress
// writes to frame the request or its connection. Headers keep the case the
// producer gives (sns-http-delivery.md 3.2), so net/http would send such a
// header beside its own, and two Content-Length values, or Content-Length
// with Transfer-Encoding, leave the receiver to guess where the body ends.
func framingHeader(name string) bool {
	for _, f := range []string{"Host", "Content-Length", "Transfer-Encoding", "Connection", "Trailer", "TE"} {
		if strings.EqualFold(name, f) {
			return true
		}
	}
	return false
}

// arnTarget checks that m delivers to the service of target, an ARN a
// target names.
func arnTarget(m delivery.Mechanism, target aws.ARN) (aws.ARN, string, *RequestError) {
	if string(m.Target()) != target.Service {
		return aws.ARN{}, "", &RequestError{Field: "Target.ARN", Reason: fmt.Sprintf("%s is not a %s resource, which %s delivers to", target, m.Target(), m)}
	}
	return target, "Target.ARN", nil
}

// deliversTo checks that m delivers to a target of kind, which lands in
// the service into ("" for an HTTP endpoint, which lands in none).
func deliversTo(m delivery.Mechanism, kind string, into ref.ServiceID) *RequestError {
	if m.Target() == into {
		return nil
	}
	return &RequestError{Field: "Target", Reason: "is an " + kind + " target, which " + m.String() + " does not deliver to"}
}

// unservedScope is the RequestError reason of an ARN scopeOf refuses.
const unservedScope = "names no account and region understudy serves"

// checkPolicy checks that p is a policy the outbox schedules, within its
// bounds, and that an sns-to-http delivery, and only one, retries under
// SNSHTTP.
func checkPolicy(m delivery.Mechanism, p delivery.RetryPolicy) *RequestError {
	if p == nil {
		return &RequestError{Field: "Policy", Reason: "is nil"}
	}
	switch _, isHTTP := p.(delivery.SNSHTTP); {
	case isHTTP && m != delivery.SNSToHTTP:
		return &RequestError{Field: "Policy", Reason: fmt.Sprintf("is SNSHTTP, an SNS http or https subscription's delivery policy, which %s deliveries do not retry under", m)}
	case !isHTTP && m == delivery.SNSToHTTP:
		return &RequestError{Field: "Policy", Reason: fmt.Sprintf("is %T; %s deliveries retry under delivery.SNSHTTP, the subscription's resolved delivery policy", p, m)}
	}
	switch p := p.(type) {
	case delivery.SNSManaged, delivery.S3Notification, delivery.None:
		return nil
	case delivery.EventBridgeTarget:
		if p.MaxAge < minEventAge || p.MaxAge > maxEventAge || p.MaxAttempts < 0 || p.MaxAttempts > maxEventRetries {
			return &RequestError{Field: "Policy", Reason: fmt.Sprintf("is EventBridgeTarget{MaxAge: %v, MaxAttempts: %d}; EventBridge allows an age of %v to %v and 0 to %d retries",
				p.MaxAge, p.MaxAttempts, minEventAge, maxEventAge, maxEventRetries)}
		}
		return nil
	case delivery.SNSHTTP:
		return checkSNSHTTP(p)
	}
	return &RequestError{Field: "Policy", Reason: fmt.Sprintf("is %T, a policy the outbox does not schedule", p)}
}

// checkSNSHTTP checks p against the documented bounds
// (sns-http-delivery.md 6.7, aws-doc): every phase count at least 0 and
// numRetries, their sum, at most 100; minDelayTarget from 1 s up to
// maxDelayTarget, which is at most 3600 s; maxReceivesPerSecond not below
// 0, 0 being no throttling; and a declared backoff function.
func checkSNSHTTP(p delivery.SNSHTTP) *RequestError {
	counts := []int{p.NoDelay, p.MinDelay, p.Backoff, p.MaxDelay}
	switch {
	case slices.ContainsFunc(counts, func(n int) bool { return n < 0 || n > maxHTTPRetries }) || p.Retries() > maxHTTPRetries:
		return &RequestError{Field: "Policy", Reason: fmt.Sprintf("is SNSHTTP with the retry counts NoDelay %d, MinDelay %d, Backoff %d and MaxDelay %d; each is at least 0 and their sum, numRetries, at most %d",
			p.NoDelay, p.MinDelay, p.Backoff, p.MaxDelay, maxHTTPRetries)}
	case p.MinDelayTarget < minHTTPDelayTarget || p.MinDelayTarget > p.MaxDelayTarget || p.MaxDelayTarget > maxHTTPDelayTarget:
		return &RequestError{Field: "Policy", Reason: fmt.Sprintf("is SNSHTTP with MinDelayTarget %v and MaxDelayTarget %v; minDelayTarget is %v up to maxDelayTarget, which is at most %v",
			p.MinDelayTarget, p.MaxDelayTarget, minHTTPDelayTarget, maxHTTPDelayTarget)}
	case p.MaxReceivesPerSecond < 0:
		return &RequestError{Field: "Policy", Reason: fmt.Sprintf("is SNSHTTP with MaxReceivesPerSecond %d; it is at least 0, 0 being no throttling", p.MaxReceivesPerSecond)}
	case !p.BackoffFunction.Valid():
		return &RequestError{Field: "Policy", Reason: fmt.Sprintf("is SNSHTTP with BackoffFunction %v, which is not linear, arithmetic, geometric or exponential", p.BackoffFunction)}
	}
	return nil
}

// targetARN returns the ARN a target names; false for one that names none:
// an S3 object, an HTTP endpoint or a nil target.
func targetARN(t delivery.Target) (aws.ARN, bool) {
	switch t := t.(type) {
	case delivery.SQSQueue:
		return t.ARN, true
	case delivery.LambdaFunction:
		return t.ARN, true
	case delivery.SNSTopic:
		return t.ARN, true
	case delivery.EventBus:
		return t.ARN, true
	case delivery.S3Object, delivery.HTTPEndpoint:
		return aws.ARN{}, false
	}
	return aws.ARN{}, false
}

// deliveredTo is the resource a delivery of req lands in: the journal's
// To, and what the fault rules and the access advisory match. It is the
// ARN the target names, an S3 object's ARN in the source's partition, or
// the zero ARN for an HTTP endpoint, which journal.Delivery.Endpoint names
// instead.
func deliveredTo(req delivery.Request) aws.ARN {
	if target, ok := targetARN(req.Target); ok {
		return target
	}
	if o, ok := req.Target.(delivery.S3Object); ok {
		return aws.ARN{Partition: req.Source.Partition, Service: "s3", Resource: o.Bucket + "/" + o.Key}
	}
	return aws.ARN{}
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
