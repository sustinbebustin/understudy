package server

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// A start that fails before serving closes the main and TLS listeners it
// was handed, since shutdown drains only what the session serves.
func TestFailedStartClosesTheListeners(t *testing.T) {
	srv := unserved(t)
	main, secure := loopback(t), loopback(t)
	// A timer kind registered after wire checked the bindings, here with no
	// handler, is the bind error start checks for once the pollers have
	// registered theirs.
	const kind = "test.unhandled"
	srv.wired.kernel.Timers().Handle(kind, nil)
	if err := srv.begin(); err != nil {
		t.Fatal(err)
	}
	sess, err := srv.start(t.Context(), main, secure)
	if ke, ok := errors.AsType[*kernel.TimerKindError](err); !ok || ke.Kind != kind {
		t.Fatalf("start = %v, want the TimerKindError for %s", err, kind)
	}
	if err := srv.shutdown(t.Context(), sess); err != nil {
		t.Errorf("shutdown = %v", err)
	}
	for name, ln := range map[string]net.Listener{"main": main, "TLS": secure} {
		if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
			t.Errorf("Accept on the %s listener after the failed start = %v, want net.ErrClosed", name, err)
		}
	}
}

// A timer armed under a kind nothing handles, as a Rearm of loaded state
// could arm, fails Serve with a named error rather than never firing.
func TestServeRefusesAnUnhandledTimer(t *testing.T) {
	srv := unserved(t)
	orphan := clock.TimerKey{Kind: "fake.unhandled", ID: "t1"}
	srv.wired.kernel.Timers().Scheduler().Arm(orphan, srv.wired.kernel.Clock().Now())
	err := srv.Serve(t.Context())
	if ue, ok := errors.AsType[*UnhandledTimerError](err); !ok || len(ue.Keys) != 1 || ue.Keys[0] != orphan {
		t.Fatalf("Serve = %v, want an UnhandledTimerError naming %v", err, orphan)
	}
}

// The boot check wire makes after loading state names every unhandled
// timer and passes a scheduler whose timers all have handlers.
func TestCheckTimersNamesUnhandledKinds(t *testing.T) {
	srv := unserved(t)
	timers := srv.wired.kernel.Timers()
	if err := checkTimers(timers); err != nil {
		t.Fatalf("checkTimers on a fresh wiring = %v, want nil", err)
	}
	orphan := clock.TimerKey{Kind: "fake.unhandled", ID: "t1"}
	timers.Scheduler().Arm(orphan, srv.wired.kernel.Clock().Now())
	err := checkTimers(timers)
	if ue, ok := errors.AsType[*UnhandledTimerError](err); !ok || len(ue.Keys) != 1 || ue.Keys[0] != orphan {
		t.Errorf("checkTimers = %v, want an UnhandledTimerError naming %v", err, orphan)
	}
}

// unserved is a Server that was never served, closed when t ends.
func unserved(t *testing.T) *Server {
	t.Helper()
	srv, err := New(Config{Docker: DockerDisabled{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	return srv
}

// loopback is a listener on an ephemeral loopback port.
func loopback(t *testing.T) net.Listener {
	t.Helper()
	ln, err := listen(t.Context(), netip.MustParseAddrPort("127.0.0.1:0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Error(err)
		}
	})
	return ln
}
