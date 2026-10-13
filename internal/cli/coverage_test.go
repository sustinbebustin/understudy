package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/understudytest"
)

// coverage --service lists every operation of the service with its
// status, without the protocol table no service owns, and only the
// service's fidelity switches.
func TestCoverageByServiceListsOperations(t *testing.T) {
	endpoint := closedEndpoint(t)
	js := runCLI(t.Context(), endpoint, nil, "coverage", "--service", "sqs", "--format", "json")
	var report struct {
		Services []struct {
			Operations []struct {
				Name   string `json:"name"`
				Status struct {
					Kind string `json:"kind"`
				} `json:"status"`
			} `json:"operations"`
		} `json:"services"`
		Switches []struct{ Name string } `json:"switches"`
	}
	if err := json.Unmarshal([]byte(js.stdout), &report); err != nil || len(report.Services) != 1 {
		t.Fatalf("coverage --service sqs --format json = %d %q: %v", js.code, js.stderr, err)
	}
	for _, s := range report.Switches {
		if !strings.HasPrefix(s.Name, "sqs.") {
			t.Errorf("coverage --service sqs reports switch %s", s.Name)
		}
	}
	text := runCLI(t.Context(), endpoint, nil, "coverage", "--service", "sqs", "--switches")
	if text.code != admin.ExitOK || strings.Contains(text.stdout, "Protocol compliance") || strings.Contains(text.stdout, "sns.") {
		t.Errorf("coverage --service sqs --switches = %d:\n%s", text.code, text.stdout)
	}
	lines := strings.Split(text.stdout, "\n")
	for _, op := range report.Services[0].Operations {
		listed := false
		for _, line := range lines {
			fields := strings.Fields(line)
			listed = listed || len(fields) >= 2 && fields[0] == op.Name && fields[1] == op.Status.Kind
		}
		if !listed {
			t.Errorf("coverage --service sqs does not list %s as %s:\n%s", op.Name, op.Status.Kind, text.stdout)
		}
	}
	events := runCLI(t.Context(), endpoint, nil, "coverage", "--service", "eventbridge", "--switches")
	if !strings.Contains(events.stdout, "events.pattern.") {
		t.Errorf("coverage --service eventbridge --switches lists none of EventBridge's switches:\n%s", events.stdout)
	}
}

// With no server at the endpoint, coverage builds the emulator in process
// and reports exactly what a server reports.
func TestCoverageOfflineEqualsServer(t *testing.T) {
	inst := understudytest.Start(t, understudytest.Options{})
	online := runCLI(t.Context(), inst.Endpoint(), nil, "coverage", "--format", "json")
	offline := runCLI(t.Context(), closedEndpoint(t), nil, "coverage", "--format", "json")
	if online.code != admin.ExitOK || offline.code != admin.ExitOK {
		t.Fatalf("coverage online %d %s, offline %d %s", online.code, online.stderr, offline.code, offline.stderr)
	}
	if online.stdout != offline.stdout {
		t.Errorf("offline report differs from the server's:\nonline:  %.300s\noffline: %.300s", online.stdout, offline.stdout)
	}
	text := runCLI(t.Context(), closedEndpoint(t), nil, "coverage", "--service", "sqs")
	if !strings.Contains(text.stdout, "this binary (no server answered") || !strings.Contains(text.stdout, "sqs") || strings.Contains(text.stdout, "  sns") {
		t.Errorf("offline text report for sqs:\n%s", text.stdout)
	}
	bad := runCLI(t.Context(), closedEndpoint(t), nil, "coverage", "--service", "nope")
	if bad.code != admin.ExitUsage || !strings.Contains(bad.stderr, "sqs") {
		t.Errorf("--service nope = %d %q, want 2 listing the services", bad.code, bad.stderr)
	}
}
