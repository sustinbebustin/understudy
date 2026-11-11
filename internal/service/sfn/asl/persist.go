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
	LastTask int64         `json:"lastTask,omitempty"`
	Tasks    []taskRecord  `json:"tasks,omitempty"`
	// JitterSeed and JitterDraws are the FULL retry jitter stream's seed and
	// the draws made from it.
	JitterSeed  uint64      `json:"jitterSeed,omitempty"`
	JitterDraws uint64      `json:"jitterDraws,omitempty"`
	LastRun     int64       `json:"lastRun,omitempty"`
	Runs        []runRecord `json:"runs,omitempty"`
}

// runRecord is a persisted Parallel or Map run.
type runRecord struct {
	ID int64 `json:"id"`
	visitRecord
	// Inputs are the JSON texts of the branch or iteration inputs; Results
	// those of the finished outputs, "" for one not finished.
	Inputs   []string `json:"inputs"`
	Results  []string `json:"results"`
	Launched int      `json:"launched,omitempty"`
	Running  int      `json:"running,omitempty"`
	Finished int      `json:"finished,omitempty"`
	Limit    int      `json:"limit"`
}

// visitRecord is a persisted Task, Parallel or Map state visit.
type visitRecord struct {
	// Run and Index are the visit's frame; zero at the top level.
	Run       int64     `json:"run,omitempty"`
	Index     int       `json:"index,omitempty"`
	State     string    `json:"state"`
	Input     string    `json:"input"`
	EnteredAt time.Time `json:"enteredAt"`
	Entered   int64     `json:"entered"`
	Attempts  []int     `json:"attempts,omitempty"`
}

// taskRecord is a persisted in-flight task.
type taskRecord struct {
	Task     int64  `json:"task"`
	Resource int    `json:"resource"`
	Function string `json:"function"`
	// Async is true for an Event invocation; absent for RequestResponse, so
	// a snapshot saved before Event support loads unchanged.
	Async bool `json:"async,omitempty"`
	// Payload is the payload's JSON text; "" when lambda:invoke sends none.
	Payload string `json:"payload,omitempty"`
	// TimeoutSeconds is the task timeout, always whole seconds.
	TimeoutSeconds int64 `json:"timeoutSeconds"`
	visitRecord
	Started int64 `json:"started"`
	Timer   int64 `json:"timer"`
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
	Run      int64  `json:"run,omitempty"`
	Index    int    `json:"index,omitempty"`
	State    string `json:"state"`
	Input    string `json:"input"`
	Previous int64  `json:"previous"`
}

type waitRecord struct {
	Run     int64  `json:"run,omitempty"`
	Index   int    `json:"index,omitempty"`
	State   string `json:"state"`
	Output  string `json:"output"`
	Entered int64  `json:"entered"`
}

type deadlineRecord struct{}

type taskTimeoutRecord struct {
	Task int64 `json:"task"`
}

type retryRecord struct {
	visitRecord
	Previous int64 `json:"previous"`
}

type runRetryRecord struct {
	visitRecord
	Previous int64 `json:"previous"`
}

func (stepRecord) isJobRecord()        {}
func (waitRecord) isJobRecord()        {}
func (deadlineRecord) isJobRecord()    {}
func (taskTimeoutRecord) isJobRecord() {}
func (retryRecord) isJobRecord()       {}
func (runRetryRecord) isJobRecord()    {}

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
		jsonx.Variant[jobRecord, taskTimeoutRecord]("task-timeout"),
		jsonx.Variant[jobRecord, retryRecord]("retry"),
		jsonx.Variant[jobRecord, runRetryRecord]("run-retry"),
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
	r := executionRecord{
		Started: x.started, LastEvent: x.lastEvent, LastTimer: int64(x.lastTimer), Deleting: x.deleting,
		LastTask: int64(x.lastTask), JitterSeed: x.jitterSeed, JitterDraws: x.jitterDraws, LastRun: x.lastRun,
	}
	for i := range x.runs {
		r.Runs = append(r.Runs, writeRun(&x.runs[i]))
	}
	if x.context != nil {
		r.Context = text(x.context)
	}
	for i := range x.tasks {
		t := &x.tasks[i]
		tr := taskRecord{
			Task: int64(t.invoke.Task), Resource: int(t.invoke.Resource), Function: t.invoke.Function,
			TimeoutSeconds: int64(t.invoke.Timeout / time.Second), visitRecord: writeVisit(t.visit),
			Started: t.started, Timer: int64(t.timer), Async: t.invoke.Invocation == AsyncInvocation,
		}
		if t.invoke.Payload != nil {
			tr.Payload = text(t.invoke.Payload)
		}
		r.Tasks = append(r.Tasks, tr)
	}
	for _, t := range x.timers {
		tr := timerRecord{ID: int64(t.id), At: t.at}
		switch j := t.job.(type) {
		case stepJob:
			tr.Job = stepRecord{Run: j.fr.run, Index: j.fr.index, State: j.state, Input: text(j.input), Previous: j.previous}
		case waitJob:
			tr.Job = waitRecord{Run: j.fr.run, Index: j.fr.index, State: j.state, Output: text(j.output), Entered: j.entered}
		case deadlineJob:
			tr.Job = deadlineRecord{}
		case taskTimeoutJob:
			tr.Job = taskTimeoutRecord{Task: int64(j.task)}
		case retryJob:
			tr.Job = retryRecord{visitRecord: writeVisit(j.visit), Previous: j.previous}
		case runRetryJob:
			tr.Job = runRetryRecord{visitRecord: writeVisit(j.visit), Previous: j.previous}
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
	out := Execution{
		started: r.Started, lastEvent: r.LastEvent, lastTimer: TimerID(r.LastTimer), deleting: r.Deleting,
		jitterSeed: r.JitterSeed, jitterDraws: r.JitterDraws, lastRun: r.LastRun,
	}
	for i := range r.Runs {
		rn, err := readRun(&r.Runs[i])
		if err != nil {
			return err
		}
		out.runs = append(out.runs, rn)
	}
	if r.Context != "" {
		if out.context, err = parseText(r.Context, "context"); err != nil {
			return err
		}
	}
	for _, tr := range r.Timers {
		j, err := readJob(tr)
		if err != nil {
			return err
		}
		out.timers = append(out.timers, armed{id: TimerID(tr.ID), at: tr.At, job: j})
	}
	out.lastTask = TaskID(r.LastTask)
	for i := range r.Tasks {
		t, err := readTask(&r.Tasks[i])
		if err != nil {
			return err
		}
		out.tasks = append(out.tasks, t)
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

// readJob reads a persisted timer's job.
func readJob(tr timerRecord) (job, error) {
	switch j := tr.Job.(type) {
	case stepRecord:
		input, err := parseText(j.Input, "step input")
		if err != nil {
			return nil, err
		}
		return stepJob{fr: frame{run: j.Run, index: j.Index}, state: j.State, input: input, previous: j.Previous}, nil
	case waitRecord:
		output, err := parseText(j.Output, "wait output")
		if err != nil {
			return nil, err
		}
		return waitJob{fr: frame{run: j.Run, index: j.Index}, state: j.State, output: output, entered: j.Entered}, nil
	case deadlineRecord:
		return deadlineJob{}, nil
	case taskTimeoutRecord:
		return taskTimeoutJob{task: TaskID(j.Task)}, nil
	case retryRecord:
		v, err := readVisit(j.visitRecord)
		if err != nil {
			return nil, err
		}
		return retryJob{visit: v, previous: j.Previous}, nil
	case runRetryRecord:
		v, err := readVisit(j.visitRecord)
		if err != nil {
			return nil, err
		}
		return runRetryJob{visit: v, previous: j.Previous}, nil
	}
	return nil, fmt.Errorf("asl: timer %d of a saved execution has no job; the execution cannot be resumed", tr.ID)
}

// writeVisit is v's persisted form.
func writeVisit(v visit) visitRecord {
	return visitRecord{
		Run: v.fr.run, Index: v.fr.index, State: v.state, Input: text(v.input), EnteredAt: v.enteredAt,
		Entered: v.entered, Attempts: v.attempts,
	}
}

// readVisit reads a persisted state visit.
func readVisit(vr visitRecord) (visit, error) {
	input, err := parseText(vr.Input, "state input")
	if err != nil {
		return visit{}, err
	}
	return visit{
		fr: frame{run: vr.Run, index: vr.Index}, state: vr.State, input: input, enteredAt: vr.EnteredAt,
		entered: vr.Entered, attempts: vr.Attempts,
	}, nil
}

// writeRun is r's persisted form.
func writeRun(r *run) runRecord {
	rr := runRecord{
		ID: r.id, visitRecord: writeVisit(r.visit), Launched: r.launched, Running: r.running,
		Finished: r.finished, Limit: r.limit,
		Inputs: make([]string, len(r.inputs)), Results: make([]string, len(r.results)),
	}
	for i, in := range r.inputs {
		rr.Inputs[i] = text(in)
	}
	for i, out := range r.results {
		if out != nil {
			rr.Results[i] = text(out)
		}
	}
	return rr
}

// readRun reads a persisted run.
func readRun(rr *runRecord) (run, error) {
	v, err := readVisit(rr.visitRecord)
	if err != nil {
		return run{}, err
	}
	if len(rr.Results) != len(rr.Inputs) {
		return run{}, fmt.Errorf("asl: run %d of a saved execution has %d inputs but %d results; the snapshot is corrupt",
			rr.ID, len(rr.Inputs), len(rr.Results))
	}
	out := run{
		id: rr.ID, visit: v, launched: rr.Launched, running: rr.Running, finished: rr.Finished, limit: rr.Limit,
		inputs: make([]document.Value, len(rr.Inputs)), results: make([]document.Value, len(rr.Results)),
	}
	for i, in := range rr.Inputs {
		if out.inputs[i], err = parseText(in, "branch or iteration input"); err != nil {
			return run{}, err
		}
	}
	for i, res := range rr.Results {
		if res == "" {
			continue
		}
		if out.results[i], err = parseText(res, "branch or iteration output"); err != nil {
			return run{}, err
		}
	}
	return out, nil
}

// readTask reads a persisted in-flight task.
func readTask(tr *taskRecord) (inflight, error) {
	v, err := readVisit(tr.visitRecord)
	if err != nil {
		return inflight{}, err
	}
	var payload document.Value
	if tr.Payload != "" {
		if payload, err = parseText(tr.Payload, "task payload"); err != nil {
			return inflight{}, err
		}
	}
	kind := ResourceKind(tr.Resource)
	if kind != FunctionARN && kind != LambdaInvoke {
		return inflight{}, fmt.Errorf("asl: task %d of a saved execution has resource kind %d, want %d or %d; the snapshot is corrupt",
			tr.Task, tr.Resource, FunctionARN, LambdaInvoke)
	}
	invocation := SyncInvocation
	if tr.Async {
		invocation = AsyncInvocation
	}
	return inflight{
		invoke: InvokeLambda{
			Task: TaskID(tr.Task), Resource: kind, Invocation: invocation, Function: tr.Function, Payload: payload,
			Timeout: time.Duration(tr.TimeoutSeconds) * time.Second,
		},
		visit:   v,
		started: tr.Started,
		timer:   TimerID(tr.Timer),
	}, nil
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
