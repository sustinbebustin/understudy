package main

import (
	"bytes"
	"errors"
	"testing"
)

func TestRunWritesVersion(t *testing.T) {
	var out bytes.Buffer
	if err := run(&out); err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}
	if got, want := out.String(), "understudy dev\n"; got != want {
		t.Errorf("run() wrote %q, want %q", got, want)
	}
}

var errClosed = errors.New("stdout closed")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errClosed }

func TestRunReturnsWriteError(t *testing.T) {
	if err := run(failingWriter{}); !errors.Is(err, errClosed) {
		t.Errorf("run() error = %v, want %v", err, errClosed)
	}
}
