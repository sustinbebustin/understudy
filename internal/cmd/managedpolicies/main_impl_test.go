package main

import (
	"strings"
	"testing"
)

// Asking for help prints the usage and succeeds: it is not a failed run.
func TestRunHelpSucceeds(t *testing.T) {
	for _, arg := range []string{"-h", "-help"} {
		var stderr strings.Builder
		if err := run(t.Context(), []string{arg}, &stderr); err != nil {
			t.Errorf("run(%q) = %v, want nil", arg, err)
		}
		if !strings.Contains(stderr.String(), "-verify") {
			t.Errorf("run(%q) printed %q, want the usage", arg, stderr.String())
		}
	}
}
