package asl

import (
	"slices"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// maxInlineConcurrency is the most iterations an inline Map runs at once:
// Inline mode "supports up to 40 concurrent iterations", and MaxConcurrency
// 0 means no limit beyond it (stepfunctions-asl.md 9.2; aws-doc
// dg/state-map-inline).
const maxInlineConcurrency = 40

// block is what Parallel and Map states share: the prefix of their event
// types, the fields that process the run's result, and Retry and Catch.
type block struct {
	kind           string // "Parallel" or "Map"
	name, next     string
	io             paths
	resultSelector template
	resultPath     target
	retry          []Retrier
	catch          []catcher
}

// blockOf is the Parallel or Map state named name; false, with the
// execution failed after last, when the definition lacks it.
func (s *stepper) blockOf(name string, last int64) (block, bool) {
	switch st := s.d.states[name].(type) {
	case parallelState:
		return block{
			kind: "Parallel", name: st.name, next: st.next, io: st.io, resultSelector: st.resultSelector,
			resultPath: st.resultPath, retry: st.retry, catch: st.catch,
		}, true
	case mapState:
		return block{
			kind: "Map", name: st.name, next: st.next, io: st.io, resultSelector: st.resultSelector,
			resultPath: st.resultPath, retry: st.retry, catch: st.catch,
		}, true
	case passState, succeedState, failState, choiceState, waitState, taskState, nil:
	}
	// Parse built the state the run names; a definition without it was
	// changed under a running execution.
	s.fail(errRuntime, "The state '"+name+"' is not in the definition the execution runs.", last)
	return block{}, false
}

// startRun starts the Parallel or Map state of visit v: it works out the
// inputs of its branches or iterations from the effective input, writes
// its *StateStarted event pointing at previous, and starts as many
// branches or iterations as may run at once (stepfunctions-asl.md 9.1,
// 9.2, 11.3). Branches and iterations start at the same instant, their
// first steps armed in index order, so steps due together run in branch or
// item order (inferred, ASL-13).
func (s *stepper) startRun(st state, v visit, previous int64) {
	b, ok := s.blockOf(v.state, previous)
	if !ok {
		return
	}
	sc := s.scopeAt(v.state, v.enteredAt)
	effective, f := sc.selectFrom(b.io.input, v.input)
	var inputs []document.Value
	var limit int
	if f == nil {
		switch st := st.(type) {
		case parallelState:
			inputs, f = sc.branchInputs(st, effective)
			limit = len(inputs)
		case mapState:
			inputs, limit, f = s.iterationInputs(st, v, effective)
		case passState, succeedState, failState, choiceState, waitState, taskState:
			// blockOf admitted only the two run states.
		}
	}
	if f != nil {
		s.runFailed(b, v, previous, f, false)
		return
	}
	// The run writes its started event and one event per branch or
	// iteration it starts before the execution's last event (inferred,
	// ASL-16).
	if s.x.lastEvent+int64(min(len(inputs), limit))+4 > maxHistoryEvents {
		s.fail(errRuntime, HistoryLimitCause, s.x.lastEvent)
		return
	}
	var details document.Value
	if b.kind == "Map" {
		details = document.Map{{Key: "length", Value: document.Number(strconv.Itoa(len(inputs)))}}
	}
	started := s.emit(b.kind+"StateStarted", previous, details)
	if m, isMap := st.(mapState); isMap && m.itemSelector != nil {
		// ItemSelector fails after MapStateStarted, the failure pointing at
		// it (ASL-04, recorded 2026-11-03).
		if inputs, f = s.selectItems(m, v, effective, inputs); f != nil {
			s.runFailed(b, v, started, f, false)
			return
		}
	}
	s.x.lastRun++
	s.x.runs = append(s.x.runs, run{id: s.x.lastRun, visit: v, inputs: inputs, results: make([]document.Value, len(inputs)), limit: limit})
	i := len(s.x.runs) - 1
	if len(inputs) == 0 {
		s.runSucceeded(i, started)
		return
	}
	r := &s.x.runs[i]
	for r.launched < len(r.inputs) && r.running < r.limit {
		s.launch(r, started)
	}
}

// branchInputs are a Parallel's branch inputs: each gets the effective
// input, Parameters applied (stepfunctions-asl.md 9.1).
func (sc scope) branchInputs(st parallelState, effective document.Value) ([]document.Value, Failure) {
	if st.parameters != nil {
		var f Failure
		if effective, f = sc.render(st.parameters, effective); f != nil {
			return nil, f
		}
	}
	inputs := make([]document.Value, len(st.branches))
	for i := range inputs {
		inputs[i] = effective
	}
	return inputs, nil
}

// iterationInputs are an inline Map's items and how many run at once
// (stepfunctions-asl.md 9.2): ItemsPath selects the items from the
// effective input. selectItems turns them into iteration inputs.
func (s *stepper) iterationInputs(st mapState, v visit, effective document.Value) ([]document.Value, int, Failure) {
	ctx := stateContext(s.x.context, v.state, v.enteredAt)
	sc := scope{context: ctx, src: s.src, rules: s.rules}
	limit, f := sc.mapLimit(st.concurrency, effective)
	if f != nil {
		return nil, 0, f
	}
	got, err := st.itemsPath.Eval(effective, ctx)
	if err != nil {
		return nil, 0, Terminal{Name: errRuntime, Cause: err.Error()}
	}
	items, ok := got.(document.List)
	if !ok {
		// Inline mode accepts only JSON arrays (aws-doc dg/state-map-inline);
		// AWS's text for anything else is unrecorded (UNVERIFIED).
		return nil, 0, Terminal{Name: errRuntime, Cause: "The ItemsPath field '" + st.itemsPath.String() +
			"' selected " + text(got) + ", which is not an array"}
	}
	return slices.Clone(items), limit, nil
}

// selectItems applies the Map's ItemSelector to each of items: it builds
// the iteration's input from the effective input and $$.Map.Item {Index,
// Value} (stepfunctions-asl.md 9.2).
func (s *stepper) selectItems(st mapState, v visit, effective document.Value, items []document.Value) ([]document.Value, Failure) {
	ctx := stateContext(s.x.context, v.state, v.enteredAt)
	inputs := make([]document.Value, len(items))
	for i, item := range items {
		isc := scope{src: s.src, rules: s.rules}
		isc.context = append(slices.Clone(ctx), document.Entry{Key: "Map", Value: document.Map{
			{Key: "Item", Value: document.Map{
				{Key: "Index", Value: document.Number(strconv.Itoa(i))},
				{Key: "Value", Value: item},
			}},
		}})
		var f Failure
		if inputs[i], f = isc.render(st.itemSelector, effective); f != nil {
			return nil, f
		}
	}
	return inputs, nil
}

// mapLimit is how many iterations run at once: MaxConcurrency, or the
// MaxConcurrencyPath value in the effective input, with 0 meaning
// maxInlineConcurrency (stepfunctions-asl.md 9.2). A MaxConcurrencyPath
// value that is not an integer fails with States.Runtime and the recorded
// text (stepfunctions-asl.md 4.6); a negative one left AWS's execution
// with only ExecutionStarted, so understudy's States.Runtime cause for it
// is UNVERIFIED.
func (sc scope) mapLimit(c concurrency, input document.Value) (int, Failure) {
	var n int64
	switch c := c.(type) {
	case maxConcurrency:
		n = c.n
	case maxConcurrencyPath:
		v, err := c.path.Eval(input, sc.context)
		if err != nil {
			return 0, Terminal{Name: errRuntime, Cause: err.Error()}
		}
		num, isNumber := v.(document.Number)
		got, err := num.Int64()
		if !isNumber || err != nil {
			return 0, Terminal{Name: errRuntime, Cause: "The MaxConcurrencyPath field refers to value \"" + shown(v) +
				"\" which is not a valid integer: " + c.path.String()}
		}
		if got < 0 {
			return 0, Terminal{Name: errRuntime, Cause: "The MaxConcurrencyPath field refers to value \"" + shown(v) +
				"\" which is negative: " + c.path.String()}
		}
		n = got
	}
	if n == 0 || n > maxInlineConcurrency {
		return maxInlineConcurrency, nil
	}
	return int(n), nil
}

// launch starts r's next branch or iteration: a Map iteration writes
// MapIterationStarted pointing at previous, and the branch's or
// iteration's first state points at that, or for a Parallel branch at
// previous (stepfunctions-asl.md 11.2 rule 6, 11.3).
func (s *stepper) launch(r *run, previous int64) {
	i := r.launched
	r.launched++
	r.running++
	first := previous
	var start string
	switch st := s.d.states[r.state].(type) {
	case parallelState:
		start = st.branches[i]
	case mapState:
		start = st.processor
		first = s.emit("MapIterationStarted", previous, iterationDetails(r.state, i))
	case passState, succeedState, failState, choiceState, waitState, taskState, nil:
		// A run exists only for the two run states (blockOf).
	}
	s.arm(s.at, stepJob{fr: frame{run: r.id, index: i}, state: start, input: r.inputs[i], previous: first})
}

// iterationDetails are a MapIteration* event's {name, index}
// (stepfunctions-asl.md 11.3).
func iterationDetails(name string, index int) document.Map {
	return document.Map{
		{Key: "name", Value: document.String(name)},
		{Key: "index", Value: document.Number(strconv.Itoa(index))},
	}
}

// runAt is the index in s.x.runs of the run id; -1 when it is not running.
func (s *stepper) runAt(id int64) int {
	return slices.IndexFunc(s.x.runs, func(r run) bool { return r.id == id })
}

// scopeDone ends the step's frame with output, its last event last: at the
// top level the execution succeeds; a branch or iteration hands its output
// to its run. A Map iteration writes MapIterationSucceeded pointing at
// last, and the next waiting iteration starts pointing at last too, not at
// MapIterationSucceeded (stepfunctions-asl.md 11.2 rule 6). The run
// succeeds when its last branch or iteration finishes.
func (s *stepper) scopeDone(output document.Value, last int64) {
	if s.fr.run == 0 {
		s.succeed(output, last)
		return
	}
	i := s.runAt(s.fr.run)
	if i < 0 {
		// A stopped run's jobs are withdrawn with it.
		s.fail(errRuntime, "A branch or iteration finished outside its Parallel or Map state.", last)
		return
	}
	r := &s.x.runs[i]
	r.results[s.fr.index] = output
	r.running--
	r.finished++
	done := last
	if _, isMap := s.d.states[r.state].(mapState); isMap {
		done = s.emit("MapIterationSucceeded", last, iterationDetails(r.state, s.fr.index))
		for r.launched < len(r.inputs) && r.running < r.limit {
			s.launch(r, last)
		}
	}
	if r.finished == len(r.inputs) {
		s.runSucceeded(i, done)
	}
}

// runSucceeded ends the run at index i whose last inner event is last:
// *StateSucceeded and *StateExited both point at last (stepfunctions-asl.md
// 11.2 rule 5, recorded for Map; for Parallel inferred, ASL-13). The
// result is the outputs in branch or item order whatever the finishing
// order (9.1, 9.2), then ResultSelector, ResultPath into the raw input and
// OutputPath; a failure there comes after *StateSucceeded, as a Task's
// does after its succeeded event.
func (s *stepper) runSucceeded(i int, last int64) {
	r := s.x.runs[i]
	s.x.runs = slices.Delete(s.x.runs, i, i+1)
	s.fr = r.fr
	b, ok := s.blockOf(r.state, last)
	if !ok {
		return
	}
	succeeded := s.emit(b.kind+"StateSucceeded", last, nil)
	sc := s.scopeAt(r.state, r.enteredAt)
	var result document.Value = document.List(r.results)
	var f Failure
	if b.resultSelector != nil {
		result, f = sc.render(b.resultSelector, result)
	}
	var out document.Value
	if f == nil {
		out, f = placeResult(b.resultPath, r.input, result)
	}
	if f == nil {
		out, f = sc.selectFrom(b.io.output, out)
	}
	if f == nil {
		f = dataLimit(b.name, out)
	}
	if f != nil {
		s.runFailed(b, r.visit, succeeded, f, false)
		return
	}
	exited := s.emit(b.kind+"StateExited", last, stateExited(b.name, out))
	s.transition(b.next, out, exited)
}

// scopeFailed ends the step's frame with f, its last event last: at the top
// level the execution fails; a failing branch or iteration fails its run,
// whose other branches or iterations are stopped (stepfunctions-asl.md 9.1,
// 9.2). A Map iteration writes MapIterationFailed pointing at last, after
// the events of the siblings it stops (abortWaitingIterations). A Terminal
// fails the execution at once from any depth: ExecutionFailed points at
// last, with no MapIterationFailed or *StateFailed between (ASL-05, ASL-08
// and ASL-15 in a Parallel branch, ASL-07 in a Map iteration, recorded
// 2026-11-03).
func (s *stepper) scopeFailed(f Failure, last int64) {
	if t, ok := f.(Terminal); ok {
		s.fail(t.Name, t.Cause, last)
		return
	}
	if s.fr.run == 0 {
		if e, ok := f.(StateError); ok {
			s.fail(e.Name, e.Cause, last)
		}
		return
	}
	i := s.runAt(s.fr.run)
	if i < 0 {
		s.fail(errRuntime, "A branch or iteration failed outside its Parallel or Map state.", last)
		return
	}
	r := s.x.runs[i]
	if _, isMap := s.d.states[r.state].(mapState); isMap {
		s.abortWaitingIterations(r, s.fr.index, last)
		s.emit("MapIterationFailed", last, iterationDetails(r.state, s.fr.index))
	}
	s.stopRun(r.id)
	s.fr = r.fr
	b, ok := s.blockOf(r.state, last)
	if !ok {
		return
	}
	s.runFailed(b, r.visit, last, f, true)
}

// runFailed handles a failure of the Parallel or Map state b in visit v
// whose last event is last; started says the failure came from a branch or
// iteration. A StateError goes to Retry, which starts every branch or
// iteration again (stepfunctions-asl.md 8.2) and writes no *StateFailed
// (aws-recorded localstack@8b9a79f
// v2/scenarios/test_base_scenarios.snapshot.json:L1484), then to Catch;
// otherwise *StateFailed points at last and the error is the enclosing
// frame's, a Fail state's error the state's own (7.2; for Parallel
// ASL-10 and ASL-13, recorded 2026-11-03). A Terminal is never retried or
// caught, at any depth (ASL-15, recorded 2026-11-03): one from a branch or
// iteration never reaches here (scopeFailed), and one from the state's own
// fields gets the state's States.Runtime prefix (4.6).
func (s *stepper) runFailed(b block, v visit, last int64, f Failure, started bool) {
	e, ok := f.(StateError)
	if !ok {
		s.stateFailedAfter(b.name, v.entered, last, f)
		return
	}
	again := func(v visit) job { return runRetryJob{visit: v, previous: last} }
	if s.retrying(b.retry, v, e, again) {
		return
	}
	if started {
		last = s.emit(b.kind+"StateFailed", last, nil)
	}
	if s.catching(b.catch, b.kind+"StateExited", v, last, e) {
		return
	}
	s.raise(e, last)
}

// runRetry starts a retried Parallel or Map state again.
func (s *stepper) runRetry(j runRetryJob) {
	if _, ok := s.blockOf(j.state, j.previous); ok {
		s.startRun(s.d.states[j.state], j.visit, j.previous)
	}
}

// stopRun withdraws the run id and everything running inside it: nested
// runs, their armed timers and in-flight tasks, whose late results are then
// dropped (stepfunctions-asl.md 9.1: running Lambda functions are not
// stopped).
func (s *stepper) stopRun(id int64) {
	inside := func(fr frame) bool {
		for fr.run != 0 {
			if fr.run == id {
				return true
			}
			i := s.runAt(fr.run)
			if i < 0 {
				return false
			}
			fr = s.x.runs[i].fr
		}
		return false
	}
	gone := map[int64]bool{id: true}
	for i := range s.x.runs {
		if inside(s.x.runs[i].fr) {
			gone[s.x.runs[i].id] = true
		}
	}
	var withdrawn []TimerID
	s.x.tasks = slices.DeleteFunc(s.x.tasks, func(t inflight) bool {
		if inside(t.fr) {
			withdrawn = append(withdrawn, t.timer)
			return true
		}
		return false
	})
	s.x.timers = slices.DeleteFunc(s.x.timers, func(t armed) bool {
		fr, ok := jobFrame(t.job)
		if (ok && inside(fr)) || slices.Contains(withdrawn, t.id) {
			s.cancels = append(s.cancels, CancelTimer{Timer: t.id})
			return true
		}
		return false
	})
	s.x.runs = slices.DeleteFunc(s.x.runs, func(r run) bool { return gone[r.id] })
}

// abortWaitingIterations writes, for each iteration of the Map run r other
// than failed that waits in a Wait state of its own, in index order,
// MapIterationAborted and WaitStateAborted pointing at last; an iteration
// between states writes nothing (ASL-13 04, recorded 2026-11-03, for one
// waiting sibling; the order among several and a sibling waiting on a task
// or in a nested state are unrecorded, and write nothing here).
func (s *stepper) abortWaitingIterations(r run, failed int, last int64) {
	var waiting []int
	for _, t := range s.x.timers {
		if w, ok := t.job.(waitJob); ok && w.fr.run == r.id && w.fr.index != failed {
			waiting = append(waiting, w.fr.index)
		}
	}
	slices.Sort(waiting)
	for _, index := range slices.Compact(waiting) {
		s.emit("MapIterationAborted", last, iterationDetails(r.state, index))
		s.emit("WaitStateAborted", last, document.Map{})
	}
}

// jobFrame is the frame a job runs in; false for the jobs that belong to
// the execution or to a task: the deadline and task timeouts.
func jobFrame(j job) (frame, bool) {
	switch j := j.(type) {
	case stepJob:
		return j.fr, true
	case waitJob:
		return j.fr, true
	case retryJob:
		return j.fr, true
	case runRetryJob:
		return j.fr, true
	case deadlineJob, taskTimeoutJob:
	}
	return frame{}, false
}
