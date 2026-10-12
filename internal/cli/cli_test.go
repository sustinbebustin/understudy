package cli_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// serve runs in both output modes: it becomes ready, writes its logs in
// that format, and exits 0 when its context ends.
func TestServeInBothOutputModes(t *testing.T) {
	for _, mode := range []string{"text", "json"} {
		s := serve(t, "--output", mode)
		r := runCLI(t.Context(), s.endpoint, nil, "health")
		if code := s.stop(); code != admin.ExitOK || r.code != admin.ExitOK {
			t.Errorf("serve --output %s = %d, health %d %s; stderr:\n%s", mode, code, r.code, r.stderr, s.stderr)
		}
		first, _, _ := strings.Cut(s.stderr.String(), "\n")
		if isJSON := json.Valid([]byte(first)); isJSON != (mode == "json") {
			t.Errorf("serve --output %s logged %q", mode, first)
		}
	}
}

// serve applies the settings of its configuration file rather than
// refusing them: the clock starts frozen at the configured time, or scaled,
// and a call with a key understudy does not know lands in the configured
// default account.
func TestServeAppliesTheConfigurationFile(t *testing.T) {
	settings := `{
		"clock": "frozen@2026-01-01T00:00:00Z",
		"accounts": {"default": "111111111111"},
		"journal": {"capacity": 1000, "maxBytes": 1048576},
		"lambda": {"network": "disabled", "startConcurrency": 4, "idleTimeout": "1m", "maxEnvironments": 8},
		"dynamodb": {"memoryLimit": 536870912}
	}`
	s := serve(t, "--config", writeConfig(t, settings))
	var now admin.ClockState
	runJSON(t, s.endpoint, &now, "clock", "now")
	if want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC); now.Mode != admin.Frozen || !now.Now.Equal(want) {
		t.Errorf("clock now = %s %s, want frozen at %s", now.Mode, now.Now, want)
	}
	createQueue(t, s.endpoint, "orders")
	var list admin.ResourceList
	runJSON(t, s.endpoint, &list, "resources", "--service", "sqs")
	if len(list.Resources) != 1 || list.Resources[0].Account != "111111111111" {
		t.Errorf("resources = %+v, want the queue in the default account 111111111111", list.Resources)
	}
	if code := s.stop(); code != admin.ExitOK {
		t.Errorf("serve = %d:\n%s", code, s.stderr)
	}

	scaled := serve(t, "--config", writeConfig(t, `{"clockScale": 60}`))
	defer scaled.stop()
	runJSON(t, scaled.endpoint, &now, "clock", "now")
	if now.Mode != admin.Scaled || now.Factor != 60 {
		t.Errorf("clock now with clockScale 60 = %s x%v, want scaled x60", now.Mode, now.Factor)
	}
}

// writeConfig writes a configuration file holding settings.
func writeConfig(t *testing.T, settings string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "understudy.json")
	if err := os.WriteFile(file, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// runJSON runs args against endpoint with --output json and decodes the
// result into v.
func runJSON(t *testing.T, endpoint string, v any, args ...string) {
	t.Helper()
	r := runCLI(t.Context(), endpoint, nil, append(args, "--output", "json")...)
	if r.code != admin.ExitOK {
		t.Fatalf("%q = %d, stderr %s", args, r.code, r.stderr)
	}
	if err := json.Unmarshal([]byte(r.stdout), v); err != nil {
		t.Fatalf("%q wrote %q: %v", args, r.stdout, err)
	}
}

// serve on an address another process holds exits 2 naming the address
// and the command to run instead.
func TestServeOnABusyAddressNamesTheNextCommand(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	var out, errOut syncBuffer
	code := cli.Run(t.Context(), []string{"serve", "--listen", addr, "--docker", "disabled"}, cli.Options{Stdout: &out, Stderr: &errOut, ReadFile: os.ReadFile})
	msg := errOut.String()
	if code != admin.ExitUsage || !strings.Contains(msg, addr+" is in use") || !strings.Contains(msg, "understudy serve --listen 127.0.0.1:0") || strings.Contains(msg, "Serve again") {
		t.Errorf("serve --listen %s while it is taken = %d, stderr %q; want 2 naming the address and the next command", addr, code, msg)
	}
}

// A configuration serve refuses is the JSON error envelope when JSON is
// asked for, by --output, --log-format or UNDERSTUDY_LOG_FORMAT.
func TestServeErrorsFollowTheOutputFormat(t *testing.T) {
	cases := []struct {
		args []string
		env  []string
	}{
		{args: []string{"--output", "json", "--listen-auth", "bogus"}},
		{args: []string{"--listen-auth=bogus", "--log-format=json"}},
		{args: []string{"--listen-auth", "bogus"}, env: []string{"UNDERSTUDY_LOG_FORMAT=json"}},
	}
	for _, c := range cases {
		var out, errOut syncBuffer
		code := cli.Run(t.Context(), append([]string{"serve"}, c.args...), cli.Options{Stdout: &out, Stderr: &errOut, Env: c.env, ReadFile: os.ReadFile})
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(errOut.String()), &envelope); code != admin.ExitUsage || err != nil || envelope.Error.Code != "usage" {
			t.Errorf("serve %q (env %q) = %d, stderr %q; want 2 with the JSON usage envelope", c.args, c.env, code, errOut.String())
		}
	}
}

// env --tls trusts the TLS listener of serve --tls: the CA file it writes
// verifies the listener, and each SDK gets the variable it reads.
func TestEnvTLS(t *testing.T) {
	s := serve(t, "--tls")
	defer s.stop()
	if s.tlsEndpoint == "" {
		t.Fatalf("serve --tls wrote no tlsEndpoint:\n%s", s.stderr)
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	r := runCLI(t.Context(), s.endpoint, nil, "env", "--sdk", "go", "--format", "dotenv", "--tls-endpoint", s.tlsEndpoint, "--ca-file", ca)
	got := stripComments(r.stdout)
	if r.code != admin.ExitOK || !strings.Contains(got, "AWS_ENDPOINT_URL="+s.tlsEndpoint+"\n") || !strings.Contains(got, "AWS_CA_BUNDLE="+ca+"\n") {
		t.Fatalf("env --tls --sdk go = %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	pemData, err := os.ReadFile(ca)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemData) {
		t.Fatalf("%s holds no certificate: %q", ca, pemData)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.tlsEndpoint+admin.Prefix+"/health", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET health over TLS trusting only %s: %v", ca, err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	js := runCLI(t.Context(), s.endpoint, nil, "env", "--sdk", "js", "--format", "dotenv", "--tls-endpoint", s.tlsEndpoint, "--ca-file", ca)
	if !strings.Contains(js.stdout, "NODE_EXTRA_CA_CERTS="+ca+"\n") || strings.Contains(js.stdout, "AWS_CA_BUNDLE") {
		t.Errorf("env --tls --sdk js:\n%s", js.stdout)
	}
	plain := runCLI(t.Context(), s.endpoint, nil, "env", "--tls-endpoint", s.endpoint)
	if plain.code != admin.ExitUsage {
		t.Errorf("env --tls-endpoint http://... = %d, want 2", plain.code)
	}
}

// Each exit code comes from a scripted case: 0 success, 1 an operation the
// server refused, 2 a usage error, 3 no server, 4 a condition not met.
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
		{"operation failed", inst.Endpoint(), []string{"faults", "remove", "no-such-rule"}, admin.ExitFailed, "not_found"},
		{"usage", inst.Endpoint(), []string{"faults", "add", "--operation", "sqs.SendMessage"}, admin.ExitUsage, "exactly one action"},
		{"unknown flag", inst.Endpoint(), []string{"health", "--wiat", "1s"}, admin.ExitUsage, "flag provided but not defined"},
		{"unreachable", closedEndpoint(t), []string{"health"}, admin.ExitUnreachable, "understudy serve"},
		{"not ready", notReady.URL, []string{"health", "--wait", "50ms"}, admin.ExitConditionNotMet, "health --wait"},
	}
	for _, c := range cases {
		r := runCLI(t.Context(), c.endpoint, nil, c.args...)
		if r.code != c.code || !strings.Contains(r.stderr, c.stderr) {
			t.Errorf("%s: %q = %d, stderr %q; want %d and %q", c.name, c.args, r.code, r.stderr, c.code, c.stderr)
		}
	}
}

// journal --follow prints the matching events, then the new ones as they
// are recorded, and ends cleanly when its context does.
func TestJournalFollow(t *testing.T) {
	inst := start(t)
	createQueue(t, inst.Endpoint(), "first")
	ctx, cancel := context.WithCancel(t.Context())
	var out, errOut syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- cli.Run(ctx, []string{"journal", "--operation", "sqs.CreateQueue", "--follow", "--output", "json"}, cli.Options{
			Stdout: &out, Stderr: &errOut, Env: []string{config.EndpointEnv + "=" + inst.Endpoint()}, ReadFile: os.ReadFile,
		})
	}()
	waitLines(t, &out, 1)
	createQueue(t, inst.Endpoint(), "second")
	waitLines(t, &out, 2)
	cancel()
	if code := <-done; code != admin.ExitOK {
		t.Fatalf("journal --follow after cancel = %d, stderr %s", code, errOut.String())
	}
	var ids []uint64
	for line := range strings.Lines(out.String()) {
		var e admin.JournalEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		ids = append(ids, e.ID)
	}
	if len(ids) != 2 || ids[0] >= ids[1] {
		t.Errorf("followed events %v, want the two CreateQueue calls in order", ids)
	}
}

// journal --follow prints every matching event recorded before it, whatever
// --limit says, then the new ones.
func TestJournalFollowIgnoresTheLimit(t *testing.T) {
	inst := start(t)
	createQueue(t, inst.Endpoint(), "first")
	createQueue(t, inst.Endpoint(), "second")
	ctx, cancel := context.WithCancel(t.Context())
	var out, errOut syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- cli.Run(ctx, []string{"journal", "--operation", "sqs.CreateQueue", "--limit", "1", "--follow", "--output", "json"}, cli.Options{
			Stdout: &out, Stderr: &errOut, Env: []string{config.EndpointEnv + "=" + inst.Endpoint()}, ReadFile: os.ReadFile,
		})
	}()
	waitLines(t, &out, 2)
	createQueue(t, inst.Endpoint(), "third")
	waitLines(t, &out, 3)
	cancel()
	if code := <-done; code != admin.ExitOK {
		t.Fatalf("journal --follow after cancel = %d, stderr %s", code, errOut.String())
	}
	if n := strings.Count(out.String(), "\n"); n != 3 {
		t.Errorf("journal --limit 1 --follow printed %d events, want all 3:\n%s", n, out.String())
	}
}

// journal --follow loses nothing recorded between its query and the
// stream: with nothing matching at first, a call recorded after the query
// answered, before the CLI could act on the answer, is printed.
func TestJournalFollowLosesNothingAfterAnEmptyQuery(t *testing.T) {
	inst := start(t)
	target, err := url.Parse(inst.Endpoint())
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	var answered atomic.Bool
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != admin.Prefix+"/journal/events" {
			proxy.ServeHTTP(w, r)
			return
		}
		// Hold the query's answer until a matching call is recorded.
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, r)
		if answered.CompareAndSwap(false, true) {
			if err := sendCreateQueue(r.Context(), inst.Endpoint(), "late"); err != nil {
				t.Errorf("recording the late call: %v", err)
			}
		}
		maps.Copy(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		if _, err := w.Write(rec.Body.Bytes()); err != nil {
			t.Errorf("relaying the query's answer: %v", err)
		}
	}))
	t.Cleanup(front.Close)
	ctx, cancel := context.WithCancel(t.Context())
	var out, errOut syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- cli.Run(ctx, []string{"journal", "--operation", "sqs.CreateQueue", "--follow", "--output", "json"}, cli.Options{
			Stdout: &out, Stderr: &errOut, Env: []string{config.EndpointEnv + "=" + front.URL}, ReadFile: os.ReadFile,
		})
	}()
	waitLines(t, &out, 1)
	cancel()
	if code := <-done; code != admin.ExitOK {
		t.Fatalf("journal --follow after cancel = %d, stderr %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"CreateQueue"`) || strings.Count(out.String(), "\n") != 1 {
		t.Errorf("followed %q, want the one late CreateQueue call", out.String())
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

// serve refuses a listener beyond loopback on a host, naming the token,
// the published container port and the loopback fix.
func TestServeRefusesANonLoopbackListenerOnAHost(t *testing.T) {
	var out, errOut syncBuffer
	onHost := func(path string) ([]byte, error) {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	code := cli.Run(t.Context(), []string{"serve", "--listen", "0.0.0.0:0"}, cli.Options{Stdout: &out, Stderr: &errOut, ReadFile: onHost})
	msg := errOut.String()
	for _, want := range []string{"UNDERSTUDY_ADMIN_TOKEN", "container-published", "--listen 127.0.0.1:0"} {
		if code != admin.ExitUsage || !strings.Contains(msg, want) {
			t.Errorf("serve --listen 0.0.0.0:0 = %d, stderr %q; want 2 naming %q", code, msg, want)
		}
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
	r = runCLI(t.Context(), closedEndpoint(t), []string{"DOCKER_HOST=tcp://docker:2375"}, "doctor", "--output", "json")
	var report struct {
		Checks []struct{ Name, Status, Detail string } `json:"checks"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &report); err != nil {
		t.Fatalf("doctor = %d %q: %v", r.code, r.stdout, err)
	}
	warned := false
	for _, c := range report.Checks {
		warned = warned || (c.Name == "configuration" && c.Status == "warn" && strings.Contains(c.Detail, "tcp://docker:2375"))
	}
	if !warned {
		t.Errorf("doctor with DOCKER_HOST=tcp://docker:2375 reports %+v, want a configuration warning", report.Checks)
	}
}

// env prints the exact variables an SDK reads, in each format.
func TestEnvFormats(t *testing.T) {
	endpoint := closedEndpoint(t)
	// The loopback IP literal becomes localhost, the endpoint ADR 0013
	// documents, so clients can address <bucket>.localhost and
	// <account>.localhost.
	sdk := "http://localhost:" + endpoint[strings.LastIndexByte(endpoint, ':')+1:]
	cases := map[string]string{
		"sh":     "export AWS_ENDPOINT_URL='" + sdk + "'\nexport AWS_ACCESS_KEY_ID='test'\nexport AWS_SECRET_ACCESS_KEY='test'\nexport AWS_REGION='eu-west-1'\n",
		"fish":   "set -gx AWS_ENDPOINT_URL '" + sdk + "'\nset -gx AWS_ACCESS_KEY_ID 'test'\nset -gx AWS_SECRET_ACCESS_KEY 'test'\nset -gx AWS_REGION 'eu-west-1'\n",
		"dotenv": "AWS_ENDPOINT_URL=" + sdk + "\nAWS_ACCESS_KEY_ID=test\nAWS_SECRET_ACCESS_KEY=test\nAWS_REGION=eu-west-1\n",
		"github": "AWS_ENDPOINT_URL=" + sdk + "\nAWS_ACCESS_KEY_ID=test\nAWS_SECRET_ACCESS_KEY=test\nAWS_REGION=eu-west-1\n",
	}
	for format, want := range cases {
		r := runCLI(t.Context(), endpoint, nil, "env", "--sdk", "go", "--format", format, "--region", "eu-west-1")
		got := r.stdout
		if format != "github" {
			got = stripComments(got)
		}
		if r.code != admin.ExitOK || got != want {
			t.Errorf("env --format %s = %d\n%s\nwant\n%s", format, r.code, got, want)
		}
		if !strings.Contains(r.stdout+r.stderr, "UsePathStyle") {
			t.Errorf("env --sdk go --format %s does not name UsePathStyle", format)
		}
	}
	r := runCLI(t.Context(), endpoint, nil, "env", "--sdk", "python", "--format", "dotenv")
	if !strings.Contains(r.stdout, "AWS_DEFAULT_REGION=us-east-1\n") {
		t.Errorf("env --sdk python lacks AWS_DEFAULT_REGION:\n%s", r.stdout)
	}
	// Terraform 6.x reads bucket tags through S3 Control at
	// <account>.<endpoint host>, so it needs skip_requesting_account_id
	// where *.localhost does not resolve.
	tf := runCLI(t.Context(), endpoint, nil, "env", "--sdk", "terraform")
	if !strings.Contains(tf.stdout, "skip_requesting_account_id = true") || !strings.Contains(tf.stdout, "s3_use_path_style = true") {
		t.Errorf("env --sdk terraform names neither skip_requesting_account_id nor s3_use_path_style:\n%s", tf.stdout)
	}
	// A hostname endpoint is kept as given.
	named := runCLI(t.Context(), "http://understudy:4566", nil, "env", "--format", "dotenv")
	if !strings.Contains(named.stdout, "AWS_ENDPOINT_URL=http://understudy:4566\n") {
		t.Errorf("env with a hostname endpoint rewrote it:\n%s", named.stdout)
	}
}

func stripComments(s string) string {
	var b strings.Builder
	for line := range strings.Lines(s) {
		if !strings.HasPrefix(line, "#") {
			b.WriteString(line)
		}
	}
	return b.String()
}
