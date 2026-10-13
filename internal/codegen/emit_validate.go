package codegen

import (
	"fmt"
	"math/big"
	"slices"
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
	v := &validator{f: f, style: ir.PathStyle, memo: map[*ShapeIR]bool{}, renders: map[*ShapeIR]bool{}, secrets: map[*ShapeIR]bool{}}
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
	style     PathStyle
	memo      map[*ShapeIR]bool
	renders   map[*ShapeIR]bool
	secrets   map[*ShapeIR]bool // see secret
	usesAt    bool
	usesFloat bool
}

// shape emits the exported Validate of an operation input and the validate
// function of any input shape with something to check.
func (v *validator) shape(s *ShapeIR) error {
	f := v.f
	if s.Top&RoleInput != 0 {
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

// valueNeeds reports whether a present value of m has anything to check. A
// stream's content is the handler's to read, so only its presence is.
func (v *validator) valueNeeds(m *MemberIR) bool {
	if m.Streaming {
		return false
	}
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
	ind := strings.Repeat("\t", depth)
	cs, err := v.conds(m, x)
	if err != nil {
		return err
	}
	for _, c := range cs {
		v.f.p("%sif %s {", ind, c.fails)
		v.add(ind+"\t", path, v.rendered(m, x), c.constraint)
		v.f.p("%s}", ind)
	}
	return v.nested(m, x, path, depth)
}

// failCond is one constraint on a value: the Go condition under which the
// value fails it, and the awserr constraint literal a violation reports.
type failCond struct {
	fails, constraint string
}

// conds lists the constraints of member m on value x in AWS's order: enum,
// pattern, length (maximum before minimum), range (minimum first).
func (v *validator) conds(m *MemberIR, x string) ([]failCond, error) {
	t := m.Target
	var out []failCond
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
		out = append(out, failCond{"!" + recv + ".Known()", "awserr.Enum{Values: []string{" + strings.Join(vals, ", ") + "}}"})
	}
	if p := m.Pattern; p != nil {
		if t.Type != smithy.TypeString && t.Type != smithy.TypeEnum {
			return nil, failf(StageEmit, m.Var, "pattern applies to strings, not %s", t.Type)
		}
		out = append(out, failCond{"!" + v.matches(p, stringExpr(t, x)), "awserr.Pattern{Echo: " + goString(p.Echo) + "}"})
	}
	if l := m.Length; l != nil {
		cs, err := v.length(m, l, x)
		if err != nil {
			return nil, err
		}
		out = append(out, cs...)
	}
	if r := m.Range; r != nil {
		cs, err := v.rangeConds(m, r, x)
		if err != nil {
			return nil, err
		}
		out = append(out, cs...)
	}
	return out, nil
}

// stringExpr is x as a Go string: enums convert, strings are already one.
func stringExpr(t *ShapeIR, x string) string {
	if t.Type == smithy.TypeEnum {
		return "string(" + x + ")"
	}
	return x
}

// nested checks the members, elements or entries of value x of member h,
// whose target holds them.
func (v *validator) nested(h *MemberIR, x, path string, depth int) error {
	t := h.Target
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
		return v.listElements(h, x, path, depth)
	case smithy.TypeMap:
		return v.mapEntries(h, x, path, depth)
	case smithy.TypeString, smithy.TypeEnum, smithy.TypeIntEnum, smithy.TypeBlob, smithy.TypeBoolean,
		smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat,
		smithy.TypeDouble, smithy.TypeBigInteger, smithy.TypeBigDecimal, smithy.TypeTimestamp,
		smithy.TypeDocument, smithy.TypeUnit:
		// A simple value has no members; checks covered it.
	}
	return nil
}

// listElements checks every element at path.<n>.member, n from 1. Rolled
// up, an element's own constraints are reported once at the list instead,
// and only the members of structured elements keep indexed paths.
func (v *validator) listElements(h *MemberIR, x, path string, depth int) error {
	f := v.f
	ind := strings.Repeat("\t", depth)
	elem := h.Target.Members[0]
	direct := v.style == PathIndexed
	if !direct {
		e := fmt.Sprintf("e%d", depth)
		if err := v.rollUp(h, elem, x, path, "for _, "+e+" := range "+x, e, depth, "awserr.ElementScope"); err != nil {
			return err
		}
		if !v.needs(elem.Target) {
			return nil
		}
	}
	f.use("strconv")
	e, i, p := fmt.Sprintf("e%d", depth), fmt.Sprintf("i%d", depth), fmt.Sprintf("p%d", depth)
	f.p("%sfor %s, %s := range %s {", ind, i, e, x)
	f.p("%s\t%s := %s", ind, p, v.pathExpr(path, "strconv.Itoa("+i+"+1)", `"member"`))
	if err := v.elem(elem, e, p, depth+1, direct); err != nil {
		return err
	}
	f.p("%s}", ind)
	return nil
}

// mapEntries checks every key at path.<key> and every value at
// path.<key>.member, in key order. Rolled up, the keys' and the values' own
// constraints are reported once each at the map. A sensitive key never
// reaches an indexed path, which would echo it.
func (v *validator) mapEntries(h *MemberIR, x, path string, depth int) error {
	f := v.f
	ind := strings.Repeat("\t", depth)
	k, e, kp, vp := fmt.Sprintf("k%d", depth), fmt.Sprintf("e%d", depth), fmt.Sprintf("kp%d", depth), fmt.Sprintf("vp%d", depth)
	key, val := h.Target.Members[0], h.Target.Members[1]
	direct := v.style == PathIndexed
	if !direct {
		if err := v.rollUp(h, key, x, path, "for "+k+" := range "+x, k, depth, "awserr.MapKeysScope"); err != nil {
			return err
		}
		if err := v.rollUp(h, val, x, path, "for _, "+e+" := range "+x, e, depth, "awserr.MapValueScope"); err != nil {
			return err
		}
		if !v.needs(val.Target) {
			return nil
		}
	} else if v.secret(key) {
		return failf(StageEmit, string(h.Target.ID)+"$key", "the key is sensitive, but an indexed validation path "+
			"(<map>.<key>.member) would echo it; give the service dev.understudy#validationPathStyle rolledUp "+
			"or remove the constraints below this map")
	}
	f.use("slices", "maps")
	f.p("%sfor _, %s := range slices.Sorted(maps.Keys(%s)) {", ind, k, x)
	if direct && v.memberNeeds(key) {
		f.p("%s\t%s := %s", ind, kp, v.pathExpr(path, stringExpr(key.Target, k)))
		if err := v.checks(key, k, kp, depth+1); err != nil {
			return err
		}
	}
	if v.memberNeeds(val) {
		f.p("%s\t%s, %s := %s[%s], %s", ind, e, vp, x, k, v.pathExpr(path, stringExpr(key.Target, k), `"member"`))
		if err := v.elem(val, e, vp, depth+1, direct); err != nil {
			return err
		}
	}
	f.p("%s}", ind)
	return nil
}

// rollUp reports, at the collection's path with the whole collection x of
// member h as the value, one Composite of every constraint of member m when
// any element the loop header ranges over (as e) fails one
// (error-matrix.md 4, style B).
func (v *validator) rollUp(h, m *MemberIR, x, path, loop, e string, depth int, scope string) error {
	value := e
	sparse := m.Owner.Sparse && pointerable(m.Target)
	if sparse {
		value = "*" + e
	}
	cs, err := v.conds(m, value)
	if err != nil || len(cs) == 0 {
		return err
	}
	f := v.f
	ind := strings.Repeat("\t", depth)
	fails := make([]string, len(cs))
	lits := make([]string, len(cs))
	for i, c := range cs {
		fails[i], lits[i] = c.fails, c.constraint
	}
	cond := strings.Join(fails, " || ")
	if sparse {
		cond = e + " != nil && (" + cond + ")"
	}
	f.p("%sif func() bool {", ind)
	f.p("%s\t%s {", ind, loop)
	f.p("%s\t\tif %s {", ind, cond)
	f.p("%s\t\t\treturn true", ind)
	f.p("%s\t\t}", ind)
	f.p("%s\t}", ind)
	f.p("%s\treturn false", ind)
	f.p("%s}() {", ind)
	v.add(ind+"\t", path, v.rendered(h, x), "awserr.Composite{Scope: "+scope+", Of: []awserr.MemberConstraint{"+strings.Join(lits, ", ")+"}}")
	f.p("%s}", ind)
	return nil
}

// elem checks a list element or map value, skipping null in sparse ones.
// With direct false only the members of a structured value are checked: its
// own constraints were rolled up at the collection.
func (v *validator) elem(m *MemberIR, e, path string, depth int, direct bool) error {
	check := v.checks
	if !direct {
		check = v.nested
	}
	if m.Owner.Sparse && pointerable(m.Target) {
		ind := strings.Repeat("\t", depth)
		v.f.p("%sif %s != nil {", ind, e)
		x := e
		if !m.Target.IsStruct() {
			x = "*" + e
		}
		if err := check(m, x, path, depth+1); err != nil {
			return err
		}
		v.f.p("%s}", ind)
		return nil
	}
	return check(m, e, path, depth)
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

// length lists the length constraints: Unicode scalar values for strings
// (ARCHITECTURE.md 5.4), bytes for blobs, elements for collections.
func (v *validator) length(m *MemberIR, l *Bounds, x string) ([]failCond, error) {
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
		return nil, failf(StageEmit, m.Var, "length applies to strings, blobs, lists and maps, not %s", t.Type)
	}
	var out []failCond
	for _, c := range []struct{ bound, op, kind string }{{l.Max, ">", "LengthMax{Max: "}, {l.Min, "<", "LengthMin{Min: "}} {
		if c.bound == "" {
			continue
		}
		if _, err := strconv.ParseInt(c.bound, 10, 64); err != nil {
			return nil, failf(StageEmit, m.Var, "length bound %q is not an integer", c.bound)
		}
		out = append(out, failCond{n + " " + c.op + " " + c.bound, "awserr." + c.kind + c.bound + "}"})
	}
	return out, nil
}

// rangeConds lists the range constraints, minimum first, comparing in int64
// for integers and float64 for floating-point members.
func (v *validator) rangeConds(m *MemberIR, r *Bounds, x string) ([]failCond, error) {
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
		return nil, failf(StageEmit, m.Var, "range on %s is not emitted; it applies to fixed-size numbers here", t.Type)
	}
	var out []failCond
	for _, c := range []struct{ bound, op, kind, field string }{{r.Min, "<", "RangeMin", "Min"}, {r.Max, ">", "RangeMax", "Max"}} {
		if c.bound == "" {
			continue
		}
		lhs := "float64(" + x + ")"
		if integer {
			if _, ok := new(big.Int).SetString(c.bound, 10); !ok {
				return nil, failf(StageEmit, m.Var, "range bound %q is not an integer, but the member is %s", c.bound, t.Type)
			}
			lhs = "int64(" + x + ")"
		} else if _, err := strconv.ParseFloat(c.bound, 64); err != nil {
			return nil, failf(StageEmit, m.Var, "range bound %q is not a number", c.bound)
		}
		out = append(out, failCond{lhs + " " + c.op + " " + c.bound, "awserr." + c.kind + "{" + c.field + ": " + goString(c.bound) + "}"})
	}
	return out, nil
}

// rendered is the awserr.Rendered expression of value x of member m: Java
// toString text, blobs and sensitive values omitted, as the sensitive trait
// requires of exception messages.
func (v *validator) rendered(m *MemberIR, x string) string {
	if v.secret(m) {
		return "awserr.RenderedOmitted{}"
	}
	t := m.Target
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

// secret reports whether a value of member m holds sensitive data: m or its
// target is sensitive, or it is a list or map whose keys or elements hold
// some. A structure renders no text, so its members do not count.
func (v *validator) secret(m *MemberIR) bool {
	if m.Sensitive || m.Target.Sensitive {
		return true
	}
	t := m.Target
	if t.Type != smithy.TypeList && t.Type != smithy.TypeMap {
		return false
	}
	if s, ok := v.secrets[t]; ok {
		return s
	}
	v.secrets[t] = false
	s := slices.ContainsFunc(t.Members, v.secret)
	v.secrets[t] = s
	return s
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
	// elemText renders one element or value e; ok is false for a shape AWS
	// renders with no text v knows (structures, blobs), which become "".
	elemText := func(m *MemberIR, e string) (string, bool) {
		x := e
		if m.Owner.Sparse && pointerable(m.Target) && !m.Target.IsStruct() {
			x = "*" + e
		}
		r := v.rendered(m, x)
		if after, ok := strings.CutPrefix(r, "awserr.RenderedText{Text: "); ok {
			return strings.TrimSuffix(after, "}"), true
		}
		return goString(""), false
	}
	// nullable guards a sparse collection's nil element, rendered null.
	nullable := func(m *MemberIR, e, ind string) {
		if m.Owner.Sparse && pointerable(m.Target) {
			f.p("%sif %s == nil {", ind, e)
			f.p("%s\tparts = append(parts, \"null\")", ind)
			f.p("%s\tcontinue", ind)
			f.p("%s}", ind)
		}
	}
	if t.Type == smithy.TypeList {
		elem := t.Members[0]
		text, ok := elemText(elem, "e")
		if ok {
			f.p("\tfor _, e := range v {")
			nullable(elem, "e", "\t\t")
		} else {
			f.p("\tfor range v {")
		}
		f.p("\t\tparts = append(parts, %s)", text)
		f.p("\t}")
		f.p("\treturn \"[\" + strings.Join(parts, \", \") + \"]\"")
	} else {
		f.use("slices", "maps")
		val := t.Members[1]
		text, ok := elemText(val, "e")
		f.p("\tfor _, k := range slices.Sorted(maps.Keys(v)) {")
		if ok {
			f.p("\t\te := v[k]")
			nullable(val, "e", "\t\t")
		}
		f.p("\t\tparts = append(parts, %s+\"=\"+%s)", stringExpr(t.Members[0].Target, "k"), text)
		f.p("\t}")
		f.p("\treturn \"{\" + strings.Join(parts, \", \") + \"}\"")
	}
	f.p("}\n")
}
