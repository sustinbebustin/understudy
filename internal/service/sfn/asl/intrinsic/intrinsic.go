// Package intrinsic is the States.* intrinsic functions of the Amazon States
// Language: the grammar of stepfunctions-asl.md 5.1 and the 18 functions of
// 5.2, over document.Value.
package intrinsic

// MaxDepth is the deepest nesting of calls a definition may use
// (stepfunctions-asl.md 3.7).
const MaxDepth = 10

// Call is a parsed intrinsic call, built only by Parse.
type Call struct {
	// function is the full name, "States.Format".
	function string
	args     []arg
}

// Function returns the called function's full name, such as
// "States.Format".
func (c Call) Function() string { return c.function }

// Source supplies the values the service draws from its ids kinds.
type Source interface {
	// UUID is a States.UUID value.
	UUID() string
	// Random is an unseeded States.MathRandom value in [start, end).
	Random(start, end int64) int64
}

// SyntaxError is a call that does not parse: bad grammar, an unknown
// function, a wrong argument count, nesting deeper than MaxDepth.
type SyntaxError struct {
	Expr   string // as written
	Reason string // understudy's wording
	// Err is the path argument's own error when a path argument does not
	// parse (a *jsonpath.SyntaxError or *jsonpath.UnsupportedError); nil
	// otherwise.
	Err error
}

func (e *SyntaxError) Error() string {
	return "invalid intrinsic function '" + e.Expr + "': " + e.Reason
}

// Unwrap returns the path argument's error, if any.
func (e *SyntaxError) Unwrap() error { return e.Err }

// FunctionError is a function failing at run time (index out of range,
// chunk size 0, bad Base64, ...); the engine names it with the
// sfn.intrinsic.failure-error switch.
type FunctionError struct {
	Function string // "States.ArrayGetItem"
	Cause    string // understudy's wording (UNVERIFIED, ASL-05)
}

func (e *FunctionError) Error() string { return e.Function + ": " + e.Cause }

// Parse parses s, a call such as States.Format('{}', $.name). The error is a
// *SyntaxError.
func Parse(s string) (Call, error) {
	return parse(s)
}
