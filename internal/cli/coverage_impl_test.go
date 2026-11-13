package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
)

// coverage --parity adds the checked-in replay baseline: with the overlays,
// the Markdown gains the evidence column and the pending-retirement list,
// in which the switch its matching step settles is listed
// (internal/parity/baseline.json, ses/SES-03#02-both).
func TestCoverageParityAddsTheEvidenceColumn(t *testing.T) {
	t.Parallel()
	md := runCLI(t.Context(), closedEndpoint(t), nil, "coverage", "--format", "markdown",
		"--overlays", "../../codegen/overlays", "--parity", "../parity/baseline.json")
	if md.code != admin.ExitOK {
		t.Fatalf("coverage --parity = %d: %s", md.code, md.stderr)
	}
	for _, want := range []string{
		"Evidence is the weakest claim attributed to the operation",
		"| Operation | Status | Evidence | Note |",
		"### Settled by recording, pending retirement",
		"| `ses.error.unverified.text` | inferred | ses/SES-03/02-both.json | body/ErrorResponse/Error/Message |",
	} {
		if !strings.Contains(md.stdout, want) {
			t.Errorf("coverage --parity Markdown lacks %q", want)
		}
	}
	js := runCLI(t.Context(), closedEndpoint(t), nil, "coverage", "--format", "json", "--service", "kms", "--parity", "../parity/baseline.json")
	if js.code != admin.ExitOK || !strings.Contains(js.stdout, `"parity":{"kind":"read"`) {
		t.Errorf("coverage --format json --parity = %d %s: %.200s", js.code, js.stderr, js.stdout)
	}
}

// A baseline that cannot be read, parsed or squared with the switches is
// refused, naming the file and what failed, with the step that fixes it.
func TestCoverageParityRefusesABadBaseline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	missing := filepath.Join(dir, "missing.json")
	broken := write("broken.json", `{"version": 1,`)
	unknownSwitch := write("unknown-switch.json", `{"version": 1, "scenarios": [{"scenario": "kms/K", "steps": [
	  {"step": "a", "fixture": "kms/K/a.json", "operation": "kms.CreateAlias", "outcome": {"kind": "match"}}],
	  "claims": [{"target": {"kind": "switch", "name": "kms.no-such-switch"}, "step": "a", "field": "status", "result": {"kind": "settled"}}]}]}`)
	for _, tc := range []struct {
		path string
		want []string
	}{
		{missing, []string{"reading parity baseline " + missing}},
		{broken, []string{"parsing parity baseline " + broken, "not a baseline document"}},
		{unknownSwitch, []string{"checking parity baseline " + unknownSwitch + " against the switches", "kms.no-such-switch"}},
	} {
		res := runCLI(t.Context(), closedEndpoint(t), nil, "coverage", "--format", "markdown", "--parity", tc.path)
		if res.code != admin.ExitFailed {
			t.Errorf("coverage --parity %s = %d, want %d", tc.path, res.code, admin.ExitFailed)
		}
		for _, w := range append(tc.want, "Point --parity at internal/parity/baseline.json, or run 'just parity-baseline'") {
			if !strings.Contains(res.stderr, w) {
				t.Errorf("coverage --parity %s: stderr %q lacks %q", tc.path, res.stderr, w)
			}
		}
		if res.stdout != "" {
			t.Errorf("coverage --parity %s printed a report: %.200s", tc.path, res.stdout)
		}
	}
}
