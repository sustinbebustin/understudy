package parity

import (
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/coverage"
)

// flatStep and flatClaim are one baseline key as either reader sees it.
type (
	flatStep  struct{ Key, Fixture, Operation, Outcome, Reason string }
	flatClaim struct{ Key, Target, Name, Covers, Result, By string }
)

// The coverage page reads baseline.json with its own reader, since coverage
// does not import parity (contract D13); both readers accept the
// checked-in file and agree on every step's fixture, operation and outcome
// and every claim's target and result.
func TestBaselineReadsInCoverage(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("baseline.json")
	if err != nil {
		t.Fatalf("reading internal/parity/baseline.json: %v; run just parity-baseline", err)
	}
	b, err := ParseBaseline(data)
	if err != nil {
		t.Fatalf("ParseBaseline(baseline.json) = %v", err)
	}
	read, err := coverage.ParseParity(data)
	if err != nil {
		t.Fatalf("coverage.ParseParity(baseline.json) = %v", err)
	}
	var steps, coverageSteps []flatStep
	var claims, coverageClaims []flatClaim
	for _, sc := range b.Scenarios {
		for _, s := range sc.Steps {
			f := flatStep{Key: sc.Scenario + "#" + s.Step, Fixture: s.Fixture, Operation: s.Operation, Outcome: OutcomeName(s.Outcome)}
			switch o := s.Outcome.(type) {
			case Differs:
				f.Reason = o.Reason
			case Skipped:
				f.Reason = string(o.Reason)
			case Match, Reconstructed, Pending:
			}
			steps = append(steps, f)
		}
		for _, c := range sc.Claims {
			f := flatClaim{Key: sc.Scenario + "#" + c.Step + " " + string(c.Field)}
			switch tg := c.Target.(type) {
			case SwitchTarget:
				f.Target, f.Name = "switch", tg.Name
			case OverlayTarget:
				f.Target, f.Name, f.Covers = "overlay", tg.Shape, strings.Join(tg.Covers, ",")
			}
			switch r := c.Result.(type) {
			case Settled:
				f.Result = "settled"
			case Insensitive:
				f.Result, f.By = "insensitive", string(r.By)
			case Contradicted:
				f.Result = "contradicted"
			case Open:
				f.Result = "open"
			}
			claims = append(claims, f)
		}
	}
	for _, sc := range read.Scenarios {
		for _, s := range sc.Steps {
			f := flatStep{Key: sc.Scenario + "#" + s.Step, Fixture: s.Fixture, Operation: s.Operation}
			switch o := s.Outcome.(type) {
			case coverage.StepMatch:
				f.Outcome = "match"
			case coverage.StepDiffers:
				f.Outcome, f.Reason = "differs", o.Reason
			case coverage.StepReconstructed:
				f.Outcome = "reconstructed"
			case coverage.StepSkipped:
				f.Outcome, f.Reason = "skipped", o.Reason
			case coverage.StepPending:
				f.Outcome = "pending"
			}
			coverageSteps = append(coverageSteps, f)
		}
		for _, c := range sc.Claims {
			f := flatClaim{Key: sc.Scenario + "#" + c.Step + " " + c.Field}
			switch tg := c.Target.(type) {
			case coverage.TargetSwitch:
				f.Target, f.Name = "switch", tg.Name
			case coverage.TargetOverlay:
				f.Target, f.Name, f.Covers = "overlay", tg.Shape, strings.Join(tg.Covers, ",")
			}
			switch r := c.Result.(type) {
			case coverage.ClaimSettled:
				f.Result = "settled"
			case coverage.ClaimInsensitive:
				f.Result, f.By = "insensitive", r.By
			case coverage.ClaimContradicted:
				f.Result = "contradicted"
			case coverage.ClaimOpen:
				f.Result = "open"
			}
			coverageClaims = append(coverageClaims, f)
		}
	}
	if len(steps) == 0 {
		t.Fatal("baseline.json holds no step; the check proves nothing")
	}
	if diff := cmp.Diff(steps, coverageSteps); diff != "" {
		t.Errorf("steps: parity (-) and coverage (+) read baseline.json differently:\n%s", diff)
	}
	if diff := cmp.Diff(claims, coverageClaims); diff != "" {
		t.Errorf("claims: parity (-) and coverage (+) read baseline.json differently:\n%s", diff)
	}
}
