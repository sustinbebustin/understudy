package asl

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/intrinsic"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/jsonpath"
)

// Definition is a parsed state machine definition. It is immutable and built
// only by Parse.
type Definition struct {
	startAt string
	// timeoutSeconds is the top-level TimeoutSeconds; 0 when absent.
	timeoutSeconds int64
	// states holds every state of the machine, nested branches and
	// processors included: names are unique machine-wide
	// (stepfunctions-asl.md 3.2).
	states map[string]state
}

// state is one parsed state.
//
//sumtype:decl
type state interface{ isState() }

// In every state below, next is the state that follows, "" when the state
// ends its scope ("End": true).

// passState is a Pass state (stepfunctions-asl.md 7.3): its output is
// Result when the definition gives one, else its input.
type passState struct {
	name string
	next string
	io   paths
	// parameters is the Parameters template; nil when absent.
	parameters template
	// result is the literal Result; nil when the state has none.
	result     document.Value
	resultPath target
}

// succeedState is a Succeed state: its output is its input.
type succeedState struct {
	name string
	io   paths
}

// failState is a Fail state (stepfunctions-asl.md 7.2).
type failState struct {
	name         string
	error, cause message
}

// choiceState is a Choice state (stepfunctions-asl.md 6).
type choiceState struct {
	name    string
	io      paths
	choices []choiceRule
	// otherwise is the Default state; "" when there is none.
	otherwise string
}

// waitState is a Wait state (stepfunctions-asl.md 7.1).
type waitState struct {
	name  string
	next  string
	io    paths
	until wait
}

// taskState is a Lambda Task state (stepfunctions-asl.md 8).
type taskState struct {
	name     string
	next     string
	io       paths
	resource ResourceKind
	// function is the function ARN of a FunctionARN resource; "" for
	// LambdaInvoke, whose Parameters name the function.
	function       string
	parameters     template
	resultSelector template
	resultPath     target
	timeout        timeout
	retry          []Retrier
	catch          []catcher
}

// parallelState is a Parallel state (stepfunctions-asl.md 9.1); each
// branch is named by its StartAt.
type parallelState struct {
	name           string
	next           string
	io             paths
	parameters     template
	resultSelector template
	resultPath     target
	branches       []string
	retry          []Retrier
	catch          []catcher
}

// mapState is an inline Map state (stepfunctions-asl.md 9.2).
type mapState struct {
	name string
	next string
	io   paths
	// itemsPath is ItemsPath, "$" when absent.
	itemsPath jsonpath.Path
	// itemSelector is ItemSelector, or the deprecated Parameters; nil when
	// absent.
	itemSelector   template
	resultSelector template
	resultPath     target
	// processor is the StartAt of ItemProcessor or Iterator.
	processor   string
	concurrency concurrency
	retry       []Retrier
	catch       []catcher
}

func (passState) isState()     {}
func (succeedState) isState()  {}
func (failState) isState()     {}
func (choiceState) isState()   {}
func (waitState) isState()     {}
func (taskState) isState()     {}
func (parallelState) isState() {}
func (mapState) isState()      {}

// paths are a state's InputPath and OutputPath.
type paths struct {
	input, output selector
}

// selector is an InputPath or OutputPath: absent selects the whole value,
// null selects {} (stepfunctions-asl.md 4.2).
//
//sumtype:decl
type selector interface{ isSelector() }

// selectAll is an absent path, "$".
type selectAll struct{}

// selectNone is a null path.
type selectNone struct{}

// selectPath is a Plain Path.
type selectPath struct{ path jsonpath.Path }

func (selectAll) isSelector()  {}
func (selectNone) isSelector() {}
func (selectPath) isSelector() {}

// target is a ResultPath: absent replaces the input with the result, null
// discards the result, a Reference Path places it (stepfunctions-asl.md
// 4.2, 4.4).
//
//sumtype:decl
type target interface{ isTarget() }

// replaceInput is an absent ResultPath, "$".
type replaceInput struct{}

// discardResult is a null ResultPath.
type discardResult struct{}

// placeAt is a Reference Path into the raw input.
type placeAt struct{ path jsonpath.Path }

func (replaceInput) isTarget()  {}
func (discardResult) isTarget() {}
func (placeAt) isTarget()       {}

// template is a payload template value (stepfunctions-asl.md 4.3): an
// object whose ".$" members are evaluated, or a value copied as is.
//
//sumtype:decl
type template interface{ isTemplate() }

// templateObject is an object; a member's key has its ".$" suffix removed.
type templateObject struct{ members []templateMember }

type templateMember struct {
	key   string
	value template
}

// templateLiteral is a value copied as written. Arrays are literals: no
// recording shows a ".$" member inside an array being evaluated.
type templateLiteral struct{ value document.Value }

// templatePath is a ".$" member holding a Path or a "$$" context path.
type templatePath struct {
	field string // the member's key as written, "value.$"
	path  jsonpath.Path
}

// templateCall is a ".$" member holding an intrinsic call.
type templateCall struct {
	field string
	call  intrinsic.Call
}

func (templateObject) isTemplate()  {}
func (templateLiteral) isTemplate() {}
func (templatePath) isTemplate()    {}
func (templateCall) isTemplate()    {}

// message is a Fail state's error or cause.
//
//sumtype:decl
type message interface{ isMessage() }

// noMessage is neither the field nor its Path.
type noMessage struct{}

// literalMessage is Error or Cause.
type literalMessage struct{ text string }

// pathMessage is ErrorPath or CausePath holding a Reference Path.
type pathMessage struct{ path jsonpath.Path }

// callMessage is ErrorPath or CausePath holding an intrinsic call.
type callMessage struct{ call intrinsic.Call }

func (noMessage) isMessage()      {}
func (literalMessage) isMessage() {}
func (pathMessage) isMessage()    {}
func (callMessage) isMessage()    {}

// wait is how long a Wait state waits: exactly one of its four fields.
//
//sumtype:decl
type wait interface{ isWait() }

type waitSeconds struct{ seconds int64 }

type waitSecondsPath struct{ path jsonpath.Path }

// waitTimestamp is a literal Timestamp; AWS considers it to the second
// (stepfunctions-asl.md 7.1).
type waitTimestamp struct{ at time.Time }

type waitTimestampPath struct{ path jsonpath.Path }

func (waitSeconds) isWait()       {}
func (waitSecondsPath) isWait()   {}
func (waitTimestamp) isWait()     {}
func (waitTimestampPath) isWait() {}

// timeout is a Task's TimeoutSeconds or TimeoutSecondsPath; absent is the
// 99,999,999-second default (stepfunctions-asl.md 8.6).
//
//sumtype:decl
type timeout interface{ isTimeout() }

type defaultTimeout struct{}

type timeoutSeconds struct{ seconds int64 }

type timeoutPath struct{ path jsonpath.Path }

func (defaultTimeout) isTimeout() {}
func (timeoutSeconds) isTimeout() {}
func (timeoutPath) isTimeout()    {}

// concurrency is a Map's MaxConcurrency or MaxConcurrencyPath; 0 and absent
// mean no limit beyond inline mode's 40 (stepfunctions-asl.md 9.2).
//
//sumtype:decl
type concurrency interface{ isConcurrency() }

type maxConcurrency struct{ n int64 }

type maxConcurrencyPath struct{ path jsonpath.Path }

func (maxConcurrency) isConcurrency()     {}
func (maxConcurrencyPath) isConcurrency() {}

// catcher is one Catch entry (stepfunctions-asl.md 8.4).
type catcher struct {
	errorEquals []string
	next        string
	resultPath  target
}

// choiceRule is a top-level Choice rule and the state it chooses.
type choiceRule struct {
	when condition
	next string
}

// condition is a Choice rule's test (stepfunctions-asl.md 6.1).
//
//sumtype:decl
type condition interface{ isCondition() }

// compare tests Variable against a literal with op, an operator of
// stepfunctions-asl.md 6.2 without a Path suffix (Is* operators included,
// with their boolean).
type compare struct {
	variable jsonpath.Path
	op       string
	operand  document.Value
}

// comparePath tests Variable against the value of another path; op is the
// operator without its Path suffix.
type comparePath struct {
	variable, operand jsonpath.Path
	op                string
}

type allOf struct{ rules []condition }

type anyOf struct{ rules []condition }

type not struct{ rule condition }

func (compare) isCondition()     {}
func (comparePath) isCondition() {}
func (allOf) isCondition()       {}
func (anyOf) isCondition()       {}
func (not) isCondition()         {}

// Feature is a group of definition constructs that lands with one runtime
// ticket. A construct whose Feature is outside the accepted set is refused
// with code UNSUPPORTED_BY_UNDERSTUDY.
type Feature int

// Features. The zero value is no feature.
const (
	// FeatPass: Pass states (Result, Next, End).
	FeatPass Feature = iota + 1
	// FeatSucceed: Succeed states.
	FeatSucceed
	// FeatFail: Fail states (Error, Cause, ErrorPath, CausePath).
	FeatFail
	// FeatChoice: Choice states, every operator of stepfunctions-asl.md 6.2,
	// And, Or, Not, Default.
	FeatChoice
	// FeatWait: Wait states in their four forms.
	FeatWait
	// FeatTimeout: the top-level TimeoutSeconds.
	FeatTimeout
	// FeatPathDialect: Plain Path constructs beyond a Reference Path
	// (wildcard, slice, index union, negative index, "..", one-comparison
	// filter) wherever a Plain Path is allowed (stepfunctions-asl.md 4.5).
	FeatPathDialect
	// FeatPayloadTemplates: InputPath, Parameters, ResultSelector,
	// ResultPath, OutputPath, ".$" keys and "$$" context paths
	// (stepfunctions-asl.md 4.1-4.4, 4.7).
	FeatPayloadTemplates
	// FeatIntrinsics: States.* calls in ".$" values and Fail
	// ErrorPath/CausePath (stepfunctions-asl.md 5).
	FeatIntrinsics
	// FeatLambdaTask: Task states whose Resource is a Lambda function ARN or
	// arn:aws:states:::lambda:invoke, with TimeoutSeconds or
	// TimeoutSecondsPath.
	FeatLambdaTask
	// FeatRetryCatch: Retry and Catch fields (stepfunctions-asl.md 8.2-8.4).
	FeatRetryCatch
	// FeatParallel: Parallel states.
	FeatParallel
	// FeatMap: inline Map states (ItemProcessor or Iterator, ItemsPath,
	// ItemSelector or Parameters, MaxConcurrency or MaxConcurrencyPath).
	FeatMap
)

// Features is a set of Feature.
type Features struct {
	set uint64
}

// NewFeatures returns the set of fs.
func NewFeatures(fs ...Feature) Features {
	var s Features
	for _, f := range fs {
		if f > 0 && f < 64 {
			s.set |= 1 << uint(f)
		}
	}
	return s
}

// Has reports whether f is in the set.
func (s Features) Has(f Feature) bool {
	return f > 0 && f < 64 && s.set&(1<<uint(f)) != 0
}

// Severity is a diagnostic's severity.
type Severity int

// Severities.
const (
	SeverityError Severity = iota + 1
	SeverityWarning
)

// String returns the wire value: "ERROR" or "WARNING".
func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "ERROR"
	case SeverityWarning:
		return "WARNING"
	default:
		return ""
	}
}

// CodeUnsupported is the diagnostic code of a construct understudy refuses
// (stepfunctions-asl.md 10.4); it is not one of AWS's codes.
const CodeUnsupported = "UNSUPPORTED_BY_UNDERSTUDY"

// Diagnostic is one problem in a definition, in
// ValidateStateMachineDefinition's terms (stepfunctions-asl.md 3.6).
type Diagnostic struct {
	Severity Severity
	Code     string // AWS's code, or CodeUnsupported
	Message  string
	Location string // "/States/<name>/<Field>"; "" when AWS gives none
}

// InvalidDefinition returns the InvalidDefinition message CreateStateMachine
// and UpdateStateMachine answer for d (stepfunctions-asl.md 3.6):
// "Invalid State Machine Definition: '<Code>: <Message> at <Location>'",
// without " at <Location>" when Location is empty.
func (d Diagnostic) InvalidDefinition() string {
	body := d.Code + ": " + d.Message
	if d.Location != "" {
		body += " at " + d.Location
	}
	return "Invalid State Machine Definition: '" + body + "'"
}
