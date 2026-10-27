package asl

// Definition is a parsed state machine definition. It is immutable and built
// only by Parse.
type Definition struct {
	_ struct{}
}

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

// Parse parses definition, accepting the constructs of accept. It returns
// every diagnostic in document order; the Definition is nil when any
// diagnostic is SeverityError.
//
// Not implemented yet: it refuses every definition.
func Parse(definition string, accept Features) (*Definition, []Diagnostic) {
	_, _ = definition, accept
	return nil, []Diagnostic{{
		Severity: SeverityError,
		Code:     CodeUnsupported,
		Message:  "understudy does not implement Step Functions definitions yet. Run 'understudy coverage --service sfn' for what is supported.",
		Location: "/",
	}}
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
