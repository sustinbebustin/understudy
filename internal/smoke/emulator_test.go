//go:build smoke

package smoke_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// understudyBin is the path of the binary TestMain builds.
var understudyBin string

// readyWait bounds the wait for a started emulator to answer health.
const readyWait = 60 * time.Second

// TestMain builds the binary once into a temporary directory.
func TestMain(m *testing.M) {
	os.Exit(buildAndRun(m))
}

func buildAndRun(m *testing.M) int {
	dir, err := os.MkdirTemp("", "understudy-smoke-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "smoke: creating a directory for the binary: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)
	understudyBin = filepath.Join(dir, "understudy")
	build := exec.CommandContext(context.Background(), "go", "build", "-o", understudyBin, "github.com/sustinbebustin/understudy/cmd/understudy")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "smoke: building the binary failed: %v\n%s", err, out)
		return 1
	}
	return m.Run()
}

// lockedBuffer collects a child's output while it runs.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// emulator is an understudy the workload drives: its endpoints and an
// HTTPS client that trusts its TLS certificate.
type emulator struct {
	endpoint string
	// tls is the TLS listener's URL, empty without one.
	tls    string
	client *http.Client
}

// childEnv is the test's environment without UNDERSTUDY_* and AWS_*
// settings, which would change what the emulator does, plus extra.
func childEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "UNDERSTUDY_") || strings.HasPrefix(kv, "AWS_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

// serve starts `understudy serve` with args on an ephemeral loopback port
// with a ready file, a frozen clock and seed 1, waits until it is ready,
// and stops it with SIGINT on cleanup, failing the test when it does not
// exit cleanly.
func serve(t *testing.T, env []string, args ...string) *emulator {
	t.Helper()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready.json")
	args = append([]string{"serve", "--listen", "127.0.0.1:0", "--ready-file", ready, "--clock", "frozen", "--seed", "1"}, args...)
	// Cleanup stops the emulator with an interrupt, after the test's
	// context has ended, so the command outlives it.
	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), understudyBin, args...)
	// The working directory holds no understudy.json, so only the flags
	// configure the emulator.
	cmd.Dir = dir
	cmd.Env = env
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", understudyBin, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		if err := cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("interrupting the emulator: %v", err)
		}
		select {
		case err := <-exited:
			if err != nil {
				t.Errorf("the emulator exited with %v after an interrupt; its output:\n%s", err, out)
			}
		case <-time.After(readyWait):
			t.Errorf("the emulator ignored an interrupt for %s (killing it: %v); its output:\n%s", readyWait, cmd.Process.Kill(), out)
		}
	})
	var doc struct {
		Endpoint    string `json:"endpoint"`
		TLSEndpoint string `json:"tlsEndpoint"`
	}
	deadline := time.After(readyWait)
	for {
		if data, err := os.ReadFile(ready); err == nil && json.Unmarshal(data, &doc) == nil && doc.Endpoint != "" {
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("the emulator exited before it was ready (%v); its output:\n%s", err, out)
		case <-deadline:
			t.Fatalf("no ready file after %s; the emulator's output:\n%s", readyWait, out)
		case <-time.After(20 * time.Millisecond):
		}
	}
	emu := &emulator{endpoint: doc.Endpoint, tls: doc.TLSEndpoint, client: &http.Client{Timeout: 3 * time.Minute}}
	if emu.tls != "" {
		emu.client.Transport = trusting(t, emu.tls)
	}
	awaitHealth(t, emu)
	return emu
}

// trusting returns a transport that trusts the certificate the TLS
// listener at endpoint presents, as AWS_CA_BUNDLE pointed at it would.
func trusting(t *testing.T, endpoint string) *http.Transport {
	t.Helper()
	dialer := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // reads the certificate to trust it
	conn, err := dialer.DialContext(t.Context(), "tcp", strings.TrimPrefix(endpoint, "https://"))
	if err != nil {
		t.Fatalf("dialing the TLS listener: %v", err)
	}
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		t.Fatalf("tls.Dialer returned %T", conn)
	}
	roots := x509.NewCertPool()
	roots.AddCert(tlsConn.ConnectionState().PeerCertificates[0])
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	return &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
}

// awaitHealth waits for GET /_understudy/v1/health to answer 200.
func awaitHealth(t *testing.T, emu *emulator) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), readyWait)
	defer cancel()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, emu.endpoint+"/_understudy/v1/health", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		if resp, err := emu.client.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s/_understudy/v1/health did not answer 200 within %s", emu.endpoint, readyWait)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// cli runs an understudy subcommand against emu and returns its stdout.
func cli(t *testing.T, emu *emulator, args ...string) string {
	t.Helper()
	args = append(args, "--endpoint", emu.endpoint)
	cmd := exec.CommandContext(t.Context(), understudyBin, args...)
	cmd.Env = childEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("understudy %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, &stdout, &stderr)
	}
	return stdout.String()
}
