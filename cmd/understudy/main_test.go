package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
)

func TestVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(t.Context(), []string{"version"}, &out, &errOut); code != admin.ExitOK || out.String() != "understudy dev\n" {
		t.Errorf("version = %d %q %q", code, out.String(), errOut.String())
	}
}

var errClosed = errors.New("stdout closed")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errClosed }

func TestVersionReportsAFailedWrite(t *testing.T) {
	var errOut bytes.Buffer
	if code := run(t.Context(), []string{"version"}, failingWriter{}, &errOut); code != admin.ExitFailed || !strings.Contains(errOut.String(), "stdout closed") {
		t.Errorf("version to a closed stdout = %d %q", code, errOut.String())
	}
}

func TestUsageErrorsExit2AndSayWhatToFix(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"bogus"},
		{"serve", "--listen", "localhost"},
		{"serve", "--clock", "slow"},
		{"serve", "--seed", "-1"},
		{"serve", "extra"},
	} {
		var out, errOut bytes.Buffer
		if code := run(t.Context(), args, &out, &errOut); code != admin.ExitUsage || errOut.Len() == 0 {
			t.Errorf("run(%q) = %d, stderr %q; want 2 and a message", args, code, errOut.String())
		}
	}
	var out bytes.Buffer
	if code := run(t.Context(), []string{"serve", "--help"}, &out, &out); code != admin.ExitOK || !strings.Contains(out.String(), "Examples:") {
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

// TestServeBinary finds health ready within 250 ms of process start and
// stops the server with SIGINT.
func TestServeBinary(t *testing.T) {
	s := startServe(t)
	if elapsed := time.Since(s.started); elapsed > 250*time.Millisecond {
		t.Errorf("health was ready %s after the process started, over the 250 ms budget", elapsed)
	}
	if err := s.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := s.cmd.Wait(); err != nil {
		t.Fatalf("serve after SIGINT: %v\n%s", err, s.logs.String())
	}
	if _, err := os.Stat(s.ready); !os.IsNotExist(err) {
		t.Errorf("the ready file outlived the process: %v", err)
	}
	if !strings.Contains(s.logs.String(), s.endpoint) {
		t.Errorf("the banner does not name the endpoint %s:\n%s", s.endpoint, s.logs.String())
	}
}

// TestSecondSignalForcesExit holds the graceful shutdown open with a
// request whose body never finishes arriving, which drain waits
// shutdownGrace (5 s) for, and checks a second SIGINT kills the process
// at once instead of being swallowed.
func TestSecondSignalForcesExit(t *testing.T) {
	s := startServe(t)
	var dialer net.Dialer
	conn, err := dialer.DialContext(t.Context(), "tcp", strings.TrimPrefix(s.endpoint, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	stalled := "POST / HTTP/1.1\r\nHost: localhost\r\n" +
		"Content-Type: application/x-amz-json-1.0\r\nX-Amz-Target: AmazonSQS.SendMessage\r\n" +
		"Authorization: AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20260101/us-east-1/sqs/aws4_request, SignedHeaders=host, Signature=0\r\n" +
		"Content-Length: 1000\r\n\r\n{\"QueueUrl\":"
	if _, err := io.WriteString(conn, stalled); err != nil {
		t.Fatal(err)
	}
	if _, ok := healthy(t, &http.Client{Timeout: time.Second}, s.ready); !ok {
		t.Fatal("health is not ready while the stalled request is in flight")
	}
	exited := make(chan error, 1)
	go func() { exited <- s.cmd.Wait() }()
	if err := s.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	waitListenerClosed(t, s.endpoint, exited)
	if err := s.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("second SIGINT: %v (the process exited before it; did the stalled request hold shutdown open?)\n%s", err, s.logs.String())
	}
	// The wait is generous so that a loaded machine does not fail it. It
	// outlasts the server's 5 s shutdown grace, which is safe: if the
	// second signal were swallowed, the process would exit on its own once
	// the grace ran out, and the death-by-SIGINT check below would fail.
	select {
	case err := <-exited:
		exit, ok := errors.AsType[*exec.ExitError](err)
		if !ok {
			t.Fatalf("serve after a second SIGINT = %v, want death by SIGINT\n%s", err, s.logs.String())
		}
		if ws, ok := exit.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGINT {
			t.Errorf("serve after a second SIGINT = %v, want death by SIGINT\n%s", err, s.logs.String())
		}
	case <-time.After(readyTimeout):
		t.Fatalf("serve was still running %s after a second SIGINT, so the signal was swallowed\n%s", readyTimeout, s.logs.String())
	}
}

// waitListenerClosed waits until the main listener refuses connections,
// which means the graceful shutdown has begun. It fails the test when the
// process exits first, since the stalled request should hold it open.
func waitListenerClosed(t *testing.T, endpoint string, exited <-chan error) {
	t.Helper()
	deadline := time.After(readyTimeout)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	var dialer net.Dialer
	for {
		conn, err := dialer.DialContext(t.Context(), "tcp", strings.TrimPrefix(endpoint, "http://"))
		if err != nil {
			return
		}
		_ = conn.Close()
		select {
		case err := <-exited:
			t.Fatalf("serve exited %v after the first SIGINT although a request was in flight", err)
		case <-deadline:
			t.Fatalf("the listener at %s still accepted connections %s after SIGINT", endpoint, readyTimeout)
		case <-tick.C:
		}
	}
}

// served is a serve process that reported ready.
type served struct {
	cmd      *exec.Cmd
	ready    string
	endpoint string
	logs     *syncBuffer
	started  time.Time
}

// syncBuffer is serve's stderr, which a test may read while exec's copier
// still writes it, as when a second signal does not end the process.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startServe builds the binary, starts it on an ephemeral port with a
// ready file, and waits until health is ready.
func startServe(t *testing.T) served {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "understudy")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	ready := filepath.Join(dir, "ready.json")
	logs := new(syncBuffer)
	cmd := exec.CommandContext(t.Context(), bin, "serve", "--listen", "127.0.0.1:0", "--ready-file", ready)
	cmd.Stderr = logs
	started := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	endpoint := waitReady(t, cmd, ready, logs)
	return served{cmd: cmd, ready: ready, endpoint: endpoint, logs: logs, started: started}
}

// readyTimeout bounds the wait for serve to become ready.
const readyTimeout = 10 * time.Second

// waitReady polls the ready file, then health, and returns the endpoint.
// When readyTimeout passes it kills cmd and fails the test with its stderr,
// read once Wait has finished copying it.
func waitReady(t *testing.T, cmd *exec.Cmd, ready string, stderr *syncBuffer) string {
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
