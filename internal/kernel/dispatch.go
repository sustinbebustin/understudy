package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Decoded is what the edge hands the kernel. The kernel is the only caller
// of journal.Begin, journal.End and the access recorder, so a rejected
// decode and a 501 are journaled by the same code as a handled call.
//
//sumtype:decl
type Decoded interface{ isDecoded() }

// Ok is a decoded input: a *In from Kernel.NewInput for a typed operation,
// a RawExchange for a raw HTTP operation. Wire carries the client's
// original request to a BindRaw handler; nil means Synthesized. In is nil
// only for an unbound operation, which Dispatch answers with 501 before it
// reads the input.
type Ok struct {
	In   any
	Wire Wire
}

// Rejected is a request the edge refused before the handler: a body that
// does not decode, an operation the service does not model.
type Rejected struct{ Err *awserr.Error }

// Unauthenticated is a request the edge refused at authentication:
// credentials that cannot be read or do not verify, or a body the
// signature could not be checked against. It is journaled like
// Rejected, but no IAM action is recorded for it, since on AWS such a call
// never reaches authorization.
type Unauthenticated struct{ Err *awserr.Error }

func (Ok) isDecoded()              {}
func (Rejected) isDecoded()        {}
func (Unauthenticated) isDecoded() {}

// Result is what the pipeline returns to the edge or to Invoke.
//
//sumtype:decl
type Result interface{ isResult() }

// Output is a handler's output and the HTTP status to send it with.
type Output struct {
	Out    any
	Status int
}

// Failure is the error the call answers with.
type Failure struct{ Err *awserr.Error }

// StatusOKFailure is S3's 200-with-error answer (CompleteMultipartUpload,
// CopyObject, UploadPartCopy): the codec renders Err with status 200. Only
// a StatusOKError fault rule produces it.
type StatusOKFailure struct{ Err *awserr.Error }

func (Output) isResult()          {}
func (Failure) isResult()         {}
func (StatusOKFailure) isResult() {}

// DecodedError is a Dispatch that cannot run the pipeline: no decoded
// input, or an input for an operation the service does not model.
type DecodedError struct {
	Op     ref.OperationID
	Reason string
}

func (e *DecodedError) Error() string {
	return fmt.Sprintf("kernel.Dispatch of %s: %s", e.Op, e.Reason)
}

// PanicError is service code that panicked during a call: a validator,
// check or resource resolver (steps 7 and 8) or the handler (step 9). The
// stack goes to the journal and the log, never to the client.
type PanicError struct {
	Op    string
	Value any
	// Stack is the panicking goroutine's stack.
	Stack string
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("the %s call panicked: %v", e.Op, e.Value)
}

// Dispatch runs pipeline steps 6 to 12 of ARCHITECTURE.md 4 for c: journal
// begin; for a modeled, bound operation the generated and typed
// validators, request-phase faults, the handler, after-commit faults; the
// access record; journal end. An unknown operation, an unbound one (501)
// and a rejected decode end after step 6, journaled and access-recorded
// like any other call; an unauthenticated request ends there too,
// journaled but not access-recorded.
func (k *Kernel) Dispatch(ctx context.Context, c *Call, d Decoded) Result {
	bnd, _ := k.bindings.lookup(c.op)
	k.begin(ctx, c, d, bnd)
	used := secondariesBefore(bnd)
	res, inj := k.pipeline(ctx, c, d, bnd, &used)
	if _, unauthenticated := d.(Unauthenticated); inj == nil && !unauthenticated {
		k.recordAccess(ctx, c, res, used)
	}
	k.end(ctx, c, res, inj)
	return res
}

// pipeline runs steps 7 to 10, setting *used to what the binding says
// about the secondary IAM actions a valid input used (WithActions). A
// panic anywhere in them, in the bound validators, checks and resolvers as
// much as in the handler, becomes Internal carrying the stack, so steps 11
// and 12 still record the call and the client gets 500 InternalFailure.
func (k *Kernel) pipeline(ctx context.Context, c *Call, d Decoded, bnd *binding, used *secondaries) (res Result, inj *fault.Injection) {
	defer func() {
		if v := recover(); v != nil {
			res, inj = Failure{Err: awserr.Internalf(&PanicError{Op: c.label, Value: v, Stack: string(debug.Stack())})}, nil
		}
	}()
	var in Ok
	switch d := d.(type) {
	case Rejected:
		return Failure(d), nil
	case Unauthenticated:
		return Failure(d), nil
	case Ok:
		in = d
	case nil:
		return Failure{Err: awserr.Internalf(&DecodedError{Op: c.opID, Reason: "no decoded input"})}, nil
	}
	switch {
	case c.op == nil:
		return Failure{Err: awserr.Internalf(&DecodedError{Op: c.opID, Reason: "an input for an operation the service does not model; the edge rejects such requests"})}, nil
	case bnd == nil:
		return Failure{Err: NotImplemented(c.op)}, nil
	}
	if err := bnd.validate(in.In); err != nil {
		return Failure{Err: err}, nil
	}
	c.Touch(bnd.resources(c, in.In)...)
	*used = bnd.secondaries(c, in.In)
	if res, inj, stop := k.before(ctx, c); stop {
		return res, inj
	}
	out, err := k.handle(ctx, c, bnd, in)
	if err != nil {
		return Failure{Err: err}, nil
	}
	if res, inj, hit := k.after(ctx, c); hit {
		k.discard(ctx, c, out)
		return res, inj
	}
	if k.strict {
		if err := checkStrict(c.op, out); err != nil {
			k.discard(ctx, c, out)
			return Failure{Err: err}, nil
		}
	}
	status := c.op.SuccessCode
	if status == 0 {
		status = http.StatusOK
	}
	return Output{Out: out, Status: status}, nil
}

// before runs the request-phase fault rules (step 8). It reports stop when
// a rule answers the call; a latency rule holds the call for its wall time
// and lets the rules after it decide.
func (k *Kernel) before(ctx context.Context, c *Call) (Result, *fault.Injection, bool) {
	if k.faults == nil {
		return nil, nil, false
	}
	decision := k.faults.Evaluate(fault.BeforeHandler, k.matchInput(c))
	for {
		switch d := decision.(type) {
		case fault.Fail:
			k.journalFault(ctx, c, d.Injection)
			return Failure{Err: d.Err}, &d.Injection, true
		case fault.Delay:
			k.journalFault(ctx, c, d.Injection)
			if err := k.wallDelay(ctx, d.Wall); err != nil {
				k.journalSpent(ctx, c, d.Then)
				return Failure{Err: err}, nil, true
			}
			decision = d.Then
		case fault.None, fault.FailAfterCommit, fault.FailStatusOK, fault.Drop, nil:
			return nil, nil, false
		}
	}
}

// journalSpent journals the hits of decision, the rules after a latency
// hold the client left: Evaluate decided them with the latency rule, so
// their budgets are spent although they never answer the call.
func (k *Kernel) journalSpent(ctx context.Context, c *Call, decision fault.Decision) {
	for {
		switch d := decision.(type) {
		case fault.Fail:
			k.journalFault(ctx, c, d.Injection)
			return
		case fault.Delay:
			k.journalFault(ctx, c, d.Injection)
			decision = d.Then
		case fault.None, fault.FailAfterCommit, fault.FailStatusOK, fault.Drop, nil:
			return
		}
	}
}

// after runs the after-commit fault rules (step 10) on a call whose handler
// succeeded.
func (k *Kernel) after(ctx context.Context, c *Call) (Result, *fault.Injection, bool) {
	if k.faults == nil {
		return nil, nil, false
	}
	switch d := k.faults.Evaluate(fault.AfterHandler, k.matchInput(c)).(type) {
	case fault.FailAfterCommit:
		k.journalFault(ctx, c, d.Injection)
		return Failure{Err: d.Err}, &d.Injection, true
	case fault.FailStatusOK:
		k.journalFault(ctx, c, d.Injection)
		return StatusOKFailure{Err: d.Err}, &d.Injection, true
	case fault.None, fault.Fail, fault.Delay, fault.Drop, nil:
	}
	return nil, nil, false
}

// handle runs the handler (step 9); pipeline recovers its panics.
func (k *Kernel) handle(ctx context.Context, c *Call, bnd *binding, in Ok) (any, *awserr.Error) {
	w := in.Wire
	if w == nil {
		w = Synthesized{}
	}
	return bnd.run(ctx, c, in.In, w)
}

// maxWait stands for "no emulator deadline" in a wall-time wait.
const maxWait = 100 * 365 * 24 * time.Hour

// wallDelay holds the call for d of wall time, which a frozen clock cannot
// stretch; it answers RequestAborted when the client leaves first.
func (k *Kernel) wallDelay(ctx context.Context, d time.Duration) *awserr.Error {
	<-k.clock.Until(ctx, clock.Deadline{Emu: k.clock.Now().Add(maxWait), WallCap: k.clock.Wall().Add(d)})
	if ctx.Err() != nil {
		return awserr.RequestAborted()
	}
	return nil
}

func (k *Kernel) matchInput(c *Call) fault.MatchInput {
	return fault.MatchInput{Service: c.opID.Service, Op: c.opID, Touched: c.Touched(), Scope: c.scope, Origin: c.origin}
}

// begin opens the call's journal event (step 6).
func (k *Kernel) begin(ctx context.Context, c *Call, d Decoded, bnd *binding) {
	call := journal.APICall{
		Op:          c.opID,
		RequestID:   c.id,
		Origin:      c.origin,
		Principal:   identity.Render(c.principal),
		Mode:        modeName(c.mode),
		TraceHeader: c.trace,
		Input:       k.redacted(ctx, c, d, bnd),
	}
	id, err := k.journal.Begin(journal.Event{Cause: c.cause, Links: c.links, At: c.admitted, Scope: c.scope, Detail: call})
	if err != nil {
		k.log.LogAttrs(ctx, slog.LevelWarn, "journal refused a call; it runs unrecorded",
			slog.String("op", c.label), slog.String("request_id", string(c.id)), slog.String("error", err.Error()))
		return
	}
	c.event = id
	if c.cause.Trace == 0 && c.cause.Parent == 0 {
		c.cause.Trace = id
	}
}

// redacted renders the input for the journal through the generated Redact.
func (k *Kernel) redacted(ctx context.Context, c *Call, d Decoded, bnd *binding) journal.Redacted {
	in, ok := d.(Ok)
	if !ok || bnd == nil || in.In == nil {
		return journal.Redacted{}
	}
	data, err := bnd.redact(in.In)
	if err == nil {
		var r journal.Redacted
		if r, err = journal.NewRedacted(data); err == nil {
			return r
		}
	}
	k.log.LogAttrs(ctx, slog.LevelWarn, "input not journaled",
		slog.String("op", c.label), slog.String("request_id", string(c.id)), slog.String("error", err.Error()))
	return journal.Redacted{}
}

// end records the call's outcome (step 12) and logs the cause of an
// Internal error, which the client never sees.
func (k *Kernel) end(ctx context.Context, c *Call, res Result, inj *fault.Injection) {
	var outcome journal.Outcome
	switch r := res.(type) {
	case Output:
		outcome = journal.OK{}
	case Failure:
		outcome = k.failed(ctx, c, r.Err, inj)
	case StatusOKFailure:
		outcome = k.failed(ctx, c, r.Err, inj)
	}
	if c.event == 0 || outcome == nil {
		return
	}
	if err := k.journal.End(c.event, outcome, c.elapsed(), c.Touched()...); err != nil {
		k.log.LogAttrs(ctx, slog.LevelWarn, "journal refused a call's outcome",
			slog.String("op", c.label), slog.String("request_id", string(c.id)), slog.String("error", err.Error()))
	}
}

func (k *Kernel) failed(ctx context.Context, c *Call, e *awserr.Error, inj *fault.Injection) journal.Outcome {
	id := journal.ErrorIdentity{Shape: string(e.ID.Shape)}
	if inj != nil {
		return journal.Injected{Rule: inj.Rule, Identity: id}
	}
	failed := journal.Failed{Identity: id, Status: e.ID.Status, Message: e.Message}
	if in, ok := e.Origin.(awserr.Internal); ok {
		cause := internalCause(in)
		k.log.LogAttrs(ctx, slog.LevelError, "internal failure",
			slog.String("op", c.label), slog.String("request_id", string(c.id)), slog.String("cause", cause))
		failed.Message += " Cause: " + cause
	}
	return failed
}

// internalCause renders the cause of an Internal error for the journal and
// the log, with the stack of a panic; the client only ever sees the generic
// message.
func internalCause(in awserr.Internal) string {
	if in.Cause == nil {
		return "unknown; the Internal error carries no cause"
	}
	cause := in.Cause.Error()
	if p, ok := errors.AsType[*PanicError](in.Cause); ok {
		cause += "\n" + p.Stack
	}
	return cause
}

func (k *Kernel) journalFault(ctx context.Context, c *Call, inj fault.Injection) {
	_, err := k.journal.Append(journal.Event{
		Cause:  ref.Cause{Parent: c.event, Trace: c.cause.Trace},
		At:     c.admitted,
		Scope:  c.scope,
		Detail: journal.FaultInjected{Rule: inj.Rule, Hit: inj.Hit, Of: inj.Of},
	})
	if err != nil {
		k.log.LogAttrs(ctx, slog.LevelWarn, "journal refused a fault hit",
			slog.String("rule", string(inj.Rule)), slog.String("error", err.Error()))
	}
}

// recordAccess records the IAM actions the call used (step 11), as
// recordedActions selects them from the operation's list and what its
// binding declared. It records nothing for an anonymous caller, a service
// acting on its own behalf (AsPlatform), which no policy grants, or an
// internal operation that declares no action. Dispatch skips it for an
// injected fault and for an Unauthenticated request.
func (k *Kernel) recordAccess(ctx context.Context, c *Call, res Result, used secondaries) {
	if _, platform := c.mode.(AsPlatform); k.access == nil || c.op == nil || platform {
		return
	}
	principal, ok := accessPrincipal(c.principal)
	if !ok {
		return
	}
	actions := k.recordedActions(ctx, c, used)
	if len(actions) == 0 {
		return
	}
	var outcome access.Outcome = access.Succeeded{}
	switch r := res.(type) {
	case Failure:
		outcome = access.Failed{Code: r.Err.ID.Shape.Name()}
	case StatusOKFailure:
		outcome = access.Failed{Code: r.Err.ID.Shape.Name()}
	case Output:
	}
	err := k.access.Record(access.Entry{
		Principal: principal,
		Mode:      accessMode(c.mode),
		Service:   c.op.Service.IAMPrefix,
		Actions:   actions,
		Resources: c.Touched(),
		Outcome:   outcome,
		At:        c.admitted.Wire(),
	})
	if err != nil {
		k.log.LogAttrs(ctx, slog.LevelWarn, "access recorder refused a call",
			slog.String("op", c.label), slog.String("request_id", string(c.id)), slog.String("error", err.Error()))
	}
}

// accessPrincipal maps a caller onto the access recorder's principals: a
// role session is its role; an anonymous caller has nothing to record.
func accessPrincipal(p identity.Principal) (access.Principal, bool) {
	switch p := p.(type) {
	case identity.Root:
		return access.Root{Account: p.Account}, true
	case identity.User:
		return access.User{Account: p.Account, Path: p.Path, Name: p.Name}, true
	case identity.AssumedRole:
		return access.Role{Account: p.Account, Path: p.Role.Path, Name: p.Role.Name}, true
	case identity.ServicePrincipal:
		return access.ServicePrincipal{Name: p.Name}, true
	case identity.Anonymous, nil:
	}
	return nil, false
}

func accessMode(m PrincipalMode) access.Mode {
	switch m := m.(type) {
	case AsCaller:
		return access.AsCaller{}
	case AsExecutionRole:
		return access.AsExecutionRole{}
	case AsService:
		return access.AsService{Source: m.Source}
	case AsPlatform:
		// recordAccess records no AsPlatform call; Direct keeps the switch
		// whole.
	}
	return access.Direct{}
}
