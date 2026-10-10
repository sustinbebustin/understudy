package codegen

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// emitValidate writes validate_gen.go: Validate per input structure, which
// reports constraint violations member by member in model order and, within
// a member, required, then enum, then pattern, then length (maximum before
// minimum), then range (pattern before length is AWS's order,
// error-matrix.md 4); and ValidationError per operation input, which turns
// violations into the error dev.understudy#validationError selects.
func emitValidate(ir *ServiceIR) ([]byte, error) {
	f := newFile(ir, "validate_gen.go")
	f.use("awserr")
	v := &validator{f: f, memo: map[*ShapeIR]bool{}, renders: map[*ShapeIR]bool{}}
	emitPatterns(f, ir)
	for _, s := range sortedShapes(ir) {
		if s.Role&RoleInput == 0 {
			continue
		}
		if err := v.shape(s); err != nil {
			return nil, err
		}
	}
	if err := emitPolicies(f, ir); err != nil {
		return nil, err
	}
	v.helpers(ir)
	return f.bytes()
}

type validator struct {
	f         *file
	memo      map[*ShapeIR]bool
	renders   map[*ShapeIR]bool
	usesAt    bool
	usesFloat bool
}

// shape emits the exported Validate of an operation input and the validate
// function of any input shape with something to check.
func (v *validator) shape(s *ShapeIR) error {
	f := v.f
	if s.IO != nil && s == s.IO.Input {
		f.p("// Validate reports the constraint violations of v in AWS's order; nil")
		f.p("// means v satisfies every generated constraint.")
		f.p("func (v *%s) Validate() []awserr.Violation {", s.GoName)
		if v.needs(s) {
			f.p("\tvar out []awserr.Violation")
			f.p("\tv.validate(nil, &out)")
			f.p("\treturn out")
		} else {
			f.p("\treturn nil")
		}
		f.p("}\n")
	}
	if !v.needs(s) {
		return nil
	}
	switch s.Type {
	case smithy.TypeStructure:
		return v.structure(s)
	case smithy.TypeUnion:
		return v.union(s)
	case smithy.TypeList, smithy.TypeMap, smithy.TypeString, smithy.TypeEnum, smithy.TypeIntEnum,
		smithy.TypeBlob, smithy.TypeBoolean, smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger,
		smithy.TypeLong, smithy.TypeFloat, smithy.TypeDouble, smithy.TypeBigInteger, smithy.TypeBigDecimal,
		smithy.TypeTimestamp, smithy.TypeDocument, smithy.TypeUnit:
		// Collections are checked inline by the member that holds them (nested).
	}
	return nil
}

// helpers emits the rendering and path functions the validators used.
func (v *validator) helpers(ir *ServiceIR) {
	f := v.f
	for _, s := range sortedShapes(ir) {
		if v.renders[s] {
			v.renderFunc(s)
		}
	}
	if v.usesAt {
		f.use("slices")
		f.p("// at extends a violation path without aliasing the parent's array.")
		f.p("func at(p awserr.Path, segments ...string) awserr.Path { return append(slices.Clip(p), segments...) }\n")
	}
	if v.usesFloat {
		f.use("strconv", "strings")
		f.p("// renderFloat renders a number as Java's Double.toString does for the")
		f.p("// values a range constraint echoes: 900.0, 0.5.")
		f.p("func renderFloat(x float64) string {")
		f.p("\ts := strconv.FormatFloat(x, 'f', -1, 64)")
		f.p("\tif !strings.ContainsAny(s, \".eE\") {")
		f.p("\t\ts += \".0\"")
		f.p("\t}")
		f.p("\treturn s")
		f.p("}\n")
	}
}

// needs reports whether a shape has anything to check, itself or below.
func (v *validator) needs(s *ShapeIR) bool {
	if r, ok := v.memo[s]; ok {
		return r
	}
	v.memo[s] = false
	r := false
	for _, m := range s.Members {
		if v.memberNeeds(m) {
			r = true
		}
	}
	v.memo[s] = r
	return r
}

func (v *validator) memberNeeds(m *MemberIR) bool {
	return m.Bit >= 0 || v.valueNeeds(m)
}

// valueNeeds reports whether a present value of m has anything to check.
func (v *validator) valueNeeds(m *MemberIR) bool {
	if m.Length != nil || m.Range != nil || m.Pattern != nil {
		return true
	}
	t := m.Target
	if (t.Type == smithy.TypeEnum || t.Type == smithy.TypeIntEnum) && m.EnumPolicy == EnumClosed {
		return true
	}
	return v.needs(t)
}

func (v *validator) structure(s *ShapeIR) error {
	v.f.p("func (v *%s) validate(path awserr.Path, out *[]awserr.Violation) {", s.GoName)
	for _, m := range s.Members {
		if !v.memberNeeds(m) {
			continue
		}
		if err := v.structMember(m); err != nil {
			return err
		}
	}
	v.f.p("}\n")
	return nil
}

// structMember checks one structure member: presence for a tracked
// required member, then its value when present. A required member whose Go
// type has its own nil (a list, map, union, blob, document or big integer)
// is also missing when nil, so an input built in Go rather than decoded is
// held to the same rule; Deserialize stores a present value as non-nil.
func (v *validator) structMember(m *MemberIR) error {
	f := v.f
	path := v.pathExpr("path", goString(lowerCamel(m.Name)))
	switch {
	case m.Bit >= 0:
		if nilAbsent(m.Target.Type) {
			f.p("\tif v.missing&(1<<%d) != 0 || v.%s == nil {", m.Bit, m.GoName)
		} else {
			f.p("\tif v.missing&(1<<%d) != 0 {", m.Bit)
		}
		f.p("\t\t*out = append(*out, awserr.Violation{Path: %s, Value: awserr.RenderedNull{}, Constraint: awserr.Required{}})", path)
		if v.valueNeeds(m) {
			f.p("\t} else {")
			if err := v.checks(m, "v."+m.GoName, path, 2); err != nil {
				return err
			}
		}
		f.p("\t}")
	case m.Value:
		return v.checks(m, "v."+m.GoName, path, 1)
	default:
		f.p("\tif v.%s != nil {", m.GoName)
		x := "v." + m.GoName
		if pointerable(m.Target) && !m.Target.IsStruct() {
			x = "*v." + m.GoName
		}
		if err := v.checks(m, x, path, 2); err != nil {
			return err
		}
		f.p("\t}")
	}
	return nil
}

func (v *validator) union(s *ShapeIR) error {
	f := v.f
	f.p("func validate%s(path awserr.Path, u %s, out *[]awserr.Violation) {", s.GoName, s.GoName)
	f.p("\tswitch x := u.(type) {")
	for _, m := range s.Members {
		if !v.memberNeeds(m) {
			continue
		}
		f.p("\tcase %sMember%s:", s.GoName, m.GoName)
		if err := v.checks(m, "x.Value", v.pathExpr("path", goString(lowerCamel(m.Name))), 2); err != nil {
			return err
		}
	}
	f.p("\t}")
	f.p("}\n")
	return nil
}

func (v *validator) pathExpr(base string, segs ...string) string {
	v.usesAt = true
	return "at(" + base + ", " + strings.Join(segs, ", ") + ")"
}

func (v *validator) add(ind, path, value, constraint string) {
	v.f.p("%s*out = append(*out, awserr.Violation{Path: %s, Value: %s, Constraint: %s})", ind, path, value, constraint)
}

// checks emits the checks of member m on value x at path. depth is the
// indentation; nested loops name their variables by depth.
func (v *validator) checks(m *MemberIR, x, path string, depth int) error {
	f := v.f
	ind := strings.Repeat("\t", depth)
	t := m.Target
	if (t.Type == smithy.TypeEnum || t.Type == smithy.TypeIntEnum) && m.EnumPolicy == EnumClosed {
		vals := make([]string, len(t.Enum))
		for i, e := range t.Enum {
			vals[i] = goString(e.Value)
			if t.Type == smithy.TypeIntEnum {
				vals[i] = goString(strconv.FormatInt(e.Number, 10))
			}
		}
		recv := x
		if strings.HasPrefix(x, "*") {
			recv = "(" + x + ")"
		}
		f.p("%sif !%s.Known() {", ind, recv)
		v.add(ind+"\t", path, v.rendered(t, x), "awserr.Enum{Values: []string{"+strings.Join(vals, ", ")+"}}")
		f.p("%s}", ind)
	}
	if p := m.Pattern; p != nil {
		if t.Type != smithy.TypeString && t.Type != smithy.TypeEnum {
			return failf(StageEmit, m.Var, "pattern applies to strings, not %s", t.Type)
		}
		f.p("%sif !%s {", ind, v.matches(p, stringExpr(t, x)))
		v.add(ind+"\t", path, v.rendered(t, x), "awserr.Pattern{Echo: "+goString(p.Echo)+"}")
		f.p("%s}", ind)
	}
	if l := m.Length; l != nil {
		if err := v.length(m, l, x, path, ind); err != nil {
			return err
		}
	}
	if r := m.Range; r != nil {
		if err := v.rangeChecks(m, r, x, path, ind); err != nil {
			return err
		}
	}
	return v.nested(t, x, path, depth)
}

// stringExpr is x as a Go string: enums convert, strings are already one.
func stringExpr(t *ShapeIR, x string) string {
	if t.Type == smithy.TypeEnum {
		return "string(" + x + ")"
	}
	return x
}

func (v *validator) nested(t *ShapeIR, x, path string, depth int) error {
	if !v.needs(t) {
		return nil
	}
	ind := strings.Repeat("\t", depth)
	switch t.Type {
	case smithy.TypeStructure:
		v.f.p("%s%s.validate(%s, out)", ind, x, path)
	case smithy.TypeUnion:
		v.f.p("%svalidate%s(%s, %s, out)", ind, t.GoName, path, x)
	case smithy.TypeList:
		return v.listElements(t, x, path, depth)
	case smithy.TypeMap:
		return v.mapEntries(t, x, path, depth)
	case smithy.TypeString, smithy.TypeEnum, smithy.TypeIntEnum, smithy.TypeBlob, smithy.TypeBoolean,
		smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat,
		smithy.TypeDouble, smithy.TypeBigInteger, smithy.TypeBigDecimal, smithy.TypeTimestamp,
		smithy.TypeDocument, smithy.TypeUnit:
		// A simple value has no members; checks covered it.
	}
	return nil
}

// listElements checks every element at path.<n>.member, n from 1.
func (v *validator) listElements(t *ShapeIR, x, path string, depth int) error {
	f := v.f
	ind := strings.Repeat("\t", depth)
	f.use("strconv")
	e, i, p := fmt.Sprintf("e%d", depth), fmt.Sprintf("i%d", depth), fmt.Sprintf("p%d", depth)
	f.p("%sfor %s, %s := range %s {", ind, i, e, x)
	f.p("%s\t%s := %s", ind, p, v.pathExpr(path, "strconv.Itoa("+i+"+1)", `"member"`))
	if err := v.elem(t.Members[0], e, p, depth+1); err != nil {
		return err
	}
	f.p("%s}", ind)
	return nil
}

// mapEntries checks every key at path.<key> and every value at
// path.<key>.member, in key order.
func (v *validator) mapEntries(t *ShapeIR, x, path string, depth int) error {
	f := v.f
	ind := strings.Repeat("\t", depth)
	f.use("slices", "maps")
	k, e, kp, vp := fmt.Sprintf("k%d", depth), fmt.Sprintf("e%d", depth), fmt.Sprintf("kp%d", depth), fmt.Sprintf("vp%d", depth)
	key, val := t.Members[0], t.Members[1]
	f.p("%sfor _, %s := range slices.Sorted(maps.Keys(%s)) {", ind, k, x)
	if v.memberNeeds(key) {
		f.p("%s\t%s := %s", ind, kp, v.pathExpr(path, stringExpr(key.Target, k)))
		if err := v.checks(key, k, kp, depth+1); err != nil {
			return err
		}
	}
	if v.memberNeeds(val) {
		f.p("%s\t%s, %s := %s[%s], %s", ind, e, vp, x, k, v.pathExpr(path, stringExpr(key.Target, k), `"member"`))
		if err := v.elem(val, e, vp, depth+1); err != nil {
			return err
		}
	}
	f.p("%s}", ind)
	return nil
}

// elem checks a list element or map value, skipping null in sparse ones.
func (v *validator) elem(m *MemberIR, e, path string, depth int) error {
	if m.Owner.Sparse && pointerable(m.Target) {
		ind := strings.Repeat("\t", depth)
		v.f.p("%sif %s != nil {", ind, e)
		x := e
		if !m.Target.IsStruct() {
			x = "*" + e
		}
		if err := v.checks(m, x, path, depth+1); err != nil {
			return err
		}
		v.f.p("%s}", ind)
		return nil
	}
	return v.checks(m, e, path, depth)
}

func (v *validator) matches(p *PatternIR, s string) string {
	if p.Validator == nil {
		return p.Var + ".MatchString(" + s + ")"
	}
	var parts []string
	for i := range p.Validator.AllOf {
		parts = append(parts, fmt.Sprintf("%sAll%d.MatchString(%s)", p.Var, i, s))
	}
	for i := range p.Validator.NoneOf {
		parts = append(parts, fmt.Sprintf("!%sNone%d.MatchString(%s)", p.Var, i, s))
	}
	return "(" + strings.Join(parts, " && ") + ")"
}

// length emits the length checks: Unicode scalar values for strings
// (ARCHITECTURE.md 5.4), bytes for blobs, elements for collections.
func (v *validator) length(m *MemberIR, l *Bounds, x, path, ind string) error {
	f := v.f
	t := m.Target
	var n string
	switch t.Type {
	case smithy.TypeString, smithy.TypeEnum:
		f.use("utf8")
		n = "utf8.RuneCountInString(" + stringExpr(t, x) + ")"
	case smithy.TypeBlob, smithy.TypeList, smithy.TypeMap:
		n = "len(" + x + ")"
	case smithy.TypeStructure, smithy.TypeUnion, smithy.TypeIntEnum, smithy.TypeBoolean, smithy.TypeByte,
		smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat, smithy.TypeDouble,
		smithy.TypeBigInteger, smithy.TypeBigDecimal, smithy.TypeTimestamp, smithy.TypeDocument, smithy.TypeUnit:
		return failf(StageEmit, m.Var, "length applies to strings, blobs, lists and maps, not %s", t.Type)
	}
	for _, c := range []struct{ bound, op, kind string }{{l.Max, ">", "LengthMax{Max: "}, {l.Min, "<", "LengthMin{Min: "}} {
		if c.bound == "" {
			continue
		}
		if _, err := strconv.ParseInt(c.bound, 10, 64); err != nil {
			return failf(StageEmit, m.Var, "length bound %q is not an integer", c.bound)
		}
		f.p("%sif %s %s %s {", ind, n, c.op, c.bound)
		v.add(ind+"\t", path, v.rendered(t, x), "awserr."+c.kind+c.bound+"}")
		f.p("%s}", ind)
	}
	return nil
}

// rangeChecks emits the range checks, minimum first, comparing in int64 for
// integers and float64 for floating-point members.
func (v *validator) rangeChecks(m *MemberIR, r *Bounds, x, path, ind string) error {
	f := v.f
	t := m.Target
	var integer bool
	switch t.Type {
	case smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeIntEnum:
		integer = true
	case smithy.TypeFloat, smithy.TypeDouble:
		integer = false
	case smithy.TypeStructure, smithy.TypeUnion, smithy.TypeList, smithy.TypeMap, smithy.TypeString,
		smithy.TypeEnum, smithy.TypeBlob, smithy.TypeBoolean, smithy.TypeBigInteger, smithy.TypeBigDecimal,
		smithy.TypeTimestamp, smithy.TypeDocument, smithy.TypeUnit:
		return failf(StageEmit, m.Var, "range on %s is not emitted; it applies to fixed-size numbers here", t.Type)
	}
	for _, c := range []struct{ bound, op, kind, field string }{{r.Min, "<", "RangeMin", "Min"}, {r.Max, ">", "RangeMax", "Max"}} {
		if c.bound == "" {
			continue
		}
		lhs := "float64(" + x + ")"
		if integer {
			if _, ok := new(big.Int).SetString(c.bound, 10); !ok {
				return failf(StageEmit, m.Var, "range bound %q is not an integer, but the member is %s", c.bound, t.Type)
			}
			lhs = "int64(" + x + ")"
		} else if _, err := strconv.ParseFloat(c.bound, 64); err != nil {
			return failf(StageEmit, m.Var, "range bound %q is not a number", c.bound)
		}
		f.p("%sif %s %s %s {", ind, lhs, c.op, c.bound)
		v.add(ind+"\t", path, v.rendered(t, x), "awserr."+c.kind+"{"+c.field+": "+goString(c.bound)+"}")
		f.p("%s}", ind)
	}
	return nil
}

// rendered is the awserr.Rendered expression of value x of shape t: Java
// toString text, blobs omitted.
func (v *validator) rendered(t *ShapeIR, x string) string {
	switch t.Type {
	case smithy.TypeString, smithy.TypeEnum:
		return "awserr.RenderedText{Text: " + stringExpr(t, x) + "}"
	case smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeIntEnum:
		v.f.use("strconv")
		return "awserr.RenderedText{Text: strconv.FormatInt(int64(" + x + "), 10)}"
	case smithy.TypeFloat, smithy.TypeDouble:
		v.usesFloat = true
		return "awserr.RenderedText{Text: renderFloat(float64(" + x + "))}"
	case smithy.TypeList, smithy.TypeMap:
		v.markRender(t)
		return "awserr.RenderedText{Text: render" + t.GoName + "(" + x + ")}"
	case smithy.TypeBlob, smithy.TypeStructure, smithy.TypeUnion, smithy.TypeBoolean, smithy.TypeBigInteger,
		smithy.TypeBigDecimal, smithy.TypeTimestamp, smithy.TypeDocument, smithy.TypeUnit:
		return "awserr.RenderedOmitted{}"
	default:
		return "awserr.RenderedOmitted{}"
	}
}

func (v *validator) markRender(t *ShapeIR) {
	if v.renders[t] {
		return
	}
	v.renders[t] = true
	for _, m := range t.Members {
		if m.Target.Type == smithy.TypeList || m.Target.Type == smithy.TypeMap {
			v.markRender(m.Target)
		}
	}
}

// renderFunc emits the Java toString rendering of a list or map: [a, b] and
// {k=v}.
func (v *validator) renderFunc(t *ShapeIR) {
	f := v.f
	f.use("strings")
	f.p("func render%s(v %s) string {", t.GoName, f.valueType(t))
	f.p("\tparts := make([]string, 0, len(v))")
	elemText := func(m *MemberIR, x string) string {
		r := v.rendered(m.Target, x)
		if after, ok := strings.CutPrefix(r, "awserr.RenderedText{Text: "); ok {
			return strings.TrimSuffix(after, "}")
		}
		return goString("")
	}
	if t.Type == smithy.TypeList {
		f.p("\tfor _, e := range v {")
		f.p("\t\tparts = append(parts, %s)", elemText(t.Members[0], "e"))
		f.p("\t}")
		f.p("\treturn \"[\" + strings.Join(parts, \", \") + \"]\"")
	} else {
		f.use("slices", "maps")
		f.p("\tfor _, k := range slices.Sorted(maps.Keys(v)) {")
		f.p("\t\tparts = append(parts, %s+\"=\"+%s)", stringExpr(t.Members[0].Target, "k"), elemText(t.Members[1], "v[k]"))
		f.p("\t}")
		f.p("\treturn \"{\" + strings.Join(parts, \", \") + \"}\"")
	}
	f.p("}\n")
}
