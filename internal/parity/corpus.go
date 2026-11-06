package parity

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
)

// scenarioName is the file that makes a fixture directory a scenario.
const scenarioName = "scenario.json"

// scenarioIDPattern is a scenario id, "<svc>/<item>".
var scenarioIDPattern = regexp.MustCompile(`^[a-z0-9]+/[A-Za-z0-9-]+$`)

// ListScenarios returns the id ("<svc>/<item>") of every directory of fsys,
// rooted at testdata/parity, that holds a scenario.json, sorted.
func ListScenarios(fsys fs.FS) ([]string, error) {
	files, err := fs.Glob(fsys, "*/*/"+scenarioName)
	if err != nil {
		return nil, fmt.Errorf("listing parity scenarios: %w", err)
	}
	ids := make([]string, 0, len(files))
	for _, f := range files {
		ids = append(ids, strings.TrimSuffix(f, "/"+scenarioName))
	}
	slices.Sort(ids)
	return ids, nil
}

// ReadScenario reads and validates <id>/scenario.json under fsys, rooted at
// testdata/parity, whose item must be the id's directory. An absent file is
// an error wrapping fs.ErrNotExist; one the format refuses is a
// *ScenarioError naming id.
func ReadScenario(fsys fs.FS, id string) (Scenario, error) {
	_, item, _ := strings.Cut(id, "/")
	if !scenarioIDPattern.MatchString(id) {
		return Scenario{}, &ScenarioError{Scenario: id, Reason: fmt.Sprintf("%q is not a scenario id <svc>/<item>, such as kms/KMS-alias", id)}
	}
	data, err := fs.ReadFile(fsys, id+"/"+scenarioName)
	if err != nil {
		return Scenario{}, fmt.Errorf("reading parity scenario testdata/parity/%s/%s: %w", id, scenarioName, err)
	}
	s, err := ParseScenario(data)
	if se, ok := errors.AsType[*ScenarioError](err); ok {
		return Scenario{}, &ScenarioError{Scenario: id, Field: se.Field, Reason: se.Reason}
	}
	if err != nil {
		return Scenario{}, err
	}
	if s.Item != item {
		return Scenario{}, &ScenarioError{Scenario: id, Field: "item", Reason: fmt.Sprintf("%q is not %q, the scenario directory's name", s.Item, item)}
	}
	return s, nil
}

// ReadFixture reads the fixture at path under fsys, rooted at
// testdata/parity. An absent fixture is an error wrapping fs.ErrNotExist; one
// that does not parse is a *FixtureError naming path.
func ReadFixture(fsys fs.FS, path string) (Fixture, error) {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return Fixture{}, fmt.Errorf("reading parity fixture testdata/parity/%s: %w", path, err)
	}
	f, err := ParseFixture(data)
	if fe, ok := errors.AsType[*FixtureError](err); ok {
		return Fixture{}, &FixtureError{Path: path, Reason: fe.Reason}
	}
	return f, err
}
