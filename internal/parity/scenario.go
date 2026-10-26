package parity

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"strings"
	"time"
)

// Scenario is one testdata/parity/<svc>/<item>/scenario.json: how to replay
// a record-backlog item and what its steps settle.
type Scenario struct {
	Version int
	// Item is the item directory's name, such as "R01" or "KMS-alias".
	Item string
	// Account is the masked recording account, which the replay signs as
	// (ADR 0012: a 12-digit key is that account's root).
	Account string
	Region  string
	// Client is the client of the Record commands.
	Client  Client
	Needs   []Need
	Setup   []Call
	Steps   []Step
	Waivers []Waiver
	Settles []Claim
	// Record holds the commands that record a queued scenario's steps.
	Record []Command
}

// Need is something a scenario's replay needs beyond the in-process server.
type Need string

// NeedDocker is the need of a scenario that runs Lambda functions.
const NeedDocker Need = "docker"

// Call is a setup request the recording kit ran outside its capture helper.
// It settles nothing.
type Call struct {
	// Service is a registry service id ("kms", "eventbridge").
	Service string
	// Operation is the model's operation name ("CreateKey").
	Operation string
	// Input is the call's input, a compact JSON object.
	Input string
	Bind  []SetupBind
	// Before is the step the call runs before; "" runs it before the
	// first step.
	Before string
}

// Step is one recorded step, replayed in the scenario's order.
type Step struct {
	// Fixture is the fixture path under testdata/parity.
	Fixture string
	Compare CompareMode
	Bind    []Bind
	// Advance moves the emulator clock before the step; 0 moves nothing.
	Advance time.Duration
}

// Name is the step's name: its fixture's base name without ".json".
func (s Step) Name() string {
	name := s.Fixture
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimSuffix(name, ".json")
}

// CompareMode selects which recorded exchanges of a step are replayed and
// compared.
type CompareMode string

// The compare modes.
const (
	// CompareAll replays and compares every exchange, in order.
	CompareAll CompareMode = "all"
	// CompareFinal replays and compares the final exchange only, for steps
	// whose earlier exchanges are client retries.
	CompareFinal CompareMode = "final"
)

// Bind maps the AWS value at Path in a step's final recorded response to
// understudy's value at the same path.
type Bind struct {
	Path  Path
	Shape Shape
}

// SetupBind maps AWS, a value no recorded response holds, to understudy's
// value at Path in a setup call's response.
type SetupBind struct {
	Path  Path
	Shape Shape
	AWS   string
}

// Waiver drops the differences at and below Field in one step.
type Waiver struct {
	Step   string
	Field  Path
	Reason string
}

// Claim is a settles entry: Field of Step settles Target.
type Claim struct {
	Target Target
	Step   string
	Field  Path
}

// Target is what a settles entry settles.
//
//sumtype:decl
type Target interface{ isTarget() }

// SwitchTarget is a fidelity switch, by name.
type SwitchTarget struct {
	Name string
}

// OverlayTarget is an overlay entry, by shape id; Covers, when set, picks
// the evidence item whose covers include every trait listed.
type OverlayTarget struct {
	Shape  string
	Covers []string
}

func (SwitchTarget) isTarget()  {}
func (OverlayTarget) isTarget() {}

// Command is one entry of a queued scenario's record list.
//
//sumtype:decl
type Command interface{ isCommand() }

// RecCommand records Step with the recording kit's rec helper.
type RecCommand struct {
	Step string
	Argv []string
}

// RunCommand runs outside rec; Capture, when set, names the shell variable
// assigned its output.
type RunCommand struct {
	Argv    []string
	Capture string
}

func (RecCommand) isCommand() {}
func (RunCommand) isCommand() {}

// ScenarioError reports a scenario file that does not follow the format.
type ScenarioError struct {
	// Scenario is the scenario id, empty when parsed from bytes alone.
	Scenario string
	// Field locates the offending member, such as "steps[2].bind[0].shape".
	Field  string
	Reason string
}

func (e *ScenarioError) Error() string {
	where := "scenario"
	if e.Scenario != "" {
		where = "testdata/parity/" + e.Scenario + "/scenario.json"
	}
	if e.Field != "" {
		where += ": " + e.Field
	}
	return fmt.Sprintf("%s: %s", where, e.Reason)
}

// ParseScenario reads and validates a scenario file (testdata/README.md,
// scenarios). An unknown, repeated or missing member, or a value the format
// refuses, is a *ScenarioError at that member.
func ParseScenario(data []byte) (Scenario, error) {
	var f scenarioFile
	if err := json.Unmarshal(data, &f, json.RejectUnknownMembers(true)); err != nil {
		return Scenario{}, decodeError("", err)
	}
	s, err := f.scenario()
	if err != nil {
		return Scenario{}, err
	}
	if err := s.check(); err != nil {
		return Scenario{}, err
	}
	return s, nil
}

// Encode writes s in the canonical form: members in the format's order,
// two-space indentation, a trailing newline, empty optional members left
// out, a setup input as nested JSON and an advance as
// time.Duration.String writes it. Encode refuses, as a *ScenarioError, a
// scenario ParseScenario would refuse, so what it writes reads back as s.
func (s Scenario) Encode() ([]byte, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	f, err := s.file()
	if err != nil {
		return nil, fmt.Errorf("encoding scenario %s: %w", s.Item, err)
	}
	data, err := json.Marshal(f, jsontext.WithIndent("  "))
	if err != nil {
		return nil, fmt.Errorf("encoding scenario %s: %w", s.Item, err)
	}
	return append(data, '\n'), nil
}
