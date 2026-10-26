package coverage

import (
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Claim is one piece of evidence attributed to an operation, and how the
// page labels it.
type Claim struct {
	Source ClaimSource
	Label  ClaimLabel
}

// ClaimSource is where a claim comes from.
//
//sumtype:decl
type ClaimSource interface{ isClaimSource() }

// FromSwitch is a fidelity switch whose Ops list the operation.
type FromSwitch struct {
	Name string
}

// FromOverlay is an overlay evidence item whose shape is the operation, its
// input or output, or one of its errors.
type FromOverlay struct {
	File, Shape string
	Covers      []string
}

// FromStep is a parity step replayed against the operation.
type FromStep struct {
	Scenario, Step, Fixture string
}

func (FromSwitch) isClaimSource()  {}
func (FromOverlay) isClaimSource() {}
func (FromStep) isClaimSource()    {}

// ClaimLabel is a claim's evidence as the page prints it.
//
//sumtype:decl
type ClaimLabel interface{ isClaimLabel() }

// CodeLabel is the label the code or the overlay states.
type CodeLabel struct {
	Label evidence.Label
}

// RecordedLabel is a recorded step that matches AWS, printed
// "aws-recorded (<fixture>)".
type RecordedLabel struct {
	Fixture string
}

// ContradictedLabel is a recorded step that differs from AWS, printed
// "contradicted (<fixture>)".
type ContradictedLabel struct {
	Fixture string
}

func (CodeLabel) isClaimLabel()         {}
func (RecordedLabel) isClaimLabel()     {}
func (ContradictedLabel) isClaimLabel() {}

// ClaimLabelText is the label as the page prints it. Not implemented yet:
// it returns "".
func ClaimLabelText(ClaimLabel) string { return "" }

// Evidence is one operation's evidence cell: its weakest claim and how many
// of its claims carry that label.
type Evidence struct {
	// Weakest is nil when the operation has no claim.
	Weakest   ClaimLabel
	AtWeakest int
	Claims    int
}

// String is the cell's text. Not implemented yet: it returns "".
func (e Evidence) String() string { return "" }

// Retirement is a switch or overlay evidence item a recording settles while
// its code label is below aws-recorded.
type Retirement struct {
	Source  ClaimSource
	Code    evidence.Label
	Fixture string
	Field   string
}

// Claims are the claims attributed to operation op of service svc, in claim
// order. Not implemented yet: there are none.
func (r Report) Claims(ref.ServiceID, string) []Claim { return nil }

// OperationEvidence is the evidence cell of operation op of service svc.
// Not implemented yet: it is unassessed.
func (r Report) OperationEvidence(ref.ServiceID, string) Evidence { return Evidence{} }

// PendingRetirement lists the switches and overlay evidence items settled
// by a recording whose code label is below aws-recorded. Not implemented
// yet: it lists none.
func (r Report) PendingRetirement() []Retirement { return nil }

// CheckEvidence checks the switches' Ops and Settles against the services
// and the parity baseline. Not implemented yet: it refuses every report.
func (r Report) CheckEvidence() error {
	return &ParityError{Problems: []string{"checking evidence against the parity baseline is not implemented yet"}}
}
