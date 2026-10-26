package coverage_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/coverage"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
)

// settlesReport holds a switch for each Settles kind and one that states
// none.
func settlesReport() coverage.Report {
	return coverage.Build(coverage.Input{Switches: []fidelity.Entry{
		{
			Name: "a.scenario", Value: "on", Label: evidence.Inferred, Ref: "r.md#1",
			Ops:     []string{"sqs.DeleteMessage", "sqs.DeleteMessageBatch"},
			Settles: fidelity.SettledBy{Step: fidelity.StepRef{Scenario: "sqs/R01", Step: "03-recv-after-6s"}},
		},
		{
			Name: "b.none", Value: "off", Label: evidence.Inferred, Ref: "r.md#2",
			Ops: []string{"kms.Encrypt"}, Settles: fidelity.NoScenario{Reason: "unrecorded"},
		},
		{Name: "c.unstated", Value: "x", Label: evidence.Inferred, Ref: "r.md#3"},
	}})
}

// A switch's Ops and Settles reach the report's JSON in the tagged-union
// convention, a scenario step as "kind":"scenario" with the step reference
// and no scenario as "kind":"none" with the reason, and read back as the
// switches that wrote them.
func TestJSONCarriesSwitchOpsAndSettles(t *testing.T) {
	t.Parallel()
	r := settlesReport()
	data, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON = %v", err)
	}
	for _, want := range []string{
		`"ops":["sqs.DeleteMessage","sqs.DeleteMessageBatch"]`,
		`"settles":{"kind":"scenario","step":"sqs/R01#03-recv-after-6s"}`,
		`"settles":{"kind":"none","reason":"unrecorded"}`,
	} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("JSON lacks %s: %s", want, data)
		}
	}
	if n := bytes.Count(data, []byte(`"settles"`)); n != 2 {
		t.Errorf("JSON has %d settles members, want 2 (none for the switch that states none): %s", n, data)
	}
	got, err := coverage.ParseJSON(data)
	if err != nil {
		t.Fatalf("ParseJSON = %v", err)
	}
	if !reflect.DeepEqual(got.Switches, r.Switches) {
		t.Errorf("switches read back as %+v, want %+v", got.Switches, r.Switches)
	}
}

// A step reference that is not <svc>/<item>#<step> does not read.
func TestParseJSONRefusesABadStepReference(t *testing.T) {
	t.Parallel()
	data, err := settlesReport().JSON()
	if err != nil {
		t.Fatalf("JSON = %v", err)
	}
	bad := strings.Replace(string(data), "sqs/R01#03-recv-after-6s", "sqs-R01-03", 1)
	if _, err := coverage.ParseJSON([]byte(bad)); err == nil || !strings.Contains(err.Error(), "sqs-R01-03") {
		t.Errorf("ParseJSON of a report with step sqs-R01-03 = %v, want an error naming it", err)
	}
}
