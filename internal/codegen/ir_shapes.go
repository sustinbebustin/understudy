package codegen

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// dataShapes creates a ShapeIR per data shape with its shape-level traits.
func (b *builder) dataShapes() error {
	for _, id := range slices.Sorted(maps.Keys(b.clo.shapes)) {
		s := b.clo.shapes[id]
		ir := &ShapeIR{
			ID: id, Type: s.Type, Sparse: s.Traits.Has(traitSparse), Sensitive: s.Traits.Has(traitSensitive),
			Streaming: s.Traits.Has(traitStreaming),
		}
		if ir.Streaming && s.Type != smithy.TypeBlob && s.Type != smithy.TypeUnion {
			return failf(StageIR, string(id), "@streaming applies to a blob or an event stream union, not a %s", s.Type)
		}
		if err := b.shapeTraits(ir, s); err != nil {
			return err
		}
		b.shapes[id] = ir
		b.ir.Shapes = append(b.ir.Shapes, ir)
	}
	return nil
}

func (b *builder) shapeTraits(ir *ShapeIR, s *Shape) error {
	subject := string(s.ID)
	if raw, ok := s.Traits[traitTimestampFormat]; ok {
		f, err := parseTimestampFormat(raw)
		if err != nil {
			return failf(StageIR, subject, "%w", err)
		}
		ir.TimestampFormat = f
	}
	if raw, ok := s.Traits[traitMediaType]; ok {
		if err := decodeTrait(raw, &ir.MediaType); err != nil {
			return failf(StageIR, subject, "%s: %w", traitMediaType, err)
		}
	}
	if raw, ok := s.Traits[traitXMLName]; ok {
		if err := decodeTrait(raw, &ir.XMLName); err != nil {
			return failf(StageIR, subject, "%s: %w", traitXMLName, err)
		}
	}
	if raw, ok := s.Traits[traitXMLNamespace]; ok {
		ns, err := parseXMLNamespace(raw)
		if err != nil {
			return failf(StageIR, subject, "%s: %w", traitXMLNamespace, err)
		}
		ir.XMLNamespace = &ns
	}
	if s.Type == smithy.TypeEnum || s.Type == smithy.TypeIntEnum {
		return b.enumValues(ir, s)
	}
	if raw, ok := s.Traits[traitEnum]; ok && s.Type == smithy.TypeString {
		return legacyEnum(ir, raw)
	}
	return nil
}

// legacyEnum reads the Smithy 1.0 enum trait on a string shape, which
// Smithy 2.0 defines as an enum shape (the corpus's EnumTraitString): the
// shape becomes an enum with the trait's values, named by their name or,
// without one, their value.
func legacyEnum(ir *ShapeIR, raw json.RawMessage) error {
	var defs []struct {
		Value string   `json:"value"`
		Name  string   `json:"name"`
		Tags  []string `json:"tags"`
		Doc   string   `json:"documentation"`
	}
	if err := decodeTrait(raw, &defs); err != nil {
		return failf(StageIR, string(ir.ID), "%s: %w", traitEnum, err)
	}
	ir.Type = smithy.TypeEnum
	seen := map[string]bool{}
	for _, d := range defs {
		name := enumConstName(cmp.Or(d.Name, d.Value))
		if seen[name] {
			return failf(StageIR, string(ir.ID), "two %s values map onto the Go constant suffix %s", traitEnum, name)
		}
		seen[name] = true
		ir.Enum = append(ir.Enum, EnumValueIR{Name: name, Value: d.Value})
	}
	return nil
}

func parseTimestampFormat(raw json.RawMessage) (smithy.TimestampFormat, error) {
	var v string
	if err := decodeTrait(raw, &v); err != nil {
		return 0, err
	}
	switch v {
	case "date-time":
		return smithy.DateTime, nil
	case "http-date":
		return smithy.HTTPDate, nil
	case "epoch-seconds":
		return smithy.EpochSeconds, nil
	default:
		return 0, fmt.Errorf("%s %q is not date-time, http-date or epoch-seconds", traitTimestampFormat, v)
	}
}

func (b *builder) enumValues(ir *ShapeIR, s *Shape) error {
	seen := map[string]bool{}
	for _, m := range s.Members {
		v := EnumValueIR{Name: enumConstName(m.Name)}
		raw, ok := m.Traits[traitEnumValue]
		switch {
		case s.Type == smithy.TypeEnum && ok:
			if err := decodeTrait(raw, &v.Value); err != nil {
				return failf(StageIR, string(s.ID)+"$"+m.Name, "%s: %w", traitEnumValue, err)
			}
		case s.Type == smithy.TypeEnum:
			v.Value = m.Name
		case ok:
			if err := decodeTrait(raw, &v.Number); err != nil {
				return failf(StageIR, string(s.ID)+"$"+m.Name, "%s: %w", traitEnumValue, err)
			}
		default:
			return failf(StageIR, string(s.ID)+"$"+m.Name, "an intEnum member needs %s", traitEnumValue)
		}
		if seen[v.Name] {
			return failf(StageIR, string(s.ID)+"$"+m.Name, "two members map onto the Go constant suffix %s", v.Name)
		}
		seen[v.Name] = true
		ir.Enum = append(ir.Enum, v)
	}
	return nil
}

// roles marks shapes by reachability from inputs and from outputs and errors,
// and decides value or pointer fields and presence tracking.
func (b *builder) roles() error {
	for _, o := range b.ir.Operations {
		if o.HasIO() {
			markRole(o.Input, RoleInput)
			markRole(o.Output, RoleOutput)
		}
	}
	for _, s := range b.ir.Shapes {
		if !s.Unit && b.clo.shapes[s.ID].Traits.Has(traitError) {
			markRole(s, RoleOutput)
		}
	}
	for _, s := range b.ir.Shapes {
		if err := s.fieldKinds(); err != nil {
			return err
		}
	}
	return nil
}

func markRole(s *ShapeIR, r Role) {
	if s.Role&r != 0 {
		return
	}
	s.Role |= r
	for _, m := range s.Members {
		// A stream's bytes reach the handler unread: its target needs no
		// serde of its own.
		if !m.Streaming {
			markRole(m.Target, r)
		}
	}
}

// fieldKinds decides, per member, a plain value or a pointer, and gives each
// required member of an input structure a presence bit. A required member
// with a default always has a value, so it needs none.
func (s *ShapeIR) fieldKinds() error {
	for _, m := range s.Members {
		if m.AlwaysEmit && s.Role&RoleOutput == 0 {
			return failf(StageRoles, string(s.ID)+"$"+m.Name, "%s belongs on output members", utAlwaysEmit)
		}
		if m.Streaming && s.Role == RoleInput|RoleOutput {
			return failf(StageRoles, string(s.ID)+"$"+m.Name, "streams in a structure used as both input and output; a stream is a smithy.Body on input and a smithy.BodySource on output, so give each direction its own structure")
		}
		m.Value = valueField(m)
		if s.Type != smithy.TypeStructure || !m.Required || m.Default != nil || s.Role&RoleInput == 0 {
			continue
		}
		if s.Tracked == 64 {
			return failf(StageRoles, string(s.ID), "has more than 64 required members; presence tracking holds 64")
		}
		m.Bit = s.Tracked
		s.Tracked++
	}
	return nil
}

// valueField reports whether a member is a plain Go value: required,
// defaulted or alwaysEmit scalars and structures. Collections, unions,
// documents, blobs, big integers and streams use nil for absent instead.
func valueField(m *MemberIR) bool {
	switch {
	case m.Streaming:
		return false
	case m.Owner.Type != smithy.TypeStructure:
		return true
	case nilAbsent(m.Target.Type):
		return false
	case m.Target.Type == smithy.TypeStructure:
		return m.Required
	default:
		return m.Required || m.Default != nil || m.AlwaysEmit
	}
}
