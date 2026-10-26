package parity

import (
	"io/fs"
	"time"
)

// Transport carries one scenario's requests to one understudy instance. The
// replay test implements it over an in-process server.
type Transport interface {
	// Send delivers req, signed, and returns understudy's response and the
	// operation the call's journal event names ("kms.CreateAlias"), or ""
	// when none.
	Send(req Outgoing) (Response, string, error)
	// Call issues setup call op of service svc with input, a JSON object,
	// encoded in the operation's protocol and signed.
	Call(svc, op string, input []byte) (Response, error)
	// Advance moves the emulator clock forward by d.
	Advance(d time.Duration) error
}

// Outgoing is a recorded request rewritten for understudy: bound values
// substituted, the headers a fresh signature replaces dropped.
type Outgoing struct {
	Method string
	// Target is the path and query of the recorded URL.
	Target string
	Header []Header
	Body   []byte
	// SigningName is the first DNS label of the recorded host ("kms").
	SigningName string
	Region      string
}

// Options are a run's environment.
type Options struct {
	// Docker reports whether a Docker daemon serves the run.
	Docker bool
}

// Result is the replay of one scenario.
type Result struct {
	Scenario string
	Steps    []StepResult
	Claims   []ClaimResult
}

// StepResult is the replay of one step.
type StepResult struct {
	Step, Fixture string
	// Operation is the operation understudy routed the final sent exchange
	// to, "" when the step was not sent.
	Operation string
	Outcome   Outcome
	// Differences are a Differs step's unwaived differences.
	Differences []Difference
}

// ClaimResult is what the replay says of one settles entry.
type ClaimResult struct {
	Claim Claim
	State ClaimState
}

// Outcome is a replayed step's result.
//
//sumtype:decl
type Outcome interface{ isOutcome() }

// Match is a step understudy answers as AWS did.
type Match struct{}

// Differs is a step with a difference or a failed bind; Reason lists the
// paths.
type Differs struct {
	Reason string
}

// Reconstructed is a step whose recording is incomplete, so it settles
// nothing.
type Reconstructed struct{}

// Skipped is a step this replayer cannot run, and why.
type Skipped struct {
	Reason SkipReason
}

// Pending is a queued step with no fixture yet.
type Pending struct{}

func (Match) isOutcome()         {}
func (Differs) isOutcome()       {}
func (Reconstructed) isOutcome() {}
func (Skipped) isOutcome()       {}
func (Pending) isOutcome()       {}

// SkipReason is why a step is skipped.
type SkipReason string

// The skip reasons.
const (
	// SkipDocker is a step of a scenario that needs Docker when none
	// serves the run.
	SkipDocker SkipReason = "docker"
	// SkipRESTRouting is a request neither awsJson nor awsQuery.
	SkipRESTRouting SkipReason = "rest-routing"
	// SkipNoExchange is a step whose client refused the call before
	// sending it.
	SkipNoExchange SkipReason = "no-exchange"
)

// OutcomeName is an outcome's spelling in baseline.json. Not implemented
// yet: it returns "".
func OutcomeName(Outcome) string { return "" }

// ClaimState is whether a settles entry is settled.
//
//sumtype:decl
type ClaimState interface{ isClaimState() }

// Settled is a claim whose step matches and fails once the field is
// mutated.
type Settled struct{}

// Insensitive is a claim whose step matches also with the field mutated,
// so a waiver or a normalizer hides the field.
type Insensitive struct {
	By HiddenBy
}

// Contradicted is a claim whose step differs from AWS.
type Contradicted struct{}

// Open is a claim whose step was not compared: reconstructed, skipped or
// pending.
type Open struct{}

func (Settled) isClaimState()      {}
func (Insensitive) isClaimState()  {}
func (Contradicted) isClaimState() {}
func (Open) isClaimState()         {}

// HiddenBy is what hides an insensitive field.
type HiddenBy string

// The hiding causes.
const (
	HiddenByWaiver     HiddenBy = "waiver"
	HiddenByNormalizer HiddenBy = "normalizer"
)

// Run reads scenario id and its fixtures from fsys, rooted at
// testdata/parity, replays it through t and runs the sensitivity check of
// every settles entry. Not implemented yet.
func Run(_ fs.FS, id string, _ Transport, _ Options) (Result, error) {
	return Result{Scenario: id}, notImplemented("Run")
}
