package kernel

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Call is the context of one operation invocation, external or internal.
// The edge (NewCall), Invoke and the timer dispatch build it; handlers read
// it through accessors and may Touch resources. A Call belongs to one
// pipeline run and is not reused.
type Call struct {
	k     *Kernel
	id    ids.RequestID
	scope aws.Scope
	// region is where an external request asserts its region from; nil
	// for internal and timer calls, which assert none.
	region    aws.RegionSource
	principal identity.Principal
	origin    ref.Origin
	// mode is how an internal call is authorized; nil for external and
	// timer calls.
	mode PrincipalMode
	op   *registry.Operation // nil only for an operation the service does not model
	opID ref.OperationID
	// label names the call in diagnostics: its operation, or the timer kind
	// of a timer callback.
	label    string
	event    ref.EventID
	cause    ref.Cause
	admitted clock.Instant
	endpoint Endpoint
	trace    string
	headers  http.Header
	elapsed  func() time.Duration

	holding atomic.Bool

	mu      sync.Mutex
	touched []aws.ARN
}

// Endpoint is the scheme and host a client addressed the emulator with, as
// emulator-minted URLs echo it (ADR 0013).
type Endpoint struct {
	Scheme string // "http" or "https"
	Host   string // the Host header as received, port included
}

// String returns scheme://host.
func (e Endpoint) String() string { return e.Scheme + "://" + e.Host }

// ID returns the request id: x-amzn-RequestId for external calls.
func (c *Call) ID() ids.RequestID { return c.id }

// Scope returns the account and region the call runs in.
func (c *Call) Scope() aws.Scope { return c.scope }

// RegionSource returns where the call's region came from (ARCHITECTURE.md
// 7): the credential scope a SigV4 request signed for, or aws.None for a
// SigV2 or anonymous request and for every internal and timer call, whose
// region is the scope it was given.
func (c *Call) RegionSource() aws.RegionSource {
	if c.region == nil {
		return aws.None{}
	}
	return c.region
}

// Principal returns who makes the call.
func (c *Call) Principal() identity.Principal { return c.principal }

// Origin returns where the call came from.
func (c *Call) Origin() ref.Origin { return c.origin }

// Op returns the operation's descriptor. It is nil only for a request that
// names an operation its service does not model, which never reaches a
// handler.
func (c *Call) Op() *registry.Operation { return c.op }

// OperationID returns the operation the call names, modeled or not.
func (c *Call) OperationID() ref.OperationID { return c.opID }

// Event returns the call's journal event, the parent of everything the call
// causes.
func (c *Call) Event() ref.EventID { return c.event }

// Cause returns the call's parent event and trace; a root call has a zero
// parent.
func (c *Call) Cause() ref.Cause { return c.cause }

// Admitted returns the admission instant: the journal time and request time
// of the call (F17). Deferred work carries the instant that triggered it.
// State deadlines use the commit instant Write passes instead.
func (c *Call) Admitted() clock.Instant { return c.admitted }

// Endpoint returns the scheme and host the client addressed; internal calls
// inherit their parent's.
func (c *Call) Endpoint() Endpoint { return c.endpoint }

// Trace returns the X-Amzn-Trace-Id the request carried, or the parent
// call's (F16).
func (c *Call) Trace() string { return c.trace }

// Header returns one of the unmodeled request headers the kernel keeps for
// handlers (CallHeaders), and false when the request did not send it or the
// header is not kept.
func (c *Call) Header(name string) (string, bool) {
	v := c.headers.Values(name)
	if len(v) == 0 {
		return "", false
	}
	return v[0], true
}

// Touch records resources the call reads or changes. The journal lists
// them on the call's event and the access recorder pairs them with the
// operation's IAM actions; fault rules match on them.
func (c *Call) Touch(arns ...aws.ARN) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, a := range arns {
		if !slices.Contains(c.touched, a) {
			c.touched = append(c.touched, a)
		}
	}
}

// Record journals d as an event the call caused, at its admission instant
// in scope (the affected resource's, which a cross-scope effect makes
// differ from the call's). The kernel stamps the cause, the call's event
// and trace, so a handler describes what happened but never links it
// (ARCHITECTURE.md 10). The journal refuses only a malformed detail, a
// caller bug, which is logged while the call goes on, as for the
// pipeline's own events. A record that describes a state change belongs on
// the Write that makes it: Tx.Record.
func (c *Call) Record(ctx context.Context, scope aws.Scope, d journal.Detail) {
	if err := c.k.record(c, c.admitted, scope, d); err != nil {
		c.k.log.LogAttrs(ctx, slog.LevelWarn, "journal refused an event a handler recorded",
			slog.String("op", c.label), slog.String("request_id", string(c.id)), slog.String("error", err.Error()))
	}
}

func (k *Kernel) record(c *Call, at clock.Instant, scope aws.Scope, d journal.Detail) error {
	_, err := k.journal.Append(journal.Event{Cause: ref.Cause{Parent: c.event, Trace: c.cause.Trace}, At: at, Scope: scope, Detail: d})
	return err
}

// Touched returns the resources the call has touched so far, in first-touch
// order.
func (c *Call) Touched() []aws.ARN {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.touched)
}

// CallHeaders is the closed set of unmodeled request headers a Call keeps
// for its handler: the trace header SQS copies into AWSTraceHeader, the
// query-compatibility mode, the client's user agent, and the four headers
// S3 reads to decode an aws-chunked body (Content-Encoding, which
// UploadPart does not model, its framing mode, declared trailer and
// decoded length).
func CallHeaders() []string {
	return []string{
		"Content-Encoding", "User-Agent", "X-Amz-Content-Sha256", "X-Amz-Decoded-Content-Length", "X-Amz-Trailer",
		"X-Amzn-Query-Mode", "X-Amzn-Trace-Id",
	}
}

// ExternalCall describes a request the edge admitted.
type ExternalCall struct {
	RequestID ids.RequestID
	// Operation is what the request names; the kernel resolves it against
	// the registry, and a name the service does not model gives a Call whose
	// Op is nil.
	Operation ref.OperationID
	Scope     aws.Scope
	// RegionSource is where the request asserts its region from (ARCHITECTURE.md
	// 7): the SigV4 credential scope, or none for SigV2 and anonymous
	// requests. nil is read as aws.None.
	RegionSource aws.RegionSource
	Principal    identity.Principal
	Origin       ref.External
	Admitted     clock.Instant
	Endpoint     Endpoint
	// Header is the request's header; the call keeps CallHeaders only.
	Header http.Header
}

// NewCall builds the Call of an external request. When a Linker ties the
// principal to a running invocation, the call becomes that invocation's
// child; otherwise it roots its own trace.
func (k *Kernel) NewCall(e ExternalCall) *Call {
	op, _ := k.reg.Operation(e.Operation)
	kept := http.Header{}
	for _, name := range CallHeaders() {
		if v := e.Header.Values(name); len(v) > 0 {
			kept[name] = slices.Clone(v)
		}
	}
	src := e.RegionSource
	if src == nil {
		src = aws.None{}
	}
	c := &Call{
		k:         k,
		id:        e.RequestID,
		scope:     e.Scope,
		region:    src,
		principal: e.Principal,
		origin:    e.Origin,
		op:        op,
		opID:      e.Operation,
		label:     e.Operation.String(),
		admitted:  e.Admitted,
		endpoint:  e.Endpoint,
		trace:     kept.Get("X-Amzn-Trace-Id"),
		headers:   kept,
		elapsed:   k.clock.Stopwatch(),
	}
	if k.linker != nil {
		if cause, ok := k.linker.Link(e.Principal, e.Origin.RemoteAddr); ok {
			c.cause = cause
		}
	}
	return c
}
