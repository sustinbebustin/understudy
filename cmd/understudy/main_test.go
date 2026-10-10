package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != exitOK || out.String() != "understudy dev\n" {
		t.Errorf("version = %d %q %q", code, out.String(), errOut.String())
	}
}

var errClosed = errors.New("stdout closed")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errClosed }

func TestVersionReportsAFailedWrite(t *testing.T) {
	var errOut bytes.Buffer
	if code := run([]string{"version"}, failingWriter{}, &errOut); code != exitFailed || !strings.Contains(errOut.String(), "stdout closed") {
		t.Errorf("version to a closed stdout = %d %q", code, errOut.String())
	}
}

func TestUsageErrorsExit2AndSayWhatToFix(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"bogus"},
		{"serve", "--listen", "localhost"},
		{"serve", "--clock", "slow"},
		{"serve", "--clock", "frozen@yesterday"},
		{"serve", "--seed", "-1"},
		{"serve", "--log-format", "xml"},
		{"serve", "--log-level", "loud"},
		{"serve", "extra"},
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != exitUsage || errOut.Len() == 0 {
			t.Errorf("run(%q) = %d, stderr %q; want 2 and a message", args, code, errOut.String())
		}
	}
	var out bytes.Buffer
	if code := run([]string{"serve", "--help"}, &out, &out); code != exitOK || !strings.Contains(out.String(), "Examples:") {
		t.Errorf("serve --help = %d %q", code, out.String())
	}
}

// The binary sets zipinsecurepath=0, so archive/zip itself refuses a
// zip-slip name in a Lambda code upload.
func TestZipRefusesInsecurePaths(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if _, err := zw.Create("../escape.txt"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len())); !errors.Is(err, zip.ErrInsecurePath) {
		t.Errorf("zip.NewReader on ../escape.txt = %v, want zip.ErrInsecurePath", err)
	}
}

// A non-loopback listener would expose the unauthenticated admin API, so
// serve refuses it and says what to do instead (ARCHITECTURE.md 20).
func TestServeRefusesANonLoopbackListener(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"serve", "--listen", "0.0.0.0:0"}, &out, &errOut)
	msg := errOut.String()
	if code != exitUsage || !strings.Contains(msg, "admin token") || !strings.Contains(msg, "published container port") || !strings.Contains(msg, "--listen 127.0.0.1:0") {
		t.Errorf("serve --listen 0.0.0.0:0 = %d, stderr %q; want 2 naming both options and the loopback fix", code, msg)
	}
}

// TestServeBinary builds the binary, starts it on an ephemeral port with a
// ready file, finds health ready within 250 ms of process start, and stops
// it with SIGINT.
func TestServeBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "understudy")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	ready := filepath.Join(dir, "ready.json")
	var logs bytes.Buffer
	cmd := exec.CommandContext(t.Context(), bin, "serve", "--listen", "127.0.0.1:0", "--ready-file", ready)
	cmd.Stderr = &logs
	started := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	endpoint := waitReady(t, cmd, ready, &logs)
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Errorf("health was ready %s after the process started, over the 250 ms budget", elapsed)
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("serve after SIGINT: %v\n%s", err, logs.String())
	}
	if _, err := os.Stat(ready); !os.IsNotExist(err) {
		t.Errorf("the ready file outlived the process: %v", err)
	}
	if !strings.Contains(logs.String(), endpoint) {
		t.Errorf("the banner does not name the endpoint %s:\n%s", endpoint, logs.String())
	}
}

// readyTimeout bounds the wait for serve to become ready.
const readyTimeout = 10 * time.Second

// waitReady polls the ready file, then health, and returns the endpoint.
// When readyTimeout passes it kills cmd and fails the test with its stderr,
// read once Wait has finished copying it.
func waitReady(t *testing.T, cmd *exec.Cmd, ready string, stderr *bytes.Buffer) string {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.After(readyTimeout)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if endpoint, ok := healthy(t, client, ready); ok {
			return endpoint
		}
		select {
		case <-deadline:
			kill := cmd.Process.Kill()
			t.Fatalf("serve was not ready after %s (kill: %v, exit: %v); stderr:\n%s", readyTimeout, kill, cmd.Wait(), stderr)
		case <-tick.C:
		}
	}
}

// healthy reports the endpoint the ready file names once the file exists
// and health there answers 200.
func healthy(t *testing.T, client *http.Client, ready string) (endpoint string, ok bool) {
	t.Helper()
	data, err := os.ReadFile(ready)
	if err != nil {
		return "", false
	}
	var doc struct {
		Endpoint string `json:"endpoint"`
		PID      int    `json:"pid"`
	}
	if err := json.Unmarshal(data, &doc); err != nil || doc.PID == 0 {
		t.Fatalf("ready file %q: %v", data, err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, doc.Endpoint+"/_understudy/v1/health", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return doc.Endpoint, resp.StatusCode == http.StatusOK
}
