package asl

import (
	"fmt"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// executionRecord is the persisted form of Execution. JSON values are kept
// as their text, so member order survives the codec's sorting.
type executionRecord struct {
	Started   bool          `json:"started,omitempty"`
	LastEvent int64         `json:"lastEvent,omitempty"`
	LastTimer int64         `json:"lastTimer,omitempty"`
	Timers    []timerRecord `json:"timers,omitempty"`
	// Context is the JSON text of the start context; "" before Start.
	Context  string        `json:"context,omitempty"`
	Outcome  outcomeRecord `json:"outcome,omitzero"`
	Deleting bool          `json:"deleting,omitempty"`
}

type timerRecord struct {
	ID  int64     `json:"id"`
	At  time.Time `json:"at"`
	Job jobRecord `json:"job"`
}

// jobRecord is the persisted job: stepRecord, waitRecord or
// deadlineRecord.
type jobRecord interface{ isJobRecord() }

type stepRecord struct {
	State    string `json:"state"`
	Input    string `json:"input"`
	Previous int64  `json:"previous"`
}

type waitRecord struct {
	State   string `json:"state"`
	Output  string `json:"output"`
	Entered int64  `json:"entered"`
}

type deadlineRecord struct{}

func (stepRecord) isJobRecord()     {}
func (waitRecord) isJobRecord()     {}
func (deadlineRecord) isJobRecord() {}

// outcomeRecord is the persisted outcome.
type outcomeRecord interface{ isOutcomeRecord() }

type succeededRecord struct {
	Output string `json:"output"`
}

type errorRecord struct {
	Error string `json:"error,omitempty"`
	Cause string `json:"cause,omitempty"`
}

type (
	failedRecord   errorRecord
	timedOutRecord errorRecord
	abortedRecord  errorRecord
)

func (succeededRecord) isOutcomeRecord() {}
func (failedRecord) isOutcomeRecord()    {}
func (timedOutRecord) isOutcomeRecord()  {}
func (abortedRecord) isOutcomeRecord()   {}

// recordCodec is the codec of executionRecord and its sums.
func recordCodec() (jsonx.Codec, error) {
	jobs, err := jsonx.NewUnion("asl.jobRecord",
		jsonx.Variant[jobRecord, stepRecord]("step"),
		jsonx.Variant[jobRecord, waitRecord]("wait"),
		jsonx.Variant[jobRecord, deadlineRecord]("deadline"),
	)
	if err != nil {
		return jsonx.Codec{}, err
	}
	outcomes, err := jsonx.NewUnion("asl.outcomeRecord",
		jsonx.Variant[outcomeRecord, succeededRecord]("succeeded"),
		jsonx.Variant[outcomeRecord, failedRecord]("failed"),
		jsonx.Variant[outcomeRecord, timedOutRecord]("timed-out"),
		jsonx.Variant[outcomeRecord, abortedRecord]("aborted"),
	)
	if err != nil {
		return jsonx.Codec{}, err
	}
	return jsonx.NewCodec(jobs, outcomes), nil
}

// MarshalJSON writes x in the jsonx tagged-union convention
// (ARCHITECTURE.md 13).
func (x Execution) MarshalJSON() ([]byte, error) {
	c, err := recordCodec()
	if err != nil {
		return nil, err
	}
	r := executionRecord{Started: x.started, LastEvent: x.lastEvent, LastTimer: int64(x.lastTimer), Deleting: x.deleting}
	if x.context != nil {
		r.Context = text(x.context)
	}
	for _, t := range x.timers {
		tr := timerRecord{ID: int64(t.id), At: t.at}
		switch j := t.job.(type) {
		case stepJob:
			tr.Job = stepRecord{State: j.state, Input: text(j.input), Previous: j.previous}
		case waitJob:
			tr.Job = waitRecord{State: j.state, Output: text(j.output), Entered: j.entered}
		case deadlineJob:
			tr.Job = deadlineRecord{}
		}
		r.Timers = append(r.Timers, tr)
	}
	switch o := x.outcome.(type) {
	case Succeeded:
		r.Outcome = succeededRecord{Output: text(o.Output)}
	case Failed:
		r.Outcome = failedRecord(o)
	case TimedOut:
		r.Outcome = timedOutRecord(o)
	case Aborted:
		r.Outcome = abortedRecord(o)
	}
	return c.Marshal(r)
}

// UnmarshalJSON reads what MarshalJSON wrote.
func (x *Execution) UnmarshalJSON(b []byte) error {
	c, err := recordCodec()
	if err != nil {
		return err
	}
	var r executionRecord
	if err := c.Unmarshal(b, &r); err != nil {
		return err
	}
	out := Execution{started: r.Started, lastEvent: r.LastEvent, lastTimer: TimerID(r.LastTimer), deleting: r.Deleting}
	if r.Context != "" {
		if out.context, err = parseText(r.Context, "context"); err != nil {
			return err
		}
	}
	for _, tr := range r.Timers {
		t := armed{id: TimerID(tr.ID), at: tr.At}
		switch j := tr.Job.(type) {
		case stepRecord:
			input, err := parseText(j.Input, "step input")
			if err != nil {
				return err
			}
			t.job = stepJob{state: j.State, input: input, previous: j.Previous}
		case waitRecord:
			output, err := parseText(j.Output, "wait output")
			if err != nil {
				return err
			}
			t.job = waitJob{state: j.State, output: output, entered: j.Entered}
		case deadlineRecord:
			t.job = deadlineJob{}
		default:
			return fmt.Errorf("asl: timer %d of a saved execution has no job; the execution cannot be resumed", tr.ID)
		}
		out.timers = append(out.timers, t)
	}
	switch o := r.Outcome.(type) {
	case succeededRecord:
		output, err := parseText(o.Output, "output")
		if err != nil {
			return err
		}
		out.outcome = Succeeded{Output: output}
	case failedRecord:
		out.outcome = Failed(o)
	case timedOutRecord:
		out.outcome = TimedOut(o)
	case abortedRecord:
		out.outcome = Aborted(o)
	}
	*x = out
	return nil
}

// parseText reads JSON text written by text; what names it in the error.
func parseText(s, what string) (document.Value, error) {
	d := jsonx.NewDecoder([]byte(s))
	v, err := d.ReadValue()
	if err == nil {
		err = d.End()
	}
	if err != nil {
		return nil, fmt.Errorf("asl: the saved execution's %s is not JSON (%w); the snapshot is corrupt", what, err)
	}
	return v, nil
}
