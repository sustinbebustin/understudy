package cli_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
)

// faults add --http-status and --http-timeout put the HTTP delivery
// actions on the server, and faults list shows them.
func TestFaultsAddHTTPActions(t *testing.T) {
	inst := start(t)
	cases := []struct {
		args   []string
		want   admin.FaultRule
		listed string
	}{
		{
			args: []string{"--id", "unavailable", "--phase", "delivery", "--mechanism", "sns-to-http", "--http-status", "503", "--next", "2"},
			want: admin.FaultRule{
				ID: "unavailable", Phase: admin.DeliveryPhase, Match: admin.FaultMatch{Mechanisms: []string{"sns-to-http"}},
				Action: admin.HTTPStatus{Status: 503}, Trigger: admin.NextN{N: 2},
			},
			listed: "http-status 503",
		},
		{
			args: []string{"--id", "hangs", "--phase", "delivery", "--mechanism", "sns-to-http", "--http-timeout"},
			want: admin.FaultRule{
				ID: "hangs", Phase: admin.DeliveryPhase, Match: admin.FaultMatch{Mechanisms: []string{"sns-to-http"}},
				Action: admin.HTTPTimeout{}, Trigger: admin.Always{},
			},
			listed: "http-timeout",
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
	list := runCLI(t.Context(), inst.Endpoint(), nil, "faults", "list")
	if list.code != admin.ExitOK {
		t.Fatalf("faults list = %d: %s", list.code, list.stderr)
	}
	for _, c := range cases {
		if !rowShows(list.stdout, c.want.ID, c.listed) {
			t.Errorf("faults list\n%s\nhas no row for %s showing %q", list.stdout, c.want.ID, c.listed)
		}
	}
}

func rowShows(table, id, action string) bool {
	for line := range strings.Lines(table) {
		if strings.HasPrefix(line, id) && strings.Contains(line, action) {
			return true
		}
	}
	return false
}

// The HTTP actions join the exactly-one-action set, and the count error
// lists them.
func TestFaultsAddCountsTheHTTPActions(t *testing.T) {
	inst := start(t)
	const want = "takes exactly one action of --error, --throttle, --latency, --after-commit, --status-ok, --drop, --http-status and --http-timeout; got "
	for _, c := range []struct {
		args []string
		got  string
	}{
		{[]string{"--phase", "delivery", "--mechanism", "sns-to-http", "--http-status", "503", "--http-timeout"}, "2"},
		{[]string{"--phase", "delivery", "--mechanism", "sns-to-http", "--drop", "--http-timeout"}, "2"},
		{[]string{"--phase", "delivery", "--mechanism", "sns-to-http"}, "0"},
	} {
		r := runCLI(t.Context(), inst.Endpoint(), nil, append([]string{"faults", "add"}, c.args...)...)
		if r.code != admin.ExitUsage || !strings.Contains(r.stderr, want+c.got) {
			t.Errorf("faults add %q = %d, stderr %q; want 2 with %q", c.args, r.code, r.stderr, want+c.got)
		}
	}
	faults, err := inst.Admin().Faults(t.Context())
	if err != nil || len(faults) != 0 {
		t.Errorf("after the refused adds the server holds %+v, %v; want no rules", faults, err)
	}
}

// A status out of range is the server's refusal, naming the field.
func TestFaultsAddRefusesAnHTTPStatusOutOfRange(t *testing.T) {
	inst := start(t)
	r := runCLI(t.Context(), inst.Endpoint(), nil, "faults", "add", "--phase", "delivery", "--mechanism", "sns-to-http", "--http-status", "600")
	if r.code != admin.ExitUsage || !strings.Contains(r.stderr, "is 600; an HTTP status is 100 to 599") {
		t.Errorf("faults add --http-status 600 = %d, stderr %q; want 2 naming the range", r.code, r.stderr)
	}
}
