package server

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// A checkpoint writes the state only when it changed since the last save:
// an unchanged state leaves the file alone, a change rewrites it.
func TestCheckpointSkipsAnUnchangedState(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(Config{Persist: PersistDir{Path: dir}, Docker: DockerDisabled{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	p := srv.wired.persist
	state := filepath.Join(dir, stateFile)
	createQueue := func(name string) {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost:4566/", strings.NewReader(`{"QueueName":"`+name+`"}`))
		req.Header.Set("Content-Type", "application/x-amz-json-1.0")
		req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("CreateQueue %s = %d %s", name, rec.Code, rec.Body)
		}
	}
	checkpoint := func() os.FileInfo {
		t.Helper()
		if err := p.checkpoint(t.Context(), false); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(state)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		return info
	}

	if checkpoint() != nil {
		t.Fatal("a checkpoint of the empty state as loaded wrote a state file")
	}
	createQueue("a")
	saved := checkpoint()
	if saved == nil {
		t.Fatal("a checkpoint after CreateQueue wrote no state file")
	}
	if again := checkpoint(); again == nil || !os.SameFile(saved, again) {
		t.Fatal("a checkpoint of the unchanged state wrote the file again")
	}
	createQueue("b")
	if again := checkpoint(); again == nil || os.SameFile(saved, again) {
		t.Fatal("a checkpoint after the second CreateQueue left the old file")
	}
}

// Serving with a data directory creates dynamodb/ under it before
// DynamoDB Local starts: the daemon refuses to bind-mount a missing
// source, and the image's uid 1000 must be able to write there whatever
// uid serve runs as.
func TestPersistCreatesTheDynamoDBLocalDirectory(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(Config{Persist: PersistDir{Path: dir}, Docker: DockerDisabled{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	info, err := os.Stat(filepath.Join(dir, ddbDir))
	if err != nil {
		t.Fatalf("the DynamoDB Local directory: %v", err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o007 != 0o007 {
		t.Fatalf("the DynamoDB Local directory is %v, want a directory every uid can write", info.Mode())
	}
}

// A crash after an object is deleted, before the next checkpoint, loses
// only the delete: the state file still names the object's blob, which
// stays stored, so the restart loads and serves the object.
func TestCrashAfterADeleteKeepsTheSavedState(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Persist: PersistDir{Path: dir}, Docker: DockerDisabled{}, LogOutput: io.Discard}
	first, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s3Call(t, first, http.MethodPut, "/crash", "", http.StatusOK)
	s3Call(t, first, http.MethodPut, "/crash/k", "hello", http.StatusOK)
	if err := first.wired.persist.checkpoint(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	s3Call(t, first, http.MethodDelete, "/crash/k", "", http.StatusNoContent)
	// Close releases what New opened without the shutdown checkpoint, as a
	// crash would.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := New(cfg)
	if err != nil {
		t.Fatalf("New after a crash = %v, want the saved state loaded", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	if body := s3Call(t, second, http.MethodGet, "/crash/k", "", http.StatusOK); body != "hello" {
		t.Errorf("GetObject after the restart = %q, want the saved object's body", body)
	}
}

// A restart resumes a Running clock at wall time, since wall time kept
// passing while no serve ran, unless the saved instant is later, which it
// keeps so emulator time never moves back. A Frozen clock holds its saved
// instant, and a Scaled one resumes from it.
func TestRestartResumesTheClock(t *testing.T) {
	const downtime = 15 * time.Hour
	wall := clock.FromWire(time.Now())
	for _, tc := range []struct {
		name  string
		saved clock.Instant
		mode  clock.Mode
		// want is the emulator time the restart starts at.
		want func(saved, wall clock.Instant) clock.Instant
	}{
		{"running, saved before the downtime", wall.Add(-downtime), clock.Running{}, func(_, wall clock.Instant) clock.Instant { return wall }},
		{"running, advanced past wall time", wall.Add(48 * time.Hour), clock.Running{}, func(saved, _ clock.Instant) clock.Instant { return saved }},
		{"frozen", wall.Add(-downtime), clock.Frozen{}, func(saved, _ clock.Instant) clock.Instant { return saved }},
		{"scaled", wall.Add(-downtime), clock.Scaled{Factor: 2}, func(saved, _ clock.Instant) clock.Instant { return saved }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Persist: PersistDir{Path: t.TempDir()}, Docker: DockerDisabled{}, LogOutput: io.Discard}
			first, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := first.wired.kernel.Clock().Restore(tc.saved, tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := first.wired.persist.checkpoint(t.Context(), true); err != nil {
				t.Fatal(err)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			before := clock.FromWire(time.Now())
			second, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			now := second.wired.kernel.Clock().Now()
			after := clock.FromWire(time.Now())
			if err := second.Close(); err != nil {
				t.Fatal(err)
			}
			// A running clock moves between the restore and the read, by
			// at most the time New took.
			low, high := tc.want(tc.saved, before), tc.want(tc.saved, after).Add(after.Sub(before))
			if now.Before(low) || high.Before(now) {
				t.Errorf("emulator time after the restart = %s, want within [%s, %s]", now, low, high)
			}
		})
	}
}

// s3Call sends a signed path-style S3 request to srv's handler and returns
// the response body, failing unless the status is want.
func s3Call(t *testing.T, srv *Server, method, path, body string, want int) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, "http://localhost:4566"+path, strings.NewReader(body))
	auth.Sign(req, []byte(body), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: aws.DefaultRegion(), Service: "s3"}, time.Now())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("%s %s = %d %s, want %d", method, path, rec.Code, rec.Body, want)
	}
	return rec.Body.String()
}
