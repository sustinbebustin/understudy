package cli_test

import (
	"encoding/json"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/cli"
)

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

// serve on an address another process holds exits 1, not the usage code,
// since the command line is valid, naming the address and the command to
// run instead.
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
	if code != admin.ExitFailed || !strings.Contains(msg, "listening on "+addr+": the address is in use") || !strings.Contains(msg, "understudy serve --listen 127.0.0.1:0") || strings.Contains(msg, "--help") {
		t.Errorf("serve --listen %s while it is taken = %d, stderr %q; want 1 naming the address and the next command, without a --help pointer", addr, code, msg)
	}
}

// A file setting the server refuses once it applies it, here a hot-reload
// directory that does not exist, is a usage error (exit 2) naming the file
// and the setting, as config names the file's other settings.
func TestServeRefusesAnUnappliableFileSettingByName(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	file := writeConfig(t, `{"lambda": {"hotReload": [{"function": "worker", "dir": "`+missing+`"}]}}`)
	var out, errOut syncBuffer
	code := cli.Run(t.Context(), []string{"serve", "--config", file, "--listen", "127.0.0.1:0", "--docker", "disabled"}, cli.Options{Stdout: &out, Stderr: &errOut, ReadFile: os.ReadFile})
	msg := errOut.String()
	if want := "config: " + file + ": lambda.hotReload[0].dir: "; code != admin.ExitUsage || !strings.Contains(msg, want) || !strings.Contains(msg, missing) {
		t.Errorf("serve with a missing hot-reload directory = %d, stderr %q; want 2 naming %q and the directory", code, msg, want)
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
