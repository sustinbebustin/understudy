package codegen

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// emitSerde writes serde_gen.go by shape role (ARCHITECTURE.md 5.4):
// deserializers for shapes inputs reach, serializers for shapes outputs and
// errors reach, both for shapes in both roles. The code names schemas and
// members and holds no protocol logic.
func emitSerde(ir *ServiceIR) ([]byte, error) {
	f := newFile(ir, "serde_gen.go")
	f.use("smithy")
	for _, s := range sortedShapes(ir) {
		if s.Role&RoleOutput != 0 {
			f.serializer(s)
		}
		if s.Role&RoleInput != 0 {
			if err := f.deserializer(s); err != nil {
				return nil, err
			}
		}
	}
	return f.bytes()
}

func (f *file) serializer(s *ShapeIR) {
	switch s.Type {
	case smithy.TypeStructure, smithy.TypeUnit:
		if s.Type == smithy.TypeUnit && s.IO == nil {
			return
		}
		f.structSerializer(s)
	case smithy.TypeUnion:
		f.unionSerializer(s)
	case smithy.TypeList:
		f.p("func serialize%s(s smithy.Serializer, m *smithy.Member, v %s) {", s.GoName, f.valueType(s))
		f.p("\ts.WriteList(m, len(v))")
		f.p("\tfor _, e := range v {")
		f.writeElem(s.Members[0], "e", 2)
		f.p("\t}")
		f.p("\ts.CloseList()")
		f.p("}\n")
	case smithy.TypeMap:
		f.use("slices", "maps")
		f.p("func serialize%s(s smithy.Serializer, m *smithy.Member, v %s) {", s.GoName, f.valueType(s))
		f.p("\ts.WriteMap(m, len(v))")
		f.p("\tfor _, k := range slices.Sorted(maps.Keys(v)) {")
		f.p("\t\ts.WriteMapKey(string(k))")
		f.p("\t\te := v[k]")
		f.writeElem(s.Members[1], "e", 2)
		f.p("\t}")
		f.p("\ts.CloseMap()")
		f.p("}\n")
	case smithy.TypeString, smithy.TypeEnum, smithy.TypeIntEnum, smithy.TypeBlob, smithy.TypeBoolean,
		smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat,
		smithy.TypeDouble, smithy.TypeBigInteger, smithy.TypeBigDecimal, smithy.TypeTimestamp,
		smithy.TypeDocument:
		// A simple value is written inline where a member holds it (write).
	}
}

func (f *file) structSerializer(s *ShapeIR) {
	if s.IO != nil || s.Error != nil {
		f.p("// Serialize writes v through s.")
		f.p("func (v *%s) Serialize(s smithy.Serializer) { v.serialize(s, nil) }\n", s.GoName)
	}
	f.p("func (v *%s) serialize(s smithy.Serializer, m *smithy.Member) {", s.GoName)
	f.p("\ts.WriteStruct(m, &%s)", s.SchemaVar)
	for _, m := range s.Members {
		f.serializeMember(m)
	}
	f.p("\ts.CloseStruct()")
	f.p("}\n")
}

func (f *file) unionSerializer(s *ShapeIR) {
	f.p("func serialize%s(s smithy.Serializer, m *smithy.Member, v %s) {", s.GoName, s.GoName)
	f.p("\ts.WriteUnion(m, &%s)", s.SchemaVar)
	if slices.ContainsFunc(s.Members, func(m *MemberIR) bool { return m.Target.Type != smithy.TypeUnit }) {
		f.p("\tswitch x := v.(type) {")
	} else {
		f.p("\tswitch v.(type) {")
	}
	for _, m := range s.Members {
		f.p("\tcase %sMember%s:", s.GoName, m.GoName)
		f.write(m, "x.Value", 2)
	}
	f.p("\t}")
	f.p("\ts.CloseUnion()")
	f.p("}\n")
}

// serializeMember writes one structure member, skipping absent values.
func (f *file) serializeMember(m *MemberIR) {
	switch {
	case m.Target.Type == smithy.TypeList || m.Target.Type == smithy.TypeMap:
		switch {
		case m.Empty == EmptyOmit:
			f.p("\tif len(v.%s) > 0 {", m.GoName)
		case m.Empty == EmptyEmit || m.Required:
			f.write(m, "v."+m.GoName, 1)
			return
		default:
			f.p("\tif v.%s != nil {", m.GoName)
		}
		f.write(m, "v."+m.GoName, 2)
		f.p("\t}")
	case !pointerable(m.Target):
		f.p("\tif v.%s != nil {", m.GoName)
		f.write(m, "v."+m.GoName, 2)
		f.p("\t}")
	case m.Value:
		f.write(m, "v."+m.GoName, 1)
	default:
		f.p("\tif v.%s != nil {", m.GoName)
		expr := "*v." + m.GoName
		if m.Target.IsStruct() {
			expr = "v." + m.GoName
		}
		f.write(m, expr, 2)
		f.p("\t}")
	}
}

// writeElem writes a list element or map value; sparse collections write
// null for nil.
func (f *file) writeElem(m *MemberIR, expr string, depth int) {
	ind := strings.Repeat("\t", depth)
	if m.Owner.Sparse && pointerable(m.Target) {
		f.p("%sif %s == nil {", ind, expr)
		f.p("%s\ts.WriteNull(&%s)", ind, m.Var)
		f.p("%s} else {", ind)
		if m.Target.IsStruct() {
			f.write(m, expr, depth+1)
		} else {
			f.write(m, "*"+expr, depth+1)
		}
		f.p("%s}", ind)
		return
	}
	f.write(m, expr, depth)
}

// scalarCalls names the Serializer and Deserializer methods of each simple
// type, and the conversion between the wire value and the Go type.
func scalarCalls(t *ShapeIR) (write, read, conv string) {
	switch t.Type {
	case smithy.TypeString:
		return "WriteString", "ReadString", ""
	case smithy.TypeEnum:
		return "WriteString", "ReadString", t.GoName
	case smithy.TypeIntEnum:
		return "WriteInt32", "ReadInt32", t.GoName
	case smithy.TypeBoolean:
		return "WriteBool", "ReadBool", ""
	case smithy.TypeByte:
		return "WriteInt8", "ReadInt8", ""
	case smithy.TypeShort:
		return "WriteInt16", "ReadInt16", ""
	case smithy.TypeInteger:
		return "WriteInt32", "ReadInt32", ""
	case smithy.TypeLong:
		return "WriteInt64", "ReadInt64", ""
	case smithy.TypeFloat:
		return "WriteFloat32", "ReadFloat32", ""
	case smithy.TypeDouble:
		return "WriteFloat64", "ReadFloat64", ""
	case smithy.TypeBigInteger:
		return "WriteBigInt", "ReadBigInt", ""
	case smithy.TypeBigDecimal:
		return "WriteBigDecimal", "ReadBigDecimal", ""
	case smithy.TypeTimestamp:
		return "WriteTimestamp", "ReadTimestamp", ""
	case smithy.TypeBlob:
		return "WriteBlob", "ReadBlob", ""
	case smithy.TypeDocument:
		return "WriteDocument", "ReadDocument", ""
	case smithy.TypeStructure, smithy.TypeUnion, smithy.TypeList, smithy.TypeMap, smithy.TypeUnit:
		return "", "", ""
	default:
		return "", "", ""
	}
}

// write emits the call that writes value expr of member m.
func (f *file) write(m *MemberIR, expr string, depth int) {
	ind := strings.Repeat("\t", depth)
	mv := "&" + m.Var
	t := m.Target
	switch t.Type {
	case smithy.TypeStructure:
		f.p("%s%s.serialize(s, %s)", ind, expr, mv)
	case smithy.TypeUnit:
		f.p("%ss.WriteStruct(%s, &%s)", ind, mv, t.SchemaVar)
		f.p("%ss.CloseStruct()", ind)
	case smithy.TypeUnion, smithy.TypeList, smithy.TypeMap:
		f.p("%sserialize%s(s, %s, %s)", ind, t.GoName, mv, expr)
	case smithy.TypeString, smithy.TypeEnum, smithy.TypeIntEnum, smithy.TypeBlob, smithy.TypeBoolean,
		smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat,
		smithy.TypeDouble, smithy.TypeBigInteger, smithy.TypeBigDecimal, smithy.TypeTimestamp,
		smithy.TypeDocument:
		call, _, conv := scalarCalls(t)
		if conv != "" {
			wire := "string"
			if t.Type == smithy.TypeIntEnum {
				wire = "int32"
			}
			expr = wire + "(" + expr + ")"
		}
		f.p("%ss.%s(%s, %s)", ind, call, mv, expr)
	}
}

func (f *file) deserializer(s *ShapeIR) error {
	switch s.Type {
	case smithy.TypeStructure, smithy.TypeUnit:
		if s.Type == smithy.TypeUnit && s.IO == nil {
			return nil
		}
		return f.structDeserializer(s)
	case smithy.TypeUnion:
		f.unionDeserializer(s)
	case smithy.TypeList:
		f.p("func deserialize%s(d smithy.Deserializer, m *smithy.Member) (%s, error) {", s.GoName, f.valueType(s))
		f.p("\tif err := d.ReadList(m); err != nil {")
		f.p("\t\treturn nil, err")
		f.p("\t}")
		f.p("\tout := %s{}", f.valueType(s))
		f.p("\tfor {")
		f.p("\t\tmore, err := d.ReadListElement()")
		f.p("\t\tif err != nil {")
		f.p("\t\t\treturn nil, err")
		f.p("\t\t}")
		f.p("\t\tif !more {")
		f.p("\t\t\treturn out, nil")
		f.p("\t\t}")
		f.readElem(s.Members[0], "out = append(out, %s)")
		f.p("\t}")
		f.p("}\n")
	case smithy.TypeMap:
		f.p("func deserialize%s(d smithy.Deserializer, m *smithy.Member) (%s, error) {", s.GoName, f.valueType(s))
		f.p("\tif err := d.ReadMap(m); err != nil {")
		f.p("\t\treturn nil, err")
		f.p("\t}")
		f.p("\tout := %s{}", f.valueType(s))
		f.p("\tfor {")
		f.p("\t\tkey, more, err := d.ReadMapKey()")
		f.p("\t\tif err != nil {")
		f.p("\t\t\treturn nil, err")
		f.p("\t\t}")
		f.p("\t\tif !more {")
		f.p("\t\t\treturn out, nil")
		f.p("\t\t}")
		k := "key"
		if key := s.Members[0].Target; key.Type == smithy.TypeEnum {
			k = key.GoName + "(key)"
		}
		f.readElem(s.Members[1], "out["+k+"] = %s")
		f.p("\t}")
		f.p("}\n")
	case smithy.TypeString, smithy.TypeEnum, smithy.TypeIntEnum, smithy.TypeBlob, smithy.TypeBoolean,
		smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat,
		smithy.TypeDouble, smithy.TypeBigInteger, smithy.TypeBigDecimal, smithy.TypeTimestamp,
		smithy.TypeDocument:
		// A simple value is read inline where a member holds it (read).
	}
	return nil
}

func (f *file) unionDeserializer(s *ShapeIR) {
	f.use("fmt")
	f.p("func deserialize%s(d smithy.Deserializer) (%s, error) {", s.GoName, s.GoName)
	f.p("\tm, err := d.ReadUnion(&%s)", s.SchemaVar)
	f.p("\tif err != nil {")
	f.p("\t\treturn nil, err")
	f.p("\t}")
	f.p("\tvar out %s", s.GoName)
	f.p("\tswitch m {")
	for _, m := range s.Members {
		f.p("\tcase &%s:", m.Var)
		if m.Target.Type == smithy.TypeUnit {
			f.readUnit(2, "return nil, err")
			f.p("\t\tout = %sMember%s{}", s.GoName, m.GoName)
			continue
		}
		f.read(m, 2, "return nil, err")
		f.p("\t\tout = %sMember%s{Value: x}", s.GoName, m.GoName)
	}
	f.p("\tdefault:")
	f.p("\t\treturn nil, fmt.Errorf(\"%s: union %s has no member %%q; the codec returned a member of another shape\", m.Name)", f.ir.Package, s.ID)
	f.p("\t}")
	f.p("\tif err := d.CloseUnion(); err != nil {")
	f.p("\t\treturn nil, err")
	f.p("\t}")
	f.p("\treturn out, nil")
	f.p("}\n")
}

func (f *file) structDeserializer(s *ShapeIR) error {
	if s.IO != nil {
		f.p("// Deserialize reads v from d. Absent members stay absent; Validate")
		f.p("// reports missing required ones.")
		f.p("func (v *%s) Deserialize(d smithy.Deserializer) error { return v.deserialize(d) }\n", s.GoName)
	}
	f.use("fmt")
	f.p("func (v *%s) deserialize(d smithy.Deserializer) error {", s.GoName)
	f.p("\tif err := d.ReadStruct(&%s); err != nil {", s.SchemaVar)
	f.p("\t\treturn err")
	f.p("\t}")
	if s.Tracked > 0 {
		f.p("\tv.missing = 1<<%d - 1", s.Tracked)
	}
	for _, m := range s.Members {
		if !m.Value || m.Default == nil {
			continue
		}
		lit, set, err := defaultLit(m)
		if err != nil {
			return err
		}
		if set {
			f.p("\tv.%s = %s", m.GoName, lit)
		}
	}
	f.p("\tfor {")
	f.p("\t\tm, ok, err := d.ReadStructMember()")
	f.p("\t\tif err != nil {")
	f.p("\t\t\treturn err")
	f.p("\t\t}")
	f.p("\t\tif !ok {")
	f.p("\t\t\treturn nil")
	f.p("\t\t}")
	f.p("\t\tswitch m {")
	for _, m := range s.Members {
		f.p("\t\tcase &%s:", m.Var)
		f.read(m, 3, "return err")
		if m.Value || !pointerable(m.Target) {
			f.p("\t\t\tv.%s = x", m.GoName)
		} else {
			f.p("\t\t\tv.%s = &x", m.GoName)
		}
		if m.Bit >= 0 {
			f.p("\t\t\tv.missing &^= 1 << %d", m.Bit)
		}
	}
	f.p("\t\tdefault:")
	f.p("\t\t\treturn fmt.Errorf(\"%s: structure %s has no member %%q; the codec returned a member of another shape\", m.Name)", f.ir.Package, s.ID)
	f.p("\t\t}")
	f.p("\t}")
	f.p("}\n")
	return nil
}

// readElem reads a list element or map value inside the loop of a
// collection deserializer; store is a format with one %s for the value.
func (f *file) readElem(m *MemberIR, store string) {
	if m.Owner.Sparse && pointerable(m.Target) {
		f.p("\t\tnull, err := d.ReadNull(&%s)", m.Var)
		f.p("\t\tif err != nil {")
		f.p("\t\t\treturn nil, err")
		f.p("\t\t}")
		f.p("\t\tif null {")
		f.p("\t\t\t%s", fmt.Sprintf(store, "nil"))
		f.p("\t\t\tcontinue")
		f.p("\t\t}")
		f.read(m, 2, "return nil, err")
		f.p("\t\t%s", fmt.Sprintf(store, "&x"))
		return
	}
	f.read(m, 2, "return nil, err")
	f.p("\t\t%s", fmt.Sprintf(store, "x"))
}

// read declares x holding the value of member m read from d; fail is the
// statement that returns a read error.
func (f *file) read(m *MemberIR, depth int, fail string) {
	ind := strings.Repeat("\t", depth)
	mv := "&" + m.Var
	t := m.Target
	failBlock := func() {
		f.p("%sif err != nil {", ind)
		f.p("%s\t%s", ind, fail)
		f.p("%s}", ind)
	}
	switch t.Type {
	case smithy.TypeStructure:
		f.p("%svar x %s", ind, t.GoName)
		f.p("%sif err := x.deserialize(d); err != nil {", ind)
		f.p("%s\t%s", ind, fail)
		f.p("%s}", ind)
	case smithy.TypeUnit:
		f.readUnit(depth, fail)
		f.p("%sx := struct{}{}", ind)
	case smithy.TypeUnion:
		f.p("%sx, err := deserialize%s(d)", ind, t.GoName)
		failBlock()
	case smithy.TypeList, smithy.TypeMap:
		f.p("%sx, err := deserialize%s(d, %s)", ind, t.GoName, mv)
		failBlock()
	case smithy.TypeString, smithy.TypeEnum, smithy.TypeIntEnum, smithy.TypeBlob, smithy.TypeBoolean,
		smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat,
		smithy.TypeDouble, smithy.TypeBigInteger, smithy.TypeBigDecimal, smithy.TypeTimestamp,
		smithy.TypeDocument:
		_, call, conv := scalarCalls(t)
		if conv == "" {
			f.p("%sx, err := d.%s(%s)", ind, call, mv)
			failBlock()
			return
		}
		f.p("%sraw, err := d.%s(%s)", ind, call, mv)
		failBlock()
		f.p("%sx := %s(raw)", ind, conv)
	}
}

// readUnit reads an empty structure value.
func (f *file) readUnit(depth int, fail string) {
	ind := strings.Repeat("\t", depth)
	f.p("%sif err := d.ReadStruct(&schemaSmithyUnit); err != nil {", ind)
	f.p("%s\t%s", ind, fail)
	f.p("%s}", ind)
	f.p("%sif _, more, err := d.ReadStructMember(); err != nil {", ind)
	f.p("%s\t%s", ind, fail)
	f.p("%s} else if more {", ind)
	f.p("%s\treturn %sfmt.Errorf(\"%s: the codec returned a member of an empty structure\")", ind, failPrefix(fail), f.ir.Package)
	f.p("%s}", ind)
	f.use("fmt")
}

func failPrefix(fail string) string {
	if strings.HasPrefix(fail, "return nil,") {
		return "nil, "
	}
	return ""
}

// defaultLit renders the default of a plain-value member as a Go literal of
// its type. set is false for a zero default, which needs no assignment. A
// default the generator cannot render fails rather than being dropped.
func defaultLit(m *MemberIR) (lit string, set bool, err error) {
	text := strings.TrimSpace(string(m.Default))
	t := m.Target
	fail := func() (string, bool, error) {
		return "", false, failf(StageEmit, m.Var, "default %s on a %s member is not rendered by this generator", text, t.Type)
	}
	switch t.Type {
	case smithy.TypeBoolean:
		return text, text == "true", nil
	case smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat,
		smithy.TypeDouble, smithy.TypeIntEnum:
		n, perr := strconv.ParseFloat(text, 64)
		if perr != nil {
			return fail()
		}
		if t.Type == smithy.TypeIntEnum {
			text = t.GoName + "(" + text + ")"
		}
		return text, n != 0, nil
	case smithy.TypeString, smithy.TypeEnum:
		var s string
		if json.Unmarshal(m.Default, &s) != nil {
			return fail()
		}
		lit = goString(s)
		if t.Type == smithy.TypeEnum {
			lit = t.GoName + "(" + lit + ")"
		}
		return lit, s != "", nil
	case smithy.TypeStructure, smithy.TypeUnion, smithy.TypeList, smithy.TypeMap, smithy.TypeBlob,
		smithy.TypeBigInteger, smithy.TypeBigDecimal, smithy.TypeTimestamp, smithy.TypeDocument, smithy.TypeUnit:
		return fail()
	default:
		return fail()
	}
}
