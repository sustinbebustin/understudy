package codegen

import (
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// emitRedact writes redact_gen.go: Redact per operation input and output
// type. A sensitive member (the sensitive trait, on the member or its
// target, from the model or an overlay) is cleared in a copy, so the
// journal never stores it; a type that reaches nothing sensitive returns
// itself.
func emitRedact(ir *ServiceIR) ([]byte, error) {
	f := newFile(ir, "redact_gen.go")
	r := &redactor{f: f, memo: map[*ShapeIR]bool{}, emitted: map[*ShapeIR]bool{}}
	for _, o := range ir.Operations {
		if !o.HasIO() {
			continue
		}
		for _, s := range []*ShapeIR{o.Input, o.Output} {
			if r.done(s) {
				continue
			}
			f.p("// Redact returns v with its sensitive members cleared.")
			f.p("func (v *%s) Redact() *%s {", s.GoName, s.GoName)
			if !r.reaches(s) {
				f.p("\treturn v")
				f.p("}\n")
				continue
			}
			r.need(s)
			f.p("\tif v == nil {")
			f.p("\t\treturn nil")
			f.p("\t}")
			f.p("\tc := v.redacted()")
			f.p("\treturn &c")
			f.p("}\n")
		}
	}
	for len(r.queue) > 0 {
		s := r.queue[0]
		r.queue = r.queue[1:]
		r.helper(s)
	}
	return f.bytes()
}

type redactor struct {
	f       *file
	memo    map[*ShapeIR]bool
	emitted map[*ShapeIR]bool
	queue   []*ShapeIR
	seen    map[*ShapeIR]bool
}

func (r *redactor) done(s *ShapeIR) bool {
	if r.seen == nil {
		r.seen = map[*ShapeIR]bool{}
	}
	if r.seen[s] {
		return true
	}
	r.seen[s] = true
	return false
}

// reaches reports whether a value of s can hold a sensitive member.
func (r *redactor) reaches(s *ShapeIR) bool {
	if v, ok := r.memo[s]; ok {
		return v
	}
	r.memo[s] = false
	out := false
	for _, m := range s.Members {
		if cleared(m) || r.reaches(m.Target) {
			out = true
		}
	}
	r.memo[s] = out
	return out
}

func (r *redactor) need(s *ShapeIR) {
	if !r.emitted[s] {
		r.emitted[s] = true
		r.queue = append(r.queue, s)
	}
}

// helper emits the copy-and-clear function of a shape that reaches
// sensitive data.
func (r *redactor) helper(s *ShapeIR) {
	switch s.Type {
	case smithy.TypeStructure:
		r.f.p("func (v %s) redacted() %s {", s.GoName, s.GoName)
		for _, m := range s.Members {
			r.member(m)
		}
		r.f.p("\treturn v")
		r.f.p("}\n")
	case smithy.TypeList:
		r.collection(s, "i", s.Members[0])
	case smithy.TypeMap:
		r.collection(s, "k", s.Members[1])
	case smithy.TypeUnion:
		r.union(s)
	case smithy.TypeString, smithy.TypeEnum, smithy.TypeIntEnum, smithy.TypeBlob, smithy.TypeBoolean,
		smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat,
		smithy.TypeDouble, smithy.TypeBigInteger, smithy.TypeBigDecimal, smithy.TypeTimestamp,
		smithy.TypeDocument, smithy.TypeUnit:
		// A simple value has no members to recurse into; value clears it in place.
	}
}

// collection emits the copy-and-clear function of a list (index i) or a map
// (key k) whose elements reach sensitive data.
func (r *redactor) collection(s *ShapeIR, index string, elem *MemberIR) {
	f := r.f
	t := f.valueType(s)
	f.p("func redact%s(v %s) %s {", s.GoName, t, t)
	f.p("\tif v == nil {")
	f.p("\t\treturn nil")
	f.p("\t}")
	f.p("\tout := make(%s, len(v))", t)
	if cleared(elem) {
		f.p("\tfor %s := range v {", index)
	} else {
		f.p("\tfor %s, e := range v {", index)
	}
	f.p("\t\tout[%s] = %s", index, r.value(elem, "e"))
	f.p("\t}")
	f.p("\treturn out")
	f.p("}\n")
}

func (r *redactor) union(s *ShapeIR) {
	f := r.f
	f.p("func redact%s(v %s) %s {", s.GoName, s.GoName, s.GoName)
	f.p("\tswitch x := v.(type) {")
	for _, m := range s.Members {
		if !cleared(m) && !r.reaches(m.Target) {
			continue
		}
		f.p("\tcase %sMember%s:", s.GoName, m.GoName)
		f.p("\t\treturn %sMember%s{Value: %s}", s.GoName, m.GoName, r.value(m, "x.Value"))
	}
	f.p("\tdefault:")
	f.p("\t\treturn v")
	f.p("\t}")
	f.p("}\n")
}

// cleared reports whether Redact clears member m: it is sensitive, or it is
// a stream, whose bytes the journal never holds.
func cleared(m *MemberIR) bool { return m.Sensitive || m.Target.Sensitive || m.Streaming }

// member clears or recurses into one structure member of the copy v.
func (r *redactor) member(m *MemberIR) {
	f := r.f
	switch {
	case cleared(m):
		f.p("\tv.%s = %s", m.GoName, r.zero(m))
	case !r.reaches(m.Target):
	case m.Target.IsStruct() && !m.Value:
		r.need(m.Target)
		f.p("\tif v.%s != nil {", m.GoName)
		f.p("\t\tc := v.%s.redacted()", m.GoName)
		f.p("\t\tv.%s = &c", m.GoName)
		f.p("\t}")
	default:
		f.p("\tv.%s = %s", m.GoName, r.value(m, "v."+m.GoName))
	}
}

// value is the redacted copy of x, a value of member m's target.
func (r *redactor) value(m *MemberIR, x string) string {
	t := m.Target
	if cleared(m) {
		return r.zero(m)
	}
	if !r.reaches(t) {
		return x
	}
	r.need(t)
	if t.Type == smithy.TypeStructure {
		return x + ".redacted()"
	}
	return "redact" + t.GoName + "(" + x + ")"
}

// zero is the absent value of a member's Go type: nil for pointers, slices,
// maps and interfaces, else the zero value of a plain field or element.
func (r *redactor) zero(m *MemberIR) string {
	if !pointerable(m.Target) || (!m.Value && m.Owner.Type == smithy.TypeStructure) {
		return "nil"
	}
	return "*new(" + r.f.valueType(m.Target) + ")"
}
