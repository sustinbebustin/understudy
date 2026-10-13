package kernel

import (
	"context"
	"log/slog"
	"slices"

	"github.com/sustinbebustin/understudy/internal/registry"
)

type actionsOption[In any] func(*Call, *In) []registry.IAMAction

func (o actionsOption[In]) apply(cfg any) bool {
	c, ok := cfg.(*bindConfig[In])
	if ok {
		c.actions = append(c.actions, o)
	}
	return ok
}

// WithActions declares, from a call's valid input, the IAM actions of the
// operation's own service the call uses besides its primary action (the
// first of Operation.IAM): s3:PutObjectTagging for a PutObject that carries
// tags, sts:TagSession for an AssumeRole with session tags. It runs where
// WithResources does, once the input is valid. A binding with a WithActions
// or WithoutSecondaryActions option records the primary action and exactly
// the declared ones, so a policy grants what the calls used; a binding with
// neither records every action of the generated list, which over-grants
// but never under-grants. An action outside the generated list is a
// bind-site bug: it is logged and not recorded. For an internal operation,
// which has no generated list, the declared actions are all it records.
func WithActions[In any](declare func(c *Call, in *In) []registry.IAMAction) BindOption {
	return actionsOption[In](declare)
}

// noSecondaryActions is the option WithoutSecondaryActions returns. It fits
// every input type.
type noSecondaryActions struct{}

// declaresNoAction is a binding configuration that can record a decision
// to declare no secondary action.
type declaresNoAction interface{ declareNoAction() }

func (noSecondaryActions) apply(cfg any) bool {
	c, ok := cfg.(declaresNoAction)
	if ok {
		c.declareNoAction()
	}
	return ok
}

func (cfg *bindConfig[In]) declareNoAction() {
	cfg.actions = append(cfg.actions, func(*Call, *In) []registry.IAMAction { return nil })
}

// WithoutSecondaryActions records the decision that no call of the
// operation needs the other actions of its service its generated list
// holds, such as s3:GetObjectLegalHold on HeadObject, which only decides
// whether the response carries the object's lock headers. The call
// records its primary action alone, rather than the whole list a binding
// that declares nothing records. State why beside the option.
func WithoutSecondaryActions() BindOption { return noSecondaryActions{} }

// declare returns the IAM actions that every WithActions option declares
// used for the input in.
func (cfg *bindConfig[In]) declare(c *Call, in *In) []registry.IAMAction {
	out := make([]registry.IAMAction, 0, len(cfg.actions))
	for _, d := range cfg.actions {
		out = append(out, d(c, in)...)
	}
	return out
}

// Declare adds opts to op's binding, so a service can state beside its
// other declarations, rather than at op's bind site, what op's calls touch
// (WithResources) and which IAM actions they use (WithActions,
// WithoutSecondaryActions). It runs after op's bind site and, like Bind,
// while the service is constructed, before the kernel dispatches. Declaring
// an operation that is not bound yet, or one bound with BindRawHTTP, which
// takes no options, applies nothing and is a mistake Kernel.BindErr
// reports, as is an option of another input type.
func Declare[In, Out any](b *Binder, op Op[In, Out], opts ...BindOption) {
	desc := op.desc
	switch {
	case desc == nil:
		b.k.recordBindErr(&BindError{Problem: NilOperation, Service: b.svc.ID})
		return
	case desc.Service != b.svc:
		b.k.recordBindErr(&BindError{Op: desc.ID, Service: b.svc.ID, Problem: ForeignOperation})
		return
	}
	slot, ok := b.k.bindings.slot(desc)
	if !ok {
		b.k.recordBindErr(&BindError{Op: desc.ID, Service: b.svc.ID, Problem: NotInRegistry})
		return
	}
	// Extend the binding the slot holds and swap it in only if no other
	// Declare replaced it meanwhile; otherwise extend the newer one, so
	// concurrent Declares each keep their options, as Bind's
	// CompareAndSwap keeps concurrent binds safe.
	for {
		bnd := slot.Load()
		switch {
		case bnd == nil:
			b.k.recordBindErr(&BindError{Op: desc.ID, Service: b.svc.ID, Problem: DeclaredUnbound})
			return
		case bnd.extend == nil:
			b.k.recordBindErr(&BindError{Op: desc.ID, Service: b.svc.ID, Problem: DeclaredRaw})
			return
		}
		next := bnd.extend(opts)
		next.op, next.coverage, next.raw, next.passthrough = bnd.op, bnd.coverage, bnd.raw, bnd.passthrough
		if slot.CompareAndSwap(bnd, next) {
			return
		}
	}
}

// DeclaresActions reports whether op is bound with a decision on the
// secondary actions its calls use (WithActions or WithoutSecondaryActions).
// A service whose actions are reviewed tests that each of its operations
// whose generated list holds more than one of its actions has one.
func (b *Bindings) DeclaresActions(op *registry.Operation) bool {
	bnd, ok := b.lookup(op)
	return ok && bnd.actions != nil
}

// secondaries is what a call's binding says about the actions of its
// operation's own service, besides the primary, that the call used.
//
//sumtype:decl
type secondaries interface{ isSecondaries() }

// undeclared is a binding that declares no action: the call records every
// action of its operation's generated list, the over-grant of an operation
// nobody has reviewed rather than an under-grant.
type undeclared struct{}

// declared is the secondary actions a binding declared for the call's
// input: none for an invalid input, which reached no resolver.
type declared []registry.IAMAction

func (undeclared) isSecondaries() {}
func (declared) isSecondaries()   {}

// secondariesBefore is what bnd says before the call's input is valid.
func secondariesBefore(bnd *binding) secondaries {
	if bnd == nil || bnd.actions == nil {
		return undeclared{}
	}
	return declared(nil)
}

// secondaries is what bnd says for a call with the valid input in.
func (bnd *binding) secondaries(c *Call, in any) secondaries {
	if bnd.actions == nil {
		return undeclared{}
	}
	return declared(bnd.actions(c, in))
}

// recordedActions returns the actions the access record pairs with the
// call's touched resources (ARCHITECTURE.md 17, ADR 0004 amendment). For
// a binding that declared its actions: the primary action, the declared
// actions that are in the operation's generated list, and every action of
// the list under another service's prefix, such as iam:PassRole, which the
// recorder keeps only where the call touched a resource of that service.
// For one that declared none: every action of the list. An internal
// operation, which has no list, records only its declared actions.
func (k *Kernel) recordedActions(ctx context.Context, c *Call, used secondaries) []registry.IAMAction {
	op := c.op
	var out []registry.IAMAction
	switch used := used.(type) {
	case undeclared:
		out = op.IAM
	case declared:
		if op.Kind == registry.Internal {
			return compactActions(used)
		}
		if len(op.IAM) > 0 {
			out = append(out, op.IAM[0])
		}
		for _, a := range used {
			if !slices.Contains(op.IAM, a) {
				k.log.LogAttrs(ctx, slog.LevelWarn, "bug: a bind site declared an IAM action outside the operation's generated list; the action is not recorded",
					slog.String("op", c.label), slog.String("action", a.String()), slog.Any("generated", op.IAM))
				continue
			}
			out = append(out, a)
		}
		for _, a := range op.IAM {
			if a.Prefix != op.Service.IAMPrefix {
				out = append(out, a)
			}
		}
	}
	return compactActions(out)
}

// compactActions drops repeated actions, keeping the first of each.
func compactActions(as []registry.IAMAction) []registry.IAMAction {
	var out []registry.IAMAction
	for _, a := range as {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}
