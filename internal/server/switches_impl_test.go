package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/coverage"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/parity"
	"github.com/sustinbebustin/understudy/internal/server"
)

// wiredReport is the coverage report of the emulator wired in process, the
// one `understudy coverage` reads offline and docs/coverage.md is made
// from.
func wiredReport(t *testing.T) coverage.Report {
	t.Helper()
	srv, err := server.New(server.Config{Docker: server.DockerDisabled{}, DynamoDB: server.DynamoDBDisabled{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatalf("server.New = %v", err)
	}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	rec := httptest.NewRecorder()
	// Addressed as localhost: a loopback listener refuses other host names.
	srv.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost"+admin.Prefix+"/coverage?format=json", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s/coverage = %d %s", admin.Prefix, rec.Code, rec.Body)
	}
	r, err := coverage.ParseJSON(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("ParseJSON = %v", err)
	}
	if len(r.Switches) == 0 {
		t.Fatal("the wired services declare no fidelity switch; the checks prove nothing")
	}
	return r
}

// Every fidelity switch a wired service declares says what backs it, which
// operations it governs and what settles it (contract section 3): a label
// of the vocabulary, Ops naming operations of one service's registry, each
// once, and a Settles that names a scenario step or says why none exists.
// A Ruler or logsfilter switch missing from its wrapper's table has none of
// them.
func TestEverySwitchStatesItsOpsAndSettles(t *testing.T) {
	r := wiredReport(t)
	ops := map[string]bool{}
	for _, s := range r.Services {
		for _, list := range [][]coverage.Operation{s.Operations, s.Internal, s.Raw} {
			for _, op := range list {
				ops[string(s.Service)+"."+op.Name] = true
			}
		}
	}
	for _, sw := range r.Switches {
		if !slices.Contains(evidence.Labels(), sw.Label) {
			t.Errorf("%s: label %q is not one of %v", sw.Name, sw.Label, evidence.Labels())
		}
		if len(sw.Ops) == 0 {
			t.Errorf("%s: no Ops; list the operations whose behavior the switch governs", sw.Name)
		}
		services := map[string]bool{}
		for _, op := range sw.Ops {
			if !ops[op] {
				t.Errorf("%s: Ops entry %q names no operation of a wired service's registry", sw.Name, op)
			}
			svc, _, _ := strings.Cut(op, ".")
			services[svc] = true
		}
		if len(services) > 1 {
			t.Errorf("%s: Ops %v span several services; a switch governs operations of the service that declares it", sw.Name, sw.Ops)
		}
		if sorted := slices.Sorted(slices.Values(sw.Ops)); len(slices.Compact(sorted)) != len(sw.Ops) {
			t.Errorf("%s: Ops %v name an operation twice", sw.Name, sw.Ops)
		}
		switch s := sw.Settles.(type) {
		case fidelity.SettledBy:
		case fidelity.NoScenario:
			if strings.TrimSpace(s.Reason) == "" {
				t.Errorf("%s: NoScenario without a reason; say why no scenario settles it", sw.Name)
			}
		case nil:
			t.Errorf("%s: no Settles; name the scenario step that settles it, or say why none does", sw.Name)
		}
	}
}

// A switch's SettledBy and the checked-in scenarios' claims on it name each
// other: every claim on a switch is on the step the switch's Settles names,
// and every SettledBy names a step of a checked-in scenario with a claim on
// that switch, so the coverage page's evidence column can join them
// (contract section 4, CheckEvidence).
func TestSwitchSettlesMatchTheScenarioClaims(t *testing.T) {
	settledBy := map[string]string{}
	for _, sw := range wiredReport(t).Switches {
		if s, ok := sw.Settles.(fidelity.SettledBy); ok {
			settledBy[sw.Name] = s.Step.String()
		}
	}
	fsys := os.DirFS("../../testdata/parity")
	ids, err := parity.ListScenarios(fsys)
	if err != nil || len(ids) == 0 {
		t.Fatalf("ListScenarios = %v, %v; want the checked-in scenarios", ids, err)
	}
	claimed := map[string]string{}
	for _, id := range ids {
		sc, err := parity.ReadScenario(fsys, id)
		if err != nil {
			t.Fatalf("ReadScenario(%s) = %v", id, err)
		}
		for _, c := range sc.Settles {
			target, ok := c.Target.(parity.SwitchTarget)
			if !ok {
				continue
			}
			step := id + "#" + c.Step
			claimed[target.Name] = step
			if want, ok := settledBy[target.Name]; !ok || want != step {
				t.Errorf("%s settles switch %s, whose Settles names %q; set it to fidelity.SettledBy %s", step, target.Name, want, step)
			}
		}
	}
	for name, step := range settledBy {
		if claimed[name] != step {
			t.Errorf("switch %s is SettledBy %s, but no checked-in scenario claims it there; add the settles entry or make it NoScenario", name, step)
		}
	}
}
