package sfn

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

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
	ref, ok := executionOfKey(raw)
	return ref, asl.TimerID(timer), ok
}

// executionOfKey reads the execution ARN of a timer ID, in the scope the
// ARN names.
func executionOfKey(raw string) (executionRef, bool) {
	a, err := aws.ParseARN(raw)
	if err != nil {
		return executionRef{}, false
	}
	scope, err := aws.ScopeOf(a)
	if err != nil {
		return executionRef{}, false
	}
	ref, ok, aerr := executionOfARN(scope, raw)
	return ref, aerr == nil && ok
}

// TimerExpire removes a closed execution once its retention has passed.
// The key's ID is the execution ARN.
const TimerExpire kernel.TimerKind = "sfn.expire"

// retention is how long a closed Standard execution is kept: its name can
// be reused after it (stepfunctions-api.md 4.3, 6.1; aws-doc
// api/API_StartExecution, dg/service-quotas).
const retention = 90 * 24 * time.Hour

// advance applies ev to the running execution ref names, at at, and keeps
// what the engine did (record). An execution that is gone or closed is
// left alone.
func (s *Service) advance(tx *kernel.Tx, st *State, ref executionRef, ev asl.Event, at clock.Instant) *awserr.Error {
	rec, ok := st.Executions[ref.key()]
	if _, done := rec.Engine.Outcome(); !ok || done {
		return nil
	}
	def, diags := asl.Parse(rec.Run.Definition, AcceptedFeatures())
	if def == nil {
		return definitionError(diags)
	}
	x, events, effects := asl.Step(def, EngineRules(), intrinsicSource{s.ids}, rec.Engine, ev)
	s.record(tx, st, ref, rec, x, events, effects, at)
	return nil
}

// record stores into st what one engine Step did to the execution rec at
// at: the new engine state, the events it wrote, and its effects as timers
// armed and disarmed in tx and the stop instant. An execution that ends is
// journaled as a StateChange from RUNNING to its status and armed for
// removal after its retention; the last one of a DELETING machine to end
// removes the machine (stepfunctions-api.md 5.4).
func (s *Service) record(tx *kernel.Tx, st *State, ref executionRef, rec Execution, x asl.Execution, events []asl.HistoryEvent, effects []asl.Effect, at clock.Instant) {
	rec.Engine = x
	// Appending in place is safe: the stored slice's length does not cover
	// what is appended until the record is stored back, and every read
	// clones the events it returns under the cell lock. Cloning here would
	// copy a 25,000-event history at every step.
	rec.Events = append(rec.Events, historyRecords(events)...)
	finished := false
	for _, e := range effects {
		switch e := e.(type) {
		case asl.ArmTimer:
			tx.Arm(s.stepKind, timerID(ref, e.Timer), clock.FromWire(e.At))
		case asl.CancelTimer:
			tx.Disarm(s.stepKind, timerID(ref, e.Timer))
		case asl.Finish:
			finished = true
			rec.Stopped = at
			tx.Record(ref.machine.scope, journal.StateChange{
				Resource: ref.arn(),
				From:     journal.ResourceState(sfnapi.ExecutionStatusRunning),
				To:       journal.ResourceState(statusOf(x)),
			})
			tx.Arm(s.expireKind, ref.arn().String(), at.Add(retention))
		case asl.InvokeLambda:
			tx.Arm(s.invokeKind, taskKey(ref, e.Task), at)
		}
	}
	st.Executions[ref.key()] = rec
	if m, ok := st.Machines[ref.machine.name]; finished && ok && m.Deleting && !hasRunning(st, m.Name) {
		delete(st.Machines, m.Name)
	}
}

// hasRunning reports whether the machine named machine has a running
// execution.
func hasRunning(st *State, machine string) bool {
	for key := range st.Executions {
		rec := st.Executions[key]
		if _, done := rec.Engine.Outcome(); rec.Machine == machine && !done {
			return true
		}
	}
	return false
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
		return struct{}{}, s.advance(tx, st, ref, asl.TimerFired{At: due.Wire(), Timer: timer}, due)
	})
	if err != nil {
		s.k.Log().LogAttrs(context.Background(), slog.LevelError, "a Step Functions execution could not take its next step and stays RUNNING; start a new execution after fixing the error",
			slog.String("execution", ref.arn().String()), slog.String("error", err.Message))
	}
}

// onExpire removes a closed execution whose retention has passed, which
// frees its name (stepfunctions-api.md 6.1).
func (s *Service) onExpire(c *kernel.Call, key clock.TimerKey, due clock.Instant) {
	ref, ok := executionOfKey(key.ID)
	if !ok {
		s.k.Log().LogAttrs(context.Background(), slog.LevelError, "a Step Functions retention timer has a malformed ID and was dropped; this is a bug in understudy, and the execution it names is kept",
			slog.String("timer", key.ID))
		return
	}
	if _, ok := s.cells.Lookup(ref.machine.scope); !ok {
		return
	}
	_, _ = kernel.Write(c, s.cells, ref.machine.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		rec, ok := st.Executions[ref.key()]
		if _, done := rec.Engine.Outcome(); !ok || !done || due.Before(rec.Stopped.Add(retention)) {
			tx.Unchanged()
			return struct{}{}, nil
		}
		delete(st.Executions, ref.key())
		return struct{}{}, nil
	})
}
