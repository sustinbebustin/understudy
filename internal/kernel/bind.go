package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Binder binds the handlers of one service's operations into its kernel.
type Binder struct {
	k   *Kernel
	svc *registry.Service
}

// BindOption adjusts one binding: WithValidator, WithCheck and
// WithResources build them.
type BindOption interface {
	// apply adds the option to cfg, the *bindConfig of the operation's input
	// type, and reports false when the option is for another input type.
	apply(cfg any) bool
}

// bindConfig is what the options of one binding add to it.
type bindConfig[In any] struct {
	validators []func(*In) []awserr.Violation
	checks     []func(*In) *awserr.Error
	resources  []func(*Call, *In) []aws.ARN
}

// validate runs step 7 over in: the generated validator and the typed
// validators, rendered by the generated policy, then the checks.
func (cfg *bindConfig[In]) validate(in *In) *awserr.Error {
	var vs []awserr.Violation
	if g, ok := any(in).(interface{ Validate() []awserr.Violation }); ok {
		vs = g.Validate()
	}
	for _, v := range cfg.validators {
		vs = append(vs, v(in)...)
	}
	if err := Validate[In](vs); err != nil {
		return err
	}
	for _, check := range cfg.checks {
		if err := check(in); err != nil {
			return err
		}
	}
	return nil
}

// resolve returns the resources in names, from every WithResources option.
func (cfg *bindConfig[In]) resolve(c *Call, in *In) []aws.ARN {
	arns := make([]aws.ARN, 0, len(cfg.resources))
	for _, r := range cfg.resources {
		arns = append(arns, r(c, in)...)
	}
	return arns
}

type validatorOption[In any] func(*In) []awserr.Violation

func (o validatorOption[In]) apply(cfg any) bool {
	c, ok := cfg.(*bindConfig[In])
	if ok {
		c.validators = append(c.validators, o)
	}
	return ok
}

type checkOption[In any] func(*In) *awserr.Error

func (o checkOption[In]) apply(cfg any) bool {
	c, ok := cfg.(*bindConfig[In])
	if ok {
		c.checks = append(c.checks, o)
	}
	return ok
}

type resourcesOption[In any] func(*Call, *In) []aws.ARN

func (o resourcesOption[In]) apply(cfg any) bool {
	c, ok := cfg.(*bindConfig[In])
	if ok {
		c.resources = append(c.resources, o)
	}
	return ok
}

// WithValidator adds a stateless, typed validator that runs at pipeline
// step 7, after the generated validator and before fault rules, so hand-owned
// limit texts keep the Coral check order (M7). Its violations are rendered
// by the operation's generated policy.
func WithValidator[In any](v func(*In) []awserr.Violation) BindOption {
	return validatorOption[In](v)
}

// WithCheck adds a stateless, typed check that runs at step 7 after every
// validator and before fault rules, for a hand-owned answer the generated
// policy cannot render: another code than the policy's, or a text no
// overlay reason template carries yet. Prefer WithValidator whenever the
// policy renders the answer.
func WithCheck[In any](check func(*In) *awserr.Error) BindOption {
	return checkOption[In](check)
}

// WithResources names the resources a call's input addresses, so
// request-phase fault rules can match them by resource (step 8) before the
// handler runs. The kernel touches what it returns once the input is valid;
// an input that names no resolvable resource returns nothing, and the
// handler answers it as usual.
func WithResources[In any](resolve func(c *Call, in *In) []aws.ARN) BindOption {
	return resourcesOption[In](resolve)
}

// Bind binds h as op's handler with the coverage the bind site states.
// Mistakes (op bound twice, op outside the binder's service or the registry,
// an option of another input type) are kept for Kernel.BindErr.
func Bind[In, Out any](b *Binder, op Op[In, Out], h Handler[In, Out], cov registry.BindCoverage, opts ...BindOption) {
	desc := op.desc
	b.bind(desc, cov, typedBinding[In](b, desc, opts, func(ctx context.Context, c *Call, in *In, _ Wire) (any, *awserr.Error) {
		out, err := h(ctx, c, in)
		switch {
		case err != nil && out != nil:
			return nil, awserr.Internalf(&HandlerContractError{Op: desc.ID, Both: true})
		case err != nil:
			return nil, err
		case out == nil:
			return nil, awserr.Internalf(&HandlerContractError{Op: desc.ID})
		}
		return out, nil
	}))
}

// BindRaw binds the passthrough handler of op (DynamoDB, ADR 0014). The
// input is decoded and validated like any other; the handler answers with
// the bytes to send, which the call's output carries as a *RawOutput.
func BindRaw[In any](b *Binder, op Op[In, RawOutput], h RawHandler[In], cov registry.BindCoverage, opts ...BindOption) {
	bnd := typedBinding[In](b, op.desc, opts, func(ctx context.Context, c *Call, in *In, w Wire) (any, *awserr.Error) {
		resp, err := h(ctx, c, in, w)
		if err != nil {
			return nil, err
		}
		return &RawOutput{Response: resp}, nil
	})
	bnd.passthrough = true
	b.bind(op.desc, cov, bnd)
}

// BindRawHTTP binds the handler of a raw operation, which owns the HTTP body
// in both directions.
func BindRawHTTP(b *Binder, op *registry.Operation, h RawHTTPHandler, cov registry.BindCoverage) {
	if op != nil && op.Kind != registry.Raw {
		b.k.recordBindErr(&BindError{Op: op.ID, Problem: NotRaw})
		return
	}
	b.bind(op, cov, &binding{
		raw: true,
		run: func(ctx context.Context, c *Call, in any, _ Wire) (any, *awserr.Error) {
			x, ok := in.(RawExchange)
			if !ok {
				return nil, awserr.Internalf(&InputTypeError{Op: op.ID, Got: fmt.Sprintf("%T", in), Want: "kernel.RawExchange"})
			}
			if err := h(ctx, c, x.Request, x.Writer); err != nil {
				return nil, err
			}
			return RawWritten{}, nil
		},
		validate:  func(any) *awserr.Error { return nil },
		resources: func(*Call, any) []aws.ARN { return nil },
		redact:    func(any) ([]byte, error) { return nil, nil },
	})
}

// typedBinding builds the type-erased binding of an operation whose input is
// In: decode target, validators and checks, resource resolution, redaction
// and the handler adapter.
func typedBinding[In any](b *Binder, desc *registry.Operation, opts []BindOption, run func(ctx context.Context, c *Call, in *In, w Wire) (any, *awserr.Error)) *binding {
	var id ref.OperationID
	if desc != nil {
		id = desc.ID
	}
	var cfg bindConfig[In]
	for _, o := range opts {
		if !o.apply(&cfg) {
			b.k.recordBindErr(&BindError{Op: id, Problem: OptionType})
		}
	}
	return &binding{
		newInput: func() any { return new(In) },
		validate: func(in any) *awserr.Error {
			typed, ok := in.(*In)
			if !ok {
				return awserr.Internalf(&InputTypeError{Op: id, Got: fmt.Sprintf("%T", in), Want: fmt.Sprintf("%T", (*In)(nil))})
			}
			return cfg.validate(typed)
		},
		resources: func(c *Call, in any) []aws.ARN {
			typed, ok := in.(*In)
			if !ok {
				return nil
			}
			return cfg.resolve(c, typed)
		},
		redact: func(in any) ([]byte, error) {
			if r, ok := in.(interface{ Redact() *In }); ok {
				return json.Marshal(r.Redact())
			}
			return json.Marshal(in)
		},
		run: func(ctx context.Context, c *Call, in any, w Wire) (any, *awserr.Error) {
			typed, ok := in.(*In)
			if !ok {
				return nil, awserr.Internalf(&InputTypeError{Op: id, Got: fmt.Sprintf("%T", in), Want: fmt.Sprintf("%T", (*In)(nil))})
			}
			return run(ctx, c, typed, w)
		},
	}
}

func (b *Binder) bind(op *registry.Operation, cov registry.BindCoverage, bnd *binding) {
	if op == nil {
		b.k.recordBindErr(&BindError{Problem: NilOperation, Service: b.svc.ID})
		return
	}
	bnd.op, bnd.coverage = op, cov
	switch {
	case cov == nil:
		b.k.recordBindErr(&BindError{Op: op.ID, Service: b.svc.ID, Problem: NoCoverage})
		return
	case op.Service != b.svc:
		b.k.recordBindErr(&BindError{Op: op.ID, Service: b.svc.ID, Problem: ForeignOperation})
		return
	}
	slot, ok := b.k.bindings.slot(op)
	if !ok {
		b.k.recordBindErr(&BindError{Op: op.ID, Service: b.svc.ID, Problem: NotInRegistry})
		return
	}
	if !slot.CompareAndSwap(nil, bnd) {
		b.k.recordBindErr(&BindError{Op: op.ID, Service: b.svc.ID, Problem: BoundTwice})
	}
}

// binding is one bound operation, type-erased for the pipeline.
type binding struct {
	op       *registry.Operation
	coverage registry.BindCoverage
	// raw is set for an operation bound with BindRawHTTP, whose input is a
	// RawExchange and whose output the handler writes itself.
	raw bool
	// passthrough is set for an operation bound with BindRaw, whose handler
	// forwards the client's bytes: the edge hands it a FromClient wire.
	passthrough bool
	// newInput returns a new *In to decode into; nil for raw HTTP bindings.
	newInput func() any
	validate func(in any) *awserr.Error
	// resources names what a valid input addresses (WithResources).
	resources func(c *Call, in any) []aws.ARN
	redact    func(in any) ([]byte, error)
	run       func(ctx context.Context, c *Call, in any, w Wire) (any, *awserr.Error)
}

// Bindings is a kernel's binding table, indexed by service and
// Operation.Ordinal. Descriptors never hold bindings.
type Bindings struct {
	slots map[*registry.Service][]atomic.Pointer[binding]
}

func newBindings(reg *registry.Registry) *Bindings {
	b := &Bindings{slots: map[*registry.Service][]atomic.Pointer[binding]{}}
	for _, s := range reg.Services() {
		b.slots[s] = make([]atomic.Pointer[binding], len(s.Operations))
	}
	return b
}

// Status is what the kernel reports for an operation: bound with the
// coverage its bind site stated, or unbound.
//
//sumtype:decl
type Status interface{ isStatus() }

// Bound is an operation with a handler.
type Bound struct{ Coverage registry.BindCoverage }

// Unbound is an operation without a handler; calls to it answer 501.
type Unbound struct{}

func (Bound) isStatus()   {}
func (Unbound) isStatus() {}

// Status reports whether op is bound in this kernel.
func (b *Bindings) Status(op *registry.Operation) Status {
	if bnd, ok := b.lookup(op); ok {
		return Bound{Coverage: bnd.coverage}
	}
	return Unbound{}
}

// NewInput returns a new value of op's input type to decode into, and false
// when op is unbound or raw: Dispatch answers an unbound operation with 501
// before it reads the input.
func (k *Kernel) NewInput(op *registry.Operation) (any, bool) {
	bnd, ok := k.bindings.lookup(op)
	if !ok || bnd.newInput == nil {
		return nil, false
	}
	return bnd.newInput(), true
}

// Passthrough reports whether op is bound with BindRaw. Its handler
// forwards the client's request, so the edge buffers the body (within the
// operation's cap) and passes it as a FromClient wire (ADR 0014).
func (k *Kernel) Passthrough(op *registry.Operation) bool {
	bnd, ok := k.bindings.lookup(op)
	return ok && bnd.passthrough
}

func (b *Bindings) lookup(op *registry.Operation) (*binding, bool) {
	slot, ok := b.slot(op)
	if !ok {
		return nil, false
	}
	bnd := slot.Load()
	return bnd, bnd != nil
}

// slot returns op's slot when op is the descriptor this kernel's registry
// holds at op's ordinal.
func (b *Bindings) slot(op *registry.Operation) (*atomic.Pointer[binding], bool) {
	if op == nil {
		return nil, false
	}
	slots, ok := b.slots[op.Service]
	if !ok || op.Ordinal < 0 || op.Ordinal >= len(slots) || op.Service.Operations[op.Ordinal] != op {
		return nil, false
	}
	return &slots[op.Ordinal], true
}

// BindProblem says what is wrong with a bind.
type BindProblem uint8

// BindProblem values.
const (
	BoundTwice BindProblem = iota + 1
	ForeignOperation
	NotInRegistry
	NilOperation
	NoCoverage
	NotRaw
	OptionType
)

// BindError reports a bind the kernel refused; Kernel.BindErr collects them.
type BindError struct {
	Service ref.ServiceID
	Op      ref.OperationID
	Problem BindProblem
}

func (e *BindError) Error() string {
	var why string
	switch e.Problem {
	case BoundTwice:
		why = "it is already bound; bind each operation once"
	case ForeignOperation:
		why = fmt.Sprintf("it belongs to another service than the binder's %q; bind it through its own service's binder", e.Service)
	case NotInRegistry:
		why = "its service is not in the kernel's registry; add the service to registry.New in wire.go"
	case NilOperation:
		why = "the operation descriptor is nil"
	case NoCoverage:
		why = "the bind site states no coverage; pass registry.Full, StoredOnly or Partial"
	case NotRaw:
		why = "BindRawHTTP binds only operations of kind raw"
	case OptionType:
		why = "a WithValidator, WithCheck or WithResources option is for another input type than the operation's"
	default:
		why = "the bind is invalid"
	}
	return fmt.Sprintf("kernel: cannot bind %s: %s", e.Op, why)
}

// HandlerContractError is a handler that returned both an output and an
// error, or neither.
type HandlerContractError struct {
	Op   ref.OperationID
	Both bool
}

func (e *HandlerContractError) Error() string {
	if e.Both {
		return fmt.Sprintf("the %s handler returned an output and an error; return exactly one", e.Op)
	}
	return fmt.Sprintf("the %s handler returned neither an output nor an error; return &Output{} for a unit output", e.Op)
}

// InputTypeError is an input of another type than the bound operation's.
type InputTypeError struct {
	Op        ref.OperationID
	Got, Want string
}

func (e *InputTypeError) Error() string {
	return fmt.Sprintf("the %s binding received an input of type %s, want %s", e.Op, e.Got, e.Want)
}
