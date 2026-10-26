package parity

import "io/fs"

// ListScenarios returns the id ("<svc>/<item>") of every directory of fsys,
// rooted at testdata/parity, that holds a scenario.json, sorted. Not
// implemented yet.
func ListScenarios(fs.FS) ([]string, error) { return nil, notImplemented("ListScenarios") }

// ReadScenario reads and validates <id>/scenario.json. Not implemented yet.
func ReadScenario(_ fs.FS, id string) (Scenario, error) {
	return Scenario{}, &ScenarioError{Scenario: id, Reason: notImplemented("ReadScenario").Error()}
}

// ReadFixture reads the fixture at path under fsys; an absent fixture is an
// error wrapping fs.ErrNotExist. Not implemented yet.
func ReadFixture(_ fs.FS, path string) (Fixture, error) {
	return Fixture{}, &FixtureError{Path: path, Reason: notImplemented("ReadFixture").Error()}
}
