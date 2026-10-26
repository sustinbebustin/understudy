package fidelity

import "fmt"

// Settles says what settles a switch: a parity scenario step whose replay
// against the recorded AWS answer decides it, or no scenario, and why.
//
//sumtype:decl
type Settles interface{ isSettles() }

// SettledBy names the parity scenario step that settles the switch.
type SettledBy struct {
	Step StepRef `json:"step"`
}

// NoScenario says no parity scenario settles the switch, and why.
type NoScenario struct {
	Reason string `json:"reason"`
}

func (SettledBy) isSettles()  {}
func (NoScenario) isSettles() {}

// StepRef names one step of a parity scenario, written
// "<svc>/<item>#<step>": the scenario directory under testdata/parity and
// the step's fixture name without ".json".
type StepRef struct {
	Scenario string // "sqs/R01"
	Step     string // "03-delete-stale"
}

// String returns "<scenario>#<step>". Not implemented yet: it returns "".
func (r StepRef) String() string { return "" }

// MarshalText writes the reference as String does. Not implemented yet.
func (r StepRef) MarshalText() ([]byte, error) {
	return nil, &StepRefError{Text: r.Scenario + "#" + r.Step, Reason: "not implemented yet"}
}

// UnmarshalText reads a reference as ParseStepRef does. Not implemented
// yet.
func (r *StepRef) UnmarshalText(b []byte) error {
	ref, err := ParseStepRef(string(b))
	if err != nil {
		return err
	}
	*r = ref
	return nil
}

// StepRefError reports text that is not a scenario step reference.
type StepRefError struct {
	Text   string
	Reason string
}

func (e *StepRefError) Error() string {
	return fmt.Sprintf("scenario step reference %q: %s; write <svc>/<item>#<step>, such as sqs/R01#03-delete-stale", e.Text, e.Reason)
}

// ParseStepRef reads "<svc>/<item>#<step>". Not implemented yet: it refuses
// every reference.
func ParseStepRef(s string) (StepRef, error) {
	return StepRef{}, &StepRefError{Text: s, Reason: "not implemented yet"}
}
