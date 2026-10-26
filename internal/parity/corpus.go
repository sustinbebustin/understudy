package parity

import (
	"errors"
	"fmt"
	"io/fs"
)

// ListScenarios returns the id ("<svc>/<item>") of every directory of fsys,
// rooted at testdata/parity, that holds a scenario.json, sorted. Not
// implemented yet.
func ListScenarios(fs.FS) ([]string, error) { return nil, notImplemented("ListScenarios") }

// ReadScenario reads and validates <id>/scenario.json. Not implemented yet.
func ReadScenario(_ fs.FS, id string) (Scenario, error) {
	return Scenario{}, &ScenarioError{Scenario: id, Reason: notImplemented("ReadScenario").Error()}
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
