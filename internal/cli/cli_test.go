package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/cli"
	"github.com/sustinbebustin/understudy/internal/config"
	"github.com/sustinbebustin/understudy/understudytest"
)

// result is one CLI run.
type result struct {
	code           int
	stdout, stderr string
}

// syncBuffer is a buffer a command running in another goroutine writes;
// a waiter learns of each write through changes.
type syncBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	wrote chan struct{}
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if b.wrote != nil {
		select {
		case b.wrote <- struct{}{}:
		default:
		}
	}
	return n, err
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// changes receives after every write from now on; a write while nobody
// receives is kept for the next receive.
func (b *syncBuffer) changes() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.wrote == nil {
		b.wrote = make(chan struct{}, 1)
	}
	return b.wrote
}

// waitTimeout bounds every wait for a command running in the background.
const waitTimeout = 10 * time.Second

// waitLines waits, woken by each write, until out holds n lines.
func waitLines(t *testing.T, out *syncBuffer, n int) {
	t.Helper()
	changed := out.changes()
	timeout := time.NewTimer(waitTimeout)
	defer timeout.Stop()
	for strings.Count(out.String(), "\n") < n {
		select {
		case <-changed:
		case <-timeout.C:
			t.Fatalf("waited %s for %d line(s), got %q", waitTimeout, n, out.String())
		}
	}
}

// served is a serve command running in the background.
type served struct {
	endpoint, tlsEndpoint string
	stderr                *syncBuffer
	cancel                context.CancelFunc
	done                  chan int
}

// stop ends serve and returns its exit code.
func (s *served) stop() int {
	s.cancel()
	return <-s.done
}

// serve runs 'understudy serve' with args on an ephemeral port and returns
// once its ready file names the endpoints. The banner follows the ready
// file on stderr, so each stderr write wakes the check.
func serve(t *testing.T, args ...string) *served {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready.json")
	ctx, cancel := context.WithCancel(t.Context())
	s := &served{stderr: &syncBuffer{}, cancel: cancel, done: make(chan int, 1)}
	changed := s.stderr.changes()
	go func() {
		s.done <- cli.Run(ctx, append([]string{"serve", "--listen", "127.0.0.1:0", "--ready-file", ready, "--docker", "disabled"}, args...), cli.Options{
			Stdout: io.Discard, Stderr: s.stderr, ReadFile: os.ReadFile, Version: "test",
		})
	}()
	timeout := time.NewTimer(waitTimeout)
	defer timeout.Stop()
	for {
		if data, err := os.ReadFile(ready); err == nil {
			var doc struct {
				Endpoint    string `json:"endpoint"`
				TLSEndpoint string `json:"tlsEndpoint"`
			}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatalf("ready file %q: %v", data, err)
			}
			s.endpoint, s.tlsEndpoint = doc.Endpoint, doc.TLSEndpoint
			return s
		}
		select {
		case <-changed:
		case code := <-s.done:
			t.Fatalf("serve %q exited %d before it was ready:\n%s", args, code, s.stderr)
		case <-timeout.C:
			cancel()
			t.Fatalf("serve %q wrote no ready file in %s:\n%s", args, waitTimeout, s.stderr)
		}
	}
}

// runCLI runs args against endpoint with the extra environment env.
func runCLI(ctx context.Context, endpoint string, env []string, args ...string) result {
	var out, errOut syncBuffer
	code := cli.Run(ctx, args, cli.Options{
		Stdout:   &out,
		Stderr:   &errOut,
		Env:      append([]string{config.EndpointEnv + "=" + endpoint}, env...),
		ReadFile: os.ReadFile,
		Version:  "test",
	})
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

// start runs an instance without Docker on a frozen clock.
func start(t *testing.T) *understudytest.Instance {
	t.Helper()
	return understudytest.Start(t, understudytest.Options{
		Seed:   new(uint64(1)),
		Clock:  understudytest.Frozen{At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		Docker: understudytest.DockerDisabled{},
	})
}

// createQueue sends a signed awsJson1_0 CreateQueue, as aws-sdk-go-v2 would.
func createQueue(t *testing.T, endpoint, name string) {
	t.Helper()
	if err := sendCreateQueue(t.Context(), endpoint, name); err != nil {
		t.Fatal(err)
	}
}

// sendCreateQueue is createQueue for a goroutine that may not call
// t.Fatal.
func sendCreateQueue(ctx context.Context, endpoint, name string) error {
	body := `{"QueueName":"` + name + `"}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	region, err := aws.KnownRegions().Parse("us-east-1")
	if err != nil {
		return err
	}
	auth.Sign(req, []byte(body), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: region, Service: "sqs"}, time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	if err := resp.Body.Close(); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("CreateQueue %s = %d", name, resp.StatusCode)
	}
	return nil
}

// closedEndpoint is an endpoint where nothing listens.
func closedEndpoint(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return "http://" + addr
}

// firstEventID is the id of the first API call the journal holds.
func firstEventID(t *testing.T, c *admin.Client) uint64 {
	t.Helper()
	page, err := c.JournalEvents(t.Context(), admin.JournalQuery{Kind: "api-call", Limit: 1})
	if err != nil || len(page.Events) == 0 {
		t.Fatalf("journal query = %+v, %v", page, err)
	}
	return page.Events[0].ID
}

// validJSON reports whether out is JSON, or NDJSON when lines is set.
func validJSON(out string, lines bool) bool {
	if !lines {
		return json.Valid([]byte(out))
	}
	for line := range strings.Lines(out) {
		if !json.Valid([]byte(line)) {
			return false
		}
	}
	return true
}

// Every command runs in both output modes against a live instance: text
// writes a result, JSON writes JSON, and an error in JSON mode is the
// error envelope on stderr.
func TestEveryCommandInBothOutputModes(t *testing.T) {
	inst := start(t)
	createQueue(t, inst.Endpoint(), "orders")
	event := strconv.FormatUint(firstEventID(t, inst.Admin()), 10)
	dir := t.TempDir()
	rules := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(rules, []byte(`[{"phase": "request", "match": {"operations": ["sqs.SendMessage"]}, "action": {"kind": "throttle"}, "trigger": {"kind": "always"}}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args  func(mode string) []string
		code  int
		lines bool
	}{
		{args: fixed("version")},
		{args: fixed("health")},
		{args: fixed("settle")},
		{args: fixed("clock", "now")},
		{args: fixed("clock", "freeze")},
		{args: fixed("clock", "advance", "1m", "--settle")},
		{args: func(mode string) []string {
			day := map[string]string{"text": "01", "json": "02"}[mode]
			return []string{"clock", "set", "2030-01-" + day + "T00:00:00Z"}
		}},
		{args: fixed("clock", "scale", "2")},
		{args: fixed("clock", "resume")},
		{args: fixed("clock", "freeze")},
		{args: func(mode string) []string {
			return []string{"faults", "add", "--id", "f-" + mode, "--operation", "sqs.SendMessage", "--throttle", "--next", "3"}
		}},
		{args: fixed("faults", "list")},
		{args: func(mode string) []string { return []string{"faults", "remove", "f-" + mode} }},
		{args: fixed("faults", "load", rules)},
		{args: fixed("faults", "clear")},
		{args: fixed("journal", "--service", "sqs"), lines: true},
		{args: fixed("journal", "trace", event)},
		{args: fixed("journal", "trace", event, "--canonical")},
		{args: func(mode string) []string { return []string{"snapshot", "save", filepath.Join(dir, mode+".tar")} }},
		{args: func(mode string) []string { return []string{"snapshot", "inspect", filepath.Join(dir, mode+".tar")} }},
		{args: func(mode string) []string {
			return []string{"snapshot", "diff", filepath.Join(dir, mode+".tar"), filepath.Join(dir, mode+".tar"), "--normalize"}
		}},
		{args: func(mode string) []string { return []string{"snapshot", "load", filepath.Join(dir, mode+".tar")} }},
		{args: fixed("resources")},
		{args: fixed("coverage")},
		{args: fixed("iam", "usage")},
		{args: fixed("iam", "policy", "--principal", "arn:aws:iam::000000000000:root")},
		{args: fixed("lambda", "hot-reload", "missing-function", dir), code: admin.ExitFailed},
		{args: fixed("images", "pull"), code: admin.ExitFailed},
		{args: fixed("env")},
		{args: fixed("doctor")},
		{args: fixed("cleanup", "--all"), code: admin.ExitFailed},
		{args: fixed("reset")},
	}
	env := []string{"UNDERSTUDY_DOCKER=disabled"}
	for _, c := range cases {
		for _, mode := range []string{"text", "json"} {
			args := append(c.args(mode), "--output", mode)
			r := runCLI(t.Context(), inst.Endpoint(), env, args...)
			switch {
			case r.code != c.code:
				t.Errorf("%q = %d, want %d\nstdout: %s\nstderr: %s", args, r.code, c.code, r.stdout, r.stderr)
			case r.code == admin.ExitOK && r.stdout == "":
				t.Errorf("%q wrote nothing", args)
			case r.code == admin.ExitOK && mode == "json" && !validJSON(r.stdout, c.lines):
				t.Errorf("%q --output json wrote %q", args, r.stdout)
			case r.code != admin.ExitOK && mode == "json" && (!json.Valid([]byte(r.stderr)) || !strings.Contains(r.stderr, `"error"`)):
				t.Errorf("%q --output json failed with %q on stderr, want the JSON error envelope", args, r.stderr)
			}
		}
	}
}

func fixed(args ...string) func(string) []string {
	return func(string) []string { return args }
}

// Each exit code comes from a scripted case: 0 success, 1 an operation the
// server refused, 2 a usage error, 3 no server, 4 a condition not met; 5,
// a refused token, is TestUnauthorizedNamesTheTokenVariable's.
func TestExitCodes(t *testing.T) {
	inst := start(t)
	notReady := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		if _, err := w.Write([]byte(`{"ready": false, "waiting": ["listener"]}`)); err != nil {
			t.Errorf("writing the scripted health answer: %v", err)
		}
	}))
	t.Cleanup(notReady.Close)
	cases := []struct {
		name     string
		endpoint string
		args     []string
		code     int
		stderr   string
	}{
		{"success", inst.Endpoint(), []string{"version"}, admin.ExitOK, ""},
		{"operation failed", inst.Endpoint(), []string{"faults", "remove", "no-such-rule"}, admin.ExitFailed, `faults remove: no active fault rule has id "no-such-rule"`},
		{"usage", inst.Endpoint(), []string{"faults", "add", "--operation", "sqs.SendMessage"}, admin.ExitUsage, "exactly one action"},
		{"unknown flag", inst.Endpoint(), []string{"health", "--wiat", "1s"}, admin.ExitUsage, "--wiat is not a flag of understudy health; the closest valid name is --wait"},
		{"unreachable", closedEndpoint(t), []string{"health"}, admin.ExitUnreachable, "understudy serve"},
		{"not ready", notReady.URL, []string{"health", "--wait", "50ms"}, admin.ExitConditionNotMet, "health --wait"},
	}
	for _, c := range cases {
		r := runCLI(t.Context(), c.endpoint, nil, c.args...)
		if r.code != c.code || !strings.Contains(r.stderr, c.stderr) {
			t.Errorf("%s: %q = %d, stderr %q; want %d and %q", c.name, c.args, r.code, r.stderr, c.code, c.stderr)
		}
	}
	// A refusal whose hint names the next step gets no --help line too.
	r := runCLI(t.Context(), inst.Endpoint(), nil, "clock", "set", "2020-01-01T00:00:00Z")
	if r.code != admin.ExitUsage || !strings.Contains(r.stderr, "understudy reset --clock") || strings.Contains(r.stderr, "--help") {
		t.Errorf("clock set into the past = %d, stderr %q; want 2 with the server's hint and no --help line", r.code, r.stderr)
	}
}

// A flag value that does not parse names the flag as the CLI spells it and
// the form it takes, and help shows each flag with -- and a placeholder.
func TestFlagValuesNameTheirForm(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"journal", "--since", "garbage"}, `--since "garbage" is not a duration such as 90s`},
		{[]string{"journal", "--limit", "many"}, `--limit "many" is not a whole number`},
		{[]string{"journal", "--trace", "x"}, `--trace "x" is not an event id`},
		{[]string{"journal", "--trace", "0"}, `--trace "0" is not an event id`},
		{[]string{"health", "--wait", "abc"}, `--wait "abc" is not a duration`},
		{[]string{"settle", "--timeout", "abc"}, `--timeout "abc" is not a duration`},
		{[]string{"clock", "advance", "1m", "--settle-timeout", "abc"}, `--settle-timeout "abc" is not a duration`},
		{[]string{"faults", "add", "--throttle", "--latency", "5x"}, `--latency "5x" is not a duration`},
		{[]string{"faults", "add", "--throttle", "--next", "x"}, `--next "x" is not a whole number`},
		{[]string{"faults", "add", "--throttle", "--probability", "half"}, `--probability "half" is not a number such as 0.25`},
	}
	for _, c := range cases {
		r := runCLI(t.Context(), closedEndpoint(t), nil, c.args...)
		if r.code != admin.ExitUsage || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%q = %d, stderr %q; want 2 and %q", c.args, r.code, r.stderr, c.want)
		}
	}
	help := runCLI(t.Context(), closedEndpoint(t), nil, "journal", "--help")
	for _, want := range []string{"\n  --since DURATION\n", "\n  --limit N\n", "\n  --trace ID\n", "\n  --endpoint URL\n", "\n  --errors\n"} {
		if !strings.Contains(help.stdout, want) {
			t.Errorf("journal --help lacks %q:\n%s", want, help.stdout)
		}
	}
}

// A flag before the command is named as misplaced, not as an unknown
// command, and --version is the version command.
func TestFlagsBeforeTheCommand(t *testing.T) {
	r := runCLI(t.Context(), closedEndpoint(t), nil, "--endpoint", "http://127.0.0.1:4566", "health")
	if r.code != admin.ExitUsage || !strings.Contains(r.stderr, "flags go after it") || strings.Contains(r.stderr, "is not a command") {
		t.Errorf("--endpoint before health = %d, stderr %q; want 2 saying flags go after the command", r.code, r.stderr)
	}
	r = runCLI(t.Context(), closedEndpoint(t), nil, "faults", "--output", "json", "list")
	if r.code != admin.ExitUsage || !strings.Contains(r.stderr, "'understudy faults <command> --output ...'") {
		t.Errorf("--output before faults list = %d, stderr %q", r.code, r.stderr)
	}
	r = runCLI(t.Context(), closedEndpoint(t), nil, "--version")
	if r.code != admin.ExitOK || r.stdout != "understudy test\n" {
		t.Errorf("--version = %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
}

// Command-line mistakes are named as the user typed them, with the fix: an
// unknown flag with the closest valid name, a negative duration for what
// it asks, a missing argument as none; -v is --version, and serve's flag
// errors read like the others' with one pointer at --help.
func TestCommandLineMistakesNameTheFix(t *testing.T) {
	cases := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"-v"}, admin.ExitOK, "understudy test\n"},
		{
			[]string{"faults", "list", "--endpont", "http://127.0.0.1:1"},
			admin.ExitUsage,
			"understudy faults list: --endpont is not a flag of understudy faults list; the closest valid name is --endpoint\n",
		},
		{[]string{"clock", "advance", "-5s"}, admin.ExitUsage, `"-5s" would move emulator time back, and it only moves forward; 'understudy reset --clock'`},
		{[]string{"clock", "advance"}, admin.ExitUsage, "takes one argument, DURATION; got none\n"},
		{[]string{"clock", "now", "a", "b"}, admin.ExitUsage, `takes no arguments; got "a", "b"` + "\n"},
		{[]string{"journal", "--follow=maybe"}, admin.ExitUsage, `--follow takes no value, or true or false; got "maybe"`},
		{[]string{"journal", "--since"}, admin.ExitUsage, "--since needs a value: only events from this long ago"},
		{
			[]string{"serve", "--listne", "127.0.0.1:0"},
			admin.ExitUsage,
			"understudy serve: --listne is not a serve flag; the closest valid name is --listen\nRun 'understudy serve --help' for usage and examples.\n",
		},
	}
	for _, c := range cases {
		r := runCLI(t.Context(), closedEndpoint(t), nil, c.args...)
		out := r.stderr
		if c.code == admin.ExitOK {
			out = r.stdout
		}
		if r.code != c.code || !strings.Contains(out, c.want) || strings.Count(r.stderr, "--help") > 1 {
			t.Errorf("%q = %d, stdout %q, stderr %q; want %d and %q", c.args, r.code, r.stdout, r.stderr, c.code, c.want)
		}
	}
}

// The root help lists the exit codes a script can tell apart.
func TestRootHelpListsTheExitCodes(t *testing.T) {
	r := runCLI(t.Context(), closedEndpoint(t), nil, "--help")
	for _, want := range []string{"Exit codes:", "  2  usage error", "  3  understudy is not reachable", "  4  a condition was not met", "  5  the server refused the admin token"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("understudy --help lacks %q:\n%s", want, r.stdout)
		}
	}
}

// An admin call the server refuses for its token names the variable that
// carries the token and whether it was missing or rejected, not the
// command's usage or the Go client's option.
func TestUnauthorizedNamesTheTokenVariable(t *testing.T) {
	guarded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		body := `{"error": {"code": "unauthorized", "message": "this listener requires the admin token as Authorization: Bearer <token>",` +
			` "hint": "send Authorization: Bearer <token> with the token serve was configured with (admin.Options.Token for Go clients)"}}`
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("writing the scripted refusal: %v", err)
		}
	}))
	t.Cleanup(guarded.Close)
	cases := []struct {
		env  []string
		want string
	}{
		{nil, "UNDERSTUDY_ADMIN_TOKEN is not set"},
		{[]string{"UNDERSTUDY_ADMIN_TOKEN=wrong"}, "the server rejected the token in UNDERSTUDY_ADMIN_TOKEN"},
	}
	for _, c := range cases {
		r := runCLI(t.Context(), guarded.URL, c.env, "faults", "list")
		if r.code != admin.ExitUnauthorized || !strings.Contains(r.stderr, c.want) || strings.Contains(r.stderr, "--help") || strings.Contains(r.stderr, "admin.Options") {
			t.Errorf("faults list with %q against a guarded server = %d, stderr %q; want %q", c.env, r.code, r.stderr, c.want)
		}
		js := runCLI(t.Context(), guarded.URL, c.env, "faults", "list", "--output", "json")
		var envelope struct {
			Error struct{ Code, Hint string } `json:"error"`
		}
		if err := json.Unmarshal([]byte(js.stderr), &envelope); err != nil || envelope.Error.Code != "unauthorized" || !strings.Contains(envelope.Error.Hint, c.want) {
			t.Errorf("faults list --output json with %q against a guarded server: stderr %q", c.env, js.stderr)
		}
	}
}

// Without --endpoint or UNDERSTUDY_ENDPOINT, a command finds the server
// serve's own UNDERSTUDY_LISTEN names, on loopback when it listens on every
// interface; UNDERSTUDY_ENDPOINT still wins.
func TestEndpointFollowsTheListenAddress(t *testing.T) {
	inst := start(t)
	u, err := url.Parse(inst.Endpoint())
	if err != nil {
		t.Fatal(err)
	}
	health := func(env ...string) int {
		return cli.Run(t.Context(), []string{"health"}, cli.Options{Stdout: io.Discard, Stderr: io.Discard, Env: env, ReadFile: os.ReadFile})
	}
	if code := health(config.ListenEnv + "=0.0.0.0:" + u.Port()); code != 0 {
		t.Errorf("health with only %s=0.0.0.0:%s = %d, want 0", config.ListenEnv, u.Port(), code)
	}
	if code := health(config.ListenEnv+"=0.0.0.0:"+u.Port(), config.EndpointEnv+"="+closedEndpoint(t)); code != admin.ExitUnreachable {
		t.Errorf("health with %s at a closed port = %d, want %d: the endpoint variable wins over the listen address", config.EndpointEnv, code, admin.ExitUnreachable)
	}
}

// Every command's help has examples, on stdout with exit 0; a group
// without a subcommand prints its help on stderr with exit 2.
func TestHelp(t *testing.T) {
	paths := [][]string{
		{},
		{"serve"},
		{"health"},
		{"settle"},
		{"reset"},
		{"clock"},
		{"clock", "now"},
		{"clock", "freeze"},
		{"clock", "resume"},
		{"clock", "advance"},
		{"clock", "set"},
		{"clock", "scale"},
		{"faults"},
		{"faults", "list"},
		{"faults", "add"},
		{"faults", "remove"},
		{"faults", "clear"},
		{"faults", "load"},
		{"journal"},
		{"journal", "trace"},
		{"snapshot"},
		{"snapshot", "save"},
		{"snapshot", "load"},
		{"snapshot", "inspect"},
		{"snapshot", "diff"},
		{"resources"},
		{"coverage"},
		{"iam"},
		{"iam", "usage"},
		{"iam", "policy"},
		{"lambda"},
		{"lambda", "hot-reload"},
		{"images"},
		{"images", "pull"},
		{"env"},
		{"doctor"},
		{"cleanup"},
		{"version"},
	}
	for _, p := range paths {
		r := runCLI(t.Context(), closedEndpoint(t), nil, append(p, "--help")...)
		if r.code != admin.ExitOK || !strings.Contains(r.stdout, "Examples:") {
			t.Errorf("%q --help = %d, stdout %q", p, r.code, r.stdout)
		}
	}
	r := runCLI(t.Context(), closedEndpoint(t), nil, "faults")
	if r.code != admin.ExitUsage || !strings.Contains(r.stderr, "Commands:") {
		t.Errorf("faults alone = %d, stderr %q", r.code, r.stderr)
	}
	help := runCLI(t.Context(), closedEndpoint(t), nil, "help", "journal", "trace")
	if help.code != admin.ExitOK || !strings.Contains(help.stdout, "understudy journal trace (ID") {
		t.Errorf("help journal trace = %d, stdout %q", help.code, help.stdout)
	}
	bogus := runCLI(t.Context(), closedEndpoint(t), nil, "bogus")
	if bogus.code != admin.ExitUsage || !strings.HasPrefix(bogus.stderr, "understudy: ") || !strings.Contains(bogus.stderr, "Run 'understudy --help'") {
		t.Errorf("bogus = %d, stderr %q", bogus.code, bogus.stderr)
	}
}

// A failure of the CLI's own file I/O names the file and the next step, in
// text and as the JSON hint.
func TestLocalErrorsNameTheNextStep(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	text := runCLI(t.Context(), closedEndpoint(t), nil, "faults", "load", missing)
	if text.code != admin.ExitFailed || !strings.Contains(text.stderr, missing) || !strings.Contains(text.stderr, "Check the path, then run 'understudy faults load FILE' again.") {
		t.Errorf("faults load of a missing file = %d, stderr %q", text.code, text.stderr)
	}
	js := runCLI(t.Context(), closedEndpoint(t), nil, "snapshot", "inspect", missing, "--output", "json")
	var envelope struct {
		Error struct{ Code, Hint string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(js.stderr), &envelope); err != nil || js.code != admin.ExitFailed || !strings.Contains(envelope.Error.Hint, "snapshot inspect") {
		t.Errorf("snapshot inspect of a missing file = %d, stderr %q", js.code, js.stderr)
	}
	garbage := filepath.Join(t.TempDir(), "garbage.tar")
	if err := os.WriteFile(garbage, []byte("not an archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	js = runCLI(t.Context(), closedEndpoint(t), nil, "snapshot", "diff", garbage, garbage, "--output", "json")
	if err := json.Unmarshal([]byte(js.stderr), &envelope); err != nil || js.code != admin.ExitFailed ||
		!strings.Contains(envelope.Error.Hint, "understudy snapshot save") || !strings.Contains(envelope.Error.Hint, "snapshot diff") {
		t.Errorf("snapshot diff of a file that is no archive = %d, stderr %q; want the hint to name snapshot save and diff", js.code, js.stderr)
	}
}

// doctor and cleanup run with UNDERSTUDY_ADMIN_TOKEN set, the CLI's own
// credential, which serve would refuse on a loopback listener; doctor
// reports a DOCKER_HOST understudy cannot use as a configuration warning.
func TestClientCommandsAcceptTheAdminToken(t *testing.T) {
	env := []string{"UNDERSTUDY_ADMIN_TOKEN=s3cret", "UNDERSTUDY_DOCKER=disabled"}
	if r := runCLI(t.Context(), closedEndpoint(t), env, "cleanup", "--persist", t.TempDir()); r.code != admin.ExitOK {
		t.Errorf("cleanup with a token = %d, stderr %q", r.code, r.stderr)
	}
	r := runCLI(t.Context(), closedEndpoint(t), env, "doctor", "--output", "json")
	if r.code != admin.ExitOK || !strings.Contains(r.stdout, `"name": "configuration"`) || !strings.Contains(r.stdout, `"status": "ok"`) {
		t.Errorf("doctor with a token = %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	r = runCLI(t.Context(), closedEndpoint(t), []string{"DOCKER_HOST=ssh://user@docker"}, "doctor", "--output", "json")
	var report struct {
		Checks []struct{ Name, Status, Detail string } `json:"checks"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &report); err != nil {
		t.Fatalf("doctor = %d %q: %v", r.code, r.stdout, err)
	}
	warned := false
	for _, c := range report.Checks {
		warned = warned || (c.Name == "configuration" && c.Status == "warn" && strings.Contains(c.Detail, "ssh://user@docker"))
	}
	if !warned {
		t.Errorf("doctor with DOCKER_HOST=ssh://user@docker reports %+v, want a configuration warning", report.Checks)
	}
}
