package parity

import (
	"bytes"
	"testing"
)

// Every checked-in scenario is in the canonical form Encode writes, so a
// hand edit that drifts from it is caught here rather than in review.
func TestCheckedInScenariosAreCanonical(t *testing.T) {
	t.Parallel()
	fsys := recordings()
	ids, err := ListScenarios(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 {
		t.Fatal("no scenario under testdata/parity; the check proves nothing")
	}
	for _, id := range ids {
		sc, err := ReadScenario(fsys, id)
		if err != nil {
			t.Errorf("ReadScenario(%s): %v", id, err)
			continue
		}
		enc, err := sc.Encode()
		if err != nil {
			t.Errorf("Encode(%s): %v", id, err)
			continue
		}
		if raw := readRecorded(t, id+"/scenario.json"); !bytes.Equal(enc, raw) {
			t.Errorf("testdata/parity/%s/scenario.json is not in canonical form; write it as Scenario.Encode does:\n%s", id, enc)
		}
	}
}
