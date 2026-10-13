package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// faults add refuses a rule the server would refuse with the flag to fix,
// and lists the errors an operation sends in the form --error takes: the
// short name, or the full shape id where two of them share it.
func TestFaultsAddRefusalsNameTheFix(t *testing.T) {
	inst := start(t)
	cases := []struct {
		args       []string
		want, none string
	}{
		{[]string{"--operation", "SendMessage", "--throttle"}, `--operation "SendMessage" is not service.Operation, such as sqs.SendMessage`, "json:"},
		{[]string{"--service", "sqs", "--error", "QueueDoesNotExist"}, "--error needs at least one --operation service.Operation", "match.operations"},
		{[]string{"--operation", "sqs.SendMessage", "--error", "Nope"}, "QueueDoesNotExist", "com.amazonaws.sqs#"},
		{[]string{"--operation", "dynamodb.GetItem", "--error", "Nope"}, "com.amazonaws.dynamodb#ThrottlingException", "com.amazonaws.dynamodb#ResourceNotFoundException"},
		{[]string{"--operation", "dynamodb.GetItem", "--error", "ThrottlingException"}, "(known: com.amazonaws.dynamodb#ThrottlingException, com.amazon.coral.service#ThrottlingException)", "json:"},
	}
	for _, c := range cases {
		r := runCLI(t.Context(), inst.Endpoint(), nil, append([]string{"faults", "add"}, c.args...)...)
		if r.code != admin.ExitUsage || !strings.Contains(r.stderr, c.want) || strings.Contains(r.stderr, c.none) {
			t.Errorf("faults add %q = %d, stderr %q; want 2 naming %q without %q", c.args, r.code, r.stderr, c.want, c.none)
		}
	}
}

// A refused rule the user gave no id is never named by the id the CLI
// minted for it: faults add says nothing of an id, and faults load names
// the rule by its index, adding the id only when the file gave one.
func TestFaultRefusalsNeverQuoteAMintedID(t *testing.T) {
	inst := start(t)
	r := runCLI(t.Context(), inst.Endpoint(), nil, "faults", "add", "--operation", "sqs.SendMessage", "--error", "Nope")
	if r.code != admin.ExitUsage || strings.Contains(r.stderr, "rule-") {
		t.Errorf("faults add of a refused rule = %d, stderr %q; want 2 without a minted id", r.code, r.stderr)
	}
	rules := filepath.Join(t.TempDir(), "rules.json")
	file := `[{"id": "named", "phase": "request", "match": {"operations": ["sqs.SendMessage"]}, "action": {"kind": "throttle"}, "trigger": {"kind": "always"}},
		{"phase": "request", "match": {"operations": ["sqs.SendMessage"]}, "action": {"kind": "error", "shape": "Nope"}, "trigger": {"kind": "always"}}]`
	if err := os.WriteFile(rules, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	r = runCLI(t.Context(), inst.Endpoint(), nil, "faults", "load", rules)
	if r.code == admin.ExitOK || !strings.Contains(r.stderr, "rule 1 of "+rules+", after 1 rule(s) were added") || strings.Contains(r.stderr, "rule-") {
		t.Errorf("faults load with a refused unnamed rule = %d, stderr %q; want rule 1 named by index only", r.code, r.stderr)
	}
}

// A trigger or action flag given a zero value is passed on rather than
// read as unset, so --next 0 or --probability 0 is refused instead of
// adding a rule that fires on every matching call.
func TestFaultsAddZeroValuesAreRefused(t *testing.T) {
	inst := start(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--throttle", "--next", "0"}, "at least 1"},
		{[]string{"--throttle", "--probability", "0"}, "probability"},
		{[]string{"--latency", "0s"}, `--latency "0s" is not a positive duration`},
		{[]string{"--latency", "-1s"}, `--latency "-1s" is not a positive duration`},
		{[]string{"--throttle", "--next", "0", "--probability", "0.5"}, "at most one of --next and --probability"},
	}
	for _, c := range cases {
		r := runCLI(t.Context(), inst.Endpoint(), nil, append([]string{"faults", "add", "--operation", "sqs.SendMessage"}, c.args...)...)
		if r.code != admin.ExitUsage || !strings.Contains(r.stderr, c.want) {
			t.Errorf("faults add %q = %d, stderr %q; want 2 naming %q", c.args, r.code, r.stderr, c.want)
		}
	}
	faults, err := inst.Admin().Faults(t.Context())
	if err != nil || len(faults) != 0 {
		t.Errorf("after the refused adds the server holds %+v, %v; want no rules", faults, err)
	}
}

// A warning about a rule goes to stderr, naming the rule once, so stdout
// holds the table's rows only; JSON carries it on the rule instead.
func TestFaultWarningsGoToStderr(t *testing.T) {
	inst := start(t)
	add := runCLI(t.Context(), inst.Endpoint(), nil, "faults", "add", "--id", "t1", "--operation", "sqs.SendMessage", "--throttle", "--next", "3")
	list := runCLI(t.Context(), inst.Endpoint(), nil, "faults", "list")
	for name, r := range map[string]result{"faults add": add, "faults list": list} {
		if r.code != admin.ExitOK || strings.Contains(r.stdout, "warning") || !strings.HasPrefix(r.stderr, `warning: fault rule "t1" injects `) ||
			strings.Count(r.stderr, "t1") != 1 {
			t.Errorf("%s = %d, stdout %q, stderr %q; want the rows on stdout and one warning naming t1 once on stderr", name, r.code, r.stdout, r.stderr)
		}
	}
	js := runCLI(t.Context(), inst.Endpoint(), nil, "faults", "list", "--output", "json")
	var listed admin.FaultList
	if err := json.Unmarshal([]byte(js.stdout), &listed); err != nil || len(listed.Faults) != 1 || listed.Faults[0].Warning == "" || js.stderr != "" {
		t.Errorf("faults list --output json = %d, stdout %q, stderr %q, %v; want the warning on the rule and nothing on stderr", js.code, js.stdout, js.stderr, err)
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
