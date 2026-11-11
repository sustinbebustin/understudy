package parity

import (
	"encoding/json/jsontext"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The spellings the scenario format accepts (testdata/README.md,
// scenarios).
var (
	itemPattern    = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
	accountPattern = regexp.MustCompile(`^\d{12}$`)
	regionPattern  = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-\d$`)
	fixturePattern = regexp.MustCompile(`^[a-z0-9]+/[A-Za-z0-9-]+/[A-Za-z0-9._-]+\.json$`)
	// servicePattern is a registry service id or an ARN's service.
	servicePattern   = regexp.MustCompile(`^[a-z0-9-]+$`)
	operationPattern = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	// signingPattern is a credential scope's service name ("dynamodb",
	// "streams.dynamodb").
	signingPattern = regexp.MustCompile(`^[a-z0-9-]+(?:\.[a-z0-9-]+)*$`)
	switchPattern  = regexp.MustCompile(`^[a-z0-9]+(?:\.[A-Za-z0-9-]+)+$`)
	capturePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

// check says what makes s a scenario the format refuses, as a
// *ScenarioError at the first offending member in file order; nil when s
// is valid. ParseScenario and Encode both apply it, so what Encode writes
// always reads back.
func (s Scenario) check() error {
	steps := make(map[string]bool, len(s.Steps))
	for _, st := range s.Steps {
		steps[st.Name()] = true
	}
	if err := s.checkHead(); err != nil {
		return err
	}
	if err := checkEach("setup", s.Setup, func(c Call, field string) error { return c.check(field, steps) }); err != nil {
		return err
	}
	if err := checkSteps(s.Steps); err != nil {
		return err
	}
	if err := checkEach("waivers", s.Waivers, func(w Waiver, field string) error { return w.check(field, steps) }); err != nil {
		return err
	}
	if err := checkEach("settles", s.Settles, func(c Claim, field string) error { return c.check(field, steps) }); err != nil {
		return err
	}
	return checkEach("record", s.Record, func(c Command, field string) error { return checkCommand(field, c, steps) })
}

// checkHead checks the members before setup.
func (s Scenario) checkHead() error {
	switch {
	case s.Version == 0:
		return fieldError("version", `is missing: write "version": 1`)
	case s.Version != 1:
		return fieldError("version", fmt.Sprintf("%d is not 1, the only scenario format version", s.Version))
	case s.Item == "":
		return fieldError("item", "is empty: write the item directory's name, such as R01 or KMS-alias")
	case !itemPattern.MatchString(s.Item):
		return fieldError("item", fmt.Sprintf("%q is not an item directory's name, of letters, digits and -", s.Item))
	case !accountPattern.MatchString(s.Account):
		return fieldError("account", fmt.Sprintf("%q is not 12 digits: write the masked recording account, 123456789012 (testdata/README.md)", s.Account))
	case !regionPattern.MatchString(s.Region):
		return fieldError("region", fmt.Sprintf("%q is not a region name, such as us-east-2", s.Region))
	case !s.Client.known():
		return fieldError("client", fmt.Sprintf("%q is not one of aws-cli, boto3, curl, http.client, socket", s.Client))
	}
	for i, n := range s.Needs {
		switch {
		case n != NeedDocker:
			return fieldError(index("needs", i), fmt.Sprintf("%q is not a need the replayer knows; the one need is docker", n))
		case slices.Contains(s.Needs[:i], n):
			return fieldError(index("needs", i), fmt.Sprintf("%q is listed twice", n))
		}
	}
	return nil
}

func (c Call) check(field string, steps map[string]bool) error {
	switch {
	case !servicePattern.MatchString(c.Service):
		return fieldError(field+".service", fmt.Sprintf("%q is not a registry service id, such as kms or eventbridge", c.Service))
	case !operationPattern.MatchString(c.Operation):
		return fieldError(field+".operation", fmt.Sprintf("%q is not a model operation name, such as CreateKey", c.Operation))
	}
	if reason := inputReason(c.Input); reason != "" {
		return fieldError(field+".input", reason)
	}
	if err := checkEach(field+".bind", c.Bind, SetupBind.check); err != nil {
		return err
	}
	if c.Before != "" && !steps[c.Before] {
		return fieldError(field+".before", stepReason(c.Before))
	}
	return nil
}

// inputReason says why input is not a setup call's input as a Call holds
// it, a JSON object in heldInput's form; "" when it is one.
func inputReason(input string) string {
	if input == "" {
		return "is missing: write {} for a call without input"
	}
	held, ok := heldInput(jsontext.Value(input))
	switch {
	case !ok:
		return "is not JSON"
	case held.Kind() != jsontext.KindBeginObject:
		return fmt.Sprintf("is a JSON %s; a call's input is an object", kindName(held.Kind()))
	case string(held) != input:
		return "is not in the form ParseScenario holds, " + string(held)
	}
	return ""
}

func (b SetupBind) check(field string) error {
	if err := checkLeaf(field+".path", b.Path); err != nil {
		return err
	}
	if err := checkShape(field+".shape", b.Shape); err != nil {
		return err
	}
	if b.AWS == "" {
		return fieldError(field+".aws", "is missing: a setup bind names the AWS value it stands for, which no recorded response holds")
	}
	return nil
}

// checkSteps checks the steps, whose names are unique.
func checkSteps(steps []Step) error {
	if len(steps) == 0 {
		return fieldError("steps", "lists no step; a scenario replays at least one")
	}
	first := make(map[string]int, len(steps))
	for i, st := range steps {
		field := index("steps", i)
		if err := st.check(field); err != nil {
			return err
		}
		if j, ok := first[st.Name()]; ok {
			return fieldError(field+".fixture", fmt.Sprintf("names step %q, as steps[%d] does; step names are unique in a scenario", st.Name(), j))
		}
		first[st.Name()] = i
	}
	return nil
}

func (s Step) check(field string) error {
	switch {
	case !fixturePattern.MatchString(s.Fixture):
		return fieldError(field+".fixture", fmt.Sprintf("%q is not a fixture path <svc>/<item>/<step>.json under testdata/parity", s.Fixture))
	case s.Name() == "scenario":
		return fieldError(field+".fixture", "names a scenario file, not a fixture")
	case s.Compare == "":
		return fieldError(field+".compare", "is missing: all, or final for a step whose earlier exchanges are client retries")
	case s.Compare != CompareAll && s.Compare != CompareFinal:
		return fieldError(field+".compare", fmt.Sprintf("%q is not all or final", s.Compare))
	}
	if err := checkEach(field+".bind", s.Bind, Bind.check); err != nil {
		return err
	}
	if s.Advance < 0 {
		return fieldError(field+".advance", fmt.Sprintf("%s is negative: an advance moves the emulator clock forward", s.Advance))
	}
	if s.Signing != "" && !signingPattern.MatchString(s.Signing) {
		return fieldError(field+".signing", fmt.Sprintf("%q is not a credential scope's service name, such as dynamodb or streams.dynamodb", s.Signing))
	}
	return nil
}

func (b Bind) check(field string) error {
	if err := checkLeaf(field+".path", b.Path); err != nil {
		return err
	}
	return checkShape(field+".shape", b.Shape)
}

func (w Waiver) check(field string, steps map[string]bool) error {
	if !steps[w.Step] {
		return fieldError(field+".step", stepReason(w.Step))
	}
	if _, err := ParsePath(string(w.Field)); err != nil {
		return fieldError(field+".field", err.Error())
	}
	if w.Reason == "" {
		return fieldError(field+".reason", "is empty: say why the difference is not a finding")
	}
	return nil
}

func (c Claim) check(field string, steps map[string]bool) error {
	if err := checkTarget(field, c.Target); err != nil {
		return err
	}
	if !steps[c.Step] {
		return fieldError(field+".step", stepReason(c.Step))
	}
	return checkLeaf(field+".field", c.Field)
}

func checkTarget(field string, t Target) error {
	switch t := t.(type) {
	case nil:
		return fieldError(field, "names neither a switch nor an overlay entry; a settles entry settles one")
	case SwitchTarget:
		if !switchPattern.MatchString(t.Name) {
			return fieldError(field+".switch", fmt.Sprintf("%q is not a fidelity switch name, such as sqs.fifo.receipt-expiry", t.Name))
		}
	case OverlayTarget:
		if !isShapeID(t.Shape) {
			return fieldError(field+".overlay", fmt.Sprintf("%q is not a shape id namespace#Name, such as com.amazonaws.kms#AlreadyExistsException", t.Shape))
		}
		for k, trait := range t.Covers {
			if !isShapeID(trait) {
				return fieldError(index(field+".covers", k), fmt.Sprintf("%q is not a trait's shape id, such as dev.understudy#statusOverride", trait))
			}
		}
	}
	return nil
}

// isShapeID reports whether s is a shape id: a namespace, "#" and a name.
func isShapeID(s string) bool {
	ns, name, ok := strings.Cut(s, "#")
	return ok && ns != "" && name != ""
}

func checkCommand(field string, c Command, steps map[string]bool) error {
	switch c := c.(type) {
	case nil:
		return fieldError(field, "is empty: a record entry is a rec command {step, argv} or a command run outside rec {argv, capture}")
	case RecCommand:
		if !steps[c.Step] {
			return fieldError(field+".step", stepReason(c.Step))
		}
		return checkArgv(field+".argv", c.Argv)
	case RunCommand:
		if err := checkArgv(field+".argv", c.Argv); err != nil {
			return err
		}
		if c.Capture != "" && !capturePattern.MatchString(c.Capture) {
			return fieldError(field+".capture", fmt.Sprintf("%q is not a shell variable name of upper-case letters, digits and _", c.Capture))
		}
	}
	return nil
}

func checkArgv(field string, argv []string) error {
	if len(argv) == 0 || argv[0] != "aws" {
		return fieldError(field, fmt.Sprintf("%q does not start with aws: record runs AWS CLI commands", argv))
	}
	return nil
}

// checkLeaf checks p, a bind's path or a settles field: a path naming one
// value of a step's final response.
func checkLeaf(field string, p Path) error {
	if _, err := ParsePath(string(p)); err != nil {
		return fieldError(field, err.Error())
	}
	if reason := p.leafReason(); reason != "" {
		return fieldError(field, fmt.Sprintf("%q %s", p, reason))
	}
	return nil
}

// stepReason says why name, a step reference, names no step.
func stepReason(name string) string {
	if name == "" {
		return "is missing: name one of the scenario's steps"
	}
	return fmt.Sprintf("%q is not a step of the scenario; a step's name is its fixture's base name without .json", name)
}

// checkEach checks each element of xs, the members field[i]; the first
// failure stops it.
func checkEach[T any](field string, xs []T, check func(T, string) error) error {
	for i, x := range xs {
		if err := check(x, index(field, i)); err != nil {
			return err
		}
	}
	return nil
}

// index is the field of element i of the array at field.
func index(field string, i int) string { return fmt.Sprintf("%s[%d]", field, i) }

// fieldError is a *ScenarioError at field.
func fieldError(field, reason string) error {
	return &ScenarioError{Field: field, Reason: reason}
}
