package sfn

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// TimerStep fires one engine timer of an execution: a state transition
// (contract 5.5). The key's ID is "<execution ARN>/<TimerID>".
const TimerStep kernel.TimerKind = "sfn.step"

// timerID is the TimerStep ID of timer of the execution ref.
func timerID(ref executionRef, timer asl.TimerID) string {
	return ref.arn().String() + "/" + strconv.FormatInt(int64(timer), 10)
}

// timerOf reads a TimerStep ID back.
func timerOf(id string) (executionRef, asl.TimerID, bool) {
	raw, n, found := strings.Cut(id, "/")
	if !found {
		return executionRef{}, 0, false
	}
	timer, err := strconv.ParseInt(n, 10, 64)
	if err != nil {
		return executionRef{}, 0, false
	}
	a, err := aws.ParseARN(raw)
	if err != nil {
		return executionRef{}, 0, false
	}
	scope, err := aws.ScopeOf(a)
	if err != nil {
		return executionRef{}, 0, false
	}
	ref, ok, aerr := executionOfARN(scope, raw)
	if aerr != nil || !ok {
		return executionRef{}, 0, false
	}
	return ref, asl.TimerID(timer), true
}

// record keeps what one engine Step did to the execution rec at at: the
// new engine state, the events it wrote, and its effects as timers armed
// and disarmed in tx and the stop instant. An execution that ends is
// journaled as a StateChange from RUNNING to its status.
func (s *Service) record(tx *kernel.Tx, ref executionRef, rec *Execution, x asl.Execution, events []asl.HistoryEvent, effects []asl.Effect, at clock.Instant) {
	rec.Engine = x
	// Appending in place is safe: the stored slice's length does not cover
	// what is appended until the record is stored back, and every read
	// clones the events it returns under the cell lock. Cloning here would
	// copy a 25,000-event history at every step.
	rec.Events = append(rec.Events, historyRecords(events)...)
	for _, e := range effects {
		switch e := e.(type) {
		case asl.ArmTimer:
			tx.Arm(s.step, timerID(ref, e.Timer), clock.FromWire(e.At))
		case asl.CancelTimer:
			tx.Disarm(s.step, timerID(ref, e.Timer))
		case asl.Finish:
			rec.Stopped = at
			tx.Record(ref.machine.scope, journal.StateChange{
				Resource: ref.arn(),
				From:     journal.ResourceState(sfnapi.ExecutionStatusRunning),
				To:       journal.ResourceState(statusOf(x)),
			})
		case asl.InvokeLambda:
			// No built state invokes Lambda yet; Parse refuses Task states.
		}
	}
}

// onStep applies a due engine timer to its execution. An execution gone or
// ended since the timer was armed is left alone.
func (s *Service) onStep(c *kernel.Call, key clock.TimerKey, due clock.Instant) {
	ref, timer, ok := timerOf(key.ID)
	if !ok {
		s.k.Log().LogAttrs(context.Background(), slog.LevelError, "a Step Functions timer has a malformed ID and was dropped; this is a bug in understudy",
			slog.String("timer", key.ID))
		return
	}
	if _, ok := s.cells.Lookup(ref.machine.scope); !ok {
		return
	}
	_, err := kernel.Write(c, s.cells, ref.machine.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		rec, ok := st.Executions[ref.key()]
		if _, done := rec.Engine.Outcome(); !ok || done {
			tx.Unchanged()
			return struct{}{}, nil
		}
		def, diags := asl.Parse(rec.Run.Definition, AcceptedFeatures())
		if def == nil {
			return struct{}{}, definitionError(diags)
		}
		x, events, effects := asl.Step(def, EngineRules(), intrinsicSource{s.ids}, rec.Engine, asl.TimerFired{At: due.Wire(), Timer: timer})
		if len(events) == 0 && len(effects) == 0 {
			tx.Unchanged()
			return struct{}{}, nil
		}
		s.record(tx, ref, &rec, x, events, effects, due)
		st.Executions[ref.key()] = rec
		return struct{}{}, nil
	})
	if err != nil {
		s.k.Log().LogAttrs(context.Background(), slog.LevelError, "a Step Functions execution could not take its next step and stays RUNNING; start a new execution after fixing the error",
			slog.String("execution", ref.arn().String()), slog.String("error", err.Message))
	}
}
