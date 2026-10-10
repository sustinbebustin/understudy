package validationpolicy

import "github.com/sustinbebustin/understudy/internal/awserr"

// constraintKind is a set of the constraint kinds a rule covers.
type constraintKind uint8

// constraintKind values, one per generated constraint.
const (
	kindRequired constraintKind = 1 << iota
	kindLength
	kindRange
	kindPattern
	kindEnum
)

// validationStyle is the message grammar of a rule.
type validationStyle uint8

// validationStyle values.
const (
	// styleCoral is AWS's validation grammar (awserr.CoralMessage) over every
	// violation that maps to the same code.
	styleCoral validationStyle = iota + 1
	// styleParameter is SQS's hand text for the first violation.
	styleParameter
	// styleMissingParameter is SQS's hand text for a missing member.
	styleMissingParameter
	// styleSNSParameter is SNS's hand text for the first violation.
	styleSNSParameter
)

// validationRule is one dev.understudy#validationError rule.
type validationRule struct {
	kinds  constraintKind // the kinds the rule covers; 0 covers every kind
	id     func() awserr.Identity
	style  validationStyle
	reason string
}

// validationPath is one member path of an operation input; "*" stands for a
// list index or map key.
type validationPath struct {
	segments []string
	param    string // the member's wire name, for hand-text messages
	rules    []validationRule
}

// validationPolicy is the code selection of one operation.
type validationPolicy struct {
	paths     []validationPath
	operation []validationRule
	service   []validationRule
}

// render returns the error AWS sends for vs, or nil when vs is empty.
func (p *validationPolicy) render(vs []awserr.Violation) *awserr.Error {
	if len(vs) == 0 {
		return nil
	}
	rules := make([]validationRule, len(vs))
	params := make([]string, len(vs))
	for i, v := range vs {
		rules[i], params[i] = p.resolve(v)
	}
	first := rules[0]
	id := first.id()
	var msg string
	switch first.style {
	case styleParameter:
		msg = "Value " + renderedText(vs[0].Value) + " for parameter " + params[0] + " is invalid. Reason: " + first.reason
	case styleMissingParameter:
		msg = "The request must contain the parameter " + params[0] + "."
	case styleSNSParameter:
		msg = "Invalid parameter: " + params[0] + " Reason: " + first.reason
	case styleCoral:
		msg = coralUnder(id, vs, rules)
	default:
		// The generator emits only the styles above; an unknown one renders
		// as AWS's common grammar rather than an empty message.
		msg = coralUnder(id, vs, rules)
	}
	return &awserr.Error{ID: id, Message: msg, Origin: awserr.Validation{Violations: vs}}
}

// coralUnder renders, as one Coral message, every violation whose rule is
// Coral-styled and names the code id.
func coralUnder(id awserr.Identity, vs []awserr.Violation, rules []validationRule) string {
	var same []awserr.Violation
	for i, v := range vs {
		if rules[i].style == styleCoral && rules[i].id().Shape == id.Shape {
			same = append(same, v)
		}
	}
	return awserr.CoralMessage(same)
}

// resolve returns the rule that answers v and the parameter name a hand
// text prints: a rule of the first path matching v that covers v's kind,
// else the operation's, else the service's.
func (p *validationPolicy) resolve(v awserr.Violation) (rule validationRule, param string) {
	kind := constraintKindOf(v.Constraint)
	if len(v.Path) > 0 {
		param = v.Path[len(v.Path)-1]
	}
	for _, path := range p.paths {
		if !pathMatches(path.segments, v.Path) {
			continue
		}
		param = path.param
		if r, ok := pickRule(path.rules, kind); ok {
			return r, param
		}
		break
	}
	if r, ok := pickRule(p.operation, kind); ok {
		return r, param
	}
	r, _ := pickRule(p.service, kind)
	return r, param
}

// pickRule returns the first rule with a code that covers kind; a kind of 0
// (a constraint no filter names) matches only unfiltered rules.
func pickRule(rules []validationRule, kind constraintKind) (validationRule, bool) {
	for _, r := range rules {
		if r.id != nil && (r.kinds == 0 || r.kinds&kind != 0) {
			return r, true
		}
	}
	return validationRule{}, false
}

func pathMatches(template []string, path awserr.Path) bool {
	if len(template) != len(path) {
		return false
	}
	for i, s := range template {
		if s != "*" && s != path[i] {
			return false
		}
	}
	return true
}

// constraintKindOf is the kind a filter names for c, or 0 for a constraint
// no filter names (Encoding, Composite).
func constraintKindOf(c awserr.Constraint) constraintKind {
	switch c.(type) {
	case awserr.Required:
		return kindRequired
	case awserr.LengthMin, awserr.LengthMax:
		return kindLength
	case awserr.RangeMin, awserr.RangeMax:
		return kindRange
	case awserr.Pattern:
		return kindPattern
	case awserr.Enum:
		return kindEnum
	case awserr.Encoding, awserr.Composite, nil:
	}
	return 0
}

// renderedText is a value as SQS's and SNS's hand text print it: unquoted,
// empty for an omitted value, null for an absent one.
func renderedText(r awserr.Rendered) string {
	switch r := r.(type) {
	case awserr.RenderedText:
		return r.Text
	case awserr.RenderedOmitted:
		return ""
	case awserr.RenderedNull, nil:
	}
	return "null"
}
