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
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(`{"QueueName":"`+name+`"}`))
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
