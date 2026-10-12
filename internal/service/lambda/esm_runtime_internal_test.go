package lambda

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// A poll timer that found a poller registered after its step ended it, and
// before retire removed it, starts no step on it: a poller started for the
// same mapping once it is removed never runs a step beside it.
func TestRetiredPollerStartsNoStep(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := New(k.Deps(lambdaapi.Service()), Options{})
		p := &pollers{x: &Execution{svc: svc, k: k.Kernel}, byID: map[string]*poller{}}
		pl := &poller{id: "arn:aws:lambda:us-east-1:000000000000:event-source-mapping:m", uuid: "m", scope: k.Scope, stop: func() {}, phase: phaseStepping}
		p.byID[pl.id] = pl
		if !p.end(pl, pollStop{}) {
			k.Fatal("a step that stopped its poller did not retire it")
		}
		p.onPoll(nil, clock.TimerKey{Kind: TimerPoll, ID: pl.id}, k.Clock.Now())
		pl.mu.Lock()
		got := pl.phase
		pl.mu.Unlock()
		if got != phaseRetired {
			k.Fatalf("a poll timer started a step on a retired poller: phase %d, want %d", got, phaseRetired)
		}
		p.retire(pl)
		if _, ok := p.byID[pl.id]; ok {
			k.Fatal("retire left the poller registered")
		}
	}, kerneltest.Services(lambdaapi.Service()))
}
