package asl_test

import (
	"fmt"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// fataler is what the helpers need of *testing.T and *rapid.T.
type fataler interface {
	Fatalf(format string, args ...any)
}

// allFeatures accepts every construct the engine builds.
var allFeatures = asl.NewFeatures(
	asl.FeatPass, asl.FeatSucceed, asl.FeatFail, asl.FeatChoice, asl.FeatWait, asl.FeatTimeout,
	asl.FeatPathDialect, asl.FeatPayloadTemplates, asl.FeatIntrinsics, asl.FeatLambdaTask,
	asl.FeatRetryCatch, asl.FeatParallel, asl.FeatMap,
)

// t0 is when test executions start. It has a millisecond fraction so
// timestamps that render it show one.
var t0 = time.Date(2024, 1, 2, 3, 4, 5, 678_000_000, time.UTC)

const (
	machineArn   = "arn:aws:states:us-east-1:000000000000:stateMachine:sm"
	executionArn = "arn:aws:states:us-east-1:000000000000:execution:sm:run-1"
	roleArn      = "arn:aws:iam::000000000000:role/sfn-role"
	functionArn  = "arn:aws:lambda:us-east-1:000000000000:function:fn"
)

func decode(f fataler, s string) document.Value {
	d := jsonx.NewDecoder([]byte(s))
	v, err := d.ReadValue()
	if err == nil {
		err = d.End()
	}
	if err != nil {
		f.Fatalf("decode %q: %v", s, err)
	}
	return v
}

func encode(v document.Value) string {
	if v == nil {
		return "<nil>"
	}
	e := jsonx.NewEncoder(jsonx.WireOrder)
	e.Value(v)
	b, err := e.Bytes()
	if err != nil {
		return fmt.Sprintf("<unencodable %#v: %v>", v, err)
	}
	return string(b)
}

// equal compares JSON values as a client does: objects as member sets,
// arrays in order, numbers by value.
func equal(a, b document.Value) bool {
	switch a := a.(type) {
	case document.Map:
		b, ok := b.(document.Map)
		if !ok || len(a) != len(b) {
			return false
		}
		for _, ea := range a {
			eb, ok := member(b, ea.Key)
			if !ok || !equal(ea.Value, eb) {
				return false
			}
		}
		return true
	case document.List:
		b, ok := b.(document.List)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !equal(a[i], b[i]) {
				return false
			}
		}
		return true
	case document.Number:
		b, ok := b.(document.Number)
		if !ok {
			return false
		}
		x, okx := new(big.Float).SetString(string(a))
		y, oky := new(big.Float).SetString(string(b))
		return okx && oky && x.Cmp(y) == 0
	case document.String:
		b, ok := b.(document.String)
		return ok && a == b
	case document.Bool:
		b, ok := b.(document.Bool)
		return ok && a == b
	case document.Null:
		_, ok := b.(document.Null)
		return ok
	}
	return a == nil && b == nil
}

func member(m document.Map, key string) (document.Value, bool) {
	for _, e := range m {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}

// keys lists a map's keys, sorted.
func keys(v document.Value) []string {
	m, ok := v.(document.Map)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(m))
	for _, e := range m {
		out = append(out, e.Key)
	}
	slices.Sort(out)
	return out
}

func mustParse(f fataler, def string) *asl.Definition {
	d, diags := asl.Parse(def, allFeatures)
	if d == nil {
		f.Fatalf("Parse = %+v, want a definition of\n%s", diags, def)
	}
	return d
}

// context is the Start context the service supplies: the
// stepfunctions-asl.md 4.7 Execution and StateMachine members.
func context(input document.Value) document.Value {
	return document.Map{
		{Key: "Execution", Value: executionContext(input)},
		{Key: "StateMachine", Value: document.Map{
			{Key: "Id", Value: document.String(machineArn)},
			{Key: "Name", Value: document.String("sm")},
		}},
	}
}

// executionContext is the context's Execution member.
func executionContext(input document.Value) document.Map {
	return document.Map{
		{Key: "Id", Value: document.String(executionArn)},
		{Key: "Input", Value: input},
		{Key: "Name", Value: document.String("run-1")},
		{Key: "RoleArn", Value: document.String(roleArn)},
		{Key: "StartTime", Value: document.String("2024-01-02T03:04:05.678Z")},
		{Key: "RedriveCount", Value: document.Number("0")},
	}
}

// at reads the member key of v, nil when v is not an object or has no
// such member.
func at(v document.Value, key string) document.Value {
	m, ok := v.(document.Map)
	if !ok {
		return nil
	}
	out, _ := member(m, key)
	return out
}

// lambdaAnswer answers one InvokeLambda effect at the given instant.
type lambdaAnswer func(inv asl.InvokeLambda, at time.Time) asl.Event

// succeedWith answers every invocation with out.
func succeedWith(out document.Value) lambdaAnswer {
	return func(inv asl.InvokeLambda, at time.Time) asl.Event {
		return asl.LambdaSucceeded{At: at, Task: inv.Task, Output: out}
	}
}

// echo answers every invocation with its payload.
func echo(inv asl.InvokeLambda, at time.Time) asl.Event {
	return asl.LambdaSucceeded{At: at, Task: inv.Task, Output: inv.Payload}
}

// fixedSource is the intrinsic.Source of engine tests.
type fixedSource struct{}

func (fixedSource) UUID() string                { return "00000000-0000-4000-8000-000000000000" }
func (fixedSource) Random(start, _ int64) int64 { return start }

// driver runs an execution through the engine's effects alone:
// it fires the earliest armed timer (ties by TimerID), answers each
// InvokeLambda in TaskID order, and drops cancelled timers.
type driver struct {
	f       fataler
	def     *asl.Definition
	rules   asl.Rules
	x       asl.Execution
	now     time.Time
	history []asl.HistoryEvent
	effects [][]asl.Effect
	timers  map[asl.TimerID]time.Time
	pending []asl.InvokeLambda
	invoked []asl.InvokeLambda
	invokes []time.Time
	answer  lambdaAnswer
	finish  []asl.Finish
}

func newDriver(f fataler, def string, rules asl.Rules) *driver {
	return &driver{f: f, def: mustParse(f, def), rules: rules, timers: map[asl.TimerID]time.Time{}, answer: echo}
}

// start applies Start with input (JSON text) at t0.
func (d *driver) start(input string) []asl.Effect {
	in := decode(d.f, input)
	return d.apply(asl.Start{At: t0, Input: in, Context: context(in)})
}

// apply steps the execution with ev and records what it wrote.
func (d *driver) apply(ev asl.Event) []asl.Effect {
	if at := eventTime(ev); at.After(d.now) {
		d.now = at
	}
	x, hs, effs := asl.Step(d.def, d.rules, fixedSource{}, d.x, ev)
	d.x = x
	d.history = append(d.history, hs...)
	d.effects = append(d.effects, effs)
	for _, e := range effs {
		switch e := e.(type) {
		case asl.ArmTimer:
			d.timers[e.Timer] = e.At
		case asl.CancelTimer:
			delete(d.timers, e.Timer)
		case asl.InvokeLambda:
			d.pending = append(d.pending, e)
			d.invoked = append(d.invoked, e)
			d.invokes = append(d.invokes, d.now)
		case asl.Finish:
			d.finish = append(d.finish, e)
		}
	}
	return effs
}

func eventTime(ev asl.Event) time.Time {
	switch ev := ev.(type) {
	case asl.Start:
		return ev.At
	case asl.TimerFired:
		return ev.At
	case asl.LambdaSucceeded:
		return ev.At
	case asl.LambdaFailed:
		return ev.At
	case asl.Stop:
		return ev.At
	case asl.MachineDeleting:
		return ev.At
	}
	return time.Time{}
}

// nextTimer is the earliest armed timer, ties by TimerID.
func (d *driver) nextTimer() (asl.TimerID, time.Time, bool) {
	var (
		id    asl.TimerID
		at    time.Time
		found bool
	)
	for tid, tat := range d.timers {
		if !found || tat.Before(at) || (tat.Equal(at) && tid < id) {
			id, at, found = tid, tat, true
		}
	}
	return id, at, found
}

// fire fires the earliest armed timer; false when none is armed.
func (d *driver) fire() bool {
	id, at, ok := d.nextTimer()
	if !ok {
		return false
	}
	delete(d.timers, id)
	d.apply(asl.TimerFired{At: at, Timer: id})
	return true
}

// next takes one step: an outstanding invocation first, else the earliest
// timer. It reports false when the execution has ended or nothing is left.
func (d *driver) next() bool {
	if _, done := d.x.Outcome(); done {
		return false
	}
	if len(d.pending) > 0 {
		inv := d.pending[0]
		d.pending = d.pending[1:]
		d.apply(d.answer(inv, d.now))
		return true
	}
	return d.fire()
}

// runToEnd steps until the execution ends, failing after limit steps.
func (d *driver) runToEnd(limit int) asl.Outcome {
	for range limit {
		if !d.next() {
			break
		}
	}
	o, done := d.x.Outcome()
	if !done {
		d.f.Fatalf("execution still running after %d steps; history:\n%s", limit, d.shape())
	}
	return o
}

// runUntilWaiting steps until only timers later than now remain or the
// execution ends.
// It leaves invocations unanswered, so a test can hold a Task in flight.
func (d *driver) runUntilWaiting() {
	for range 1000 {
		if _, done := d.x.Outcome(); done || len(d.pending) > 0 {
			return
		}
		_, at, ok := d.nextTimer()
		if !ok || at.After(d.now) {
			return
		}
		d.fire()
	}
}

// shape writes the history as "<Type> <id><-<previous>" lines, the form
// stepfunctions-asl.md 11.3 records.
func (d *driver) shape() string {
	return shape(d.history)
}

func shape(h []asl.HistoryEvent) string {
	lines := make([]string, len(h))
	for i, e := range h {
		lines[i] = fmt.Sprintf("%s %d<-%d", e.Type, e.ID, e.PreviousEventID)
	}
	return strings.Join(lines, "\n")
}

// wantShape fails unless the history is exactly want.
func (d *driver) wantShape(source string, want ...string) {
	if got := d.shape(); got != strings.Join(want, "\n") {
		d.f.Fatalf("history (%s):\n%s\nwant:\n%s", source, got, strings.Join(want, "\n"))
	}
}

// event returns the first history event of type typ.
func (d *driver) event(typ string) asl.HistoryEvent {
	for _, e := range d.history {
		if e.Type == typ {
			return e
		}
	}
	d.f.Fatalf("no %s event in history:\n%s", typ, d.shape())
	return asl.HistoryEvent{}
}

// events returns every history event of type typ.
func (d *driver) events(typ string) []asl.HistoryEvent {
	var out []asl.HistoryEvent
	for _, e := range d.history {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// detail reads a member of an event's details.
func detail(e asl.HistoryEvent, key string) (document.Value, bool) {
	m, ok := e.Details.(document.Map)
	if !ok {
		return nil, false
	}
	return member(m, key)
}

// detailString reads a string member of an event's details, "" when it
// is absent.
func detailString(e asl.HistoryEvent, key string) string {
	v, _ := detail(e, key)
	if s, ok := v.(document.String); ok {
		return string(s)
	}
	return ""
}

// detailJSON reads a JSON-text member (input, output) of an event's
// details as a value.
func detailJSON(f fataler, e asl.HistoryEvent, key string) document.Value {
	v, ok := detail(e, key)
	s, isString := v.(document.String)
	if !ok || !isString {
		f.Fatalf("%s details %s = %s, want JSON text", e.Type, key, encode(e.Details))
	}
	return decode(f, string(s))
}

// rules returns the engine rules the service builds from its switches.
func rules() asl.Rules { return sfn.EngineRules() }

// runtimePrefix is the recorded start of every States.Runtime cause
// (stepfunctions-asl.md 4.6).
func runtimePrefix(state string, entered int64) string {
	return fmt.Sprintf("An error occurred while executing the state '%s' (entered at the event id #%d). ", state, entered)
}

// failedWith fails unless the outcome is Failed with error name and a
// cause passing check.
func failedWith(tb testing.TB, o asl.Outcome, name string, check func(cause string) bool) {
	tb.Helper()
	f, ok := o.(asl.Failed)
	if !ok || f.Error != name || !check(f.Cause) {
		tb.Fatalf("outcome = %#v, want Failed with error %q", o, name)
	}
}
