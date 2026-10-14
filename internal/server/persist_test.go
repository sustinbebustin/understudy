package server

import (
	"bytes"
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
	"github.com/sustinbebustin/understudy/internal/snapshot"
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

// A checkpoint whose archive would pass snapshotMaxBytes fails naming the
// setting and keeps the previous state file, which the next start loads;
// a start whose cap the state file passes is refused naming the setting.
func TestCheckpointOverTheCapKeepsThePreviousState(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Persist: PersistDir{Path: dir}, SnapshotMaxBytes: 128 << 10, Docker: DockerDisabled{}, LogOutput: io.Discard}
	first, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sqsCall(t, first, "CreateQueue", `{"QueueName":"kept"}`)
	if err := first.wired.persist.checkpoint(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, stateFile)
	saved, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	// The message body is part of the queue's state, so it takes the
	// archive past the 128 KiB cap.
	sqsCall(t, first, "SendMessage", `{"QueueUrl":"http://localhost:4566/000000000000/kept","MessageBody":"`+strings.Repeat("x", 200<<10)+`"}`)
	err = first.wired.persist.checkpoint(t.Context(), false)
	if !errors.As(err, new(*snapshot.WriteLimitError)) || !strings.Contains(err.Error(), "--snapshot-max-bytes") || !strings.Contains(err.Error(), "previous state file is kept") {
		t.Fatalf("checkpoint over the cap = %v; want *snapshot.WriteLimitError naming --snapshot-max-bytes and keeping the previous state file", err)
	}
	if now, err := os.ReadFile(state); err != nil || !bytes.Equal(now, saved) {
		t.Fatalf("state file after the refused checkpoint changed (%v); want the previous one kept", err)
	}
	if tmps, err := filepath.Glob(filepath.Join(dir, stateFile+".*.tmp")); err != nil || len(tmps) != 0 {
		t.Fatalf("the refused checkpoint left %v (%v)", tmps, err)
	}
	// Close releases what New opened without the shutdown checkpoint.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	tight := cfg
	tight.SnapshotMaxBytes = int64(len(saved)) - 1
	_, err = New(tight)
	if !errors.As(err, new(*snapshot.SizeError)) || !strings.Contains(err.Error(), "--snapshot-max-bytes") {
		t.Fatalf("New with a cap below the state file = %v; want *snapshot.SizeError naming --snapshot-max-bytes", err)
	}

	second, err := New(cfg)
	if err != nil {
		t.Fatalf("New after the refused checkpoint = %v, want the previous state loaded", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	sqsCall(t, second, "GetQueueUrl", `{"QueueName":"kept"}`)
	if body := sqsCall(t, second, "ReceiveMessage", `{"QueueUrl":"http://localhost:4566/000000000000/kept"}`); strings.Contains(body, "xxxx") {
		t.Errorf("ReceiveMessage after the restart = %.200s; want the state of the last checkpoint that fit, without the message", body)
	}
}

// sqsCall sends an SQS JSON request to srv's handler and returns the
// response body, failing unless it answers 200.
func sqsCall(t *testing.T, srv *Server, operation, body string) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost:4566/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS."+operation)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d %.300s", operation, rec.Code, rec.Body)
	}
	return rec.Body.String()
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
