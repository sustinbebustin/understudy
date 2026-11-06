package protocoltest_test

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/jsonrpc10"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/restjson"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/rpcv2protocol"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// setField assigns v to f, through a fresh pointer when f is one: a
// defaulted member is a plain value or a pointer depending on its default
// (smithy-protocol-tests.md 14.2), and the tests hold for either.
func setField(f reflect.Value, v any) {
	target := f
	if f.Kind() == reflect.Pointer {
		p := reflect.New(f.Type().Elem())
		f.Set(p)
		target = p.Elem()
	}
	rv := reflect.ValueOf(v)
	if rv.Type().AssignableTo(target.Type()) {
		target.Set(rv)
		return
	}
	target.Set(rv.Convert(target.Type()))
}

// defaultsMember is one member of OperationWithDefaultsOutput, or of the
// Defaults structure, which models the same members and defaults: its Go
// field, its name on the wire, its modeled default as a Go value and as the
// wire value case 7 expects, and a drawer of explicit values.
type defaultsMember struct {
	field, name string
	// hasDefault is false for defaultNullDocument, whose @default(null)
	// means no default (smithy-protocol-tests.md 10.1).
	hasDefault bool
	def        any
	defWire    wire
	// document members are absent from the rpcv2Cbor shape
	// (smithy-protocol-tests.md 10.1).
	document bool
	draw     func(t *rapid.T, label string) (any, wire)
}

func drawText(t *rapid.T, label string) string {
	return rapid.StringMatching(`[A-Za-z0-9 ]{0,8}`).Draw(t, label)
}

func drawString(t *rapid.T, label string) (any, wire) {
	s := drawText(t, label)
	return s, wString(s)
}

func drawBool(t *rapid.T, label string) (any, wire) {
	b := rapid.Bool().Draw(t, label)
	return b, wBool(b)
}

func drawStringList(t *rapid.T, label string) (any, wire) {
	n := rapid.IntRange(0, 3).Draw(t, label+".len")
	l := make([]string, 0, n)
	w := make(wList, 0, n)
	for i := range n {
		s := drawText(t, label+"["+strconv.Itoa(i)+"]")
		l = append(l, s)
		w = append(w, wString(s))
	}
	return l, w
}

// drawKeys draws up to n distinct map keys.
func drawKeys(t *rapid.T, n int, label string) []string {
	return rapid.SliceOfNDistinct(rapid.StringMatching(`[a-z]{1,6}`), 0, n, func(s string) string { return s }).Draw(t, label)
}

func drawStringMap(t *rapid.T, label string) (any, wire) {
	keys := drawKeys(t, 3, label+".keys")
	m := make(map[string]string, len(keys))
	w := make(wObject, 0, len(keys))
	for _, k := range keys {
		v := drawText(t, label+"."+k)
		m[k] = v
		w = append(w, wEntry{k, wString(v)})
	}
	return m, w
}

// drawDocument draws a document that is not null: a null top-level
// document cannot be told from an absent member on the wire.
func drawDocument(t *rapid.T, label string) (any, wire) {
	var d document.Value
	switch rapid.IntRange(0, 4).Draw(t, label) {
	case 0:
		d = document.String(drawText(t, label+".string"))
	case 1:
		d = document.Bool(rapid.Bool().Draw(t, label+".bool"))
	case 2:
		d = document.Number(rapid.StringMatching(`-?(0|[1-9][0-9]{0,6})`).Draw(t, label+".number"))
	case 3:
		n := rapid.IntRange(0, 2).Draw(t, label+".len")
		l := make(document.List, 0, n)
		for i := range n {
			l = append(l, document.String(drawText(t, label+"["+strconv.Itoa(i)+"]")))
		}
		d = l
	default:
		keys := drawKeys(t, 2, label+".keys")
		m := make(document.Map, 0, len(keys))
		for _, k := range keys {
			m = append(m, document.Entry{Key: k, Value: document.String(drawText(t, label+"."+k))})
		}
		d = m
	}
	return d, wDoc{d}
}

func drawTimestamp(t *rapid.T, label string) (any, wire) {
	s := rapid.Int64Range(0, 4102444800).Draw(t, label)
	return time.Unix(s, 0).UTC(), wTime(s)
}

func drawBlob(t *rapid.T, label string) (any, wire) {
	b := append([]byte{}, rapid.SliceOfN(rapid.Byte(), 0, 6).Draw(t, label)...)
	return b, wBlob(b)
}

func drawInt8(t *rapid.T, label string) (any, wire) {
	v := rapid.Int8().Draw(t, label)
	return v, wInt(v)
}

func drawInt16(t *rapid.T, label string) (any, wire) {
	v := rapid.Int16().Draw(t, label)
	return v, wInt(v)
}

func drawInt32(t *rapid.T, label string) (any, wire) {
	v := rapid.Int32().Draw(t, label)
	return v, wInt(v)
}

func drawInt64(t *rapid.T, label string) (any, wire) {
	v := rapid.Int64Range(-1<<53, 1<<53).Draw(t, label)
	return v, wInt(v)
}

// drawFloat32 draws quarters, which float32 and the JSON text hold exactly.
func drawFloat32(t *rapid.T, label string) (any, wire) {
	v := float32(rapid.IntRange(-4000, 4000).Draw(t, label)) / 4
	return v, wFloat(v)
}

func drawFloat64(t *rapid.T, label string) (any, wire) {
	v := float64(rapid.IntRange(-4000, 4000).Draw(t, label)) / 4
	return v, wFloat(v)
}

// drawEnum draws a TestEnum value (third_party/smithy-protocol-tests/model.json,
// aws.protocoltests.json10#TestEnum: FOO, BAR, BAZ).
func drawEnum(t *rapid.T, label string) (any, wire) {
	v := rapid.SampledFrom([]string{"FOO", "BAR", "BAZ"}).Draw(t, label)
	return v, wString(v)
}

// drawIntEnum draws a TestIntEnum value (ONE = 1, TWO = 2).
func drawIntEnum(t *rapid.T, label string) (any, wire) {
	v := rapid.SampledFrom([]int32{1, 2}).Draw(t, label)
	return v, wInt(v)
}

// defaultsMembers are the members of OperationWithDefaultsOutput with the
// defaults case 7's body carries (smithy-protocol-tests.md 10.1; the model
// at third_party/smithy-protocol-tests/model.json L6716-L6889 and L8316).
func defaultsMembers() []defaultsMember {
	return []defaultsMember{
		{field: "DefaultString", name: "defaultString", hasDefault: true, def: "hi", defWire: wString("hi"), draw: drawString},
		{field: "DefaultBoolean", name: "defaultBoolean", hasDefault: true, def: true, defWire: wBool(true), draw: drawBool},
		{field: "DefaultList", name: "defaultList", hasDefault: true, def: []string{}, defWire: wList{}, draw: drawStringList},
		{field: "DefaultDocumentMap", name: "defaultDocumentMap", hasDefault: true, def: document.Map{}, defWire: wDoc{document.Map{}}, document: true, draw: drawDocument},
		{field: "DefaultDocumentString", name: "defaultDocumentString", hasDefault: true, def: document.String("hi"), defWire: wDoc{document.String("hi")}, document: true, draw: drawDocument},
		{field: "DefaultDocumentBoolean", name: "defaultDocumentBoolean", hasDefault: true, def: document.Bool(true), defWire: wDoc{document.Bool(true)}, document: true, draw: drawDocument},
		{field: "DefaultDocumentList", name: "defaultDocumentList", hasDefault: true, def: document.List{}, defWire: wDoc{document.List{}}, document: true, draw: drawDocument},
		{field: "DefaultNullDocument", name: "defaultNullDocument", document: true, draw: drawDocument},
		// @default(0) is epoch second 0 (smithy-protocol-tests.md 10.2).
		{field: "DefaultTimestamp", name: "defaultTimestamp", hasDefault: true, def: time.Unix(0, 0).UTC(), defWire: wTime(0), draw: drawTimestamp},
		// "YWJj" is the base64 of the bytes abc (smithy-protocol-tests.md 10.2).
		{field: "DefaultBlob", name: "defaultBlob", hasDefault: true, def: []byte("abc"), defWire: wBlob("abc"), draw: drawBlob},
		{field: "DefaultByte", name: "defaultByte", hasDefault: true, def: int8(1), defWire: wInt(1), draw: drawInt8},
		{field: "DefaultShort", name: "defaultShort", hasDefault: true, def: int16(1), defWire: wInt(1), draw: drawInt16},
		{field: "DefaultInteger", name: "defaultInteger", hasDefault: true, def: int32(10), defWire: wInt(10), draw: drawInt32},
		{field: "DefaultLong", name: "defaultLong", hasDefault: true, def: int64(100), defWire: wInt(100), draw: drawInt64},
		{field: "DefaultFloat", name: "defaultFloat", hasDefault: true, def: float32(1), defWire: wFloat(1), draw: drawFloat32},
		{field: "DefaultDouble", name: "defaultDouble", hasDefault: true, def: float64(1), defWire: wFloat(1), draw: drawFloat64},
		{field: "DefaultMap", name: "defaultMap", hasDefault: true, def: map[string]string{}, defWire: wObject{}, draw: drawStringMap},
		{field: "DefaultEnum", name: "defaultEnum", hasDefault: true, def: "FOO", defWire: wString("FOO"), draw: drawEnum},
		{field: "DefaultIntEnum", name: "defaultIntEnum", hasDefault: true, def: int32(1), defWire: wInt(1), draw: drawIntEnum},
		{field: "EmptyString", name: "emptyString", hasDefault: true, def: "", defWire: wString(""), draw: drawString},
		{field: "FalseBoolean", name: "falseBoolean", hasDefault: true, def: false, defWire: wBool(false), draw: drawBool},
		{field: "EmptyBlob", name: "emptyBlob", hasDefault: true, def: []byte{}, defWire: wBlob{}, draw: drawBlob},
		{field: "ZeroByte", name: "zeroByte", hasDefault: true, def: int8(0), defWire: wInt(0), draw: drawInt8},
		{field: "ZeroShort", name: "zeroShort", hasDefault: true, def: int16(0), defWire: wInt(0), draw: drawInt16},
		{field: "ZeroInteger", name: "zeroInteger", hasDefault: true, def: int32(0), defWire: wInt(0), draw: drawInt32},
		{field: "ZeroLong", name: "zeroLong", hasDefault: true, def: int64(0), defWire: wInt(0), draw: drawInt64},
		{field: "ZeroFloat", name: "zeroFloat", hasDefault: true, def: float32(0), defWire: wFloat(0), draw: drawFloat32},
		{field: "ZeroDouble", name: "zeroDouble", hasDefault: true, def: float64(0), defWire: wFloat(0), draw: drawFloat64},
	}
}

// defaultsProtocol is OperationWithDefaults in one protocol's corpus
// service.
type defaultsProtocol struct {
	wireProtocol
	op     *registry.Operation
	output reflect.Type
}

func defaultsProtocols() []defaultsProtocol {
	return []defaultsProtocol{
		{awsJSON10(), jsonrpc10.OpOperationWithDefaults.Descriptor(), reflect.TypeFor[jsonrpc10.OperationWithDefaultsOutput]()},
		{restJSON1(), restjson.OpOperationWithDefaults.Descriptor(), reflect.TypeFor[restjson.OperationWithDefaultsOutput]()},
		{rpcv2CBOR(), rpcv2protocol.OpOperationWithDefaults.Descriptor(), reflect.TypeFor[rpcv2protocol.OperationWithDefaultsOutput]()},
	}
}

// newValue returns a new *T of a generated type as both serde interfaces.
func newValue(t fataler, typ reflect.Type) (reflect.Value, smithy.Serializable, smithy.Deserializable) {
	t.Helper()
	v := reflect.New(typ)
	s, ok := reflect.TypeAssert[smithy.Serializable](v)
	if !ok {
		t.Fatalf("%s is not serializable", typ)
	}
	d, ok := reflect.TypeAssert[smithy.Deserializable](v)
	if !ok {
		t.Fatalf("%s is not deserializable", typ)
	}
	return v, s, d
}

// A server writes every defaulted member the handler left unset as its
// modeled default, and the value it set otherwise, whichever members those
// are (smithy-protocol-tests.md 10.1, 10.2, 14.3); explicit values win,
// including an explicit zero for a member whose default is not zero
// (10.4). Decoding the body gives the value back with the unset members
// holding their defaults (14.4).
func TestUnsetDefaultsSerializeAsModeledAndRoundTrip(t *testing.T) {
	for _, p := range defaultsProtocols() {
		t.Run(p.name, func(t *testing.T) {
			rapid.Check(t, func(t *rapid.T) {
				v, out, _ := newValue(t, p.output)
				want, _, _ := newValue(t, p.output)
				var body wObject
				for _, m := range defaultsMembers() {
					if m.document && p.cbor {
						continue
					}
					if rapid.Bool().Draw(t, m.name+".set") {
						gv, w := m.draw(t, m.name)
						setField(v.Elem().FieldByName(m.field), gv)
						setField(want.Elem().FieldByName(m.field), gv)
						body = append(body, wEntry{m.name, w})
						continue
					}
					if m.hasDefault {
						setField(want.Elem().FieldByName(m.field), m.def)
						body = append(body, wEntry{m.name, m.defWire})
					}
				}
				got := p.encode(t, p.op, out)
				p.compare(t, p.body(t, body), got)

				back, _, in := newValue(t, p.output)
				p.decode(t, p.op, got, in)
				if err := diffInput(want.Interface(), back.Interface()); err != nil {
					t.Fatalf("decoding %s: %v", got, err)
				}
			})
		})
	}
}

// nestedProtocol is OperationWithNestedStructure in one protocol's corpus
// service.
type nestedProtocol struct {
	wireProtocol
	op     *registry.Operation
	output reflect.Type
}

func nestedProtocols() []nestedProtocol {
	return []nestedProtocol{
		{awsJSON10(), jsonrpc10.OpOperationWithNestedStructure.Descriptor(), reflect.TypeFor[jsonrpc10.OperationWithNestedStructureOutput]()},
		{restJSON1(), restjson.OpOperationWithNestedStructure.Descriptor(), reflect.TypeFor[restjson.OperationWithNestedStructureOutput]()},
	}
}

// drawDialog draws a Dialog as the handler builds it and as the server must
// write and decode it: greeting defaults to "hi" and a present farewell's
// phrase to "bye", while an absent farewell stays absent
// (smithy-protocol-tests.md 10.1; model.json L6889 and L7046).
func drawDialog(t *rapid.T, typ reflect.Type, label string) (set, want reflect.Value, w wObject) {
	set, want = reflect.New(typ).Elem(), reflect.New(typ).Elem()
	if rapid.Bool().Draw(t, label+".language.set") {
		s := drawText(t, label+".language")
		setField(set.FieldByName("Language"), s)
		setField(want.FieldByName("Language"), s)
		w = append(w, wEntry{"language", wString(s)})
	}
	greeting := "hi"
	if rapid.Bool().Draw(t, label+".greeting.set") {
		greeting = drawText(t, label+".greeting")
		setField(set.FieldByName("Greeting"), greeting)
	}
	setField(want.FieldByName("Greeting"), greeting)
	w = append(w, wEntry{"greeting", wString(greeting)})
	if rapid.Bool().Draw(t, label+".farewell.present") {
		ft := set.FieldByName("Farewell").Type().Elem()
		fs, fw := reflect.New(ft), reflect.New(ft)
		phrase := "bye"
		if rapid.Bool().Draw(t, label+".farewell.phrase.set") {
			phrase = drawText(t, label+".farewell.phrase")
			setField(fs.Elem().FieldByName("Phrase"), phrase)
		}
		setField(fw.Elem().FieldByName("Phrase"), phrase)
		set.FieldByName("Farewell").Set(fs)
		want.FieldByName("Farewell").Set(fw)
		w = append(w, wEntry{"farewell", wObject{{"phrase", wString(phrase)}}})
	}
	return set, want, w
}

// Defaults apply at every depth: in the required dialog, in list elements,
// in map values and inside a present farewell, and an unset dialogList or
// dialogMap is written as its default [] or {} (smithy-protocol-tests.md
// 10.1, 14.3).
func TestNestedDefaultsApplyAtEveryDepth(t *testing.T) {
	for _, p := range nestedProtocols() {
		t.Run(p.name, func(t *testing.T) {
			rapid.Check(t, func(t *rapid.T) {
				v, out, _ := newValue(t, p.output)
				want, _, _ := newValue(t, p.output)
				dialogType := v.Elem().FieldByName("Dialog").Type()

				d, dw, w := drawDialog(t, dialogType, "dialog")
				v.Elem().FieldByName("Dialog").Set(d)
				want.Elem().FieldByName("Dialog").Set(dw)

				list := wList{}
				wantList := reflect.MakeSlice(reflect.SliceOf(dialogType), 0, 3)
				if rapid.Bool().Draw(t, "dialogList.set") {
					setList := reflect.MakeSlice(reflect.SliceOf(dialogType), 0, 3)
					for i := range rapid.IntRange(0, 3).Draw(t, "dialogList.len") {
						d, dw, w := drawDialog(t, dialogType, "dialogList["+strconv.Itoa(i)+"]")
						setList = reflect.Append(setList, d)
						wantList = reflect.Append(wantList, dw)
						list = append(list, w)
					}
					v.Elem().FieldByName("DialogList").Set(setList)
				}
				want.Elem().FieldByName("DialogList").Set(wantList)

				entries := wObject{}
				wantMap := reflect.MakeMap(reflect.MapOf(reflect.TypeFor[string](), dialogType))
				if rapid.Bool().Draw(t, "dialogMap.set") {
					setMap := reflect.MakeMap(reflect.MapOf(reflect.TypeFor[string](), dialogType))
					for _, k := range drawKeys(t, 3, "dialogMap.keys") {
						d, dw, w := drawDialog(t, dialogType, "dialogMap."+k)
						setMap.SetMapIndex(reflect.ValueOf(k), d)
						wantMap.SetMapIndex(reflect.ValueOf(k), dw)
						entries = append(entries, wEntry{k, w})
					}
					v.Elem().FieldByName("DialogMap").Set(setMap)
				}
				want.Elem().FieldByName("DialogMap").Set(wantMap)
				body := wObject{{"dialog", w}, {"dialogList", list}, {"dialogMap", entries}}

				got := p.encode(t, p.op, out)
				p.compare(t, p.body(t, body), got)

				back, _, in := newValue(t, p.output)
				p.decode(t, p.op, got, in)
				if err := diffInput(want.Interface(), back.Interface()); err != nil {
					t.Fatalf("decoding %s: %v", got, err)
				}
			})
		})
	}
}

// The awsJson1_0 server request cases for defaults carry no X-Amz-Target,
// so the bench cannot route them and skips them as corpus bugs
// (internal/protocoltest/skips.json; smithy-protocol-tests.md 10.6). Their
// bodies and params still pin what the decoder fills in: decoded through
// the codec, each body gives the case's params, every omitted defaulted
// member holding its default at every depth (10.2, 14.4).
func TestAwsJson10DefaultedRequestBodiesDecodeToTheCaseParams(t *testing.T) {
	cases := map[string]gen.RequestCase{}
	for _, c := range jsonrpc10.Suite().Requests {
		cases[c.ID] = c
	}
	p := awsJSON10()
	for _, id := range []string{
		"AwsJson10ServerPopulatesDefaultsWhenMissingInRequestBody",
		"AwsJson10ServerPopulatesNestedDefaultsWhenMissingInRequestBody",
	} {
		c, ok := cases[id]
		if !ok {
			t.Fatalf("the corpus has no request case %s", id)
		}
		_, _, in := newValue(t, reflect.TypeOf(c.Params).Elem())
		p.decode(t, c.Operation, []byte(c.Body), in)
		if err := diffInput(c.Params, in); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
}
