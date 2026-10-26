package parity

import (
	"os"
	"strings"
	"testing"
)

// readmeScenarios is the "Replay scenarios" section of testdata/README.md,
// which documents the scenario format for whoever writes the next one.
func readmeScenarios(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../testdata/README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(data), "\n## Replay scenarios\n")
	if !ok {
		t.Fatal(`testdata/README.md has no "## Replay scenarios" section; the scenario format is documented there`)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	return section
}

// The README's example is a scenario the loader accepts, so the documented
// format cannot drift from the one ParseScenario enforces.
func TestReadmeScenarioExampleParses(t *testing.T) {
	t.Parallel()
	_, rest, ok := strings.Cut(readmeScenarios(t), "\n```json\n")
	if !ok {
		t.Fatal("the Replay scenarios section of testdata/README.md has no ```json example")
	}
	example, _, ok := strings.Cut(rest, "\n```\n")
	if !ok {
		t.Fatal("the scenario example in testdata/README.md has no closing ```")
	}
	if _, err := ParseScenario([]byte(example)); err != nil {
		t.Errorf("the scenario example in testdata/README.md does not load: %v; correct the example or the loader", err)
	}
}

// The README names every checked-in scenario, so a new one is documented
// where the format is.
func TestReadmeNamesEveryScenario(t *testing.T) {
	t.Parallel()
	section := readmeScenarios(t)
	ids, err := ListScenarios(recordings())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 {
		t.Fatal("no scenario under testdata/parity; the check proves nothing")
	}
	for _, id := range ids {
		if !strings.Contains(section, "`"+id+"`") {
			t.Errorf("testdata/README.md, Replay scenarios, does not name the checked-in scenario `%s`; add it to the list there", id)
		}
	}
}
