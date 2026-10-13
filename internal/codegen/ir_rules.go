package codegen

import (
	"encoding/json"
	"slices"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// rules resolves validationError rules against the error identities.
func (b *builder) rules(subject string, raw json.RawMessage) ([]RuleIR, error) {
	parsed, err := parseValidationRules(raw)
	if err != nil {
		return nil, failf(StageIR, subject, "%s: %w", utValidationError, err)
	}
	out := make([]RuleIR, 0, len(parsed))
	for _, r := range parsed {
		ir := RuleIR{Style: r.Style, Kinds: r.Kinds, Reason: r.Reason, EnumPolicy: r.EnumPolicy, PatternPolicy: r.PatternPolicy}
		if r.Code != "" {
			e, ok := b.errors[r.Code]
			if !ok {
				return nil, failf(StageIR, subject, "%s code %s is not an error structure in the model or an overlay", utValidationError, r.Code)
			}
			ir.Error = e
		}
		out = append(out, ir)
	}
	return out, nil
}

// policies resolves validation rules at every level, enum policies and the
// per-operation path tables, and adds validation identities to each
// operation's error list.
func (b *builder) policies() error {
	if err := b.serviceRules(); err != nil {
		return err
	}
	if err := b.memberRules(); err != nil {
		return err
	}
	for _, o := range b.ir.Operations {
		if !o.HasIO() {
			continue
		}
		if err := b.operationRules(o); err != nil {
			return err
		}
		o.Paths = inputPaths(o.Input)
		b.addValidationErrors(o)
	}
	return nil
}

// serviceRules reads the mandatory service-level validationError, which
// needs an unfiltered rule so every violation has a code.
func (b *builder) serviceRules() error {
	svc := b.clo.service
	raw, ok := svc.Traits[utValidationError]
	if !ok {
		return failf(StageIR, string(svc.ID), "has no %s; every service states its constraint-failure code (ValidationException, InvalidParameterValue, ...) on its service shape in its overlay", utValidationError)
	}
	rules, err := b.rules(string(svc.ID), raw)
	if err != nil {
		return err
	}
	b.ir.Validation = rules
	if !slices.ContainsFunc(rules, func(r RuleIR) bool { return r.Error != nil && r.Kinds == 0 }) {
		return failf(StageIR, string(svc.ID), "%s needs a rule without a constraints filter, so every violation has a code", utValidationError)
	}
	for _, r := range rules {
		if r.EnumPolicy != 0 {
			b.enumDef = r.EnumPolicy
		}
		if r.PatternPolicy != 0 {
			return failf(StageIR, string(svc.ID), "patternPolicy applies at member level, where the handler that checks the pattern is named")
		}
	}
	return nil
}

// memberRules resolves member-level rules, each member's enum policy, and
// drops the pattern a patternPolicy handler rule leaves to the handler.
func (b *builder) memberRules() error {
	for _, s := range b.ir.Shapes {
		for _, m := range s.Members {
			if m.rulesRaw != nil {
				var err error
				if m.Rules, err = b.rules(string(s.ID)+"$"+m.Name, m.rulesRaw); err != nil {
					return err
				}
			}
			b.applyPolicies(m)
		}
	}
	return nil
}

// applyPolicies sets m's enum policy, the service default unless a rule of
// m's sets one, and drops its pattern when a rule leaves it to the handler.
func (b *builder) applyPolicies(m *MemberIR) {
	m.EnumPolicy = b.enumDef
	for _, r := range m.Rules {
		if r.EnumPolicy != 0 {
			m.EnumPolicy = r.EnumPolicy
		}
		if r.PatternPolicy == PatternHandler {
			m.Pattern = nil
		}
	}
}

// operationRules resolves an operation's rules; enumPolicy is not one of
// them, as the enum check is generated per member.
func (b *builder) operationRules(o *OperationIR) error {
	raw, ok := b.model.Shapes[o.ID].Traits[utValidationError]
	if !ok {
		return nil
	}
	var err error
	if o.Validation, err = b.rules(string(o.ID), raw); err != nil {
		return err
	}
	for _, r := range o.Validation {
		if r.EnumPolicy != 0 {
			return failf(StageIR, string(o.ID), "enumPolicy applies at service or member level, where the validator that checks the enum is generated")
		}
		if r.PatternPolicy != 0 {
			return failf(StageIR, string(o.ID), "patternPolicy applies at member level, where the handler that checks the pattern is named")
		}
	}
	return nil
}

func (b *builder) addValidationErrors(o *OperationIR) {
	add := func(rules []RuleIR) {
		for _, r := range rules {
			if r.Error != nil && !slices.Contains(o.Errors, r.Error) {
				o.Errors = append(o.Errors, r.Error)
			}
		}
	}
	for _, p := range o.Paths {
		add(p.Rules)
	}
	add(o.Validation)
	add(b.ir.Validation)
}

// inputPaths enumerates the member paths of an input structure, list
// elements (prefix.*.member), map keys (prefix.*) and map values
// (prefix.*.member) included, so a rule on a collection member applies. A
// collection's elements, keys and values name the structure member that
// holds the collection as their parameter, as SQS's hand text does. A shape
// already on the current path stops the walk, so recursive shapes end.
func inputPaths(in *ShapeIR) []PathIR {
	var out []PathIR
	var walk func(s *ShapeIR, prefix []string, param string, onPath map[*ShapeIR]bool)
	walk = func(s *ShapeIR, prefix []string, param string, onPath map[*ShapeIR]bool) {
		if onPath[s] {
			return
		}
		onPath[s] = true
		defer delete(onPath, s)
		switch s.Type {
		case smithy.TypeStructure, smithy.TypeUnion:
			for _, m := range s.Members {
				p := append(slices.Clip(prefix), lowerCamel(m.Name))
				out = append(out, PathIR{Segments: p, Param: m.Name, Rules: m.Rules})
				walk(m.Target, p, m.Name, onPath)
			}
		case smithy.TypeList:
			elem := s.Members[0]
			p := append(slices.Clip(prefix), "*", "member")
			out = append(out, PathIR{Segments: p, Param: param, Rules: elem.Rules})
			walk(elem.Target, p, param, onPath)
		case smithy.TypeMap:
			key, val := s.Members[0], s.Members[1]
			out = append(out, PathIR{Segments: append(slices.Clip(prefix), "*"), Param: param, Rules: key.Rules})
			p := append(slices.Clip(prefix), "*", "member")
			out = append(out, PathIR{Segments: p, Param: param, Rules: val.Rules})
			walk(val.Target, p, param, onPath)
		case smithy.TypeString, smithy.TypeEnum, smithy.TypeIntEnum, smithy.TypeBlob, smithy.TypeBoolean,
			smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat,
			smithy.TypeDouble, smithy.TypeBigInteger, smithy.TypeBigDecimal, smithy.TypeTimestamp,
			smithy.TypeDocument, smithy.TypeUnit:
			// A simple value ends the path.
		}
	}
	walk(in, nil, "", map[*ShapeIR]bool{})
	return out
}
