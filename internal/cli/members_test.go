package cli_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
)

// The documented TransactionConflict reason (smithy-protocol-tests.md
// 12.2, aws-doc).
const conflictMembers = `{"CancellationReasons":[{"Code":"TransactionConflict","Message":"Transaction is ongoing for the item."}]}`

// faults add --members sets the --error shape's members: the server holds
// them, --output json carries them, and the table names the action as
// before.
func TestFaultsAddMembers(t *testing.T) {
	inst := start(t)
	r := runCLI(t.Context(), inst.Endpoint(), nil, "faults", "add", "--output", "json", "--id", "tx",
		"--operation", "dynamodb.TransactWriteItems", "--error", "TransactionCanceledException", "--members", conflictMembers, "--next", "1")
	if r.code != admin.ExitOK {
		t.Fatalf("faults add --members = %d: %s", r.code, r.stderr)
	}
	var f admin.Fault
	if err := json.Unmarshal([]byte(r.stdout), &f); err != nil {
		t.Fatalf("faults add --output json printed %q: %v", r.stdout, err)
	}
	a, ok := f.Rule.Action.(admin.InjectError)
	var got bytes.Buffer
	if ok && len(a.Members) > 0 {
		if err := json.Compact(&got, a.Members); err != nil {
			t.Fatal(err)
		}
	}
	if !ok || a.Shape != "TransactionCanceledException" || got.String() != conflictMembers || f.Rule.Trigger != (admin.NextN{N: 1}) {
		t.Errorf("faults add put %+v, want TransactionCanceledException with members %s and next 1", f.Rule, conflictMembers)
	}
	faults, err := inst.Admin().Faults(t.Context())
	if err != nil || len(faults) != 1 {
		t.Fatalf("the server holds %+v, %v; want the rule", faults, err)
	}
	if held, ok := faults[0].Rule.Action.(admin.InjectError); !ok || !bytes.Contains(held.Members, []byte(`"TransactionConflict"`)) {
		t.Errorf("the server holds action %#v, want the members", faults[0].Rule.Action)
	}
	list := runCLI(t.Context(), inst.Endpoint(), nil, "faults", "list")
	if list.code != admin.ExitOK || !strings.Contains(list.stdout, "error TransactionCanceledException") {
		t.Errorf("faults list = %d %q, want the action shown as error TransactionCanceledException", list.code, list.stdout)
	}
}

// --members goes only with --error, and only as a JSON object; both
// mistakes are usage errors saying what to send.
func TestFaultsAddMembersUsage(t *testing.T) {
	inst := start(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--throttle", "--members", conflictMembers}, "--members goes with --error"},
		{
			[]string{"--error", "TransactionCanceledException", "--members", `[1]`},
			`--members "[1]" is not a JSON object, such as '{"CancellationReasons": [{"Code": "TransactionConflict"}]}'`,
		},
	} {
		r := runCLI(t.Context(), inst.Endpoint(), nil, append([]string{"faults", "add", "--operation", "dynamodb.TransactWriteItems"}, c.args...)...)
		if r.code != admin.ExitUsage || !strings.Contains(r.stderr, c.want) {
			t.Errorf("faults add %q = %d, stderr %q; want 2 saying %q", c.args, r.code, r.stderr, c.want)
		}
	}
	if faults, err := inst.Admin().Faults(t.Context()); err != nil || len(faults) != 0 {
		t.Errorf("after the refused adds the server holds %+v, %v; want no rules", faults, err)
	}
}

// faults add --help shows how to set cancellation reasons.
func TestFaultsAddHelpShowsMembers(t *testing.T) {
	help := runCLI(t.Context(), closedEndpoint(t), nil, "faults", "add", "--help")
	example := `understudy faults add --operation dynamodb.TransactWriteItems --error TransactionCanceledException --members '` + conflictMembers + `' --next 1`
	if !strings.Contains(help.stdout, example) || !strings.Contains(help.stdout, "\n  --members ") {
		t.Errorf("faults add --help lacks --members or the example %q:\n%s", example, help.stdout)
	}
}
