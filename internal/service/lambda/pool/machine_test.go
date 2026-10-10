package pool_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool"
)

const (
	machineIdleCap     = 2
	machineGlobalCap   = 4
	machineIdleTimeout = 10 * time.Minute
)

// machine drives a pool through random acquires, invocations, releases,
// crashes, clock advances, function updates and drains, checking the pool
// contract after every step.
type machine struct {
	h         *harness
	functions []string
	// generation is the newest generation requested per function.
	generation map[string]uint64
	// leased are the environments the machine holds, in lease order.
	leased []*pool.Env
	// all is every environment ever leased.
	all map[*pool.Env]bool
}

func newMachine(t *rapid.T) *machine {
	return &machine{
		h:          newHarness(t, pool.Options{IdleCap: machineIdleCap, GlobalCap: machineGlobalCap, IdleTimeout: machineIdleTimeout}, outcomeHandler),
		functions:  []string{"alpha", "beta"},
		generation: map[string]uint64{"alpha": 1, "beta": 1},
		all:        map[*pool.Env]bool{},
	}
}

func (m *machine) acquire(t *rapid.T) {
	fn := rapid.SampledFrom(m.functions).Draw(t, "function")
	e, err := m.h.pool.Acquire(context.Background(), fnSpec(fn, m.generation[fn]))
	if capErr, ok := errors.AsType[*pool.CapacityError](err); ok {
		if len(m.leased) < capErr.Cap {
			t.Fatalf("CapacityError with %d of %d environments leased; an idle one should have been evicted", len(m.leased), capErr.Cap)
		}
		return
	}
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if slices.Contains(m.leased, e) {
		t.Fatalf("Acquire returned environment %s, already leased", e.ID())
	}
	if e.Key().Generation != m.generation[fn] {
		t.Fatalf("Acquire for generation %d returned an environment of generation %d", m.generation[fn], e.Key().Generation)
	}
	m.leased = append(m.leased, e)
	m.all[e] = true
}

func (m *machine) release(t *rapid.T) {
	if len(m.leased) == 0 {
		t.Skip("nothing leased")
	}
	i := rapid.IntRange(0, len(m.leased)-1).Draw(t, "lease")
	e := m.leased[i]
	m.leased = slices.Delete(m.leased, i, i+1)
	payload := rapid.SampledFrom([]string{"ok", "error", "timeout", "exit", "unused"}).Draw(t, "outcome")
	if payload == "unused" {
		if err := m.h.pool.Release(context.Background(), e, nil); err != nil {
			t.Fatalf("Release: %v", err)
		}
		return
	}
	m.h.invoke(t, e, payload)
	switch payload {
	case "timeout", "exit":
		if !isGone(e.State()) {
			t.Fatalf("after a %s outcome the environment is %#v, want Gone (the next invocation starts cold)", payload, e.State())
		}
	}
}

func (m *machine) crash(t *rapid.T) {
	live := m.h.launcher.Live()
	if len(live) == 0 {
		t.Skip("no live container")
	}
	id := rapid.SampledFrom(live).Draw(t, "container")
	var victim *pool.Env
	for e := range m.all {
		if e.Container().ID == id {
			victim = e
		}
	}
	m.h.launcher.Crash(id, launcher.Exited{Code: 137})
	if victim == nil {
		t.Fatalf("live container %s belongs to no environment", id)
	}
	if slices.Contains(m.leased, victim) {
		settled(t, victim, isFaulted)
	} else {
		settled(t, victim, isGone)
	}
}

func (m *machine) advance(t *rapid.T) {
	d := time.Duration(rapid.IntRange(0, 12).Draw(t, "minutes")) * time.Minute
	m.h.wall.advance(d)
	if err := m.h.pool.Reap(context.Background()); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	for e := range m.all {
		if idle, ok := e.State().(pool.Idle); ok && idle.Elapsed() >= machineIdleTimeout {
			t.Fatalf("environment %s idle for %s after Reap", e.ID(), idle.Elapsed())
		}
	}
}

func (m *machine) update(t *rapid.T) {
	fn := rapid.SampledFrom(m.functions).Draw(t, "function")
	m.generation[fn]++
}

func (m *machine) reset(t *rapid.T) {
	if err := m.h.pool.Drain(context.Background(), pool.All{}, pool.Reset); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	for e := range m.all {
		if _, idle := e.State().(pool.Idle); idle {
			t.Fatalf("environment %s still idle after a reset", e.ID())
		}
	}
}

// check is the invariant after every step.
func (m *machine) check(t *rapid.T) {
	n := m.h.pool.Len()
	if n > machineGlobalCap {
		t.Fatalf("Len = %d, above the global cap %d", n, machineGlobalCap)
	}
	// A crashed environment still leased keeps its place until released,
	// though its container is gone.
	crashedLeased := 0
	for _, e := range m.leased {
		if isFaulted(e.State()) {
			crashedLeased++
		}
	}
	if live := len(m.h.launcher.Live()); live != n-crashedLeased {
		t.Fatalf("%d live containers for %d environments (%d crashed and leased)", live, n, crashedLeased)
	}
	idlePer := map[string]int{}
	for e := range m.all {
		state := e.State()
		leased := slices.Contains(m.leased, e)
		switch s := state.(type) {
		case pool.Reserved, pool.Busy:
			if !leased {
				t.Fatalf("environment %s is %#v but nobody leases it", e.ID(), s)
			}
		case pool.Faulted:
			if !leased {
				t.Fatalf("environment %s is faulted but not removed", e.ID())
			}
		case pool.Idle:
			if leased {
				t.Fatalf("leased environment %s is idle", e.ID())
			}
			fn := m.functionOf(e)
			idlePer[fn]++
			if e.Key().Generation != m.newestAcquired(fn) {
				t.Fatalf("idle environment %s of generation %d outlived generation %d", e.ID(), e.Key().Generation, m.newestAcquired(fn))
			}
		case pool.Gone:
			if leased {
				t.Fatalf("leased environment %s is gone before release", e.ID())
			}
		case pool.Starting, pool.Draining:
			t.Fatalf("environment %s is %#v between operations", e.ID(), s)
		}
	}
	for fn, idle := range idlePer {
		if idle > machineIdleCap {
			t.Fatalf("%d idle environments of %s, above the idle cap %d", idle, fn, machineIdleCap)
		}
	}
}

func (m *machine) functionOf(e *pool.Env) string {
	for _, fn := range m.functions {
		if e.Key().Function == fnSpec(fn, 0).Function.ARN() {
			return fn
		}
	}
	return ""
}

// newestAcquired is the highest generation the pool has seen for fn: the
// machine bumps generations ahead of acquires, so take the maximum over
// the environments it holds or held.
func (m *machine) newestAcquired(fn string) uint64 {
	var newest uint64
	for e := range m.all {
		if m.functionOf(e) == fn {
			newest = max(newest, e.Key().Generation)
		}
	}
	return newest
}

// bubbleTB hides *testing.T from rapid, which would otherwise call
// T.Deadline, forbidden inside a synctest bubble.
type bubbleTB struct{ *testing.T }

func TestPoolStateMachine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { checkPoolStateMachine(bubbleTB{t}) })
}

func checkPoolStateMachine(t rapid.TB) {
	rapid.Check(t, func(t *rapid.T) {
		m := newMachine(t)
		t.Cleanup(func() {
			for _, e := range m.leased {
				_ = m.h.pool.Release(context.Background(), e, nil) //nolint:errcheck // teardown
			}
		})
		t.Repeat(map[string]func(*rapid.T){
			"acquire": m.acquire,
			"release": m.release,
			"crash":   m.crash,
			"advance": m.advance,
			"update":  m.update,
			"reset":   m.reset,
			"":        m.check,
		})
	})
}
