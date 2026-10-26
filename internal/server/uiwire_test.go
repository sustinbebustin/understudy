package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// testUISecret has the shape of a drawn secret: rand.Text's 26 characters
// of A-Z and 2-7 (https://pkg.go.dev/crypto/rand#Text).
const testUISecret = "ABCDEFGHIJKLMNOPQRSTUVWX27"

// closeOnCleanup closes srv when the test ends.
func closeOnCleanup(t *testing.T, srv *Server) {
	t.Helper()
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
}

// The text banner names the UI's URL, with this run's secret in its
// fragment, on the line after the admin line; the JSON banner carries the
// same URL as ui (ADR 0020: the secret is handed over only in the fragment
// of the UI URL the banner prints; ARCHITECTURE.md 17: the banner).
func TestBannerPrintsTheUIURL(t *testing.T) {
	srv, err := New(Config{UISecret: testUISecret, Docker: DockerDisabled{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	closeOnCleanup(t, srv)
	want := "  admin     http://127.0.0.1:4566/_understudy/v1/\n" +
		"  ui        http://127.0.0.1:4566/_understudy/ui/#s=" + testUISecret + "\n"
	if got := bannerText(t, srv); !strings.Contains(got, want) {
		t.Errorf("banner = %q, want it to hold\n%q", got, want)
	}

	var out bytes.Buffer
	srv, err = New(Config{UISecret: testUISecret, LogFormat: LogJSON, LogOutput: &out, Docker: DockerDisabled{}})
	if err != nil {
		t.Fatal(err)
	}
	closeOnCleanup(t, srv)
	srv.banner(t.Context(), Endpoints{Main: "http://127.0.0.1:4566"}, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4566})
	var found bool
	for line := range strings.Lines(out.String()) {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil || record["msg"] != "understudy is serving" {
			continue
		}
		found = true
		if got := record["ui"]; got != "http://127.0.0.1:4566/_understudy/ui/#s="+testUISecret {
			t.Errorf("JSON banner ui = %v, want http://127.0.0.1:4566/_understudy/ui/#s=%s", got, testUISecret)
		}
	}
	if !found {
		t.Fatalf("no JSON banner record in %s", out.String())
	}
}

// Without a configured secret each server draws its own (ADR 0020: each
// serve draws a secret from crypto/rand).
func TestUISecretIsDrawnWhenUnset(t *testing.T) {
	line := regexp.MustCompile(`  ui        http://127\.0\.0\.1:4566/_understudy/ui/#s=([A-Z2-7]{26})\n`)
	secrets := make([]string, 0, 2)
	for range 2 {
		srv, err := New(Config{Docker: DockerDisabled{}, LogOutput: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		closeOnCleanup(t, srv)
		got := bannerText(t, srv)
		m := line.FindStringSubmatch(got)
		if m == nil {
			t.Fatalf("banner = %q, want a ui line with a drawn secret", got)
		}
		secrets = append(secrets, m[1])
	}
	if secrets[0] == secrets[1] {
		t.Errorf("two servers drew the same secret %s", secrets[0])
	}
}

// The container listener does not serve the UI: its paths answer 404
// there, as every path but its two (the container listener exposes only
// GET /health and GET /clock, ADR 0017), with none of the UI's headers.
func TestContainerListenerDoesNotServeTheUI(t *testing.T) {
	w, err := wire(Config{Docker: DockerDisabled{}, UISecret: testUISecret}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("wire = %v", err)
	}
	t.Cleanup(func() {
		if err := w.close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	for _, path := range []string{"/_understudy/ui/", "/_understudy/ui/index.html"} {
		rec := httptest.NewRecorder()
		w.containerEdge.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:1"+path, http.NoBody))
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("container listener GET %s = %d %s, want 404 and no page", path, rec.Code, rec.Body)
		}
		if rec.Header().Get("Content-Security-Policy") != "" {
			t.Errorf("container listener GET %s carries the UI's CSP %q", path, rec.Header().Get("Content-Security-Policy"))
		}
	}
}
