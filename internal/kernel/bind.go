package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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

// BindOption adjusts one binding: WithValidator, WithCheck, WithResources,
// WithResourcesFor, WithItemCount, WithActions, WithoutSecondaryActions,
// WithConditionKeys, AuthorizedInHandler, NeverAuthorized and
// AuthorizedAfterResolve build them.
type BindOption interface {
	// apply adds the option to cfg, the *bindConfig of the operation's input
	// type, and reports false when the option is for another input type.
	apply(cfg any) bool
}

// bindConfig is what the options of one binding add to it. It is not
// generic: an input type's struct is its own GC shape, so code generic
// over In is compiled once per operation, which cost about 8 MiB of the
// binary (ADR 0001's binary budget). Its functions take the input as any
// instead, erased where each option is built over the pointer type *In,
// whose shape every input shares; they run only on an input of type In,
// which the binding checks first.
type bindConfig struct {
	// in is the (*In)(nil) of the binding's input type: the token an
	// option's matches, and what %T names in its errors.
	in         any
	validators []func(any) []awserr.Violation
	checks     []func(any) *awserr.Error
	resources  []func(*Call, any) []aws.ARN
	actions    []func(*Call, any) []registry.IAMAction
	erased     erasedConfig
	// count is the last WithItemCount; nil without one.
	count func(any) int
	// authorization is the last AuthorizedInHandler, NeverAuthorized or
	// AuthorizedAfterResolve; atStep8 without one.
	authorization authorization
}

// erasedConfig is what WithResourcesFor and WithConditionKeys add: what the
// binding hands on to step 8 and the access record as is.
type erasedConfig struct {
	scoped []scopedResources
	keys   []func(*Call, any) ConditionKeys
}

func (e erasedConfig) clone() erasedConfig {
	return erasedConfig{scoped: slices.Clone(e.scoped), keys: slices.Clone(e.keys)}
}

// validate runs step 7 over in: the generated validator and the typed
// validators, rendered by the generated policy, then the checks.
func (cfg *bindConfig) validate(in any) *awserr.Error {
	var vs []awserr.Violation
	if g, ok := in.(interface{ Validate() []awserr.Violation }); ok {
		vs = g.Validate()
	}
	for _, v := range cfg.validators {
		vs = append(vs, v(in)...)
	}
	if err := validateAs(cfg.in, vs); err != nil {
		return err
	}
	for _, check := range cfg.checks {
		if err := check(in); err != nil {
			return err
		}
	}
	return nil
}

// resolve returns the resources the input in addresses, from every
// WithResources option.
func (cfg *bindConfig) resolve(c *Call, in any) []aws.ARN {
	arns := make([]aws.ARN, 0, len(cfg.resources))
	for _, r := range cfg.resources {
		arns = append(arns, r(c, in)...)
	}
	return arns
}

// typedOption is a WithValidator, WithCheck, WithResources,
// WithResourcesFor, WithItemCount, WithActions or WithConditionKeys option,
// erased where it is built: in is the (*In)(nil) of its input type, and add
// adds it to a bindConfig of that type.
type typedOption struct {
	in  any
	add func(cfg *bindConfig)
}

func (o typedOption) apply(cfg any) bool {
	c, ok := cfg.(*bindConfig)
	if !ok || c.in != o.in {
		return false
	}
	o.add(c)
	return true
}

// The ...Of functions build the typed options over P, the input's pointer
// type *In. Every pointer type shares one GC shape under an any
// constraint, so each is compiled once, not per input type. A function
// they erase meets only an input of type P (configured checks it first);
// another input gets the zero answer.

func validatorOf[P any](v func(P) []awserr.Violation) BindOption {
	var token P
	f := func(in any) []awserr.Violation {
		typed, ok := in.(P)
		if !ok {
			return nil
		}
		return v(typed)
	}
	return typedOption{in: token, add: func(c *bindConfig) { c.validators = append(c.validators, f) }}
}

func checkOf[P any](check func(P) *awserr.Error) BindOption {
	var token P
	f := func(in any) *awserr.Error {
		typed, ok := in.(P)
		if !ok {
			return nil
		}
		return check(typed)
	}
	return typedOption{in: token, add: func(c *bindConfig) { c.checks = append(c.checks, f) }}
}

func resourcesOf[P any](resolve func(*Call, P) []aws.ARN) BindOption {
	var token P
	f := func(c *Call, in any) []aws.ARN {
		typed, ok := in.(P)
		if !ok {
			return nil
		}
		return resolve(c, typed)
	}
	return typedOption{in: token, add: func(c *bindConfig) { c.resources = append(c.resources, f) }}
}

func countOf[P any](count func(P) int) BindOption {
	var token P
	f := func(in any) int {
		typed, ok := in.(P)
		if !ok {
			return 0
		}
		return count(typed)
	}
	return typedOption{in: token, add: func(c *bindConfig) { c.count = f }}
}

func actionsOf[P any](declare func(*Call, P) []registry.IAMAction) BindOption {
	var token P
	f := func(c *Call, in any) []registry.IAMAction {
		typed, ok := in.(P)
		if !ok {
			return nil
		}
		return declare(c, typed)
	}
	return typedOption{in: token, add: func(c *bindConfig) { c.actions = append(c.actions, f) }}
}

func scopedOf[P any](actions []registry.IAMAction, resolve func(*Call, P) []aws.ARN) BindOption {
	var token P
	s := scopedResources{actions: slices.Clone(actions), resolve: func(c *Call, in any) []aws.ARN {
		typed, ok := in.(P)
		if !ok {
			return nil
		}
		return resolve(c, typed)
	}}
	return typedOption{in: token, add: func(c *bindConfig) { c.erased.scoped = append(c.erased.scoped, s) }}
}

func keysOf[P any](supply func(*Call, P) ConditionKeys) BindOption {
	var token P
	f := func(c *Call, in any) ConditionKeys {
		typed, ok := in.(P)
		if !ok {
			return nil
		}
		return supply(c, typed)
	}
	return typedOption{in: token, add: func(c *bindConfig) { c.erased.keys = append(c.erased.keys, f) }}
}

// WithValidator adds a stateless, typed validator that runs at pipeline
// step 7, after the generated validator and before fault rules, so
// hand-owned limit texts keep the Coral check order. Its violations are
// rendered by the operation's generated policy.
func WithValidator[In any](v func(*In) []awserr.Violation) BindOption {
	return validatorOf[*In](v)
}

// WithCheck adds a stateless, typed check that runs at step 7 after every
// validator and before fault rules, for a hand-owned answer the generated
// policy cannot render: another code than the policy's, or a text no
// overlay reason template carries yet. Prefer WithValidator whenever the
// policy renders the answer.
func WithCheck[In any](check func(*In) *awserr.Error) BindOption {
	return checkOf[*In](check)
}

// WithResources names the resources a call's input addresses, so
// request-phase fault rules can match them by resource (step 8) before the
// handler runs. The kernel touches what it returns once the input is valid;
// an input that names no resolvable resource returns nothing, and the
// handler answers it as usual.
func WithResources[In any](resolve func(c *Call, in *In) []aws.ARN) BindOption {
	return resourcesOf[*In](resolve)
}

// WithItemCount names how many items a transactional call's input carries
// (DynamoDB TransactItems, TransactStatements), so a fault rule's per-item
// members line up with them: once the input is valid, the kernel sets
// fault.MatchInput.Items from it for the call's request-phase rules.
// Without the option Items is 0; with several, the last one applies.
func WithItemCount[In any](count func(*In) int) BindOption {
	return countOf[*In](count)
}

// Bind binds h as op's handler with the coverage the bind site states.
// Mistakes (op bound twice, op outside the binder's service or the registry,
// an option of another input type) are kept for Kernel.BindErr.
func Bind[In, Out any](b *Binder, op Op[In, Out], h Handler[In, Out], cov registry.BindCoverage, opts ...BindOption) {
	bindHandler(b, op.desc, newInput[In], func(ctx context.Context, c *Call, in *In) (any, *awserr.Error) {
		out, err := h(ctx, c, in)
		if out == nil {
			// A nil *Out is a nil output, not a non-nil any holding it.
			return nil, err
		}
		return out, err
	}, cov, opts)
}

// newInput returns a new *In to decode into.
func newInput[In any]() any { return new(In) }

// bindHandler is Bind over the input's pointer type P, its output erased,
// so its body is compiled once rather than per operation.
func bindHandler[P any](b *Binder, desc *registry.Operation, newIn func() any, h func(context.Context, *Call, P) (any, *awserr.Error), cov registry.BindCoverage, opts []BindOption) {
	b.bind(desc, cov, typedBinding(b, desc, opts, newIn, func(ctx context.Context, c *Call, in P, _ Wire) (any, *awserr.Error) {
		out, err := h(ctx, c, in)
		switch {
		case err != nil && out != nil:
			b.k.discard(ctx, c, out)
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
	bindRaw(b, op.desc, newInput[In], h, cov, opts)
}

// bindRaw is BindRaw over the input's pointer type P.
func bindRaw[P any](b *Binder, desc *registry.Operation, newIn func() any, h func(context.Context, *Call, P, Wire) (RawResponse, *awserr.Error), cov registry.BindCoverage, opts []BindOption) {
	bnd := typedBinding(b, desc, opts, newIn, func(ctx context.Context, c *Call, in P, w Wire) (any, *awserr.Error) {
		resp, err := h(ctx, c, in, w)
		if err != nil {
			return nil, err
		}
		return &RawOutput{Response: resp}, nil
	})
	bnd.passthrough = true
	b.bind(desc, cov, bnd)
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
		items:     func(any) int { return 0 },
		redact:    func(any) ([]byte, error) { return nil, nil },
	})
}

// typedBinding builds the type-erased binding of an operation whose input
// type is P, a *In: decode target, validators and checks, resource
// resolution, declared actions, redaction and the handler adapter.
func typedBinding[P any](b *Binder, desc *registry.Operation, opts []BindOption, newIn func() any, run func(ctx context.Context, c *Call, in P, w Wire) (any, *awserr.Error)) *binding {
	var id ref.OperationID
	if desc != nil {
		id = desc.ID
	}
	var token P
	t := inputType{
		newInput: newIn,
		is: func(in any) bool {
			_, ok := in.(P)
			return ok
		},
		redact: func(in any) ([]byte, error) {
			if r, ok := in.(interface{ Redact() P }); ok {
				return json.Marshal(r.Redact())
			}
			return json.Marshal(in)
		},
		run: func(ctx context.Context, c *Call, in any, w Wire) (any, *awserr.Error) {
			typed, ok := in.(P)
			if !ok {
				return nil, awserr.Internalf(&InputTypeError{Op: id, Got: fmt.Sprintf("%T", in), Want: fmt.Sprintf("%T", token)})
			}
			return run(ctx, c, typed, w)
		},
	}
	return configured(b, id, bindConfig{in: token}, opts, t)
}

// inputType is what a binding does with its input that depends on the
// input's type, erased by typedBinding.
type inputType struct {
	newInput func() any
	// is reports whether in is of the input type.
	is     func(in any) bool
	redact func(in any) ([]byte, error)
	// run calls the handler; an input of another type is an Internal error.
	run func(ctx context.Context, c *Call, in any, w Wire) (any, *awserr.Error)
}

// clone returns a copy of cfg whose option lists share no backing array
// with cfg's, so extending one leaves the other as it was.
func (cfg *bindConfig) clone() bindConfig {
	return bindConfig{
		in:            cfg.in,
		validators:    slices.Clone(cfg.validators),
		checks:        slices.Clone(cfg.checks),
		resources:     slices.Clone(cfg.resources),
		actions:       slices.Clone(cfg.actions),
		erased:        cfg.erased.clone(),
		count:         cfg.count,
		authorization: cfg.authorization,
	}
}

// configured builds the binding of base extended with opts; its extend
// builds the same binding extended again, for Declare.
func configured(b *Binder, id ref.OperationID, base bindConfig, opts []BindOption, t inputType) *binding {
	cfg := base.clone()
	for _, o := range opts {
		if !o.apply(&cfg) {
			b.k.recordBindErr(&BindError{Op: id, Problem: OptionType})
		}
	}
	bnd := &binding{
		newInput: t.newInput,
		validate: func(in any) *awserr.Error {
			if !t.is(in) {
				return awserr.Internalf(&InputTypeError{Op: id, Got: fmt.Sprintf("%T", in), Want: fmt.Sprintf("%T", cfg.in)})
			}
			return cfg.validate(in)
		},
		resources: func(c *Call, in any) []aws.ARN {
			if !t.is(in) {
				return nil
			}
			return cfg.resolve(c, in)
		},
		items: func(in any) int {
			if !t.is(in) || cfg.count == nil {
				return 0
			}
			return cfg.count(in)
		},
		keys:          cfg.erased.keys,
		scoped:        cfg.erased.scoped,
		redact:        t.redact,
		run:           t.run,
		extend:        func(more []BindOption) *binding { return configured(b, id, cfg, more, t) },
		authorization: cfg.authorization,
	}
	if len(cfg.actions) > 0 {
		bnd.actions = func(c *Call, in any) []registry.IAMAction {
			if !t.is(in) {
				return nil
			}
			return cfg.declare(c, in)
		}
	}
	return bnd
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
	// items counts a valid input's transaction items (WithItemCount); 0
	// without the option.
	items func(in any) int
	// keys supply the request condition keys of a valid input
	// (WithConditionKeys); nil without the option and for raw HTTP
	// bindings.
	keys []func(c *Call, in any) ConditionKeys
	// scoped name, for a valid input, the resources each named action
	// applies to (WithResourcesFor); nil without the option and for raw HTTP
	// bindings.
	scoped []scopedResources
	// actions are the IAM actions a valid input declares used besides the
	// primary (WithActions, WithoutSecondaryActions); nil when the binding
	// declares none, so its calls record the operation's whole list.
	actions func(c *Call, in any) []registry.IAMAction
	redact  func(in any) ([]byte, error)
	run     func(ctx context.Context, c *Call, in any, w Wire) (any, *awserr.Error)
	// extend returns the binding with more options (Declare); nil for raw
	// HTTP bindings, which take none.
	extend func(opts []BindOption) *binding
	// authorization is what step 8 asks about the binding's calls
	// (AuthorizedInHandler, NeverAuthorized, AuthorizedAfterResolve).
	authorization authorization
}

// scoping resolves the binding's WithResourcesFor options for the valid
// input in: the resources each named action applies to, and all of them to
// touch; nil without the option.
func (bnd *binding) scoping(c *Call, in any) (scoping, []aws.ARN) {
	return scope(bnd.scoped, c, in)
}

// conditionKeys returns the request condition keys the binding's
// WithConditionKeys options supply for the valid input in; nil without one.
func (bnd *binding) conditionKeys(c *Call, in any) ConditionKeys {
	return conditionKeys(bnd.keys, c, in)
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
	// BoundTwice is a bind of an operation already bound.
	BoundTwice BindProblem = iota + 1
	// ForeignOperation is a bind of another service's operation than the
	// binder's.
	ForeignOperation
	// NotInRegistry is a bind of an operation whose service is not in the
	// kernel's registry.
	NotInRegistry
	// NilOperation is a bind of a nil operation descriptor.
	NilOperation
	// NoCoverage is a bind site that states no coverage.
	NoCoverage
	// NotRaw is a BindRawHTTP of an operation not of kind raw.
	NotRaw
	// OptionType is a WithValidator, WithCheck, WithResources,
	// WithResourcesFor, WithItemCount, WithActions or WithConditionKeys
	// option for another input type than the operation's.
	OptionType
	// DeclaredUnbound is a Declare for an operation not bound yet, so no
	// binding takes its options.
	DeclaredUnbound
	// DeclaredRaw is a Declare for an operation bound with BindRawHTTP,
	// which takes no options.
	DeclaredRaw
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
		why = "a WithValidator, WithCheck, WithResources, WithResourcesFor, WithItemCount, WithActions or WithConditionKeys option is for another input type than the operation's; build it for the operation's input"
	case DeclaredUnbound:
		why = "Declare names an operation that is not bound, so its options are not applied; call Declare after the operation's bind site"
	case DeclaredRaw:
		why = "Declare names an operation bound with BindRawHTTP, which takes no options; its options are not applied"
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
