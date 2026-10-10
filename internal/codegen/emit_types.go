package codegen

import (
	"strconv"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// emitTypes writes types_gen.go: structures (required members as values,
// optional ones as pointers), enums with Known, unions as sealed
// interfaces, and error structures.
func emitTypes(ir *ServiceIR) ([]byte, error) {
	f := newFile(ir, "types_gen.go")
	for _, s := range sortedShapes(ir) {
		switch s.Type {
		case smithy.TypeStructure:
			f.structType(s)
		case smithy.TypeUnit:
			if s.IO != nil {
				f.p("%s", typeDoc(s))
				f.p("type %s struct{}\n", s.GoName)
			}
		case smithy.TypeUnion:
			f.unionType(s)
		case smithy.TypeEnum, smithy.TypeIntEnum:
			f.enumType(s)
		case smithy.TypeList, smithy.TypeMap, smithy.TypeString, smithy.TypeBlob, smithy.TypeBoolean,
			smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat,
			smithy.TypeDouble, smithy.TypeBigInteger, smithy.TypeBigDecimal, smithy.TypeTimestamp,
			smithy.TypeDocument:
			// Collections and simple values use Go's own types (valueType).
		}
	}
	return f.bytes()
}

func (f *file) structType(s *ShapeIR) {
	f.p("%s", typeDoc(s))
	if len(s.Members) == 0 {
		f.p("type %s struct{}\n", s.GoName)
		return
	}
	f.p("type %s struct {", s.GoName)
	for _, m := range s.Members {
		f.p("\t%s %s", m.GoName, f.fieldType(m))
	}
	if s.Tracked > 0 {
		f.p("\t// missing has a bit set per required member Deserialize did not")
		f.p("\t// find, so Validate tells absent from zero. Values built in Go")
		f.p("\t// leave it zero; Validate then treats a nil required list, map,")
		f.p("\t// union, blob, document or big integer as missing.")
		f.p("\tmissing uint64")
	}
	f.p("}\n")
}

func (f *file) unionType(s *ShapeIR) {
	f.p("%s Exactly one member variant is set.", typeDoc(s))
	f.p("//")
	f.p("//sumtype:decl")
	f.p("type %s interface{ is%s() }\n", s.GoName, s.GoName)
	for _, m := range s.Members {
		v := s.GoName + "Member" + m.GoName
		f.p("// %s is the %s member of %s.", v, m.Name, s.GoName)
		if m.Target.Type == smithy.TypeUnit {
			f.p("type %s struct{}\n", v)
		} else {
			f.p("type %s struct{ Value %s }\n", v, f.valueType(m.Target))
		}
		f.p("func (%s) is%s() {}\n", v, s.GoName)
	}
}

func (f *file) enumType(s *ShapeIR) {
	base := "string"
	if s.Type == smithy.TypeIntEnum {
		base = "int32"
	}
	f.p("%s", typeDoc(s))
	f.p("type %s %s\n", s.GoName, base)
	f.p("// %s values.", s.GoName)
	f.p("const (")
	for _, v := range s.Enum {
		if s.Type == smithy.TypeIntEnum {
			f.p("\t%s%s %s = %d", s.GoName, v.Name, s.GoName, v.Number)
		} else {
			f.p("\t%s%s %s = %s", s.GoName, v.Name, s.GoName, strconv.Quote(v.Value))
		}
	}
	f.p(")\n")
	f.p("// Known reports whether v is one of the modeled values; clients treat")
	f.p("// enums as open, so a decoded value may be any %s.", base)
	f.p("func (v %s) Known() bool {", s.GoName)
	f.p("\tswitch v {")
	for _, v := range s.Enum {
		f.p("\tcase %s%s:", s.GoName, v.Name)
		f.p("\t\treturn true")
	}
	f.p("\tdefault:")
	f.p("\t\treturn false")
	f.p("\t}")
	f.p("}\n")
	f.p("// Values returns the modeled values in model order.")
	f.p("func (%s) Values() []%s {", s.GoName, s.GoName)
	f.p("\treturn []%s{", s.GoName)
	for _, v := range s.Enum {
		f.p("\t\t%s%s,", s.GoName, v.Name)
	}
	f.p("\t}")
	f.p("}\n")
}
