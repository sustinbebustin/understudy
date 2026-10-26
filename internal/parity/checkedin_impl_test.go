package parity

import "testing"

// checkedInScenarios reads every scenario under testdata/parity.
func checkedInScenarios(t *testing.T) map[string]Scenario {
	t.Helper()
	fsys := recordings()
	ids, err := ListScenarios(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 {
		t.Fatal("no scenario under testdata/parity; the check proves nothing")
	}
	scs := make(map[string]Scenario, len(ids))
	for _, id := range ids {
		sc, err := ReadScenario(fsys, id)
		if err != nil {
			t.Fatalf("ReadScenario(%s): %v", id, err)
		}
		scs[id] = sc
	}
	return scs
}

// Steps replay in the order a scenario lists them, never in file-name
// order, and state carries from one step to the next (a receipt handle, a
// deleted message), so a scenario lists them in the order they were
// recorded: by the X-Amz-Date each step's first request was signed with. A
// step without one is not placed.
func TestCheckedInStepsAreInRecordedOrder(t *testing.T) {
	t.Parallel()
	for id, sc := range checkedInScenarios(t) {
		prev, prevStep := "", ""
		for _, s := range sc.Steps {
			f, err := ReadFixture(recordings(), s.Fixture)
			if err != nil {
				t.Fatalf("%s: %v", id, err)
			}
			signed, ok := firstSigned(f)
			if !ok {
				continue
			}
			if signed < prev {
				t.Errorf("%s lists %s (signed %s) after %s (signed %s); list the steps in the order they were recorded", id, s.Name(), signed, prevStep, prev)
			}
			prev, prevStep = signed, s.Name()
		}
	}
}

// firstSigned is the X-Amz-Date of f's first recorded request, whose
// YYYYMMDD'T'HHMMSS'Z' form orders as text.
func firstSigned(f Fixture) (string, bool) {
	for _, x := range f.Exchanges {
		if x.Request != nil {
			return firstHeader(x.Request.Headers, "X-Amz-Date")
		}
	}
	return "", false
}

// A switch names the one scenario step that settles it (fidelity.SettledBy),
// and every claim on the switch must be that step's, so the corpus claims
// each switch in one step at most.
func TestCheckedInSwitchClaimsNameOneStep(t *testing.T) {
	t.Parallel()
	claimed := map[string]string{}
	for id, sc := range checkedInScenarios(t) {
		for _, c := range sc.Settles {
			sw, ok := c.Target.(SwitchTarget)
			if !ok {
				continue
			}
			ref := id + "#" + c.Step
			if was, dup := claimed[sw.Name]; dup && was != ref {
				t.Errorf("switch %s is claimed by %s and %s; keep the claim on the one step that settles it", sw.Name, was, ref)
			}
			claimed[sw.Name] = ref
		}
	}
}
