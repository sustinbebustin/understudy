//go:build unix

package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
)

// A journal file whose disk stops answering does not hold shutdown: closing
// the wiring returns at the journal's deadline with an error that names the
// file and counts the events it may lack. A named pipe nobody reads stands
// in for the stalled disk: its writer blocks once the pipe's buffer is full.
func TestCloseDoesNotWaitForAStalledJournalFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.ndjson")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := wire(Config{Journal: Journal{File: path}, Docker: DockerDisabled{}}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("wire = %v", err)
	}
	w.journalGrace = 100 * time.Millisecond
	// Far more than a pipe buffer holds.
	for i := range 5000 {
		if _, err := w.journal.Append(journal.Event{Detail: journal.TimerFired{Key: clock.TimerKey{Kind: "test.probe", ID: strconv.Itoa(i)}}}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		// Frees the writer blocked in its last write, so it can exit.
		r, err := os.Open(path)
		if err != nil {
			t.Error(err)
			return
		}
		go func() {
			// The copy ends at EOF, once the writer closes the pipe.
			// Nothing to report: the test has finished by then.
			if _, err := io.Copy(io.Discard, r); err != nil {
				return
			}
			if err := r.Close(); err != nil {
				return
			}
		}()
	})

	closed := make(chan error, 1)
	go func() { closed <- w.close(context.Background()) }()
	var closeErr error
	select {
	case closeErr = <-closed:
	case <-time.After(time.Minute):
		t.Fatal("close was still waiting for the journal file a minute after its deadline of 100ms")
	}
	var fileErr *journal.FileError
	var unwritten *journal.UnwrittenError
	if !errors.As(closeErr, &fileErr) || fileErr.Path != path || !errors.As(closeErr, &unwritten) {
		t.Fatalf("close = %v, want an error naming the journal file %s and wrapping a *journal.UnwrittenError", closeErr, path)
	}
	if unwritten.Queued == 0 {
		t.Errorf("UnwrittenError.Queued = 0, want the events still queued for the stalled file")
	}
}
