package intrinsic_test

import (
	"errors"
	"testing"

	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/intrinsic"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/jsonpath"
)

// The grammar of stepfunctions-asl.md 5.1 accepts every argument form,
// including the recorded nesting (localstack@8b9a79f
// v2/intrinsic_functions/test_generic.snapshot.json:L1283).
func TestParseAcceptsArgumentForms(t *testing.T) {
	for _, s := range []string{
		"States.ArrayGetItem(States.StringSplit($$.StateMachine.Name, '-'), States.MathAdd(States.ArrayLength(States.StringSplit($$.StateMachine.Name, '-')), -1))",
		"States.Array()",
		"States.Array( 1 , 'a' , null , true , false , -1.5e3 )",
		`States.Format('\{\} \' \\ {}', $.a[0])`,
		"States.Array($.a[?(@.b == 'x,y')], $['c)'])",
		"States.JsonMerge($.a, $.b, false)",
		"States.MathRandom(1, 10)",
		"States.MathRandom(1, 10, 7)",
	} {
		c, err := intrinsic.Parse(s)
		if err != nil {
			t.Errorf("Parse(%q) = %v, want a call", s, err)
			continue
		}
		if c.Function() == "" {
			t.Errorf("Parse(%q).Function() is empty", s)
		}
	}
}

// A backslash before " is refused at creation (stepfunctions-asl.md 3.5,
// recorded); a path argument that does not parse carries its path error.
func TestParseRefusesBadArguments(t *testing.T) {
	for _, s := range []string{`States.Format('\"')`, "States.Array(x)", "States.Array(1) extra", "States.MathRandom(1)", "States.JsonMerge($.a, $.b)"} {
		var syn *intrinsic.SyntaxError
		if _, err := intrinsic.Parse(s); !errors.As(err, &syn) || syn.Expr != s {
			t.Errorf("Parse(%q) = %v, want a *SyntaxError", s, err)
		}
	}
	_, err := intrinsic.Parse("States.Array($.a[?(@.b == 1 && @.c == 2)])")
	var un *jsonpath.UnsupportedError
	if !errors.As(err, &un) || un.Construct != "&&" {
		t.Errorf("Parse with a multi-clause filter argument = %v, want it to wrap the *jsonpath.UnsupportedError", err)
	}
}
