package launcher

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

// blockingCall is a flights call that runs until release closes, counting
// its runs and recording the context it ran under.
type blockingCall struct {
	release chan struct{}
	runs    atomic.Int32
	ctxs    chan context.Context
}

func newBlockingCall() *blockingCall {
	return &blockingCall{release: make(chan struct{}), ctxs: make(chan context.Context, 8)}
}

func (b *blockingCall) fn(ctx context.Context) (int, error) {
	n := b.runs.Add(1)
	b.ctxs <- ctx
	select {
	case <-b.release:
		return int(n), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func TestFlightsShareOneCallAmongConcurrentCallers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var g flights[string, int]
		call := newBlockingCall()
		var wg sync.WaitGroup
		results := make(chan int, 8)
		for range 8 {
			wg.Go(func() {
				v, err := g.do(t.Context(), "img", call.fn)
				if err != nil {
					t.Errorf("do = %v", err)
				}
				results <- v
			})
		}
		synctest.Wait() // every caller is waiting on the one call
		if n := call.runs.Load(); n != 1 {
			t.Fatalf("calls = %d with 8 callers waiting, want 1", n)
		}
		close(call.release)
		wg.Wait()
		close(results)
		for v := range results {
			if v != 1 {
				t.Errorf("result = %d, want the shared call's 1", v)
			}
		}
	})
}

func TestFlightsOutliveTheCallerThatStartedThem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var g flights[string, int]
		call := newBlockingCall()
		first, cancelFirst := context.WithCancel(t.Context())
		firstErr := make(chan error, 1)
		go func() {
			_, err := g.do(first, "img", call.fn)
			firstErr <- err
		}()
		synctest.Wait()
		second := make(chan int, 1)
		go func() {
			v, err := g.do(t.Context(), "img", call.fn)
			if err != nil {
				t.Errorf("second caller = %v, want the shared result", err)
			}
			second <- v
		}()
		synctest.Wait()
		cancelFirst()
		if err := <-firstErr; !errors.Is(err, context.Canceled) {
			t.Errorf("first caller after cancelling = %v, want context.Canceled", err)
		}
		callCtx := <-call.ctxs
		synctest.Wait()
		if err := callCtx.Err(); err != nil {
			t.Fatalf("call context = %v after its starter left; want it alive for the second caller", err)
		}
		close(call.release)
		if v := <-second; v != 1 {
			t.Errorf("second caller = %d, want 1", v)
		}
	})
}

// A shared pull outlives the caller that started it, but its progress
// stops reaching that caller once it returns: the callback may write to a
// response whose handler has finished.
func TestProgressStopsReachingAStarterThatLeft(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var g flights[string, int]
		reports := make(chan func(int), 1)
		release := make(chan struct{})
		pull := func(ctx context.Context, report func(int)) (int, error) {
			reports <- report
			select {
			case <-release:
				return 1, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		var starterGot, waiterGot atomic.Int32
		starter, leave := context.WithCancel(t.Context())
		starterDone := make(chan error, 1)
		go func() {
			_, err := doReporting(starter, &g, "img", func(int) { starterGot.Add(1) }, pull)
			starterDone <- err
		}()
		report := <-reports
		waiterDone := make(chan int, 1)
		go func() {
			v, err := doReporting(t.Context(), &g, "img", func(int) { waiterGot.Add(1) }, pull)
			if err != nil {
				t.Errorf("waiter = %v, want the shared result", err)
			}
			waiterDone <- v
		}()
		synctest.Wait()
		report(10)
		if n := starterGot.Load(); n != 1 {
			t.Fatalf("starter got %d reports while waiting, want 1", n)
		}
		leave()
		if err := <-starterDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("starter after leaving = %v, want context.Canceled", err)
		}
		report(20)
		if n := starterGot.Load(); n != 1 {
			t.Errorf("starter got %d reports, %d after it returned; want none after", n, n-1)
		}
		if n := waiterGot.Load(); n != 0 {
			t.Errorf("waiter got %d reports, want none: progress goes to the starter only", n)
		}
		close(release)
		if v := <-waiterDone; v != 1 {
			t.Errorf("waiter = %d, want 1 from the pull that outlived its starter", v)
		}
	})
}

func TestFlightsCancelACallNobodyWaitsFor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var g flights[string, int]
		call := newBlockingCall()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := g.do(ctx, "img", call.fn)
			done <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("do = %v, want context.Canceled", err)
		}
		abandoned := <-call.ctxs
		synctest.Wait()
		if abandoned.Err() == nil {
			t.Error("the call went on with no caller waiting; want it cancelled")
		}
		// A later caller starts afresh instead of joining the cancelled call.
		close(call.release)
		if v, err := g.do(t.Context(), "img", call.fn); err != nil || v != 2 {
			t.Errorf("later do = %d, %v; want a fresh second call", v, err)
		}
	})
}
