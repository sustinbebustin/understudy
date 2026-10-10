package store_test

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/sustinbebustin/understudy/internal/store"
)

func TestWaitersCancelledWaitsLeaveNoRegistrations(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var w store.Waiters
		const key = store.WaitKey("queue")
		for range 1000 {
			ctx, cancel := context.WithCancel(t.Context())
			woke := w.Wait(ctx, key)
			cancel()
			synctest.Wait()
			select {
			case <-woke:
				t.Fatal("a cancelled wait was woken without a Wake")
			default:
			}
		}
		if n := w.Len(key); n != 0 {
			t.Fatalf("Len = %d after 1,000 cancelled waits, want 0", n)
		}
	})
}

func TestWaitersWakeClosesEveryRegistrationForTheKey(t *testing.T) {
	t.Parallel()
	var w store.Waiters
	a1 := w.Wait(t.Context(), "a")
	a2 := w.Wait(t.Context(), "a")
	b := w.Wait(t.Context(), "b")
	w.Wake("a")
	<-a1
	<-a2
	select {
	case <-b:
		t.Fatal("Wake(a) woke a waiter on b")
	default:
	}
	if w.Len("a") != 0 || w.Len("b") != 1 {
		t.Fatalf("Len(a) = %d, Len(b) = %d; want 0 and 1", w.Len("a"), w.Len("b"))
	}
}

func TestWaitersWakeWithoutWaitersIsANoOp(t *testing.T) {
	t.Parallel()
	var w store.Waiters
	w.Wake("nobody")
	if w.Len("nobody") != 0 {
		t.Fatalf("Len = %d, want 0", w.Len("nobody"))
	}
	woke := w.Wait(t.Context(), "nobody")
	select {
	case <-woke:
		t.Fatal("an earlier Wake woke a later Wait")
	default:
	}
}

func TestWaitersWaitAfterContextEndedLeavesNoRegistration(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var w store.Waiters
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		w.Wait(ctx, "k")
		synctest.Wait()
		if n := w.Len("k"); n != 0 {
			t.Fatalf("Len = %d, want 0", n)
		}
	})
}
