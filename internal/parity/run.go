package parity

import (
	"fmt"
	"io/fs"
	"slices"
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

// Regional is a Transport that signs setup calls for the scenario's region,
// which Call does not carry: Run calls InRegion with it before any call,
// so a setup call creates its resource where the steps then look for it.
type Regional interface {
	InRegion(region string)
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
// every settles entry. A failing step is an outcome, not an error; the
// error is for a scenario that cannot run at all: an unreadable or invalid
// scenario or fixture, a step with neither a fixture nor a rec command, a
// settles field Mutate cannot flip, or a transport error.
//
// Steps run in the scenario's order. Per step, the first rule that applies
// gives the outcome (the exchanges considered are all of them under
// compare all, the final one under final):
//
//  1. no fixture: Pending (a rec command names the step);
//  2. no exchange: Skipped{no-exchange};
//  3. the scenario needs Docker and opts has none: Skipped{docker}, and no
//     setup call of the scenario runs;
//  4. a considered request is missing, has no host or an unrecorded or
//     omitted body: Reconstructed, not sent;
//  5. a considered request is neither awsJson nor awsQuery:
//     Skipped{rest-routing};
//  6. the clock advances, then each considered request is sent;
//  7. a considered response is missing, has no status or an unrecorded
//     body: Reconstructed, sent but not compared;
//  8. the binds are read, then each exchange compared: Match, or Differs
//     naming the paths that differ and the binds that failed.
//
// A setup call runs before the step its before names; one that answers
// outside 2xx, or whose bind fails, makes every step from there on differ,
// naming the call, and nothing later runs.
func Run(fsys fs.FS, id string, t Transport, opts Options) (Result, error) {
	res := Result{Scenario: id}
	sc, err := ReadScenario(fsys, id)
	if err != nil {
		return res, err
	}
	steps, err := loadSteps(fsys, id, sc)
	if err != nil {
		return res, err
	}
	if rt, ok := t.(Regional); ok {
		rt.InRegion(sc.Region)
	}
	r := &runner{id: id, sc: sc, t: t, docker: opts.Docker || !slices.Contains(sc.Needs, NeedDocker)}
	runs := make(map[string]stepRun, len(steps))
	for i, ls := range steps {
		run, err := r.step(i, ls)
		if err != nil {
			return res, err
		}
		runs[run.result.Step] = run
		res.Steps = append(res.Steps, run.result)
	}
	for i, c := range sc.Settles {
		state, err := runs[c.Step].claim(c.Field)
		if err != nil {
			return res, &ScenarioError{Scenario: id, Field: fmt.Sprintf("settles[%d].field", i), Reason: err.Error() + "; settle a field the recorded response holds, or whose parent it holds"}
		}
		res.Claims = append(res.Claims, ClaimResult{Claim: c, State: state})
	}
	return res, nil
}
