package kernel

import (
	"slices"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Catalogue implements fault.Catalogue over the registry, so fault rules
// validate against the errors each operation really sends without the fault
// package importing a descriptor.
type Catalogue struct {
	reg *registry.Registry
}

// NewCatalogue returns the catalogue of reg.
func NewCatalogue(reg *registry.Registry) Catalogue {
	return Catalogue{reg: reg}
}

// Errors returns op's modeled and overlay errors plus the generic
// vocabulary of every protocol family its service speaks, without repeats.
// The vocabulary is the descriptor's GenericErrors, which carries the
// service overlay's corrections (DynamoDB's AccessDeniedException is 400,
// iam-enforcement-surfaces.md 3.2), else awserr.Catalogue.
func (c Catalogue) Errors(op ref.OperationID) ([]awserr.Identity, bool) {
	d, ok := c.reg.Operation(op)
	if !ok {
		return nil, false
	}
	out := slices.Clone(d.Errors)
	var families []awserr.Family
	for _, p := range d.Service.Protocols {
		if f, ok := awserr.FamilyOf(p); ok && !slices.Contains(families, f) {
			families = append(families, f)
		}
	}
	for _, f := range families {
		generic := awserr.Catalogue(f)
		if d.Service.GenericErrors != nil {
			generic = d.Service.GenericErrors(f)
		}
		for _, id := range generic {
			if !slices.ContainsFunc(out, func(have awserr.Identity) bool { return have.Shape == id.Shape }) {
				out = append(out, id)
			}
		}
	}
	return out, true
}

// Throttle returns svc's throttling identity: the descriptor's
// ThrottleError, the generic ThrottlingException for a descriptor that
// carries none, or the zero Identity for a service the registry does not
// hold.
func (c Catalogue) Throttle(svc ref.ServiceID) awserr.Identity {
	d, ok := c.reg.Service(svc)
	if !ok {
		var unknown awserr.Identity
		return unknown
	}
	if d.ThrottleError.Shape != "" {
		return d.ThrottleError
	}
	return awserr.ThrottlingException()
}

// NewMembers returns a new, empty value of error shape for op's service,
// from the descriptor's generated NewErrorMembers, or false for an unknown
// operation or a service without one.
func (c Catalogue) NewMembers(op ref.OperationID, shape smithy.ShapeID) (awserr.DecodableMembers, bool) {
	d, ok := c.reg.Operation(op)
	if !ok || d.Service.NewErrorMembers == nil {
		return nil, false
	}
	return d.Service.NewErrorMembers(shape)
}
