package main

import (
	"errors"
	"io"
	"testing"
)

func TestRunNeedsExactlyOneTarget(t *testing.T) {
	for _, args := range [][]string{nil, {"-check"}, {"-service", "sqs", "-all"}, {"-service", "sqs", "extra"}} {
		if err := run(args, io.Discard); !errors.Is(err, errUsage) {
			t.Errorf("run(%q) = %v, want the usage error", args, err)
		}
	}
}

func TestRunRejectsAnUnknownService(t *testing.T) {
	if err := run([]string{"-service", "nope", "-check"}, io.Discard); err == nil {
		t.Fatal("run accepted a service without codegen/services/nope.json")
	}
}

// TestCheckedInPackagesAreCurrent is the drift check: every generated
// package, the service APIs and the compliance corpus services, matches what
// its vendored model, overlays and config produce now.
func TestCheckedInPackagesAreCurrent(t *testing.T) {
	for _, target := range []string{"-all", "-corpus"} {
		if err := run([]string{target, "-check"}, io.Discard); err != nil {
			t.Errorf("%s: %v", target, err)
		}
	}
}
