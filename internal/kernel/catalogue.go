package kernel

import (
	"slices"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Catalogue implements fault.Catalogue over the registry, so fault rules
// validate against the errors each operation really sends without the fault
// package importing a descriptor.
type Catalogue struct {
	reg       *registry.Registry
	throttles map[ref.ServiceID]awserr.Identity
}

// NewCatalogue returns the catalogue of reg. throttles holds each service's
// overlay throttleError (the generated ThrottleError of its api package),
// which the descriptors do not carry; a service missing from it throttles
// with the generic ThrottlingException.
func NewCatalogue(reg *registry.Registry, throttles map[ref.ServiceID]awserr.Identity) Catalogue {
	return Catalogue{reg: reg, throttles: throttles}
}

// Errors returns op's modeled and overlay errors plus the generic catalogue
// of every protocol family its service speaks, without repeats.
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
		for _, id := range awserr.Catalogue(f) {
			if !slices.ContainsFunc(out, func(have awserr.Identity) bool { return have.Shape == id.Shape }) {
				out = append(out, id)
			}
		}
	}
	return out, true
}

// Throttle returns svc's throttling identity, or the zero Identity for a
// service the registry does not hold.
func (c Catalogue) Throttle(svc ref.ServiceID) awserr.Identity {
	if _, ok := c.reg.Service(svc); !ok {
		var unknown awserr.Identity
		return unknown
	}
	if id, ok := c.throttles[svc]; ok {
		return id
	}
	return awserr.ThrottlingException()
}
