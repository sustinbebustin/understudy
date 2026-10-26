package coverage

import (
	"strings"
)

// Parity is the parity replay evidence of a report: the ratchet read from
// internal/parity/baseline.json, or none, because a running server does not
// carry the file. Coverage reads the file itself and never imports
// internal/parity.
//
//sumtype:decl
type Parity interface{ isParity() }

// ParityRead is the baseline's scenarios.
type ParityRead struct {
	Scenarios []ParityScenario `json:"scenarios"`
}

// ParityNotCarried is a report built without the baseline.
type ParityNotCarried struct{}

func (ParityRead) isParity()       {}
func (ParityNotCarried) isParity() {}

// ParityScenario is one scenario's steps and settles entries.
type ParityScenario struct {
	// Scenario is the scenario id, "<svc>/<item>".
	Scenario string        `json:"scenario"`
	Steps    []ParityStep  `json:"steps"`
	Claims   []ParityClaim `json:"claims,omitempty"`
}

// ParityStep is one replayed step.
type ParityStep struct {
	Step string `json:"step"`
	// Fixture is the fixture path under testdata/parity.
	Fixture string `json:"fixture"`
	// Operation is the operation understudy routed the step to
	// ("kms.CreateAlias"), empty when the step was not sent.
	Operation string        `json:"operation,omitempty"`
	Outcome   ParityOutcome `json:"outcome"`
}

// ParityOutcome is a step's replay outcome.
//
//sumtype:decl
type ParityOutcome interface{ isParityOutcome() }

// StepMatch is a step understudy answers as AWS did.
type StepMatch struct{}

// StepDiffers is a step that differs from AWS.
type StepDiffers struct {
	Reason string `json:"reason"`
}

// StepReconstructed is a step whose recording is incomplete.
type StepReconstructed struct{}

// StepSkipped is a step the replayer cannot run.
type StepSkipped struct {
	Reason string `json:"reason"`
}

// StepPending is a queued step with no fixture yet.
type StepPending struct{}

func (StepMatch) isParityOutcome()         {}
func (StepDiffers) isParityOutcome()       {}
func (StepReconstructed) isParityOutcome() {}
func (StepSkipped) isParityOutcome()       {}
func (StepPending) isParityOutcome()       {}

// ParityClaim is one settles entry and what the replay says of it.
type ParityClaim struct {
	Target ClaimTarget `json:"target"`
	Step   string      `json:"step"`
	Field  string      `json:"field"`
	Result ClaimResult `json:"result"`
}

// ClaimTarget is what a settles entry settles.
//
//sumtype:decl
type ClaimTarget interface{ isClaimTarget() }

// TargetSwitch is a fidelity switch, by name.
type TargetSwitch struct {
	Name string `json:"name"`
}

// TargetOverlay is an overlay entry, by shape id, and optionally the traits
// that pick one of its evidence items.
type TargetOverlay struct {
	Shape  string   `json:"shape"`
	Covers []string `json:"covers,omitempty"`
}

func (TargetSwitch) isClaimTarget()  {}
func (TargetOverlay) isClaimTarget() {}

// ClaimResult is whether a settles entry is settled.
//
//sumtype:decl
type ClaimResult interface{ isClaimResult() }

// ClaimSettled is a claim whose step matches and fails once the field is
// mutated.
type ClaimSettled struct{}

// ClaimInsensitive is a claim whose step matches also with the field
// mutated; By is "waiver" or "normalizer".
type ClaimInsensitive struct {
	By string `json:"by"`
}

// ClaimContradicted is a claim whose step differs from AWS.
type ClaimContradicted struct{}

// ClaimOpen is a claim whose step was not compared.
type ClaimOpen struct{}

func (ClaimSettled) isClaimResult()      {}
func (ClaimInsensitive) isClaimResult()  {}
func (ClaimContradicted) isClaimResult() {}
func (ClaimOpen) isClaimResult()         {}

// ParityError reports a baseline coverage cannot read, or evidence that
// disagrees with it.
type ParityError struct {
	Problems []string
}

func (e *ParityError) Error() string {
	return "parity evidence: " + strings.Join(e.Problems, "; ")
}

// ParseParity reads internal/parity/baseline.json strictly. Not implemented
// yet: it refuses every file.
func ParseParity([]byte) (ParityRead, error) {
	return ParityRead{}, &ParityError{Problems: []string{"reading the parity baseline is not implemented yet"}}
}
