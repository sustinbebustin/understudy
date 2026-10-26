package parity

// Baseline is internal/parity/baseline.json: the ratchet of every
// scenario's step outcomes and claim results.
type Baseline struct {
	Scenarios []BaselineScenario
}

// BaselineScenario is one scenario's entry.
type BaselineScenario struct {
	Scenario string
	Steps    []BaselineStep
	Claims   []BaselineClaim
}

// BaselineStep is one step's recorded outcome and routed operation.
type BaselineStep struct {
	Step, Fixture, Operation string
	Outcome                  Outcome
}

// BaselineClaim is one settles entry's recorded result.
type BaselineClaim struct {
	Target Target
	Step   string
	Field  Path
	Result ClaimState
}

// BaselineError reports a baseline file that does not follow the format.
type BaselineError struct {
	Reason string
}

func (e *BaselineError) Error() string { return "internal/parity/baseline.json: " + e.Reason }

// ParseBaseline reads baseline.json strictly. Not implemented yet: it
// refuses every file.
func ParseBaseline([]byte) (Baseline, error) {
	return Baseline{}, &BaselineError{Reason: notImplemented("ParseBaseline").Error()}
}

// Encode writes b in the canonical form. Not implemented yet.
func (b Baseline) Encode() ([]byte, error) { return nil, notImplemented("Baseline.Encode") }

// BaselineOf is the baseline the results give. Not implemented yet: it is
// empty.
func BaselineOf([]Result) Baseline { return Baseline{} }

// Drift is one key whose outcome or result differs from the baseline.
type Drift struct {
	// Key is "<scenario>#<step>", or that followed by a claim's target and
	// field.
	Key      string
	Was, Now string
}

// String renders the drift on one line. Not implemented yet: it returns "".
func (d Drift) String() string { return "" }

// Ratchet lists every key on which got differs from want. Not implemented
// yet: it reports none.
func Ratchet(_, _ Baseline) []Drift { return nil }
