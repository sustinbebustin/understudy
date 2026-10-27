package intrinsic_test

import (
	"errors"
	"strconv"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/intrinsic"
)

// TestASL05FunctionErrorCauses: no recording shows an intrinsic failing at
// run time (stepfunctions-asl.md 5.4), so every cause is understudy's. The
// test pins them so a change is deliberate.
func TestASL05FunctionErrorCauses(t *testing.T) {
	// inferred, ASL-05
	tests := []struct{ expr, input, cause string }{
		{`States.JsonMerge($.a, $.b, true)`, `{"a":{},"b":{}}`, "a deep merge (true) is not supported; only false is"},
		{`States.ArrayGetItem($.v, 5)`, `{"v":[1]}`, "the index 5 is outside the array of 1 items"},
		{`States.ArrayGetItem($.v, 0.5)`, `{"v":[1]}`, "the second argument must be an integer, not 0.5"},
		{`States.ArrayPartition($.v, 0)`, `{"v":[1]}`, "the chunk size must be a positive integer, not 0"},
		{`States.ArrayRange(0, 2000, 1)`, `{}`, "the range has 2001 items, more than 1000"},
		{`States.ArrayRange(0, 10, 0)`, `{}`, "the step must not be 0"},
		{`States.Base64Decode($.v)`, `{"v":"not base64!"}`, "the argument is not Base64: illegal base64 data at input byte 3"},
		{`States.Hash($.v, 'SHA-3')`, `{"v":"x"}`, `the algorithm "SHA-3" is not one of MD5, SHA-1, SHA-256, SHA-384 and SHA-512`},
		{`States.MathAdd(2147483647, 1)`, `{}`, "the sum 2147483648 is outside -2147483648..2147483647"},
		{`States.MathAdd(2147483648, -1)`, `{}`, "the first argument 2147483648 is outside -2147483648..2147483647"},
		{`States.Format('{} {}', $.v)`, `{"v":"x"}`, "the format string has more {} placeholders than the 1 arguments after it"},
		{`States.Format('{}', $.v, $.v)`, `{"v":"x"}`, "the format string has 1 {} placeholders for 2 arguments"},
		{`States.Format($.v)`, `{"v":1}`, "the first argument must be a string, not a number"},
		{`States.ArrayLength($.v)`, `{"v":{}}`, "the first argument must be an array, not an object"},
		{`States.StringSplit($.v, ',')`, `{"v":null}`, "the first argument must be a string, not null"},
		{`States.MathRandom(5, 5)`, `{}`, "the start 5 must be less than the end 5"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			_, err := eval(t, tt.expr, decode(t, tt.input), nil, &source{})
			var fe *intrinsic.FunctionError
			if !errors.As(err, &fe) || fe.Cause != tt.cause {
				t.Fatalf("%s = %v, want a *FunctionError with cause %q", tt.expr, err, tt.cause)
			}
		})
	}
}

// TestASL06EqualityAndRounding: how ArrayUnique and ArrayContains compare
// 1 with 1.0, and objects, is unrecorded, as is rounding of non-integer
// bounds outside MathAdd (stepfunctions-asl.md 5.2, 5.3). understudy
// compares numbers by value and objects as member sets, and rounds half up
// everywhere MathAdd does.
func TestASL06EqualityAndRounding(t *testing.T) {
	// inferred, ASL-06
	tests := []struct{ expr, input, want string }{
		{`States.ArrayUnique($.v)`, `{"v":[1,1.0,{"a":1,"b":2},{"b":2,"a":1},[1],[1.0]]}`, `[1,{"a":1,"b":2},[1]]`},
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

// Behavior the blind tests leave open, each understudy's reading of
// stepfunctions-asl.md 5.2.
func TestFunctionResultsBeyondTheRecords(t *testing.T) {
	tests := []struct{ name, expr, input, want string }{
		{"Format renders arrays as Java lists", `States.Format('{}', $.v)`, `{"v":[1,"a",null,{"k":true}]}`, `"[1, a, null, {k=true}]"`},
		{"Format reads its pattern from a path without escapes", `States.Format($.f, $.v)`, `{"f":"a\\{}b","v":"x"}`, `"a\\xb"`},
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
