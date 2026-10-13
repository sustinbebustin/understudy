package codegen

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// members builds every member with effective traits and the regex stage.
func (b *builder) members() error {
	for _, s := range b.ir.Shapes {
		if s.Unit || s.Type == smithy.TypeEnum || s.Type == smithy.TypeIntEnum {
			continue
		}
		src := b.clo.shapes[s.ID]
		goNames := map[string]bool{}
		for i, m := range src.Members {
			mem, err := b.member(s, src, m, i)
			if err != nil {
				return err
			}
			if goNames[mem.GoName] {
				return failf(StageIR, string(s.ID)+"$"+m.Name, "two members map onto the Go field %s", mem.GoName)
			}
			goNames[mem.GoName] = true
			s.Members = append(s.Members, mem)
		}
	}
	return nil
}

func (b *builder) member(owner *ShapeIR, src *Shape, m *Member, index int) (*MemberIR, error) {
	subject := string(src.ID) + "$" + m.Name
	tsrc := b.clo.shapes[m.Target]
	eff := maps.Clone(tsrc.Traits)
	maps.Copy(eff, m.Traits)
	mem := &MemberIR{
		Name: m.Name, GoName: exportName(m.Name), Index: index, Owner: owner, Target: b.shapes[m.Target], Bit: -1,
		Var:          "member" + owner.GoName + "_" + m.Name,
		Required:     eff.Has(traitRequired),
		XMLFlattened: m.Traits.Has(traitXMLFlattened),
		XMLAttribute: m.Traits.Has(traitXMLAttribute),
		Sensitive:    eff.Has(traitSensitive),
		AlwaysEmit:   m.Traits.Has(utAlwaysEmit),
		Empty:        b.emptyDef,
		rulesRaw:     m.Traits[utValidationError],
	}
	if raw, ok := eff[traitDefault]; ok && string(raw) != "null" {
		mem.Default = raw
	}
	if err := memberWireTraits(mem, m.Traits, eff); err != nil {
		return nil, failf(StageEffective, subject, "%w", err)
	}
	if err := b.constraints(mem, eff, tsrc, subject); err != nil {
		return nil, err
	}
	if raw, ok := m.Traits[utEmptyCollection]; ok {
		e, err := parseEmptyCollection(raw)
		if err != nil {
			return nil, failf(StageEffective, subject, "%w", err)
		}
		mem.Empty = e
	}
	if raw, ok := m.Traits[utQueryKeyAlias]; ok {
		a, err := parseQueryKeyAlias(raw)
		if err != nil {
			return nil, failf(StageEffective, subject, "%w", err)
		}
		if a.Key != queryKey(mem) {
			return nil, failf(StageEffective, subject, "%s key %q is not the member's awsQuery key %q; the alias names the key the member already has", utQueryKeyAlias, a.Key, queryKey(mem))
		}
		mem.QueryKeyAlias = &a
	}
	if tsrc.Traits.Has(traitStreaming) {
		if owner.Type != smithy.TypeStructure {
			return nil, failf(StageEffective, subject, "targets the @streaming %s; only a structure member may stream", m.Target)
		}
		mem.Streaming = true
	}
	return mem, nil
}

// queryKey is a member's awsQuery key segment: its xmlName, else its name
// (aws-query-protocol, "Query key resolution").
func queryKey(m *MemberIR) string {
	if m.XMLName != "" {
		return m.XMLName
	}
	return m.Name
}

// memberWireTraits reads the naming, namespace, timestamp and HTTP binding
// traits of a member: own traits for names and bindings, effective traits
// for the timestamp format.
func memberWireTraits(mem *MemberIR, own, eff Traits) error {
	for _, t := range []struct {
		id  string
		dst *string
	}{{traitJSONName, &mem.JSONName}, {traitXMLName, &mem.XMLName}} {
		if raw, ok := own[t.id]; ok {
			if err := decodeTrait(raw, t.dst); err != nil {
				return fmt.Errorf("%s: %w", t.id, err)
			}
		}
	}
	if raw, ok := own[traitXMLNamespace]; ok {
		ns, err := parseXMLNamespace(raw)
		if err != nil {
			return err
		}
		mem.XMLNamespace = &ns
	}
	if raw, ok := eff[traitTimestampFormat]; ok {
		f, err := parseTimestampFormat(raw)
		if err != nil {
			return err
		}
		mem.TimestampFormat = f
	}
	loc, err := httpLocation(own)
	if err != nil {
		return err
	}
	mem.HTTP = loc
	return nil
}

// httpLocation reads a member's HTTP binding trait; nil when it has none.
func httpLocation(t Traits) (smithy.Location, error) {
	switch {
	case t.Has(traitHTTPLabel):
		return smithy.HTTPLabel{}, nil
	case t.Has(traitHTTPQueryParams):
		return smithy.HTTPQueryParams{}, nil
	case t.Has(traitHTTPPayload):
		return smithy.HTTPPayload{}, nil
	case t.Has(traitHTTPResponse):
		return smithy.HTTPResponseCode{}, nil
	}
	for _, f := range []struct {
		id   string
		make func(string) smithy.Location
	}{
		{traitHTTPQuery, func(v string) smithy.Location { return smithy.HTTPQuery{Name: v} }},
		{traitHTTPHeader, func(v string) smithy.Location { return smithy.HTTPHeader{Name: v} }},
		{traitHTTPPrefix, func(v string) smithy.Location { return smithy.HTTPPrefixHeaders{Prefix: v} }},
	} {
		if raw, ok := t[f.id]; ok {
			var v string
			if err := decodeTrait(raw, &v); err != nil {
				return nil, fmt.Errorf("%s: %w", f.id, err)
			}
			return f.make(v), nil
		}
	}
	return nil, nil //nolint:nilnil // a member without a binding trait has no location, which is not an error
}

// constraints reads length, range and pattern from the effective traits and
// runs the regex stage.
func (b *builder) constraints(mem *MemberIR, eff Traits, target *Shape, subject string) error {
	for _, c := range []struct {
		id  string
		dst **Bounds
	}{{traitLength, &mem.Length}, {traitRange, &mem.Range}} {
		raw, ok := eff[c.id]
		if !ok {
			continue
		}
		var v struct {
			Min *json.Number `json:"min"`
			Max *json.Number `json:"max"`
		}
		if err := decodeTrait(raw, &v); err != nil {
			return failf(StageEffective, subject, "%s: %w", c.id, err)
		}
		bd := &Bounds{}
		if v.Min != nil {
			bd.Min = v.Min.String()
		}
		if v.Max != nil {
			bd.Max = v.Max.String()
		}
		*c.dst = bd
	}
	raw, ok := eff[traitPattern]
	if !ok {
		return nil
	}
	p, err := patternIR(raw, target)
	if err != nil {
		return failf(StageRegex, subject, "%w", err)
	}
	mem.Pattern = p
	return nil
}

// patternIR runs the regex stage on one pattern: the echo AWS prints, and
// either the RE2 translation or the target's hand validator.
func patternIR(raw json.RawMessage, target *Shape) (*PatternIR, error) {
	var src string
	if err := decodeTrait(raw, &src); err != nil {
		return nil, err
	}
	p := &PatternIR{Source: src, Echo: PatternEcho(src)}
	if raw, ok := target.Traits[utPatternEcho]; ok {
		echo, err := parseNonEmptyString(raw)
		if err != nil {
			return nil, err
		}
		p.Echo = echo
	}
	if raw, ok := target.Traits[utPatternValidator]; ok {
		pv, err := parsePatternValidator(raw)
		if err != nil {
			return nil, err
		}
		p.Validator = &pv
		return p, nil
	}
	re2, err := TranslatePattern(src)
	if err != nil {
		return nil, err
	}
	p.RE2 = re2
	return p, nil
}

// patterns names every distinct rendering an input member checks, in the
// order of its rendering key, so one variable serves every member that
// shares it.
func (b *builder) patterns() error {
	byKey := map[string][]*PatternIR{}
	for _, s := range b.ir.Shapes {
		if s.Role&RoleInput == 0 {
			continue
		}
		for _, m := range s.Members {
			if m.Pattern != nil {
				k := patternKey(m.Pattern)
				byKey[k] = append(byKey[k], m.Pattern)
			}
		}
	}
	for i, k := range slices.Sorted(maps.Keys(byKey)) {
		ps := byKey[k]
		v := &PatternVarIR{Name: fmt.Sprintf("pattern%d", i), RE2: ps[0].RE2, Validator: ps[0].Validator}
		for _, p := range ps {
			p.Var = v.Name
			v.Sources = append(v.Sources, p.Source)
		}
		slices.Sort(v.Sources)
		v.Sources = slices.Compact(v.Sources)
		b.ir.Patterns = append(b.ir.Patterns, v)
	}
	return nil
}

// patternKey identifies a pattern by its rendering: two model patterns that
// translate alike share one variable.
func patternKey(p *PatternIR) string {
	if p.Validator == nil {
		return "re2:" + p.RE2
	}
	return "validator:" + strings.Join(p.Validator.AllOf, "\x00") + "\x01" + strings.Join(p.Validator.NoneOf, "\x00")
}
