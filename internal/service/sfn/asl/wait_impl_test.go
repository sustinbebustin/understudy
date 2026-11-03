package asl_test

import (
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// oneYear is the execution limit understudy takes for a Standard
// execution's one year (stepfunctions-asl.md 3.1; aws-doc
// dg/service-quotas).
const oneYear = 365 * 24 * time.Hour

// A Wait past the one-year execution limit is not refused; the execution
// ends TIMED_OUT at the limit, from Seconds, SecondsPath or a Timestamp
// alike, and a SecondsPath too large for an instant waits as long as any
// other wait past the limit.
func TestWaitPastOneYearTimesOutAtTheLimit(t *testing.T) {
	// inferred, ASL-09
	for _, tt := range []struct{ field, input string }{
		{`"Seconds":99999999`, `{}`},
		{`"SecondsPath":"$.s"`, `{"s":40000000}`},
		{`"SecondsPath":"$.s"`, `{"s":9223372036854775807}`},
		{`"Timestamp":"2099-01-01T00:00:00Z"`, `{}`},
		{`"TimestampPath":"$.t"`, `{"t":"2099-01-01T00:00:00Z"}`},
	} {
		d := newDriver(t, `{"StartAt":"W","States":{"W":{"Type":"Wait",`+tt.field+`,"End":true}}}`, rules())
		d.start(tt.input)
		if o, ok := d.runToEnd(10).(asl.TimedOut); !ok || o != (asl.TimedOut{}) {
			t.Errorf("%s on %s: outcome = %#v, want TimedOut without error or cause", tt.field, tt.input, o)
		}
		last := d.history[len(d.history)-1]
		if last.Type != "ExecutionTimedOut" || !last.Timestamp.Equal(t0.Add(oneYear)) {
			t.Errorf("%s: last event %s at %v, want ExecutionTimedOut at the one-year limit", tt.field, last.Type, last.Timestamp)
		}
	}
}

// TimestampPath that selects nothing, or a value that is not a string,
// fails with the SecondsPath texts' form for TimestampPath; SecondsPath
// holding a numeric string is not a long.
func TestWaitTimestampPathUnrecordedTexts(t *testing.T) {
	// inferred, ASL-09
	for _, tt := range []struct{ input, rest string }{
		{`{}`, "The TimestampPath parameter does not reference an input value: $.t"},
		{`{"t":5}`, "The TimestampPath parameter does not reference a valid ISO-8601 extended offset date-time format string: $.t == 5"},
		{`{"s":"5"}`, ""},
	} {
		field := `"TimestampPath":"$.t"`
		if tt.rest == "" {
			field = `"SecondsPath":"$.s"`
			tt.rest = "The SecondsPath parameter cannot be parsed as a long value: $.s == 5"
		}
		d := newDriver(t, `{"StartAt":"W","States":{"W":{"Type":"Wait",`+field+`,"End":true}}}`, rules())
		d.start(tt.input)
		failedWith(t, d.runToEnd(10), "States.Runtime", func(c string) bool { return c == runtimePrefix("W", 2)+tt.rest })
	}
}

// ExecutionTimedOut points at no event, as ExecutionAborted from
// StopExecution does, and carries empty details; the running Wait writes no
// WaitStateAborted.
func TestExecutionTimedOutEvent(t *testing.T) {
	// inferred, ASL-12
	d := newDriver(t, `{"StartAt":"W","TimeoutSeconds":5,"States":{"W":{"Type":"Wait","Seconds":60,"End":true}}}`, rules())
	d.start(`{}`)
	d.runToEnd(10)
	d.wantShape("understudy's choice", "ExecutionStarted 1<-0", "WaitStateEntered 2<-0", "ExecutionTimedOut 3<-0")
	if got := d.event("ExecutionTimedOut").Details; !equal(got, document.Map{}) {
		t.Errorf("ExecutionTimedOut details = %s, want {}", encode(got))
	}
	if len(d.timers) != 0 {
		t.Errorf("timers still armed after the timeout: %v", d.timers)
	}
}

// A literal Timestamp counts to the second, its fraction truncated
// (stepfunctions-asl.md 7.1, aws-doc dg/state-wait); InputPath selects what
// SecondsPath reads and OutputPath the output (stepfunctions-asl.md 4.1).
func TestWaitTimestampAndPaths(t *testing.T) {
	d := newDriver(t, `{"StartAt":"W","States":{"W":{"Type":"Wait","Timestamp":"2024-01-02T03:05:05.999Z","End":true}}}`, rules())
	d.start(`{}`)
	d.runToEnd(10)
	if at := d.event("WaitStateExited").Timestamp; !at.Equal(time.Date(2024, 1, 2, 3, 5, 5, 0, time.UTC)) {
		t.Errorf("WaitStateExited at %v, want the timestamp truncated to the second", at)
	}

	p := newDriver(t, `{"StartAt":"W","States":{"W":{"Type":"Wait","InputPath":"$.in","SecondsPath":"$.s","OutputPath":"$.out","End":true}}}`, rules())
	p.start(`{"in":{"s":7,"out":{"k":1}}}`)
	o := p.runToEnd(10)
	if at := p.event("WaitStateExited").Timestamp; !at.Equal(t0.Add(7 * time.Second)) {
		t.Errorf("WaitStateExited at %v, want +7s from the effective input", at)
	}
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, `{"k":1}`)) {
		t.Errorf("outcome = %#v, want the OutputPath selection", o)
	}
}

// MachineDeleting survives a jsonx round trip: the loaded execution still
// fails at its next transition. Stop and MachineDeleting before Start do
// not apply.
func TestDeletingSurvivesPersistence(t *testing.T) {
	def := `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":60,"Next":"P"},"P":{"Type":"Pass","End":true}}}`
	r := rules()
	r.EndOnDelete = true
	for _, ev := range []asl.Event{asl.Stop{At: t0}, asl.MachineDeleting{At: t0}} {
		x, hs, effs := asl.Step(mustParse(t, def), r, fixedSource{}, asl.Execution{}, ev)
		if _, done := x.Outcome(); done || len(hs) != 0 || len(effs) != 0 {
			t.Errorf("%T before Start applied: %d events, %d effects", ev, len(hs), len(effs))
		}
	}
	d := newDriver(t, def, r)
	d.start(`{}`)
	d.runUntilWaiting()
	d.apply(asl.MachineDeleting{At: t0})
	raw, err := d.x.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var loaded asl.Execution
	if err := loaded.UnmarshalJSON(raw); err != nil {
		t.Fatalf("UnmarshalJSON(%s): %v", raw, err)
	}
	d.x = loaded
	if _, ok := d.runToEnd(10).(asl.Failed); !ok {
		t.Fatalf("after a reload the deleting execution did not fail:\n%s", d.shape())
	}
	d.wantShape("switch sfn.delete.running-execution", "ExecutionStarted 1<-0", "WaitStateEntered 2<-0", "ExecutionFailed 3<-2")
}
