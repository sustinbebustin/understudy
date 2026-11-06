package intrinsic_test

import (
	"errors"
	"slices"
	"strconv"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/intrinsic"
)

// TestASL05FunctionErrorDetails: the detail a failing function gives, AWS's
// recorded text where ASL-05 recorded one (2026-11-03,
// stepfunctions-asl.md 5.4) and the generic "Invalid arguments in
// <function>" inferred for the rest.
func TestASL05FunctionErrorDetails(t *testing.T) {
	tests := []struct{ expr, input, detail string }{
		// Recorded.
		{`States.JsonMerge($.a, $.b, true)`, `{"a":{},"b":{}}`, "Deep merge not supported in States.JsonMerge"},
		{`States.ArrayGetItem($.v, -1)`, `{"v":[1]}`, "Invalid arguments in States.ArrayGetItem"},
		{`States.ArrayPartition($.v, 0)`, `{"v":[1]}`, "Invalid arguments in States.ArrayPartition"},
		{`States.ArrayRange(1, 1001, 1)`, `{}`, "Invalid arguments in States.ArrayRange"},
		{`States.ArrayRange(1, 5, 0)`, `{}`, "Invalid arguments in States.ArrayRange"},
		{`States.Hash($.v, 'SHA-3')`, `{"v":"x"}`, "Invalid arguments in States.Hash"},
		{`States.MathAdd(2147483647, 1)`, `{}`, "Invalid arguments in States.MathAdd"},
		{`States.Format('{} {}', $.v)`, `{"v":"x"}`, "Invalid arguments in States.Format: number of arguments do not match the occurrences of {}."},
		// Inferred.
		{`States.ArrayGetItem($.v, 0.5)`, `{"v":[1]}`, "Invalid arguments in States.ArrayGetItem"},
		{`States.Base64Decode($.v)`, `{"v":"not base64!"}`, "Invalid arguments in States.Base64Decode"},
		{`States.MathAdd(2147483648, -1)`, `{}`, "Invalid arguments in States.MathAdd"},
		{`States.Format('{}', $.v, $.v)`, `{"v":"x"}`, "Invalid arguments in States.Format: number of arguments do not match the occurrences of {}."},
		{`States.Format($.v)`, `{"v":1}`, "Invalid arguments in States.Format"},
		{`States.ArrayLength($.v)`, `{"v":{}}`, "Invalid arguments in States.ArrayLength"},
		{`States.StringSplit($.v, ',')`, `{"v":null}`, "Invalid arguments in States.StringSplit"},
		{`States.MathRandom(5, 5)`, `{}`, "Invalid arguments in States.MathRandom"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			_, err := eval(t, tt.expr, decode(t, tt.input), nil, &source{})
			var fe *intrinsic.FunctionError
			if !errors.As(err, &fe) || fe.Detail != tt.detail {
				t.Fatalf("%s = %v, want a *FunctionError with detail %q", tt.expr, err, tt.detail)
			}
		})
	}
}

// The parsed call keeps its text as written, which a failure's cause
// quotes (ASL-05, recorded 2026-11-03).
func TestCallTextAsWritten(t *testing.T) {
	const expr = `States.Format('{} {}',   1)`
	c, err := intrinsic.Parse(expr)
	if err != nil || c.Text() != expr {
		t.Fatalf("Parse(%q).Text() = %q, %v; want the text as written", expr, c.Text(), err)
	}
}

// TestASL06EqualityAndRounding: ArrayUnique keeps 1 and 1.0 apart and
// ArrayContains does not (ASL-06, recorded 2026-11-03); how both compare
// objects with members in another order is unrecorded, as is rounding of
// non-integer bounds outside MathAdd (stepfunctions-asl.md 5.2, 5.3).
// understudy compares objects as member sets, and rounds half up
// everywhere MathAdd does.
func TestASL06EqualityAndRounding(t *testing.T) {
	// inferred, ASL-06
	tests := []struct{ expr, input, want string }{
		{`States.ArrayUnique($.v)`, `{"v":[1,1.0,{"a":1,"b":2},{"b":2,"a":1},[1],[1.0]]}`, `[1,1.0,{"a":1,"b":2},[1],[1.0]]`},
		{`States.ArrayContains($.v, 2)`, `{"v":[2.00]}`, `true`},
		{`States.ArrayPartition($.v, 1.5)`, `{"v":[1,2,3]}`, `[[1,2],[3]]`},
		{`States.ArrayRange(0.5, 2.5, 1)`, `{}`, `[1,2,3]`},
		{`States.ArrayRange(-0.5, 1, 1)`, `{}`, `[0,1]`},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			got, err := eval(t, tt.expr, decode(t, tt.input), nil, &source{})
			if err != nil || encode(got) != tt.want {
				t.Fatalf("%s = %s, %v; want %s", tt.expr, encode(got), err, tt.want)
			}
		})
	}
	src := &source{random: 3}
	if _, err := eval(t, "States.MathRandom(1.5, 9.5)", document.Map{}, nil, src); err != nil || len(src.asked) != 1 || src.asked[0] != [2]int64{2, 10} {
		t.Fatalf("MathRandom(1.5, 9.5) asked %v, %v; want [2, 10)", src.asked, err)
	}
}

// ASL-06's recorded results (2026-11-03): ArrayUnique keeps 1 and 1.0
// apart while ArrayContains finds 1 in [1.0], and Format and JsonToString
// render numbers as Java does. AWS's ArrayUnique order is a hash set's, so
// only its members are compared.
func TestASL06RecordedResults(t *testing.T) {
	input := decode(t, `{"tpl":"{} {} {} {} {}","u":[1,1.0,"1",[1],[1],{"a":1},{"a":1},null,null,true,true],"c":[1.0,"1"],"f":[1.0,1e21,-0.0,0.1,100000000000000000000]}`)
	got, err := eval(t, `States.ArrayUnique($.u)`, input, nil, &source{})
	unique, isList := got.(document.List)
	if err != nil || !isList {
		t.Fatalf("ArrayUnique = %v, %v; want a list", got, err)
	}
	members := make([]string, 0, len(unique))
	for _, x := range unique {
		members = append(members, encode(x))
	}
	slices.Sort(members)
	if want := []string{`"1"`, `1`, `1.0`, `[1]`, `null`, `true`, `{"a":1}`}; !slices.Equal(members, want) {
		t.Errorf("ArrayUnique members = %v, want %v", members, want)
	}
	for _, tt := range []struct{ expr, want string }{
		{`States.ArrayContains($.c, 1)`, `true`},
		{`States.Format($.tpl, $.f[0], $.f[1], $.f[2], $.f[3], $.f[4])`, `"1.0 1.0E21 -0.0 0.1 100000000000000000000"`},
		{`States.JsonToString($.f)`, `"[1.0,1.0E21,-0.0,0.1,100000000000000000000]"`},
	} {
		if got, err := eval(t, tt.expr, input, nil, &source{}); err != nil || encode(got) != tt.want {
			t.Errorf("%s = %s, %v; want %s", tt.expr, encode(got), err, tt.want)
		}
	}
}

// Behavior the blind tests leave open, each understudy's reading of
// stepfunctions-asl.md 5.2.
func TestFunctionResultsBeyondTheRecords(t *testing.T) {
	tests := []struct{ name, expr, input, want string }{
		{"Format renders arrays as Java lists", `States.Format('{}', $.v)`, `{"v":[1,"a",null,{"k":true}]}`, `"[1, a, null, {k=true}]"`},
		{"Format reads its pattern from a path without escapes", `States.Format($.f, $.v)`, `{"f":"a\\{}b","v":"x"}`, `"a\\xb"`},
		{"Format writes decimals as Java's Double.toString", `States.Format('{} {} {}', $.a, $.b, $.c)`, `{"a":0.0001,"b":12345678.9,"c":0e0}`, `"1.0E-4 1.23456789E7 0.0"`},
		{"Format keeps a lone brace", `States.Format('{ {}', $.v)`, `{"v":"x"}`, `"{ x"`},
		{"StringToJson rewrites a nested -0", `States.StringToJson($.v)`, `{"v":"{\"a\":[-0, -0.5]}"}`, `{"a":[0,-0.5]}`},
		{"JsonMerge keeps the first object's order and appends", `States.JsonMerge($.a, $.b, false)`, `{"a":{"x":1,"y":2},"b":{"z":3,"x":4}}`, `{"x":4,"y":2,"z":3}`},
		{"ArrayRange counts down with a negative step", `States.ArrayRange(10, 0, -3)`, `{}`, `[10,7,4,1]`},
		{"ArrayRange moving away from the end is empty", `States.ArrayRange(0, 10, -1)`, `{}`, `[]`},
		{"ArrayRange of one point", `States.ArrayRange(4, 4, 7)`, `{}`, `[4]`},
		{"ArrayPartition larger than the array", `States.ArrayPartition($.v, 10)`, `{"v":[1,2]}`, `[[1,2]]`},
		{"Base64 of non-ASCII text uses its UTF-8 bytes", `States.Base64Encode($.v)`, `{"v":"é"}`, `"w6k="`},
		{"JsonToString keeps member order", `States.JsonToString($.v)`, `{"v":{"b":1,"a":[true,null]}}`, `"{\"b\":1,\"a\":[true,null]}"`},
		{"ArrayContains a missing value is false", `States.ArrayContains($.v, 'x')`, `{"v":[1,"y"]}`, `false`},
		{"StringSplit without delimiters keeps the string", `States.StringSplit($.v, '')`, `{"v":"a,b"}`, `["a,b"]`},
		{"path arguments read $$", `States.Array($$.k)`, `{}`, `["ctx"]`},
	}
	ctx := decode(t, `{"k":"ctx"}`)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := eval(t, tt.expr, decode(t, tt.input), ctx, &source{})
			if err != nil || encode(got) != tt.want {
				t.Fatalf("%s on %s = %s, %v; want %s", tt.expr, tt.input, encode(got), err, tt.want)
			}
		})
	}
}

// The data limits of Base64Encode, Base64Decode and Hash are 10,000
// characters (stepfunctions-asl.md 5.2, aws-doc dg/intrinsic-functions).
func TestDataLimits(t *testing.T) {
	ok := document.String(string(make([]rune, 10000)))
	long := document.String(string(make([]rune, 10001)))
	for _, expr := range []string{"States.Base64Encode($.v)", "States.Hash($.v, 'MD5')"} {
		if _, err := eval(t, expr, document.Map{{Key: "v", Value: ok}}, nil, &source{}); err != nil {
			t.Errorf("%s of 10,000 characters = %v, want a result", expr, err)
		}
		_, err := eval(t, expr, document.Map{{Key: "v", Value: long}}, nil, &source{})
		if _, ok := errors.AsType[*intrinsic.FunctionError](err); !ok {
			t.Errorf("%s of 10,001 characters = %v, want a *FunctionError", expr, err)
		}
	}
}

// A seeded MathRandom is in [start, end) and repeats for its seed; the
// unseeded source is not consulted (aws-doc dg/intrinsic-functions).
func TestSeededMathRandomProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		start := rapid.Int64Range(-1000, 1000).Draw(t, "start")
		end := start + rapid.Int64Range(1, 1000).Draw(t, "width")
		seed := rapid.Int64Range(-1<<40, 1<<40).Draw(t, "seed")
		expr := "States.MathRandom(" + itoa(start) + ", " + itoa(end) + ", " + itoa(seed) + ")"
		src := &source{}
		a, errA := eval(t, expr, document.Map{}, nil, src)
		b, errB := eval(t, expr, document.Map{}, nil, src)
		n, ok := a.(document.Number)
		v, perr := n.Int64()
		if errA != nil || errB != nil || !ok || perr != nil || !equal(a, b) || v < start || v >= end || len(src.asked) != 0 {
			t.Fatalf("%s = %v, %v and %v, %v (asked %v); want one repeatable integer in range", expr, a, errA, b, errB, src.asked)
		}
	})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
