package fidelity

import (
	"fmt"
	"strings"
)

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

// String returns "<scenario>#<step>".
func (r StepRef) String() string { return r.Scenario + "#" + r.Step }

// MarshalText writes the reference as String does, refusing one that
// ParseStepRef would not read back.
func (r StepRef) MarshalText() ([]byte, error) {
	if _, err := ParseStepRef(r.String()); err != nil {
		return nil, err
	}
	return []byte(r.String()), nil
}

// UnmarshalText reads a reference as ParseStepRef does.
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

// ParseStepRef reads "<svc>/<item>#<step>": a service directory of a-z and
// 0-9, an item of letters, digits and "-", and a step name of letters,
// digits, ".", "_" and "-", each non-empty (the fixture paths of
// testdata/parity).
func ParseStepRef(s string) (StepRef, error) {
	scenario, step, ok := strings.Cut(s, "#")
	if !ok {
		return StepRef{}, &StepRefError{Text: s, Reason: "it has no # before the step name"}
	}
	svc, item, ok := strings.Cut(scenario, "/")
	if !ok {
		return StepRef{}, &StepRefError{Text: s, Reason: "the scenario has no / between the service and the item"}
	}
	for _, p := range []struct {
		part, value, allowed string
		ok                   func(rune) bool
	}{
		{"service", svc, "a-z and 0-9", func(r rune) bool { return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' }},
		{"item", item, "letters, digits and -", func(r rune) bool { return alnum(r) || r == '-' }},
		{"step", step, "letters, digits, ., _ and -", func(r rune) bool { return alnum(r) || strings.ContainsRune("._-", r) }},
	} {
		if p.value == "" {
			return StepRef{}, &StepRefError{Text: s, Reason: "the " + p.part + " is empty"}
		}
		for _, r := range p.value {
			if !p.ok(r) {
				return StepRef{}, &StepRefError{Text: s, Reason: fmt.Sprintf("the %s %q holds %q; it may hold only %s", p.part, p.value, r, p.allowed)}
			}
		}
	}
	return StepRef{Scenario: scenario, Step: step}, nil
}

func alnum(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' }
