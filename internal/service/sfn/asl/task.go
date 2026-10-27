package asl

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// defaultTaskTimeout is a Task's timeout when it sets none
// (stepfunctions-asl.md 8.6; aws-doc dg/state-task).
const defaultTaskTimeout = maxSeconds * time.Second

// errTimeout is the error of a Task past its TimeoutSeconds
// (stepfunctions-asl.md 8.1, 8.6).
const errTimeout = "States.Timeout"

// task enters a Lambda Task state (contract 5.5 item 3): it writes
// TaskStateEntered and makes the first attempt.
func (s *stepper) task(st taskState, j stepJob) {
	entered := s.emit("TaskStateEntered", j.previous, stateEntered(st.name, j.input))
	s.attempt(st, visit{fr: s.fr, state: st.name, input: j.input, enteredAt: s.at, entered: entered}, entered)
}

// retry makes the next attempt of a Task a retrier sent back
// (stepfunctions-asl.md 8.2). A retry writes no event of its own: the
// attempt's first event points at the failure retried (11.2 rule 7).
func (s *stepper) retry(j retryJob) {
	// An attempt writes at most its scheduled and started events before the
	// execution's last event, within step's quota margin (inferred, ASL-16).
	if s.x.lastEvent+4 > maxHistoryEvents {
		s.fail(errRuntime, "The execution reached the limit of "+strconv.Itoa(maxHistoryEvents)+" history events.", s.x.lastEvent)
		return
	}
	st, ok := s.d.states[j.state].(taskState)
	if !ok {
		// Parse built the state the retry names; a definition without it
		// was changed under a running execution.
		s.fail(errRuntime, "The state '"+j.state+"' is not in the definition the execution runs.", j.previous)
		return
	}
	s.attempt(st, j.visit, j.previous)
}

// attempt works out the invocation from the effective input, writes the
// scheduled and started events of the Task's resource form, the first
// pointing at previous (stepfunctions-asl.md 11.3), then asks for the
// invocation and arms the task timeout.
func (s *stepper) attempt(st taskState, v visit, previous int64) {
	sc := s.taskScope(v)
	selected, f := sc.selectFrom(st.io.input, v.input)
	if f != nil {
		s.taskFailed(st, v, previous, f)
		return
	}
	effective := selected
	if st.parameters != nil {
		if effective, f = sc.render(st.parameters, selected); f != nil {
			s.taskFailed(st, v, previous, f)
			return
		}
	}
	// TimeoutSecondsPath reads the state input after InputPath, as the Wait
	// paths do (aws-doc dg/state-task: "a reference path to the state
	// input").
	timeout, f := sc.taskTimeout(st.timeout, selected)
	if f != nil {
		s.taskFailed(st, v, previous, f)
		return
	}
	invoke := InvokeLambda{Resource: st.resource, Timeout: timeout}
	var scheduled int64
	switch st.resource {
	case FunctionARN:
		invoke.Function, invoke.Payload = st.function, effective
		details := document.Map{
			{Key: "resource", Value: document.String(st.function)},
			{Key: "input", Value: document.String(text(effective))},
			{Key: "inputDetails", Value: untruncated()},
		}
		scheduled = s.emit("LambdaFunctionScheduled", previous, withTimeout(details, st.timeout, timeout))
	case LambdaInvoke:
		if invoke.Function, invoke.Payload, f = invokeArguments(effective); f != nil {
			s.taskFailed(st, v, previous, f)
			return
		}
		details := slices.Concat(invokeResource(), document.Map{
			{Key: "region", Value: document.String(s.region())},
			{Key: "parameters", Value: document.String(text(effective))},
		})
		scheduled = s.emit("TaskScheduled", previous, withTimeout(details, st.timeout, timeout))
	}
	var started int64
	if st.resource == FunctionARN {
		started = s.emit("LambdaFunctionStarted", scheduled, nil)
	} else {
		started = s.emit("TaskStarted", scheduled, invokeResource())
	}
	s.x.lastTask++
	invoke.Task = s.x.lastTask
	s.allocs = append(s.allocs, invoke)
	s.arm(s.at.Add(timeout), taskTimeoutJob{task: invoke.Task})
	s.x.tasks = append(s.x.tasks, inflight{invoke: invoke, visit: v, started: started, timer: s.x.lastTimer})
}

// invokeResource are the resource members of the lambda:invoke task events
// (stepfunctions-asl.md 11.3 optimized row).
func invokeResource() document.Map {
	return document.Map{
		{Key: "resourceType", Value: document.String("lambda")},
		{Key: "resource", Value: document.String("invoke")},
	}
}

// withTimeout adds timeoutInSeconds to scheduled-event details when the
// definition sets a timeout: AWS shows it only then (stepfunctions-asl.md
// 8.6).
func withTimeout(details document.Map, t timeout, d time.Duration) document.Map {
	if _, unset := t.(defaultTimeout); unset {
		return details
	}
	return append(details, document.Entry{Key: "timeoutInSeconds", Value: document.Number(strconv.FormatInt(int64(d/time.Second), 10))})
}

// region is the execution's region, from its ARN in the context object:
// TaskScheduled names it (stepfunctions-asl.md 11.3).
func (s *stepper) region() string {
	id, ok := at(at(s.x.context, "Execution"), "Id").(document.String)
	parts := strings.SplitN(string(id), ":", 5)
	if !ok || len(parts) < 5 {
		return ""
	}
	return parts[3]
}

// taskScope is what a Task's paths and templates read in visit v: its
// context object adds State.RetryCount, the retries made so far
// (aws-recorded localstack@8b9a79f
// v2/scenarios/test_base_scenarios.snapshot.json:L10427-L10434). With
// several retriers it counts the retries of all of them (UNVERIFIED,
// ASL-10).
func (s *stepper) taskScope(v visit) scope {
	ctx := stateContext(s.x.context, v.state, v.enteredAt)
	ctx[2].Value = document.Map{
		{Key: "Name", Value: document.String(v.state)},
		{Key: "EnteredTime", Value: document.String(ContextTime(v.enteredAt))},
		{Key: "RetryCount", Value: document.Number(strconv.Itoa(v.retries()))},
	}
	return scope{context: ctx, src: s.src, rules: s.rules}
}

// taskTimeout is how long the Task waits for its invocation. A
// TimeoutSecondsPath that selects no positive integer fails with
// States.Runtime; AWS's text is unrecorded, so the causes follow the
// recorded SecondsPath ones (stepfunctions-asl.md 4.6; UNVERIFIED).
func (sc scope) taskTimeout(t timeout, input document.Value) (time.Duration, Failure) {
	switch t := t.(type) {
	case timeoutSeconds:
		return time.Duration(t.seconds) * time.Second, nil
	case timeoutPath:
		field := "The TimeoutSecondsPath parameter "
		v, err := t.path.Eval(input, sc.context)
		if err != nil {
			return 0, Terminal{Name: errRuntime, Cause: field + "does not reference an input value: " + t.path.String()}
		}
		n, isNumber := v.(document.Number)
		seconds, err := n.Int64()
		if !isNumber || err != nil {
			return 0, Terminal{Name: errRuntime, Cause: field + "cannot be parsed as a long value: " + t.path.String() + " == " + shown(v)}
		}
		if seconds < 1 {
			return 0, Terminal{Name: errRuntime, Cause: field + "references a value that is not positive: " + t.path.String() + " == " + shown(v)}
		}
		return time.Duration(min(seconds, maxSeconds)) * time.Second, nil
	case defaultTimeout:
	}
	return defaultTaskTimeout, nil
}

// invokeArguments reads lambda:invoke's resolved Parameters: FunctionName,
// with Qualifier appended when given, and Payload, nil when absent. Only
// FunctionName, Qualifier and Payload reach the invocation. An
// InvocationType that resolves to anything but RequestResponse fails with
// States.Runtime (inferred, SFN-13).
func invokeArguments(params document.Value) (function string, payload document.Value, f Failure) {
	m, isMap := params.(document.Map)
	name, ok := member(m, "FunctionName")
	fn, isString := name.(document.String)
	if !isMap || !ok || !isString || fn == "" {
		return "", nil, Terminal{Name: errRuntime, Cause: "The lambda:invoke Parameters must hold FunctionName as a non-empty string, but they are " + text(params)}
	}
	function = string(fn)
	if q, ok := member(m, "Qualifier"); ok {
		if qs, isString := q.(document.String); isString && qs != "" {
			function += ":" + string(qs)
		}
	}
	if it, ok := member(m, "InvocationType"); ok && it != document.String("RequestResponse") {
		return "", nil, Terminal{Name: errRuntime, Cause: "understudy does not implement lambda:invoke with InvocationType " + text(it) +
			"; only RequestResponse is. Run 'understudy coverage --service sfn' for what is supported."}
	}
	payload, _ = member(m, "Payload")
	return function, payload, nil
}

// takeTask removes the in-flight task id and withdraws its timeout, giving
// the instant the timeout was armed for (zero when none is armed); false
// when the execution is not waiting on it.
func (s *stepper) takeTask(id TaskID) (t inflight, deadline time.Time, ok bool) {
	i := slices.IndexFunc(s.x.tasks, func(t inflight) bool { return t.invoke.Task == id })
	if i < 0 {
		return inflight{}, time.Time{}, false
	}
	t = s.x.tasks[i]
	s.x.tasks = slices.Delete(s.x.tasks, i, i+1)
	if j := slices.IndexFunc(s.x.timers, func(a armed) bool { return a.id == t.timer }); j >= 0 {
		deadline = s.x.timers[j].at
		s.x.timers = slices.Delete(s.x.timers, j, j+1)
		s.cancels = append(s.cancels, CancelTimer{Timer: t.timer})
	}
	return t, deadline, true
}

// answered takes the task a Lambda result at answers. applies is false when
// the task is not in flight, and the result changes nothing; ok is true
// when the caller is to apply the result. A machine being deleted ends the
// execution ABORTED instead (switch sfn.delete.running-execution). The
// function runs on wall time while the task timeout is emulator time (spec
// "Runtime decisions"), so a result at or after the timeout's instant
// loses to it: the task times out there and the result is dropped
// (stepfunctions-asl.md 8.6, 9.1).
func (s *stepper) answered(id TaskID, at time.Time) (t inflight, applies, ok bool) {
	t, deadline, applies := s.takeTask(id)
	s.fr = t.fr
	switch {
	case !applies:
		return inflight{}, false, false
	case s.x.deleting:
		s.at = at
		s.abort("", "")
		return inflight{}, true, false
	}
	// The deadline is the attempt's own timeout timer, armed in the step
	// that started it (8.6: from LambdaFunctionStarted or TaskStarted), so a
	// retry counts from its own start, not the state's entry.
	if !deadline.IsZero() && !at.Before(deadline) {
		s.at = deadline
		s.taskError(t, "TimedOut", StateError{Name: errTimeout})
		return inflight{}, true, false
	}
	s.at = at
	return t, true, true
}

// taskState is the Task t waits in; false when the definition lacks it.
func (s *stepper) taskOf(t inflight) (taskState, bool) {
	st, ok := s.d.states[t.state].(taskState)
	if !ok {
		// Parse built the state the task names; a definition without it was
		// changed under a running execution.
		s.fail(errRuntime, "The state '"+t.state+"' is not in the definition the execution runs.", t.started)
	}
	return st, ok
}

// lambdaSucceeded applies an invocation's result (stepfunctions-asl.md 4.1,
// 11.3): a result over the data limit is a task failure naming the resource
// ('lambda' for lambda:invoke, the function ARN otherwise; aws-recorded
// localstack@8b9a79f v2/error_handling/test_states_errors.snapshot.json:L65,
// L352); otherwise the succeeded event, then ResultSelector, ResultPath and
// OutputPath, TaskStateExited and the transition. A path that fails there is
// States.Runtime after the succeeded event (11.2 rule 8).
func (s *stepper) lambdaSucceeded(t inflight, result document.Value) {
	st, ok := s.taskOf(t)
	if !ok {
		return
	}
	if len(text(result)) > maxDataBytes {
		resource := "lambda"
		if t.invoke.Resource == FunctionARN {
			resource = t.invoke.Function
		}
		s.taskError(t, "Failed", StateError{Name: errDataLimit, Cause: "The state/task '" + resource + "' returned a result with a size exceeding the maximum number of bytes service limit."})
		return
	}
	output := document.Map{
		{Key: "output", Value: document.String(text(result))},
		{Key: "outputDetails", Value: untruncated()},
	}
	var succeeded int64
	if t.invoke.Resource == FunctionARN {
		succeeded = s.emit("LambdaFunctionSucceeded", t.started, output)
	} else {
		succeeded = s.emit("TaskSucceeded", t.started, slices.Concat(invokeResource(), output))
	}
	sc := s.taskScope(t.visit)
	var f Failure
	if st.resultSelector != nil {
		result, f = sc.render(st.resultSelector, result)
	}
	var out document.Value
	if f == nil {
		out, f = placeResult(st.resultPath, t.input, result)
	}
	if f == nil {
		out, f = sc.selectFrom(st.io.output, out)
	}
	if f == nil {
		f = dataLimit(st.name, out)
	}
	if f != nil {
		s.taskFailed(st, t.visit, succeeded, f)
		return
	}
	exited := s.emit("TaskStateExited", succeeded, stateExited(st.name, out))
	s.transition(st.next, out, exited)
}

// taskError writes the task's Failed or TimedOut event of its resource form
// (stepfunctions-asl.md 11.3, 8.6) and hands e to Retry and Catch after it.
func (s *stepper) taskError(t inflight, ending string, e StateError) {
	st, ok := s.taskOf(t)
	if !ok {
		return
	}
	details := errorDetails(e.Name, e.Cause)
	typ := "LambdaFunction" + ending
	if t.invoke.Resource == LambdaInvoke {
		typ = "Task" + ending
		details = append(details, invokeResource()...)
	}
	s.taskFailed(st, t.visit, s.emit(typ, t.started, details), e)
}

// taskFailed handles a failure of the Task st in visit v whose last event
// is last. A Terminal ends the frame, a States.Runtime cause prefixed with
// the state's entered event (stepfunctions-asl.md 4.6, 8.5). A StateError
// is retried or caught (retrying, catching), and ends the frame when
// neither applies.
func (s *stepper) taskFailed(st taskState, v visit, last int64, fl Failure) {
	e, ok := fl.(StateError)
	if !ok {
		s.stateFailedAfter(st.name, v.entered, last, fl)
		return
	}
	again := func(v visit) job { return retryJob{visit: v, previous: last} }
	if s.retrying(st.retry, v, e, again) || s.catching(st.catch, "TaskStateExited", v, last, e) {
		return
	}
	s.raise(e, last)
}

// retrying arms a retry of the state in visit v when the first retrier
// naming e has attempts left (stepfunctions-asl.md 8.2): again is the job
// that retries, given the visit with the attempt counted. It is false when
// no retrier applies: a retrier that has run out of attempts hands the
// error to Catch, not to a later retrier (spec#complex-retry-scenarios).
func (s *stepper) retrying(retry []Retrier, v visit, e StateError, again func(visit) job) bool {
	i := slices.IndexFunc(retry, func(r Retrier) bool { return errorNamed(r.ErrorEquals, e.Name) })
	if i < 0 {
		return false
	}
	r := retry[i]
	attempts := make([]int, len(retry))
	copy(attempts, v.attempts)
	if attempts[i] >= r.MaxAttempts {
		return false
	}
	attempts[i]++
	v.attempts = attempts
	s.arm(s.at.Add(RetryDelay(r, attempts[i], s.jitter(r))), again(v))
	return true
}

// catching hands e to the first catcher naming it (stepfunctions-asl.md
// 8.4): the error output is placed by its ResultPath into the raw input,
// the state's exited event (of type exited) points at last, and the
// catcher's Next follows. It is false when no catcher names e.
func (s *stepper) catching(catch []catcher, exited string, v visit, last int64, e StateError) bool {
	i := slices.IndexFunc(catch, func(c catcher) bool { return errorNamed(c.errorEquals, e.Name) })
	if i < 0 {
		return false
	}
	c := catch[i]
	out, fl := placeResult(c.resultPath, v.input, errorOutput(e))
	if fl == nil {
		fl = dataLimit(v.state, out)
	}
	if fl != nil {
		s.stateFailedAfter(v.state, v.entered, last, fl)
		return true
	}
	id := s.emit(exited, last, stateExited(v.state, out))
	s.transition(c.next, out, id)
	return true
}

// jitter is the next FULL jitter draw for r, 0 for any other strategy.
func (s *stepper) jitter(r Retrier) float64 {
	if r.Jitter != JitterFull {
		return 0
	}
	s.x.jitterDraws++
	return jitterDraw(s.x.jitterSeed, s.x.jitterDraws)
}

// errorOutput is a catcher's result: {"Error", "Cause"}, Error first as
// recorded (stepfunctions-asl.md 8.4; aws-recorded localstack@8b9a79f
// v2/error_handling/test_states_errors.snapshot.json:L77). An error without
// a cause, such as a task's States.Timeout, has no Cause member: whether AWS
// omits it or gives "" or null is unrecorded (inferred, ASL-13).
func errorOutput(e StateError) document.Map {
	out := document.Map{{Key: "Error", Value: document.String(e.Name)}}
	if e.Cause != "" {
		out = append(out, document.Entry{Key: "Cause", Value: document.String(e.Cause)})
	}
	return out
}
