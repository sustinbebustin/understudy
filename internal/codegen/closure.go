package codegen

import (
	"cmp"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// closure is everything one service reaches: its operations (through
// resources too, Lambda's 72 resource-bound operations) and every shape
// their inputs, outputs and errors reach, plus the error shapes the overlay
// traits name.
type closure struct {
	service    *Shape
	operations []*Shape // sorted by name; overlay internal operations included
	shapes     map[smithy.ShapeID]*Shape
}

// closure walks the service. It fails on a reference to a shape the model
// does not have, naming the referrer.
func (m *Model) closure(serviceID smithy.ShapeID) (*closure, error) {
	svc, ok := m.Shapes[serviceID]
	if !ok || svc.Kind != KindService {
		return nil, failf(StageClosure, string(serviceID), "is not a service shape in the model")
	}
	c := &closure{service: svc, shapes: map[smithy.ShapeID]*Shape{}}
	ops := map[smithy.ShapeID]*Shape{}
	if err := m.walkContainer(svc, ops, map[smithy.ShapeID]bool{}); err != nil {
		return nil, err
	}
	for _, id := range slices.Sorted(maps.Keys(m.Shapes)) {
		s := m.Shapes[id]
		if s.Kind == KindOperation && s.Traits.Has(utInternalOperation) && id.Namespace() == serviceID.Namespace() {
			ops[id] = s
		}
	}
	for _, id := range slices.Sorted(maps.Keys(ops)) {
		op := ops[id]
		c.operations = append(c.operations, op)
		refs := slices.Concat([]smithy.ShapeID{op.Input, op.Output}, op.Errors, svc.Errors)
		for _, r := range refs {
			if err := m.walkData(string(id), r, c.shapes); err != nil {
				return nil, err
			}
		}
	}
	slices.SortFunc(c.operations, func(a, b *Shape) int { return cmp.Compare(a.ID.Name(), b.ID.Name()) })
	for i := 1; i < len(c.operations); i++ {
		if c.operations[i].ID.Name() == c.operations[i-1].ID.Name() {
			return nil, failf(StageClosure, string(c.operations[i].ID), "shares its name with %s", c.operations[i-1].ID)
		}
	}
	extra, err := m.referencedErrors(c)
	if err != nil {
		return nil, err
	}
	for _, r := range extra {
		if err := m.walkData(r.from, r.id, c.shapes); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// walkContainer collects the operations of a service or resource.
func (m *Model) walkContainer(s *Shape, ops map[smithy.ShapeID]*Shape, seen map[smithy.ShapeID]bool) error {
	if seen[s.ID] {
		return nil
	}
	seen[s.ID] = true
	for _, id := range slices.Concat(s.Operations, s.Lifecycle, s.CollectionOperations) {
		op, ok := m.Shapes[id]
		if !ok || op.Kind != KindOperation {
			return failf(StageClosure, string(s.ID), "names operation %s, which is not an operation shape in the model", id)
		}
		ops[id] = op
	}
	for _, id := range s.Resources {
		r, ok := m.Shapes[id]
		if !ok || r.Kind != KindResource {
			return failf(StageClosure, string(s.ID), "names resource %s, which is not a resource shape in the model", id)
		}
		if err := m.walkContainer(r, ops, seen); err != nil {
			return err
		}
	}
	return nil
}

// walkData adds id and every shape it reaches through members.
func (m *Model) walkData(from string, id smithy.ShapeID, out map[smithy.ShapeID]*Shape) error {
	if _, done := out[id]; done {
		return nil
	}
	s, ok := m.Shapes[id]
	if !ok {
		return failf(StageClosure, from, "targets %s, which is not in the model or its overlays", id)
	}
	if s.Kind != KindData {
		return failf(StageClosure, from, "targets %s, which is not a data shape", id)
	}
	out[id] = s
	for _, mem := range s.Members {
		if s.Type == smithy.TypeEnum || s.Type == smithy.TypeIntEnum {
			continue
		}
		if err := m.walkData(string(id)+"$"+mem.Name, mem.Target, out); err != nil {
			return err
		}
	}
	return nil
}

type errorRef struct {
	from string
	id   smithy.ShapeID
}

// referencedErrors lists the error shapes the service's overlay traits name:
// throttle, malformed-request and unknown-operation identities and every
// validationError code in the closure.
func (m *Model) referencedErrors(c *closure) ([]errorRef, error) {
	out, err := serviceErrorRefs(c.service)
	if err != nil {
		return nil, err
	}
	type traitSite struct {
		from   string
		traits Traits
	}
	sites := []traitSite{{string(c.service.ID), c.service.Traits}}
	for _, op := range c.operations {
		sites = append(sites, traitSite{string(op.ID), op.Traits})
	}
	for _, id := range slices.Sorted(maps.Keys(c.shapes)) {
		for _, mem := range c.shapes[id].Members {
			sites = append(sites, traitSite{string(id) + "$" + mem.Name, mem.Traits})
		}
	}
	for _, s := range sites {
		refs, err := validationErrorRefs(s.from, s.traits)
		if err != nil {
			return nil, err
		}
		out = append(out, refs...)
	}
	return out, nil
}

// serviceErrorRefs lists the identities the service-level traits name.
func serviceErrorRefs(svc *Shape) ([]errorRef, error) {
	var out []errorRef
	for _, t := range []string{utThrottleError, utMalformedRequest} {
		if raw, ok := svc.Traits[t]; ok {
			id, err := parseShapeRef(raw)
			if err != nil {
				return nil, failf(StageClosure, string(svc.ID), "%s: %w", t, err)
			}
			out = append(out, errorRef{from: string(svc.ID) + " " + t, id: id})
		}
	}
	raw, ok := svc.Traits[utUnknownOperation]
	if !ok {
		return out, nil
	}
	u, err := parseUnknownOperation(raw)
	if err != nil {
		return nil, failf(StageClosure, string(svc.ID), "%s: %w", utUnknownOperation, err)
	}
	for _, id := range []smithy.ShapeID{u.JSON, u.Query} {
		if id != "" {
			out = append(out, errorRef{from: string(svc.ID) + " " + utUnknownOperation, id: id})
		}
	}
	return out, nil
}

// validationErrorRefs lists the codes of a validationError at one site.
func validationErrorRefs(from string, traits Traits) ([]errorRef, error) {
	raw, ok := traits[utValidationError]
	if !ok {
		return nil, nil
	}
	rules, err := parseValidationRules(raw)
	if err != nil {
		return nil, failf(StageClosure, from, "%s: %w", utValidationError, err)
	}
	var out []errorRef
	for _, r := range rules {
		if r.Code != "" {
			out = append(out, errorRef{from: from + " " + utValidationError, id: r.Code})
		}
	}
	return out, nil
}
