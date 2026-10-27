package asl_test

import (
	"bytes"
	"testing"

	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// TestASL16HistoryLimit: a machine that loops through Pass states forever
// fails with States.Runtime when the next state would take the history past
// 25,000 events (aws-doc dg/service-quotas); the error and where it stops
// are understudy's choice.
func TestASL16HistoryLimit(t *testing.T) {
	// inferred, ASL-16
	d := newDriver(t, `{"StartAt":"A","States":{"A":{"Type":"Pass","Next":"B"},"B":{"Type":"Pass","Next":"A"},"S":{"Type":"Succeed"}}}`, rules())
	d.start(`{}`)
	o := d.runToEnd(20_000)
	failedWith(t, o, "States.Runtime", func(c string) bool { return c != "" })
	last := d.history[len(d.history)-1]
	if last.Type != "ExecutionFailed" || last.ID > 25_000 || last.ID != int64(len(d.history)) {
		t.Fatalf("last event %s %d of %d events, want ExecutionFailed within 25,000", last.Type, last.ID, len(d.history))
	}
	if prev := d.history[len(d.history)-2]; last.PreviousEventID != prev.ID {
		t.Errorf("ExecutionFailed points at %d, want the last event written, %d", last.PreviousEventID, prev.ID)
	}
}

// Parse reports every problem in document order; CreateStateMachine renders
// the first (contract 5.1).
func TestASL01EveryProblemInDocumentOrder(t *testing.T) {
	// inferred, ASL-01
	_, diags := asl.Parse(`{"StartAt":"P","States":{"P":{"Type":"Pass","Next":"Nope"},"W":{"Type":"Wait","Seconds":1,"End":true}},"Extra":1}`,
		asl.NewFeatures(asl.FeatPass, asl.FeatSucceed))
	locations := make([]string, 0, len(diags))
	for _, d := range diags {
		locations = append(locations, d.Location)
	}
	want := []string{"/States/P/Next", "/States/W", "/"}
	if len(locations) != len(want) {
		t.Fatalf("diagnostics at %v, want %v", locations, want)
	}
	for i := range want {
		if locations[i] != want[i] {
			t.Fatalf("diagnostics at %v, want %v", locations, want)
		}
	}
	if diags[1].Code != asl.CodeUnsupported {
		t.Errorf("Wait diagnostic = %+v, want %s", diags[1], asl.CodeUnsupported)
	}
}

// A construct outside the accepted features is refused, so a caller that
// accepts only the features whose runtime has landed never gets a
// definition the engine cannot run (spec "Every commit is honest").
func TestParseRefusesWhatTheEngineCannotRun(t *testing.T) {
	landed := asl.NewFeatures(asl.FeatPass, asl.FeatSucceed)
	for _, def := range []string{
		`{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":1,"End":true}}}`,
		`{"StartAt":"P","TimeoutSeconds":5,"States":{"P":{"Type":"Pass","End":true}}}`,
		`{"StartAt":"P","States":{"P":{"Type":"Pass","InputPath":"$.a","End":true}}}`,
	} {
		d, diags := asl.Parse(def, landed)
		if d != nil || len(diags) == 0 || diags[0].Code != asl.CodeUnsupported {
			t.Errorf("Parse(%s) = %v, %+v; want %s", def, d != nil, diags, asl.CodeUnsupported)
		}
	}
}

// A Pass state ending a loop-free machine with no terminal state is
// MISSING_END_STATE; a Pass without Next or End, or with both, is a schema
// error (stepfunctions-asl.md 3.2).
func TestParseTransitions(t *testing.T) {
	tests := []struct{ def, code string }{
		{`{"StartAt":"A","States":{"A":{"Type":"Pass","Next":"B"},"B":{"Type":"Pass","Next":"A"}}}`, "MISSING_END_STATE"},
		{`{"StartAt":"A","States":{"A":{"Type":"Pass"}}}`, "SCHEMA_VALIDATION_FAILED"},
		{`{"StartAt":"Nope","States":{"A":{"Type":"Pass","End":true}}}`, "MISSING_TRANSITION_TARGET"},
	}
	for _, tt := range tests {
		d, diags := asl.Parse(tt.def, allFeatures)
		if d != nil || len(diags) == 0 || diags[0].Code != tt.code {
			t.Errorf("Parse(%s) = %v, %+v; want %s", tt.def, d != nil, diags, tt.code)
		}
	}
}

// A jsonx round trip of the execution between any two steps of a Pass and
// Succeed machine leaves the rest of the run unchanged (contract 5.4).
func TestPassPersistenceRoundTrip(t *testing.T) {
	def := `{"StartAt":"A","States":{"A":{"Type":"Pass","Result":{"k":[1,"x",null]},"Next":"B"},"B":{"Type":"Pass","Next":"S"},"S":{"Type":"Succeed"}}}`
	whole := newDriver(t, def, rules())
	whole.start(`{"in":true}`)
	whole.runToEnd(20)
	for cut := range 4 {
		d := newDriver(t, def, rules())
		d.start(`{"in":true}`)
		for range cut {
			d.next()
		}
		raw, err := d.x.MarshalJSON()
		if err != nil {
			t.Fatalf("MarshalJSON: %v", err)
		}
		var loaded asl.Execution
		if err := loaded.UnmarshalJSON(raw); err != nil {
			t.Fatalf("UnmarshalJSON(%s): %v", raw, err)
		}
		if again, err := loaded.MarshalJSON(); err != nil || !bytes.Equal(again, raw) {
			t.Fatalf("re-marshal = %s, %v; want %s", again, err, raw)
		}
		d.x = loaded
		d.runToEnd(20)
		if fingerprint(d.history) != fingerprint(whole.history) {
			t.Fatalf("history after a reload at step %d:\n%s\nwant\n%s", cut, fingerprint(d.history), fingerprint(whole.history))
		}
	}
}
