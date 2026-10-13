package codegen

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// operations builds the operation list and names IO structures.
func (b *builder) operations() error {
	refs := map[smithy.ShapeID]int{}
	for _, s := range b.clo.shapes {
		for _, m := range s.Members {
			refs[m.Target]++
		}
	}
	for _, op := range b.clo.operations {
		refs[op.Input]++
		refs[op.Output]++
	}
	for _, op := range b.clo.operations {
		o := &OperationIR{
			Name: op.ID.Name(), ID: op.ID, Kind: registry.Modeled, SuccessCode: 200,
			Readonly: op.Traits.Has(traitReadonly), Idempotent: op.Traits.Has(traitIdempotent),
		}
		if op.Traits.Has(utInternalOperation) {
			o.Kind = registry.Internal
		}
		if err := b.operationTraits(o, op); err != nil {
			return err
		}
		var err error
		if o.Input, err = b.ioShape(o, op.Input, "Input", refs); err != nil {
			return err
		}
		if o.Output, err = b.ioShape(o, op.Output, "Output", refs); err != nil {
			return err
		}
		b.ir.Operations = append(b.ir.Operations, o)
	}
	for _, r := range b.cfg.RawOperations {
		id := smithy.ShapeID(b.cfg.Service.Namespace() + "#" + r.Name)
		if slices.ContainsFunc(b.ir.Operations, func(o *OperationIR) bool { return o.Name == r.Name }) {
			return failf(StageIR, "codegen/services/"+string(b.cfg.ID)+".json", "raw operation %s shares its name with an operation of the model", r.Name)
		}
		b.ir.Operations = append(b.ir.Operations, &OperationIR{Name: r.Name, ID: id, Kind: registry.Raw, SuccessCode: 200, IAM: r.IAM})
	}
	slices.SortFunc(b.ir.Operations, func(x, y *OperationIR) int { return cmp.Compare(x.Name, y.Name) })
	for i, o := range b.ir.Operations {
		o.Ordinal = i
	}
	return b.nameShapes()
}

// iamActions fills each modeled operation's IAM actions from the Service
// Reference table, primary first. Internal operations are never
// access-recorded and get none; raw ones state theirs in codegen/services.
func (b *builder) iamActions() error {
	config := "codegen/services/" + string(b.cfg.ID) + ".json"
	mapped := map[string]IAMPrimary{}
	for _, p := range b.cfg.IAMPrimary {
		if !slices.ContainsFunc(b.ir.Operations, func(o *OperationIR) bool { return o.Kind == registry.Modeled && o.Name == p.Operation }) {
			return failf(StageIR, config, "iamPrimary names %s, which is not a modeled operation of %s", p.Operation, b.cfg.Service)
		}
		mapped[p.Operation] = p
	}
	for _, o := range b.ir.Operations {
		if o.Kind != registry.Modeled {
			continue
		}
		row, ok := b.iam.entry(b.ir.IAMPrefix, o.Name)
		if !ok {
			return failf(StageIR, string(o.ID), "the Service Reference table has no %s entry for this operation; re-run go run ./internal/cmd/vendor -service-reference", b.ir.IAMPrefix)
		}
		ordered, err := primaryFirst(config, b.ir.IAMPrefix, o.Name, row, mapped)
		if err != nil {
			return failf(StageIR, string(o.ID), "%w", err)
		}
		o.IAM = ordered
	}
	return nil
}

func (b *builder) operationTraits(o *OperationIR, op *Shape) error {
	subject := string(op.ID)
	if raw, ok := op.Traits[traitHTTP]; ok {
		var v struct {
			Method string `json:"method"`
			URI    string `json:"uri"`
			Code   int    `json:"code"`
		}
		if err := decodeTrait(raw, &v); err != nil {
			return failf(StageIR, subject, "%s: %w", traitHTTP, err)
		}
		hb, err := registry.ParseHTTPBinding(v.Method, v.URI)
		if err != nil {
			return failf(StageIR, subject, "%s: %w", traitHTTP, err)
		}
		o.HTTP = &hb
		if v.Code != 0 {
			o.SuccessCode = v.Code
		}
	}
	if raw, ok := op.Traits[utSuccessCode]; ok {
		code, err := parseStatus(raw)
		if err != nil {
			return failf(StageIR, subject, "%w", err)
		}
		o.SuccessCode = code
	}
	if raw, ok := op.Traits[utMaxRequestBytes]; ok {
		n, err := parseMaxRequestBytes(raw)
		if err != nil {
			return failf(StageIR, subject, "%w", err)
		}
		o.MaxRequestBytes = n
	}
	if raw, ok := op.Traits[traitExamples]; ok {
		ex, err := parseExamples(raw)
		if err != nil {
			return failf(StageIR, subject, "%s: %w", traitExamples, err)
		}
		o.Examples = ex
	}
	return nil
}

// parseExamples keeps the examples whose input is meant to satisfy the
// constraints: one marked allowConstraintErrors shows a failure.
func parseExamples(raw json.RawMessage) ([]ExampleIR, error) {
	var v []struct {
		Title                 string          `json:"title"`
		Documentation         string          `json:"documentation"`
		Input                 json.RawMessage `json:"input"`
		Output                json.RawMessage `json:"output"`
		Error                 json.RawMessage `json:"error"`
		AllowConstraintErrors bool            `json:"allowConstraintErrors"`
	}
	if err := decodeTrait(raw, &v); err != nil {
		return nil, err
	}
	var out []ExampleIR
	for i, e := range v {
		if e.Title == "" {
			return nil, fmt.Errorf("example %d has no title", i)
		}
		if e.AllowConstraintErrors {
			continue
		}
		// An example without input calls the operation with none, as a
		// client sends {}.
		in := e.Input
		if in == nil {
			in = json.RawMessage("{}")
		}
		out = append(out, ExampleIR{Title: e.Title, Input: in})
	}
	return out, nil
}

// ioShape returns the IO structure of an operation. A structure only this
// operation uses this way is renamed <Op>Input or <Op>Output; a Unit gets a
// synthetic empty structure of that name.
func (b *builder) ioShape(o *OperationIR, id smithy.ShapeID, suffix string, refs map[smithy.ShapeID]int) (*ShapeIR, error) {
	name := o.Name + suffix
	top := RoleOutput
	if suffix == "Input" {
		top = RoleInput
	}
	if id == "smithy.api#Unit" {
		u := &ShapeIR{ID: id, Type: smithy.TypeUnit, GoName: name, SchemaVar: "schemaSmithyUnit", IO: o, Unit: true, Top: top}
		b.ir.Shapes = append(b.ir.Shapes, u)
		return u, nil
	}
	s, ok := b.shapes[id]
	if !ok || s.Type != smithy.TypeStructure {
		return nil, failf(StageIR, string(o.ID), "%s %s is not a structure", strings.ToLower(suffix), id)
	}
	if s.IO != nil {
		return nil, failf(StageIR, string(o.ID), "shares its %s structure %s with %s; give each operation its own", strings.ToLower(suffix), id, s.IO.Name)
	}
	if refs[id] == 1 {
		s.IO, s.GoName, s.Top = o, name, top
		return s, nil
	}
	if b.streams(id) {
		// A streaming structure used in more than one place (the corpus's
		// StreamingTraitsInputOutput is one operation's input and output)
		// gets a Go type per use, since a stream is a smithy.Body one way
		// and a smithy.BodySource the other.
		c := *s
		c.IO, c.GoName, c.Top = o, name, top
		b.ir.Shapes = append(b.ir.Shapes, &c)
		return &c, nil
	}
	s.Top |= top
	return s, nil
}

// streams reports whether structure id has a @streaming member.
func (b *builder) streams(id smithy.ShapeID) bool {
	return slices.ContainsFunc(b.clo.shapes[id].Members, func(m *Member) bool {
		t, ok := b.clo.shapes[m.Target]
		return ok && t.Traits.Has(traitStreaming)
	})
}

// nameShapes gives every shape without a Go name one, and schema variables.
func (b *builder) nameShapes() error {
	reserved := []string{"Service", "Binder", "NewBinder", "ThrottleError", "UnknownOperationError", "MalformedRequestError", "GenericErrors"}
	for _, r := range reserved {
		b.goNames[r] = "reserved"
	}
	// The service's own shapes are named first, so a shape another
	// namespace contributes (the generic vocabulary, the corpus's shared
	// shapes) yields its name: it takes a namespace prefix instead
	// (com.amazon.coral.validate#ValidationException beside CloudWatch's own
	// is CoralValidateValidationException).
	own := b.cfg.Service.Namespace()
	for _, pass := range []func(*ShapeIR) bool{
		func(s *ShapeIR) bool { return s.Unit || s.ID.Namespace() == own },
		func(s *ShapeIR) bool { return !s.Unit && s.ID.Namespace() != own },
	} {
		for _, s := range b.ir.Shapes {
			if !pass(s) {
				continue
			}
			if err := b.nameShape(s); err != nil {
				return err
			}
		}
	}
	return nil
}

// nameShape gives s its Go name and schema variable and claims the name.
func (b *builder) nameShape(s *ShapeIR) error {
	if s.Unit {
		return b.claim(s.GoName, s.ID)
	}
	if s.GoName == "" {
		s.GoName = exportName(s.ID.Name())
		if name, ok := b.clo.service.Rename[s.ID]; ok {
			s.GoName = exportName(name)
		}
		if s.ID.Namespace() == "smithy.api" {
			s.GoName = "Smithy" + s.GoName
		}
		prev, taken := b.goNames[s.GoName]
		switch {
		case taken && prev == "reserved":
			// A model shape named like a package-level function every
			// generated package declares (Logs has a Service enum).
			s.GoName += "Shape"
		case taken && prev != s.ID && s.ID.Namespace() != b.cfg.Service.Namespace():
			s.GoName = namespacePrefix(s.ID.Namespace()) + s.GoName
		}
	}
	s.SchemaVar = "schema" + s.GoName
	return b.claim(s.GoName, s.ID)
}

// claim reserves a Go name for one shape: its type or helper functions and
// its schema variable are named after it.
func (b *builder) claim(name string, id smithy.ShapeID) error {
	if prev, taken := b.goNames[name]; taken && prev != id {
		return failf(StageIR, string(id), "its Go name %s is already %s", name, prev)
	}
	b.goNames[name] = id
	return nil
}
