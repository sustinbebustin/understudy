package asl

import (
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/jsonpath"
)

const (
	errAll = "States.ALL"
	// maxDelaySeconds is one past the largest MaxDelaySeconds
	// (stepfunctions-asl.md 3.7: "less than 31622401").
	maxDelaySeconds = 31_622_400
)

// indexed is the location of element i of the array at: AWS writes the
// index in brackets, /States/P/Branches[0] (ASL-01, recorded 2026-11-03).
func indexed(at string, i int) string {
	return at + "[" + strconv.Itoa(i) + "]"
}

// objects reads an array field of objects and their locations; empty says
// whether the array may be empty.
func (p *parser) objects(v document.Value, field, at string, empty bool) (objs []document.Map, locs []string) {
	list, ok := v.(document.List)
	if !ok {
		p.schema("The field '"+field+"' must be an array", at)
		return nil, nil
	}
	if len(list) == 0 && !empty {
		p.schema("Value cannot be empty", at)
		return nil, nil
	}
	for i, x := range list {
		loc := indexed(at, i)
		obj, ok := x.(document.Map)
		if !ok {
			p.schema("Each element of '"+field+"' must be an object", loc)
			continue
		}
		objs = append(objs, obj)
		locs = append(locs, loc)
	}
	return objs, locs
}

// errorEquals reads a retrier's or catcher's ErrorEquals: a non-empty
// array of error names.
func (p *parser) errorEquals(v document.Value, at string) []string {
	list, ok := v.(document.List)
	if !ok || len(list) == 0 {
		p.schema("The field 'ErrorEquals' must be a non-empty array of error names", at)
		return nil
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		s, ok := x.(document.String)
		if !ok {
			p.schema("The field 'ErrorEquals' must be a non-empty array of error names", at)
			return nil
		}
		out = append(out, string(s))
	}
	return out
}

// checkAll enforces that States.ALL appears alone in its ErrorEquals and
// only in the last retrier or catcher (stepfunctions-asl.md 8.1), with
// AWS's message at the States.ALL entry (ASL-01, recorded 2026-11-03).
func (p *parser) checkAll(names []string, last bool, at string) {
	if i := slices.Index(names, errAll); i >= 0 && (len(names) > 1 || !last) {
		p.schema("States.ALL must appear alone and at end of list", indexed(at, i))
	}
}

// retriers reads Retry (stepfunctions-asl.md 3.7, 8.2-8.3).
func (p *parser) retriers(v document.Value, at string) []Retrier {
	objs, locs := p.objects(v, "Retry", at, true)
	out := make([]Retrier, 0, len(objs))
	for i, obj := range objs {
		loc := locs[i]
		r := Retrier{IntervalSeconds: 1, MaxAttempts: 3, BackoffRate: 2, Jitter: JitterNone}
		for _, e := range obj {
			p.retrierField(&r, e, loc)
		}
		if _, ok := member(obj, "ErrorEquals"); !ok {
			p.schema("These fields are required: [ErrorEquals]", loc)
		}
		p.checkAll(r.ErrorEquals, i == len(objs)-1, loc+"/ErrorEquals")
		out = append(out, r)
	}
	return out
}

// retrierField reads one field of the retrier at loc.
func (p *parser) retrierField(r *Retrier, e document.Entry, loc string) {
	at := loc + "/" + e.Key
	switch e.Key {
	case "ErrorEquals":
		r.ErrorEquals = p.errorEquals(e.Value, at)
	case "IntervalSeconds":
		// 0 is accepted (ASL-01, recorded 2026-11-03).
		n, _ := p.integer(e.Value, e.Key, at, 0, maxSeconds)
		r.IntervalSeconds = int(n)
	case "MaxAttempts":
		n, _ := p.integer(e.Value, e.Key, at, 0, maxSeconds)
		r.MaxAttempts = int(n)
	case "MaxDelaySeconds":
		n, _ := p.integer(e.Value, e.Key, at, 1, maxDelaySeconds)
		r.MaxDelaySeconds = int(n)
	case "BackoffRate":
		n, isNumber := e.Value.(document.Number)
		f, err := n.Float64()
		switch {
		case !isNumber || err != nil:
			p.schema("The field 'BackoffRate' must be a number of at least 1.0", at)
			return
		case f < 1:
			// Recorded (ASL-01, 2026-11-03).
			p.schema("Minimum value is 1.0", at)
			return
		}
		r.BackoffRate = f
	case "JitterStrategy":
		s, isString := e.Value.(document.String)
		switch {
		case isString && s == "FULL":
			r.Jitter = JitterFull
		case isString && s == "NONE":
			r.Jitter = JitterNone
		default:
			// Recorded (ASL-01, 2026-11-03).
			p.schema("Value should be one of the following: [NONE, FULL]", at)
		}
	case "Comment":
		p.str(e.Value, e.Key, at)
	default:
		p.unknownField(e.Key, loc)
	}
}

// catchers reads Catch (stepfunctions-asl.md 8.4).
func (p *parser) catchers(v document.Value, at string, names map[string]bool) []catcher {
	objs, locs := p.objects(v, "Catch", at, true)
	out := make([]catcher, 0, len(objs))
	for i, obj := range objs {
		loc := locs[i]
		c := catcher{resultPath: replaceInput{}}
		for _, e := range obj {
			fat := loc + "/" + e.Key
			switch e.Key {
			case "ErrorEquals":
				c.errorEquals = p.errorEquals(e.Value, fat)
			case "Next":
				c.next = p.transition(e.Value, "Next", fat, names)
			case "ResultPath":
				if t := p.target(e.Value, fat); t != nil {
					c.resultPath = t
				}
			case "Comment":
				p.str(e.Value, e.Key, fat)
			case "Assign", "Output":
				p.unsupported(refusedConstruct(e.Key), fat)
			default:
				p.unknownField(e.Key, loc)
			}
		}
		var missing []string
		for _, f := range []string{"ErrorEquals", "Next"} {
			if _, ok := member(obj, f); !ok {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			p.schema("These fields are required: ["+strings.Join(missing, ", ")+"]", loc)
		}
		p.checkAll(c.errorEquals, i == len(objs)-1, loc+"/ErrorEquals")
		out = append(out, c)
	}
	return out
}

// operand is the type of value a Choice operator compares with
// (stepfunctions-asl.md 6.2).
type operand int

const (
	operandString operand = iota + 1
	operandNumber
	operandBool
	operandTimestamp
	// operandTest is an Is* operator, whose value is a boolean.
	operandTest
)

// operators are the JSONPath Choice operators of stepfunctions-asl.md 6.2,
// without their Path forms.
func operators() map[string]operand {
	return map[string]operand{
		"StringEquals": operandString, "StringLessThan": operandString, "StringGreaterThan": operandString,
		"StringLessThanEquals": operandString, "StringGreaterThanEquals": operandString, "StringMatches": operandString,
		"NumericEquals": operandNumber, "NumericLessThan": operandNumber, "NumericGreaterThan": operandNumber,
		"NumericLessThanEquals": operandNumber, "NumericGreaterThanEquals": operandNumber,
		"BooleanEquals":   operandBool,
		"TimestampEquals": operandTimestamp, "TimestampLessThan": operandTimestamp, "TimestampGreaterThan": operandTimestamp,
		"TimestampLessThanEquals": operandTimestamp, "TimestampGreaterThanEquals": operandTimestamp,
		"IsNull": operandTest, "IsPresent": operandTest, "IsNumeric": operandTest, "IsString": operandTest,
		"IsBoolean": operandTest, "IsTimestamp": operandTest,
	}
}

// operator resolves a rule field to an operator: its name without a Path
// suffix, whether it is the Path form, and its operand type (0 for a field
// that is no operator).
func operator(field string) (base string, isPath bool, kind operand) {
	ops := operators()
	if t, ok := ops[field]; ok {
		return field, false, t
	}
	base = strings.TrimSuffix(field, "Path")
	if t, ok := ops[base]; ok && base != field && base != "StringMatches" && t != operandTest {
		return base, true, t
	}
	return "", false, 0
}

// choices reads a Choice state's Choices.
func (p *parser) choices(v document.Value, at string, names map[string]bool) []choiceRule {
	objs, locs := p.objects(v, "Choices", at, false)
	out := make([]choiceRule, 0, len(objs))
	for i, obj := range objs {
		r := ruleDraft{}
		p.rule(obj, locs[i], names, true, &r)
		out = append(out, choiceRule{when: r.condition, next: r.next})
	}
	return out
}

// ruleDraft is one Choice rule as it is read.
type ruleDraft struct {
	condition condition
	next      string
}

// rule reads a Choice rule: a data test (Variable and one operator) or one
// of And, Or, Not; only a top-level rule has Next (stepfunctions-asl.md
// 6.1).
func (p *parser) rule(obj document.Map, at string, names map[string]bool, top bool, out *ruleDraft) {
	var test condition
	var variable jsonpath.Path
	hasVariable := false
	var tests []string
	for _, e := range obj {
		fat := at + "/" + e.Key
		if base, path, kind := operator(e.Key); kind != 0 {
			tests = append(tests, e.Key)
			test = p.dataTest(e, base, path, kind, fat)
			continue
		}
		switch e.Key {
		case "Variable":
			hasVariable = true
			variable, _ = p.refPath(e.Value, e.Key, fat, refOptions{})
		case "Next":
			if !top {
				p.schema("A nested Choice rule cannot have 'Next'", fat)
				continue
			}
			out.next = p.transition(e.Value, "Next", fat, names)
		case "And", "Or":
			tests = append(tests, e.Key)
			out.condition = p.ruleList(e.Value, e.Key, fat, names)
		case "Not":
			tests = append(tests, e.Key)
			if inner, ok := e.Value.(document.Map); ok {
				nested := ruleDraft{}
				p.rule(inner, fat, names, false, &nested)
				out.condition = not{rule: nested.condition}
			} else {
				p.schema("The field 'Not' must be an object", fat)
			}
		case "Comment":
			p.str(e.Value, e.Key, fat)
		case "Assign", "Condition", "Output":
			p.unsupported(refusedConstruct(e.Key), fat)
		default:
			p.unknownField(e.Key, at)
		}
	}
	p.checkRule(obj, at, top, hasVariable, tests)
	switch t := test.(type) {
	case compare:
		t.variable = variable
		out.condition = t
	case comparePath:
		t.variable = variable
		out.condition = t
	case allOf, anyOf, not, nil:
	}
}

// dataTest reads one operator field of a data-test rule; the rule's
// Variable is filled in by the caller.
func (p *parser) dataTest(e document.Entry, base string, isPath bool, kind operand, at string) condition {
	if isPath {
		path, _ := p.refPath(e.Value, e.Key, at, refOptions{})
		return comparePath{op: base, operand: path}
	}
	return compare{op: base, operand: p.operand(e.Value, e.Key, kind, at)}
}

// checkRule reports a rule without exactly one test, a data test without
// Variable, a boolean rule with one, and a top-level rule without Next.
func (p *parser) checkRule(obj document.Map, at string, top, hasVariable bool, tests []string) {
	switch {
	case len(tests) != 1:
		p.schema("A Choice rule needs exactly one comparison operator, And, Or or Not", at)
	case tests[0] == "And" || tests[0] == "Or" || tests[0] == "Not":
		if hasVariable {
			p.schema("A Choice rule with "+tests[0]+" cannot have 'Variable'", at)
		}
	case !hasVariable:
		p.schema("These fields are required: [Variable]", at)
	}
	if _, ok := member(obj, "Next"); top && !ok {
		p.schema("These fields are required: [Next]", at)
	}
}

// ruleList reads the rules of And or Or.
func (p *parser) ruleList(v document.Value, field, at string, names map[string]bool) condition {
	objs, locs := p.objects(v, field, at, false)
	rules := make([]condition, 0, len(objs))
	for i, obj := range objs {
		nested := ruleDraft{}
		p.rule(obj, locs[i], names, false, &nested)
		rules = append(rules, nested.condition)
	}
	if field == "And" {
		return allOf{rules: rules}
	}
	return anyOf{rules: rules}
}

// operand reads a literal a Choice operator compares with.
func (p *parser) operand(v document.Value, field string, kind operand, at string) document.Value {
	ok := false
	switch kind {
	case operandString, operandTimestamp:
		_, ok = v.(document.String)
	case operandNumber:
		_, ok = v.(document.Number)
	case operandBool, operandTest:
		_, ok = v.(document.Bool)
	}
	if !ok {
		p.schema("The field '"+field+"' has a value of the wrong type", at)
		return nil
	}
	return v
}
