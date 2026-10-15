package server

import (
	"bytes"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// A clock frozen by the setting stays frozen across a --persist restart
// without it, and the restarted serve's banner says so, where the clock
// came from and how to run it; a running clock gets no clock row.
func TestBannerNamesARestoredFrozenClock(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	first, err := New(Config{Persist: PersistDir{Path: dir}, ClockStart: ClockFrozen{At: at}, Docker: DockerDisabled{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if got := bannerText(t, first); !strings.Contains(got, "clock     frozen at 2026-01-02T03:04:05Z (set by --clock") {
		t.Errorf("banner of the frozen serve = %q, want the frozen clock and its setting", got)
	}
	if err := first.wired.persist.checkpoint(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := New(Config{Persist: PersistDir{Path: dir}, Docker: DockerDisabled{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	want := "clock     frozen at 2026-01-02T03:04:05Z (restored with the saved state of --persist " + second.wired.data.path + "); 'understudy clock resume' runs it"
	if got := bannerText(t, second); !strings.Contains(got, want) {
		t.Errorf("banner after the restart = %q, want %q", got, want)
	}

	running, err := New(Config{Docker: DockerDisabled{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := running.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	if got := bannerText(t, running); strings.Contains(got, "clock ") {
		t.Errorf("banner of a running clock = %q, want no clock row", got)
	}
}

// bannerText is the text banner srv prints.
func bannerText(t *testing.T, srv *Server) string {
	t.Helper()
	var out bytes.Buffer
	srv.cfg.LogOutput = &out
	srv.banner(t.Context(), Endpoints{Main: "http://127.0.0.1:4566"}, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4566})
	return out.String()
}

// A listener asked for on port 0 is named in the exposure warning by the
// port it bound, which is what a peer reaches.
func TestExposureWarningNamesTheBoundPort(t *testing.T) {
	cfg := Config{Listen: netip.MustParseAddrPort("0.0.0.0:0"), AdminToken: "token"}
	bound := &net.TCPAddr{IP: net.IPv4zero, Port: 45123}
	got := exposureWarning(cfg, bound)
	if !strings.Contains(got, "the AWS API on 0.0.0.0:45123 answers anyone") {
		t.Errorf("exposureWarning = %q, want it to name 0.0.0.0:45123", got)
	}
}

// With a token, the exposure warning names what exposes the AWS API in
// both SigV4 modes: the public dummy pair test/test verifies even under
// --sigv4 verify, so the warning must not claim that signatures go
// unchecked.
func TestExposureWarningNamesTheDummyKey(t *testing.T) {
	for _, mode := range []SigV4Mode{SigV4ParseOnly, SigV4Verify} {
		cfg := Config{Listen: netip.MustParseAddrPort("0.0.0.0:4566"), AdminToken: "token", SigV4: mode}
		got := exposureWarning(cfg, &net.TCPAddr{IP: net.IPv4zero, Port: 4566})
		if !strings.Contains(got, "public dummy pair test/test, which verifies even under --sigv4 verify") {
			t.Errorf("exposureWarning under %v = %q, want it to name the test/test pair", mode, got)
		}
		if strings.Contains(got, "without checking signatures") {
			t.Errorf("exposureWarning under %v = %q, which claims signatures go unchecked", mode, got)
		}
	}
}
