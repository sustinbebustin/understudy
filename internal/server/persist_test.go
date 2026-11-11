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
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/snapshot"
)

// stateLockFunc is a stateLocker made of a function.
type stateLockFunc func(fn func() error) error

func (f stateLockFunc) WithStateLock(fn func() error) error { return f(fn) }

// A checkpoint is a whole-state cut, so it runs under the admin handler's
// state lock: a reset holds it from the drain to the fault clear, and a cut
// between those steps would save a half-reset state. The checkpoint takes
// the lock once, writes nothing before it holds it, and writes the state
// file before it lets go.
func TestCheckpointRunsUnderTheStateLock(t *testing.T) {
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
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost:4566/", strings.NewReader(`{"QueueName":"a"}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("CreateQueue = %d %s", rec.Code, rec.Body)
	}
	state := filepath.Join(dir, stateFile)
	var locks int
	var beforeLock, insideLock error
	var written []byte
	srv.wired.persist.admin = stateLockFunc(func(fn func() error) error {
		locks++
		_, beforeLock = os.Stat(state)
		return srv.wired.admin.WithStateLock(func() error {
			err := fn()
			written, insideLock = os.ReadFile(state)
			return err
		})
	})
	if err := srv.wired.persist.checkpoint(t.Context(), false); err != nil {
		t.Fatalf("checkpoint = %v", err)
	}
	if locks != 1 {
		t.Fatalf("a checkpoint took the state lock %d times, want 1", locks)
	}
	if !errors.Is(beforeLock, fs.ErrNotExist) {
		t.Errorf("the state file existed before the checkpoint held the state lock: stat err %v", beforeLock)
	}
	if insideLock != nil {
		t.Fatalf("the checkpoint wrote no state file while it held the state lock: %v", insideLock)
	}
	after, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, written) {
		t.Errorf("the state file changed after the checkpoint let go of the state lock: %d bytes under the lock, %d after", len(written), len(after))
	}
}

// A checkpoint writes the state only when it changed since the last save:
// an unchanged state leaves the file alone, a change rewrites it. A change
// to the runtime head counts, since a restart restores it (ADR 0009): the
// clock's mode, a Frozen clock's instant, a seeded id draw and a journaled
// event. The instant of a clock that moves with wall time does not, so an
// idle server writes nothing.
func TestCheckpointSkipsAnUnchangedState(t *testing.T) {
	dir := t.TempDir()
	seed := uint64(7)
	srv, err := New(Config{Persist: PersistDir{Path: dir}, Seed: &seed, Docker: DockerDisabled{}, LogOutput: io.Discard})
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

	k := srv.wired.kernel
	c := k.Clock()
	// rewrites reports whether a checkpoint after change replaced the
	// file, and leaves the file the next step compares against.
	rewrites := func(change func()) bool {
		t.Helper()
		before := checkpoint()
		change()
		after := checkpoint()
		return !os.SameFile(before, after)
	}
	unchanged := func() {}
	advance := func() {
		t.Helper()
		if _, err := c.Advance(time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range []struct {
		name   string
		change func()
		want   bool
	}{
		{"an idle Running clock", unchanged, false},
		{"an advance of a Running clock", advance, false},
		{"a scale", func() {
			if _, err := c.Scale(2); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"an idle Scaled clock", unchanged, false},
		{"an advance of a Scaled clock", advance, false},
		{"a freeze", func() { c.Freeze() }, true},
		{"an idle Frozen clock", unchanged, false},
		{"a set of a Frozen clock", func() {
			if _, err := c.Restore(c.Now().Add(time.Minute), clock.Frozen{}); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"a seeded id draw", func() { k.IDs().RequestID() }, true},
		{"a journaled event", func() {
			if _, err := k.Journal().Append(journal.Event{Detail: journal.TimerFired{Key: clock.TimerKey{Kind: "test.probe", ID: "1"}}}); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"a resume", func() {
			if _, err := c.Resume(); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"an idle Running clock after a resume", unchanged, false},
	} {
		if got := rewrites(step.change); got != step.want {
			t.Errorf("after %s, the checkpoint rewrote the state file = %t, want %t", step.name, got, step.want)
		}
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

// A restart resumes a Running clock at its offset from wall time at the
// checkpoint's cut, since wall time kept passing while no serve ran, so an
// advance ahead of wall time survives however long the server was stopped;
// never earlier than the saved instant, so emulator time never moves back
// (ADR 0009 amendment 2026-11-11). A Frozen clock holds its saved instant,
// and a Scaled one resumes from it.
//
// The downtime is simulated by
// moving the state file's saved instant and cut wall instant back by it,
// as if the checkpoint had been taken that long ago. An archive without
// the cut wall instant resumes a Running clock at the later of wall time
// and its saved instant, absorbing a shorter advance.
func TestRestartResumesTheClock(t *testing.T) {
	const (
		downtime = 15 * time.Hour
		// advance is shorter than the downtime, long is longer.
		advance = time.Hour
		long    = 48 * time.Hour
	)
	running := clock.Running{}
	for _, tc := range []struct {
		name string
		// ahead is the clock's offset from wall time when it is checkpointed.
		ahead time.Duration
		mode  clock.Mode
		// legacy drops the cut wall instant from the state file, as an
		// archive written before checkpoints recorded it.
		legacy bool
		// want is the emulator time the restart starts at, from the state
		// file's saved instant and wall time at the restart.
		want func(saved, wall clock.Instant) clock.Instant
	}{
		{"running", 0, running, false, func(_, wall clock.Instant) clock.Instant { return wall }},
		{"running, advanced by less than the downtime", advance, running, false, func(_, wall clock.Instant) clock.Instant { return wall.Add(advance) }},
		{"running, advanced by more than the downtime", long, running, false, func(_, wall clock.Instant) clock.Instant { return wall.Add(long) }},
		{"running, behind wall time", -advance, running, false, func(_, wall clock.Instant) clock.Instant { return wall.Add(-advance) }},
		{"running, archive without the cut wall instant", 0, running, true, func(_, wall clock.Instant) clock.Instant { return wall }},
		{"running, advanced by less than the downtime, archive without the cut wall instant", advance, running, true, func(_, wall clock.Instant) clock.Instant { return wall }},
		{"running, advanced by more than the downtime, archive without the cut wall instant", long, running, true, func(saved, _ clock.Instant) clock.Instant { return saved }},
		{"frozen", -downtime, clock.Frozen{}, false, func(saved, _ clock.Instant) clock.Instant { return saved }},
		{"scaled", -downtime, clock.Scaled{Factor: 2}, false, func(saved, _ clock.Instant) clock.Instant { return saved }},
		{"scaled, archive without the cut wall instant", -downtime, clock.Scaled{Factor: 2}, true, func(saved, _ clock.Instant) clock.Instant { return saved }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := Config{Persist: PersistDir{Path: dir}, Docker: DockerDisabled{}, LogOutput: io.Discard}
			first, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			start := clock.FromWire(time.Now())
			if _, err := first.wired.kernel.Clock().Restore(start.Add(tc.ahead), tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := first.wired.persist.checkpoint(t.Context(), true); err != nil {
				t.Fatal(err)
			}
			// A Running clock's offset at the cut trails tc.ahead by at
			// most the time from start to here.
			slack := clock.FromWire(time.Now()).Sub(start)
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			saved := stopFor(t, dir, downtime, tc.legacy)
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
			low, high := tc.want(saved, before).Add(-slack), tc.want(saved, after).Add(after.Sub(before))
			if now.Before(low) || high.Before(now) {
				t.Errorf("emulator time after the restart = %s, want within [%s, %s]", now, low, high)
			}
		})
	}
}

// stopFor rewrites the state file under dir as if its checkpoint had been
// taken d ago: the saved instant and the cut wall instant move back by d.
// legacy drops the cut wall instant instead of moving it. It returns the
// saved instant now in the file.
func stopFor(t *testing.T, dir string, d time.Duration, legacy bool) clock.Instant {
	t.Helper()
	path := filepath.Join(dir, stateFile)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := snapshot.Read(bytes.NewReader(b), snapshot.ReadOptions{MaxBytes: int64(len(b))})
	if err != nil {
		t.Fatal(err)
	}
	m := &a.Manifest
	if m.CutWall == nil {
		t.Fatal("the checkpoint recorded no cut wall instant")
	}
	m.Clock.At = m.Clock.At.Add(-d)
	if legacy {
		m.CutWall = nil
	} else {
		cutWall := m.CutWall.Add(-d)
		m.CutWall = &cutWall
	}
	var out bytes.Buffer
	if err := snapshot.Write(&out, *a, snapshot.WriteOptions{Blobs: snapshot.ExternalBlobs, MaxBytes: int64(len(b)) * 2}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return m.Clock.At
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
