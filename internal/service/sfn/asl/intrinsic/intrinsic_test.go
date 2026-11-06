package intrinsic_test

import (
	"crypto/md5"  //nolint:gosec // States.Hash offers MD5 (stepfunctions-asl.md 5.2); the test checks digests, not security
	"crypto/sha1" //nolint:gosec // States.Hash offers SHA-1 (stepfunctions-asl.md 5.2); the test checks digests, not security
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/intrinsic"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/jsonpath"
)

func decode(tb testing.TB, s string) document.Value {
	tb.Helper()
	d := jsonx.NewDecoder([]byte(s))
	v, err := d.ReadValue()
	if err == nil {
		err = d.End()
	}
	if err != nil {
		tb.Fatalf("decode %q: %v", s, err)
	}
	return v
}

func encode(v document.Value) string {
	e := jsonx.NewEncoder(jsonx.WireOrder)
	e.Value(v)
	b, err := e.Bytes()
	if err != nil {
		return fmt.Sprintf("<unencodable %#v: %v>", v, err)
	}
	return string(b)
}

// equal compares JSON values as a client does: objects as member sets,
// arrays in order, numbers by value.
func equal(a, b document.Value) bool {
	switch a := a.(type) {
	case document.Map:
		b, ok := b.(document.Map)
		if !ok || len(a) != len(b) {
			return false
		}
		for _, ea := range a {
			found := false
			for _, eb := range b {
				if ea.Key == eb.Key {
					found = equal(ea.Value, eb.Value)
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	case document.List:
		b, ok := b.(document.List)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !equal(a[i], b[i]) {
				return false
			}
		}
		return true
	case document.Number:
		b, ok := b.(document.Number)
		if !ok {
			return false
		}
		x, okx := new(big.Float).SetString(string(a))
		y, oky := new(big.Float).SetString(string(b))
		return okx && oky && x.Cmp(y) == 0
	case document.String:
		b, ok := b.(document.String)
		return ok && a == b
	case document.Bool:
		b, ok := b.(document.Bool)
		return ok && a == b
	case document.Null:
		_, ok := b.(document.Null)
		return ok
	}
	return false
}

// source is a fixed intrinsic.Source that records what it was asked.
type source struct {
	uuid   string
	random int64
	asked  [][2]int64
}

func (s *source) UUID() string { return s.uuid }

func (s *source) Random(start, end int64) int64 {
	s.asked = append(s.asked, [2]int64{start, end})
	return s.random
}

// fataler is what eval needs of *testing.T and *rapid.T.
type fataler interface {
	Fatalf(format string, args ...any)
}

// eval parses expr, failing the test when it does not parse, and
// evaluates it.
func eval(f fataler, expr string, input, context document.Value, src intrinsic.Source) (document.Value, error) {
	c, err := intrinsic.Parse(expr)
	if err != nil {
		f.Fatalf("Parse(%s) = %v, want a call", expr, err)
	}
	return c.Eval(input, context, src)
}

// The recorded and documented results of stepfunctions-asl.md 5.2 and 5.3,
// each row cited.
func TestFunctionResults(t *testing.T) {
	ctx := `{"StateMachine":{"Id":"arn:aws:states:us-east-1:000000000000:stateMachine:sm-name-x","Name":"sm-name-x"}}`
	tests := []struct {
		name, expr, input, want, source string
	}{
		{"Format empty string", `States.Format('Formatting argument {}.', $.v)`, `{"v":""}`, `"Formatting argument ."`, "aws-recorded, localstack@8b9a79f v2/intrinsic_functions/test_generic.snapshot.json:L64-L1139"},
		{"Format null", `States.Format('{}', $.v)`, `{"v":null}`, `"null"`, "aws-recorded, same lines"},
		{"Format decimal", `States.Format('{}', $.v)`, `{"v":1.1}`, `"1.1"`, "aws-recorded, same lines"},
		{"Format JSON text string", `States.Format('{}', $.v)`, `{"v":"{\"a\":1}"}`, `"{\"a\":1}"`, "aws-recorded, same lines: a JSON-text string stays as is"},
		{"Format object", `States.Format('{}', $.v)`, `{"v":{"Arg1":1,"Arg2":[]}}`, `"{Arg1=1, Arg2=[]}"`, "aws-recorded, test_generic.snapshot.json:L1067"},
		{"Format context object", `States.Format('{}', $$.StateMachine)`, `{}`, `"{Id=arn:aws:states:us-east-1:000000000000:stateMachine:sm-name-x, Name=sm-name-x}"`, "aws-recorded, test_generic.snapshot.json:L64-L1139"},
		{"Format two arguments", `States.Format('This is a custom error message for {}, caused by {}.', $.Error, $.Cause)`, `{"Error":"E1","Cause":"C1"}`, `"This is a custom error message for E1, caused by C1."`, "aws-recorded, localstack@8b9a79f v2/base/test_base.snapshot.json:L168"},
		{"Format escaped reserved characters", `States.Format('\'\{\}\\')`, `{}`, `"'{}\\"`, "aws-doc dg/intrinsic-functions, Reserved characters"},
		{"StringToJson empty", `States.StringToJson($.v)`, `{"v":""}`, `null`, "aws-recorded, test_json_manipulation.snapshot.json:L64-L681"},
		{"StringToJson blank", `States.StringToJson($.v)`, `{"v":" "}`, `null`, "aws-recorded, same lines"},
		{"StringToJson minus zero", `States.StringToJson($.v)`, `{"v":"-0"}`, `0`, "aws-recorded, same lines"},
		{"StringToJson array", `States.StringToJson($.v)`, `{"v":"[1, 2, \"HelloWorld\"]"}`, `[1,2,"HelloWorld"]`, "aws-recorded, same lines"},
		{"JsonToString object", `States.JsonToString($.v)`, `{"v":{"Arg1": 1, "Arg2": []}}`, `"{\"Arg1\":1,\"Arg2\":[]}"`, "aws-recorded, test_json_manipulation.snapshot.json:L753-L1236"},
		{"JsonToString string", `States.JsonToString($.v)`, `{"v":"HelloWorld"}`, `"\"HelloWorld\""`, "aws-recorded, same lines"},
		{"Array empty", `States.Array()`, `{}`, `[]`, "aws-recorded, test_array.snapshot.json:L394"},
		{"Array mixed", `States.Array('a', 1, null, $.v)`, `{"v":{"k":[true]}}`, `["a",1,null,{"k":[true]}]`, "aws-recorded, test_array.snapshot.json:L478-L1055: mixed types kept"},
		{"ArrayPartition empty", `States.ArrayPartition($.v, 1)`, `{"v":[]}`, `[]`, "aws-recorded, test_array.snapshot.json:L1133-L3281"},
		{"ArrayPartition last shorter", `States.ArrayPartition($.v, 3)`, `{"v":[0,1,2,3]}`, `[[0,1,2],[3]]`, "aws-recorded, same lines"},
		{"ArrayContains nested array", `States.ArrayContains($.a, $.v)`, `{"a":[[1,2,3],2],"v":[1,2,3]}`, `true`, "aws-recorded, test_array.snapshot.json:L3359-L4017"},
		{"ArrayContains object", `States.ArrayContains($.a, $.v)`, `{"a":[{"1":2,"2":[]}],"v":{"1":2,"2":[]}}`, `true`, "aws-recorded, same lines"},
		{"ArrayContains null in empty", `States.ArrayContains($.a, $.v)`, `{"a":[],"v":null}`, `false`, "aws-recorded, same lines"},
		{"ArrayRange step three", `States.ArrayRange(0, 10, 3)`, `{}`, `[0,3,6,9]`, "aws-recorded, test_array.snapshot.json:L4107-L4358"},
		{"ArrayRange inclusive single", `States.ArrayRange(1, 9, 9)`, `{}`, `[1]`, "aws-recorded, same lines"},
		{"ArrayGetItem", `States.ArrayGetItem($.v, 5)`, `{"v":[1,2,3,4,5,6,7,8,9]}`, `6`, "aws-recorded, test_array.snapshot.json:L4456"},
		{"ArrayLength", `States.ArrayLength($.v)`, `{"v":[1,2,3,4,5,6,7,8,9]}`, `9`, "aws-recorded, test_array.snapshot.json:L4548"},
		{"ArrayUnique", `States.ArrayUnique($.v)`, `{"v":[1,2,3,3,3,3,3,3,4]}`, `[1,2,3,4]`, "aws-doc dg/intrinsic-functions"},
		{"Base64Encode", `States.Base64Encode($.v)`, `{"v":"Data to encode"}`, `"RGF0YSB0byBlbmNvZGU="`, "aws-recorded, test_encode_decode.snapshot.json:L64-L270"},
		{"Base64Decode", `States.Base64Decode($.v)`, `{"v":"RGF0YSB0byBlbmNvZGU="}`, `"Data to encode"`, "aws-recorded, same lines"},
		{"Base64Encode empty", `States.Base64Encode($.v)`, `{"v":""}`, `""`, "aws-recorded, same lines"},
		{"Base64Decode empty", `States.Base64Decode($.v)`, `{"v":""}`, `""`, "aws-recorded, same lines"},
		{"ArrayGetItem past the end", `States.ArrayGetItem($.v, 5)`, `{"v":[1]}`, `null`, "aws-recorded, ASL-05 01 (2026-11-03)"},
		{"Base64Decode no Base64 character", `States.Base64Decode($.v)`, `{"v":"%%%"}`, `""`, "aws-recorded, ASL-05 06 (2026-11-03)"},
		{"Hash MD5", `States.Hash($.v, 'MD5')`, `{"v":"input data"}`, `"812f45842bc6d66ee14572ce20db8e86"`, "aws-recorded, test_hash_calculations.snapshot.json:L70-L362"},
		{"Hash SHA-1", `States.Hash($.v, 'SHA-1')`, `{"v":"input data"}`, `"aaff4a450a104cd177d28d18d74485e8cae074b7"`, "aws-recorded, same lines"},
		{"Hash SHA-256", `States.Hash($.v, 'SHA-256')`, `{"v":"input data"}`, `"b4a697a057313163aee33cd8d40c66e9f0f177e00cac2de32475ffff6169c3e3"`, "aws-recorded, same lines"},
		{"JsonMerge shallow", `States.JsonMerge($.a, $.b, false)`, `{"a":{"a":{"a1":1,"a2":2},"b":2,"d":3},"b":{"a":{"a3":1,"a4":2},"c":3,"d":4}}`, `{"a":{"a3":1,"a4":2},"b":2,"c":3,"d":4}`, "aws-recorded, test_json_manipulation.snapshot.json:L1358"},
		{"StringSplit keeps blanks", `States.StringSplit($.v, ',')`, `{"v":", , ,"}`, `[" "," "]`, "aws-recorded, test_string_operations.snapshot.json:L74-L556"},
		{"StringSplit all delimiters", `States.StringSplit($.v, ',')`, `{"v":",,,,"}`, `[]`, "aws-recorded, same lines"},
		{"StringSplit each delimiter character", `States.StringSplit($.v, '.+,=')`, `{"v":"This.is+a,test=string"}`, `["This","is","a","test","string"]`, "aws-recorded, same lines"},
		{"nested calls", `States.ArrayGetItem(States.StringSplit($$.StateMachine.Name, '-'), States.MathAdd(States.ArrayLength(States.StringSplit($$.StateMachine.Name, '-')), -1))`, `{}`, `"x"`, "aws-recorded, test_generic.snapshot.json:L1283"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := eval(t, tt.expr, decode(t, tt.input), decode(t, ctx), &source{})
			if err != nil {
				t.Fatalf("%s on %s = %v, want %s (%s)", tt.expr, tt.input, err, tt.want, tt.source)
			}
			if want := decode(t, tt.want); !equal(got, want) {
				t.Fatalf("%s on %s = %s, want %s (%s)", tt.expr, tt.input, encode(got), tt.want, tt.source)
			}
		})
	}
}

// MathAdd rounds each argument half up, as Java Math.round does
// (aws-recorded, localstack@8b9a79f
// v2/intrinsic_functions/test_math_operations.snapshot.json:L374-L1834;
// stepfunctions-asl.md 5.3).
func TestMathAddRoundsHalfUp(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.49", "1.5", 3},
		{"1.5", "1.51", 4},
		{"-1.49", "-1.5", -2},
		{"0.5", "0", 1},
		{"-0.5", "0", 0},
		{"-1.5", "0", -1},
		{"-2.5", "0", -2},
		{"2.5", "0", 3},
	}
	for _, tt := range tests {
		expr := fmt.Sprintf("States.MathAdd(%s, %s)", tt.a, tt.b)
		got, err := eval(t, expr, document.Map{}, nil, &source{})
		if err != nil || !equal(got, document.Number(strconv.Itoa(tt.want))) {
			t.Errorf("%s = %v, %v; want %d", expr, got, err, tt.want)
		}
	}
}

// halfUp is Java Math.round on hundredths: floor(x + 0.5).
func halfUp(hundredths int) int {
	n := hundredths + 50
	q := n / 100
	if n%100 != 0 && n < 0 {
		q--
	}
	return q
}

// Spec T7 rounding: MathAdd is floor(a + 0.5) + floor(b + 0.5) for every
// pair of decimals (stepfunctions-asl.md 5.3, recorded rule).
func TestMathAddProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := rapid.IntRange(-100000, 100000).Draw(t, "a")
		b := rapid.IntRange(-100000, 100000).Draw(t, "b")
		lit := func(h int) string {
			sign := ""
			if h < 0 {
				sign, h = "-", -h
			}
			return fmt.Sprintf("%s%d.%02d", sign, h/100, h%100)
		}
		expr := fmt.Sprintf("States.MathAdd(%s, %s)", lit(a), lit(b))
		got, err := eval(t, expr, document.Map{}, nil, &source{})
		want := document.Number(strconv.Itoa(halfUp(a) + halfUp(b)))
		if err != nil || !equal(got, want) {
			t.Fatalf("%s = %v, %v; want %s", expr, got, err, want)
		}
	})
}

// States.UUID and an unseeded States.MathRandom draw from the Source;
// a seeded MathRandom gives the same number for the same seed, inside
// [start, end) (aws-doc dg/intrinsic-functions; contract section 4).
func TestUUIDAndMathRandom(t *testing.T) {
	src := &source{uuid: "4f6b2bba-7b9f-4b3c-8a51-6d3e5e0b9a10", random: 7}
	got, err := eval(t, "States.UUID()", document.Map{}, nil, src)
	if err != nil || !equal(got, document.String(src.uuid)) {
		t.Errorf("States.UUID() = %v, %v; want the source's UUID", got, err)
	}
	got, err = eval(t, "States.MathRandom(1, 10)", document.Map{}, nil, src)
	if err != nil || !equal(got, document.Number("7")) || len(src.asked) != 1 || src.asked[0] != [2]int64{1, 10} {
		t.Errorf("States.MathRandom(1, 10) = %v, %v, asked %v; want the source's draw for [1, 10)", got, err, src.asked)
	}
	first, err1 := eval(t, "States.MathRandom(1, 1000, 42)", document.Map{}, nil, &source{})
	second, err2 := eval(t, "States.MathRandom(1, 1000, 42)", document.Map{}, nil, &source{})
	if err1 != nil || err2 != nil || !equal(first, second) {
		t.Fatalf("seeded MathRandom = %v, %v and %v, %v; want the same number twice", first, err1, second, err2)
	}
	n, ok := first.(document.Number)
	v, perr := n.Int64()
	if !ok || perr != nil || v < 1 || v >= 1000 {
		t.Errorf("seeded MathRandom(1, 1000, 42) = %v, want an integer in [1, 1000)", first)
	}
}

// JsonMerge with deep true is unsupported in AWS (aws-doc
// dg/intrinsic-functions); a function failing at run time is a
// *FunctionError, which the engine names by switch sfn.intrinsic.failure-error
// (contract section 4; the details are TestASL05FunctionErrorDetails').
func TestRuntimeFailuresAreFunctionErrors(t *testing.T) {
	tests := []struct{ name, expr, input string }{
		{"JsonMerge deep", `States.JsonMerge($.a, $.b, true)`, `{"a":{},"b":{}}`},
		{"ArrayGetItem negative index", `States.ArrayGetItem($.v, -1)`, `{"v":[1]}`},
		{"ArrayPartition zero chunk", `States.ArrayPartition($.v, 0)`, `{"v":[1]}`},
		{"ArrayRange over 1000 items", `States.ArrayRange(0, 2000, 1)`, `{}`},
		{"ArrayRange step zero", `States.ArrayRange(0, 10, 0)`, `{}`},
		{"Base64Decode bad input", `States.Base64Decode($.v)`, `{"v":"not base64!"}`},
		{"Hash unknown algorithm", `States.Hash($.v, 'SHA-3')`, `{"v":"x"}`},
		{"StringToJson invalid", `States.StringToJson($.v)`, `{"v":"{nope"}`},
		{"MathAdd overflow", `States.MathAdd(2147483647, 1)`, `{}`},
		{"Format too few arguments", `States.Format('{} {}', $.v)`, `{"v":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := eval(t, tt.expr, decode(t, tt.input), nil, &source{})
			var fe *intrinsic.FunctionError
			if !errors.As(err, &fe) || !strings.HasPrefix(tt.expr, fe.Function+"(") {
				t.Fatalf("%s = %v, %v; want a *FunctionError naming the function", tt.expr, got, err)
			}
		})
	}
}

// A path argument that selects nothing fails with that path's
// *jsonpath.MissingError (contract section 4).
func TestMissingPathArgument(t *testing.T) {
	_, err := eval(t, `States.Format('{}', $.nope)`, decode(t, `{"a":1}`), nil, &source{})
	var miss *jsonpath.MissingError
	if !errors.As(err, &miss) || miss.Path != "$.nope" {
		t.Fatalf("Format of a missing path = %v, want the path's *jsonpath.MissingError", err)
	}
}

func nested(depth int) string {
	return strings.Repeat("States.Array(", depth) + "1" + strings.Repeat(")", depth)
}

// Calls nest at most ten levels (aws-doc dg/intrinsic-functions;
// stepfunctions-asl.md 3.7). Unknown names, wrong arity and an unpaired
// backslash do not parse (stepfunctions-asl.md 5.1, aws-doc "Reserved
// characters").
func TestParseLimitsAndSyntax(t *testing.T) {
	if _, err := intrinsic.Parse(nested(intrinsic.MaxDepth)); err != nil {
		t.Errorf("Parse of %d nested calls = %v, want it accepted", intrinsic.MaxDepth, err)
	}
	for _, s := range []string{
		nested(intrinsic.MaxDepth + 1),
		"States.NoSuchFunction()",
		"States.ArrayLength()",
		"States.UUID(1)",
		`States.Format('a\b')`,
		`States.Format('unterminated)`,
		"Format('x')",
		"States.Array(1,",
	} {
		_, err := intrinsic.Parse(s)
		var syn *intrinsic.SyntaxError
		if !errors.As(err, &syn) || syn.Expr != s {
			t.Errorf("Parse(%q) = %v, want a *SyntaxError naming the expression", s, err)
		}
	}
}

// escape writes s as an intrinsic string literal body: ', {, } and \ take
// a backslash (stepfunctions-asl.md 5.1; aws-doc "Reserved characters").
func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`, `{`, `\{`, `}`, `\}`).Replace(s)
}

// Spec T7: escaped string literals round-trip.
func TestEscapeRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.StringOfN(rapid.SampledFrom([]rune(`ab '{}\,.$()`)), 0, 20, -1).Draw(t, "s")
		expr := "States.Array('" + escape(s) + "')"
		got, err := eval(t, expr, document.Map{}, nil, &source{})
		if err != nil || !equal(got, document.List{document.String(s)}) {
			t.Fatalf("%s = %v, %v; want [%q]", expr, got, err, s)
		}
	})
}

// Spec T7: Hash agrees with crypto/* in lower-case hex for the five
// documented algorithms (stepfunctions-asl.md 5.2).
func TestHashMatchesCrypto(t *testing.T) {
	algs := map[string]func() hash.Hash{
		"MD5": md5.New, "SHA-1": sha1.New, "SHA-256": sha256.New, "SHA-384": sha512.New384, "SHA-512": sha512.New,
	}
	names := []string{"MD5", "SHA-1", "SHA-256", "SHA-384", "SHA-512"}
	rapid.Check(t, func(t *rapid.T) {
		alg := rapid.SampledFrom(names).Draw(t, "alg")
		data := rapid.StringOfN(rapid.RuneFrom(nil, printableASCII), 0, 200, -1).Draw(t, "data")
		h := algs[alg]()
		h.Write([]byte(data))
		want := hex.EncodeToString(h.Sum(nil))
		got, err := eval(t, "States.Hash($.d, '"+alg+"')", document.Map{{Key: "d", Value: document.String(data)}}, nil, &source{})
		if err != nil || !equal(got, document.String(want)) {
			t.Fatalf("Hash(%q, %s) = %v, %v; want %s", data, alg, got, err, want)
		}
	})
}

// printableASCII is U+0020 to U+007E: the recorded Hash input is ASCII,
// so the property does not choose a byte encoding for other characters.
var printableASCII = &unicode.RangeTable{R16: []unicode.Range16{{Lo: 0x20, Hi: 0x7e, Stride: 1}}}

// splitRule is the recorded StringSplit rule: every character of delims
// is a delimiter and empty pieces are dropped (stepfunctions-asl.md 5.2).
func splitRule(s, delims string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(delims, r) })
}

// Spec T7: StringSplit against the rule.
func TestStringSplitRule(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		alphabet := rapid.SampledFrom([]rune("ab ,.+=-"))
		s := rapid.StringOfN(alphabet, 0, 30, -1).Draw(t, "s")
		delims := rapid.StringOfN(alphabet, 1, 3, -1).Draw(t, "delims")
		in := document.Map{{Key: "s", Value: document.String(s)}, {Key: "d", Value: document.String(delims)}}
		got, err := eval(t, "States.StringSplit($.s, $.d)", in, nil, &source{})
		pieces := splitRule(s, delims)
		want := make(document.List, 0, len(pieces))
		for _, p := range pieces {
			want = append(want, document.String(p))
		}
		if err != nil || !equal(got, want) {
			t.Fatalf("StringSplit(%q, %q) = %v, %v; want %s", s, delims, got, err, encode(want))
		}
	})
}
