package cli_test

import (
	"strings"
	"testing"
)

// faults add --help's usage line names every match flag a delivery rule
// needs and every action the exactly-one-action check counts, and an
// example shows an sns-to-http rule.
func TestFaultsAddUsageNamesEveryAction(t *testing.T) {
	help := runCLI(t.Context(), closedEndpoint(t), nil, "faults", "add", "--help")
	_, rest, ok := strings.Cut(help.stdout, "Usage:\n")
	if !ok {
		t.Fatalf("faults add --help has no Usage section:\n%s", help.stdout)
	}
	usage, _, _ := strings.Cut(rest, "\n")
	for _, f := range []string{"--mechanism", "--error", "--throttle", "--latency", "--after-commit", "--status-ok", "--drop", "--http-status", "--http-timeout"} {
		if !strings.Contains(usage, f+" ") && !strings.Contains(usage, f+")") {
			t.Errorf("faults add usage line lacks %s:\n%s", f, usage)
		}
	}
	const example = "understudy faults add --phase delivery --mechanism sns-to-http --http-status 503 --next 1"
	if !strings.Contains(help.stdout, example) {
		t.Errorf("faults add --help lacks the example %q:\n%s", example, help.stdout)
	}
}
