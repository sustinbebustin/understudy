package awserr

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Violation is one failed constraint on one input member. Constraint is never
// nil: a nil one is a validator bug, and Coral renders it as understudy text
// rather than AWS text.
type Violation struct {
	Path       Path
	Value      Rendered
	Constraint Constraint
}

// Path locates a member: lowerCamel member names, list elements as
// "<n>.member" with 1-based n (targets.1.member.id) or rolled up at the list
// (Lambda), map entries by key, per the service's overlay style.
type Path []string

// String joins the segments with dots.
func (p Path) String() string { return strings.Join(p, ".") }

// Rendered is a member value as AWS echoes it in a validation message.
//
//sumtype:decl
type Rendered interface{ isRendered() }

// RenderedText is a value in Java toString form, quoted when rendered:
// 'abc', '11', '[a, b]', '{2=2.0}'.
type RenderedText struct{ Text string }

// RenderedNull is an absent value, rendered as null without quotes.
type RenderedNull struct{}

// RenderedOmitted is a value AWS never echoes, a blob or a member with the
// sensitive trait: "Value at 'path'".
type RenderedOmitted struct{}

func (RenderedText) isRendered()    {}
func (RenderedNull) isRendered()    {}
func (RenderedOmitted) isRendered() {}

// Constraint is the rule a value failed: a MemberConstraint on the value
// itself, or a Composite rolled up at a collection.
//
//sumtype:decl
type Constraint interface{ isConstraint() }

// MemberConstraint is a Constraint on one value: every variant except
// Composite, so a Composite never nests inside another.
//
//sumtype:decl
type MemberConstraint interface {
	Constraint
	isMemberConstraint()
}

// Required is a missing required member.
type Required struct{}

// LengthMin is a string, blob, list or map shorter than Min. String length
// counts Unicode scalar values.
type LengthMin struct{ Min int64 }

// LengthMax is a string, blob, list or map longer than Max.
type LengthMax struct{ Max int64 }

// RangeMin is a number below Min, the model's decimal literal.
type RangeMin struct{ Min string }

// RangeMax is a number above Max, the model's decimal literal.
type RangeMax struct{ Max string }

// Pattern is a string the model's regular expression rejects. Echo is the
// text AWS prints: the pattern minus one leading ^ and one trailing $,
// unless an overlay's patternEcho says otherwise.
type Pattern struct{ Echo string }

// Enum is a value outside the enum. Values are in the order AWS prints them.
type Enum struct{ Values []string }

// Encoding is a string that is not valid UTF-8 (jsonx keeps the bytes so a
// validator can classify them).
type Encoding struct{}

// Composite is the rolled-up form (error-matrix.md section 4, style B):
// Lambda reports a list element's or map entry's constraints once, at the
// collection's path with the whole collection as the value, listing every
// constraint of the element shape. Of renders in AWS's order whatever order
// it is given in: max, then min, then pattern or enum, then not-null.
type Composite struct {
	Scope CompositeScope
	Of    []MemberConstraint
}

// CompositeScope says which part of a collection a Composite constrains.
type CompositeScope uint8

// CompositeScope values.
const (
	ElementScope  CompositeScope = iota + 1 // list elements: "Member must satisfy constraint"
	MapKeysScope                            // map keys: "Map keys must satisfy constraint"
	MapValueScope                           // map values: "Map value must satisfy constraint"
)

func (Required) isConstraint()  {}
func (LengthMin) isConstraint() {}
func (LengthMax) isConstraint() {}
func (RangeMin) isConstraint()  {}
func (RangeMax) isConstraint()  {}
func (Pattern) isConstraint()   {}
func (Enum) isConstraint()      {}
func (Encoding) isConstraint()  {}
func (Composite) isConstraint() {}

func (Required) isMemberConstraint()  {}
func (LengthMin) isMemberConstraint() {}
func (LengthMax) isMemberConstraint() {}
func (RangeMin) isMemberConstraint()  {}
func (RangeMax) isMemberConstraint()  {}
func (Pattern) isMemberConstraint()   {}
func (Enum) isMemberConstraint()      {}
func (Encoding) isMemberConstraint()  {}

// Style is the grammar of a validation message.
type Style uint8

// Style values.
const (
	// Coral is the AWS grammar every recorded service uses:
	// "1 validation error detected: Value 'x' at 'path' failed to satisfy
	// constraint: Member must ...".
	Coral Style = iota + 1
	// SmithyFramework is smithy.framework#ValidationException with a
	// fieldList, used only by the synthetic protocol-test services.
	SmithyFramework
)

// Coral renders v as one entry of an AWS validation message
// (error-matrix.md section 4). A nil Value renders as RenderedNull.
func (v Violation) Coral() string {
	var value string
	switch r := v.Value.(type) {
	case RenderedText:
		value = "Value '" + r.Text + "' at"
	case RenderedNull, nil:
		value = "Value null at"
	case RenderedOmitted:
		value = "Value at"
	}
	return fmt.Sprintf("%s '%s' failed to satisfy constraint: %s", value, v.Path, coralConstraint(v.Constraint))
}

// CoralMessage renders violations as one AWS validation message: a count
// prefix, then the entries joined by "; ". An empty vs is a validator bug: a
// failed validation always records a violation, so the text says so in
// understudy's words rather than imitating AWS with a count of 0.
func CoralMessage(vs []Violation) string {
	if len(vs) == 0 {
		return "understudy validator bug: validation failed with no violations recorded"
	}
	entries := make([]string, len(vs))
	for i, v := range vs {
		entries[i] = v.Coral()
	}
	noun := "errors"
	if len(vs) == 1 {
		noun = "error"
	}
	return strconv.Itoa(len(vs)) + " validation " + noun + " detected: " + strings.Join(entries, "; ")
}

func coralConstraint(c Constraint) string {
	switch c := c.(type) {
	case Required:
		return "Member must not be null"
	case LengthMin:
		return "Member must have length greater than or equal to " + strconv.FormatInt(c.Min, 10)
	case LengthMax:
		return "Member must have length less than or equal to " + strconv.FormatInt(c.Max, 10)
	case RangeMin:
		return "Member must have value greater than or equal to " + c.Min
	case RangeMax:
		return "Member must have value less than or equal to " + c.Max
	case Pattern:
		return "Member must satisfy regular expression pattern: " + c.Echo
	case Enum:
		return "Member must satisfy enum value set: [" + strings.Join(c.Values, ", ") + "]"
	case Encoding:
		// No AWS recording shows this text; services with recorded behavior
		// (SQS InvalidMessageContents) answer from their handlers instead.
		return "Member must contain only valid UTF-8 characters"
	case Composite:
		return coralComposite(c)
	case nil:
	}
	// Only a nil Constraint gets here; gochecksumtype keeps the switch
	// exhaustive over the variants.
	return "understudy validator bug: no constraint recorded"
}

func coralComposite(c Composite) string {
	var subject string
	switch c.Scope {
	case ElementScope:
		subject = "Member"
	case MapKeysScope:
		subject = "Map keys"
	case MapValueScope:
		subject = "Map value"
	default:
		subject = "Member (understudy validator bug: unknown composite scope)"
	}
	inner := slices.Clone(c.Of)
	slices.SortStableFunc(inner, func(a, b MemberConstraint) int { return compositeRank(a) - compositeRank(b) })
	parts := make([]string, len(inner))
	for i, ic := range inner {
		parts[i] = coralConstraint(ic)
	}
	return subject + " must satisfy constraint: [" + strings.Join(parts, ", ") + "]"
}

// compositeRank orders a Composite's constraints as AWS prints them
// (error-matrix.md section 4): max, min, pattern or enum (or encoding), then
// not-null last. A nil entry sorts with not-null.
func compositeRank(c MemberConstraint) int {
	switch c.(type) {
	case LengthMax, RangeMax:
		return 0
	case LengthMin, RangeMin:
		return 1
	case Pattern, Enum, Encoding:
		return 2
	case Required, nil:
	}
	return 3
}
