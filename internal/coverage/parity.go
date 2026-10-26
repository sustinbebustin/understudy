package coverage

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
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

// parityDoc is baseline.json as ParseParity reads it; the pointers tell an
// absent member from an empty one.
type parityDoc struct {
	Version   *int              `json:"version"`
	Scenarios *[]ParityScenario `json:"scenarios"`
}

// scenarioID is "<svc>/<item>", the directory under testdata/parity.
var scenarioID = regexp.MustCompile(`^[a-z0-9]+/[A-Za-z0-9-]+$`)

// skipReasons are the reasons a skipped step takes.
func skipReasons() []string { return []string{"docker", "rest-routing", "no-exchange"} }

// hiddenBys are what an insensitive claim's by names.
func hiddenBys() []string { return []string{"waiver", "normalizer"} }

// ParseParity reads internal/parity/baseline.json strictly: an unknown or
// missing member, an unknown kind, a reason or by its kind lacks or does
// not take, a field that is not a leaf comparison path, a step listed
// twice or a claim on a step its scenario does not list is a problem, and
// the *ParityError lists every one. It reads the file itself rather than
// through internal/parity, which coverage does not import; a parity test
// checks the two readers agree on the checked-in file.
func ParseParity(data []byte) (ParityRead, error) {
	sums, err := parityCodec()
	if err != nil {
		return ParityRead{}, err
	}
	var doc parityDoc
	if err := jsonx.NewCodec(sums...).Unmarshal(data, &doc); err != nil {
		return ParityRead{}, &ParityError{Problems: []string{"not a baseline document: " + err.Error()}}
	}
	var problems []string
	switch {
	case doc.Version == nil:
		problems = append(problems, "the version member is missing")
	case *doc.Version != 1:
		problems = append(problems, fmt.Sprintf("version %d is not 1, the only version this build reads", *doc.Version))
	}
	if doc.Scenarios == nil {
		problems = append(problems, "the scenarios member is missing")
		return ParityRead{}, &ParityError{Problems: problems}
	}
	seen := map[string]bool{}
	for i, sc := range *doc.Scenarios {
		field := fmt.Sprintf("scenarios[%d]", i)
		if seen[sc.Scenario] {
			problems = append(problems, fmt.Sprintf("%s: scenario %q is listed twice", field, sc.Scenario))
		}
		seen[sc.Scenario] = true
		problems = append(problems, sc.problems(field)...)
	}
	if len(problems) > 0 {
		return ParityRead{}, &ParityError{Problems: problems}
	}
	return ParityRead{Scenarios: *doc.Scenarios}, nil
}

// problems lists what in sc breaks the baseline format, field naming sc.
func (sc ParityScenario) problems(field string) []string {
	var out []string
	if !scenarioID.MatchString(sc.Scenario) {
		out = append(out, fmt.Sprintf("%s.scenario %q is not a scenario id <svc>/<item>", field, sc.Scenario))
	}
	if len(sc.Steps) == 0 {
		out = append(out, field+".steps is empty; every scenario has a step")
	}
	steps := map[string]bool{}
	for i, s := range sc.Steps {
		sf := fmt.Sprintf("%s.steps[%d]", field, i)
		switch {
		case s.Step == "":
			out = append(out, sf+".step is missing")
		case steps[s.Step]:
			out = append(out, fmt.Sprintf("%s.step %q is listed twice", sf, s.Step))
		}
		steps[s.Step] = true
		if s.Fixture == "" {
			out = append(out, sf+".fixture is missing")
		}
		if reason := outcomeProblem(s.Outcome); reason != "" {
			out = append(out, sf+".outcome: "+reason)
		}
	}
	for i, c := range sc.Claims {
		cf := fmt.Sprintf("%s.claims[%d]", field, i)
		if !steps[c.Step] {
			out = append(out, fmt.Sprintf("%s.step %q is not a step of the scenario", cf, c.Step))
		}
		if !leafField(c.Field) {
			out = append(out, fmt.Sprintf("%s.field %q is not a leaf comparison path: status, error/code, error/message, header/<name> or body...", cf, c.Field))
		}
		if reason := targetProblem(c.Target); reason != "" {
			out = append(out, cf+".target: "+reason)
		}
		if reason := resultProblem(c.Result); reason != "" {
			out = append(out, cf+".result: "+reason)
		}
	}
	return out
}

func outcomeProblem(o ParityOutcome) string {
	switch o := o.(type) {
	case nil:
		return "the member is missing"
	case StepDiffers:
		if o.Reason == "" {
			return "a differs outcome needs the reason: the paths that differ"
		}
	case StepSkipped:
		if !slices.Contains(skipReasons(), o.Reason) {
			return fmt.Sprintf("skip reason %q is not one of %s", o.Reason, strings.Join(skipReasons(), ", "))
		}
	case StepMatch, StepReconstructed, StepPending:
	}
	return ""
}

func targetProblem(t ClaimTarget) string {
	switch t := t.(type) {
	case nil:
		return "the member is missing"
	case TargetSwitch:
		if t.Name == "" {
			return "a switch target names the switch"
		}
	case TargetOverlay:
		if ns, name, ok := strings.Cut(t.Shape, "#"); !ok || ns == "" || name == "" {
			return fmt.Sprintf("shape %q is not a shape id, such as com.amazonaws.kms#AlreadyExistsException", t.Shape)
		}
	}
	return ""
}

func resultProblem(r ClaimResult) string {
	switch r := r.(type) {
	case nil:
		return "the member is missing"
	case ClaimInsensitive:
		if !slices.Contains(hiddenBys(), r.By) {
			return fmt.Sprintf("an insensitive result's by %q is not one of %s", r.By, strings.Join(hiddenBys(), ", "))
		}
	case ClaimSettled, ClaimContradicted, ClaimOpen:
	}
	return ""
}

// leafField reports whether f is a comparison path a settles entry may
// name: status, error/code, error/message, header/<lower-case name>, body
// or below it (internal/parity's path grammar, whose exchange/ prefix and
// bare error and header only waivers take).
func leafField(f string) bool {
	switch {
	case f == "status", f == "error/code", f == "error/message", f == "body", strings.HasPrefix(f, "body/"):
		return true
	}
	name, ok := strings.CutPrefix(f, "header/")
	return ok && name != "" && name == strings.ToLower(name) && !strings.Contains(name, "/")
}
