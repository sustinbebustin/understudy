package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
)

// Every faults add flag lands in the rule the server holds; the server
// reports the default origin, any, as absent.
func TestFaultsAddFlags(t *testing.T) {
	inst := start(t)
	cases := []struct {
		args []string
		want admin.FaultRule
	}{
		{
			args: []string{
				"--id", "throttle-orders", "--operation", "sqs.SendMessage", "--operation", "sqs.ReceiveMessage", "--resource", "arn:aws:sqs:*:*:orders-*",
				"--account", "000000000000", "--region", "us-east-1", "--origin", "internal", "--throttle", "--next", "3",
			},
			want: admin.FaultRule{
				ID: "throttle-orders", Phase: admin.RequestPhase,
				Match: admin.FaultMatch{
					Operations: []string{"sqs.SendMessage", "sqs.ReceiveMessage"}, Resource: "arn:aws:sqs:*:*:orders-*",
					Accounts: []string{"000000000000"}, Regions: []string{"us-east-1"}, Origin: admin.InternalOrigin,
				},
				Action: admin.Throttle{}, Trigger: admin.NextN{N: 3},
			},
		},
		{
			args: []string{"--id", "slow", "--service", "sqs", "--latency", "250ms", "--probability", "0.5"},
			want: admin.FaultRule{
				ID: "slow", Phase: admin.RequestPhase, Match: admin.FaultMatch{Services: []string{"sqs"}},
				Action: admin.Latency{Wall: 250 * time.Millisecond}, Trigger: admin.Probability{P: 0.5},
			},
		},
		{
			args: []string{"--id", "gone", "--operation", "sqs.GetQueueUrl", "--error", "QueueDoesNotExist", "--message", "injected"},
			want: admin.FaultRule{
				ID: "gone", Phase: admin.RequestPhase, Match: admin.FaultMatch{Operations: []string{"sqs.GetQueueUrl"}},
				Action: admin.InjectError{Shape: "QueueDoesNotExist", Message: "injected"}, Trigger: admin.Always{},
			},
		},
	}
	for _, c := range cases {
		r := runCLI(t.Context(), inst.Endpoint(), nil, append([]string{"faults", "add", "--output", "json"}, c.args...)...)
		if r.code != admin.ExitOK {
			t.Errorf("faults add %q = %d: %s", c.args, r.code, r.stderr)
			continue
		}
		var f admin.Fault
		if err := json.Unmarshal([]byte(r.stdout), &f); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.Rule, c.want) {
			t.Errorf("faults add %q put %+v, want %+v", c.args, f.Rule, c.want)
		}
	}
	both := runCLI(t.Context(), inst.Endpoint(), nil, "faults", "add", "--throttle", "--next", "1", "--probability", "0.5")
	if both.code != admin.ExitUsage {
		t.Errorf("--next with --probability = %d, want 2", both.code)
	}
}

// What faults list --output json prints loads back with faults load.
func TestFaultsListLoadsBack(t *testing.T) {
	inst := start(t)
	if r := runCLI(t.Context(), inst.Endpoint(), nil, "faults", "add", "--id", "kept", "--operation", "sqs.SendMessage", "--throttle", "--next", "2"); r.code != admin.ExitOK {
		t.Fatalf("faults add = %d %s", r.code, r.stderr)
	}
	listed := runCLI(t.Context(), inst.Endpoint(), nil, "faults", "list", "--output", "json")
	saved := filepath.Join(t.TempDir(), "saved.json")
	if err := os.WriteFile(saved, []byte(listed.stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := runCLI(t.Context(), inst.Endpoint(), nil, "faults", "clear"); r.code != admin.ExitOK {
		t.Fatalf("faults clear = %d %s", r.code, r.stderr)
	}
	if r := runCLI(t.Context(), inst.Endpoint(), nil, "faults", "load", saved); r.code != admin.ExitOK {
		t.Fatalf("faults load of the saved list = %d %s", r.code, r.stderr)
	}
	faults, err := inst.Admin().Faults(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(faults) != 1 || faults[0].Rule.ID != "kept" || faults[0].Rule.Trigger != (admin.NextN{N: 2}) {
		t.Errorf("after loading the saved list: %+v, want the rule kept with next 2", faults)
	}
}
