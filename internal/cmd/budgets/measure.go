package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// CommandError is a build or lint command that did not complete, with the
// tail of what it printed.
type CommandError struct {
	Command string
	Err     error
	Output  string
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("%s: %v\n%s", e.Command, e.Err, e.Output)
}

func (e *CommandError) Unwrap() error { return e.Err }

// command runs name with args in dir and returns its wall time. Extra
// environment entries override the inherited ones. A failure keeps the last
// 4 KiB of the combined output.
func command(ctx context.Context, dir string, env []string, name string, args ...string) (time.Duration, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	err := cmd.Run()
	took := time.Since(start)
	if err != nil {
		tail := out.Bytes()
		tail = tail[max(0, len(tail)-4096):]
		return took, &CommandError{Command: strings.Join(append([]string{name}, args...), " "), Err: err, Output: string(tail)}
	}
	return took, nil
}

// build compiles ./cmd/understudy stripped for goos/goarch into out.
func build(ctx context.Context, root, out, goos, goarch string) error {
	env := []string{"GOOS=" + goos, "GOARCH=" + goarch, "CGO_ENABLED=0"}
	_, err := command(ctx, root, env, "go", "build", "-trimpath", "-ldflags=-s -w", "-o", out, "./cmd/understudy")
	return err
}

// measureBinary builds the linux/amd64 binary and returns its size and a
// binary this host can run: the same one on linux/amd64, else a native
// build.
func measureBinary(ctx context.Context, root, dir string) (Result, string, error) {
	bin := filepath.Join(dir, "understudy-linux-amd64")
	if err := build(ctx, root, bin, "linux", "amd64"); err != nil {
		return Failed{err}, "", err
	}
	info, err := os.Stat(bin)
	if err != nil {
		return Failed{err}, "", err
	}
	size := Measured{Bytes(info.Size())}
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		return size, bin, nil
	}
	native := filepath.Join(dir, "understudy-native")
	if err := build(ctx, root, native, runtime.GOOS, runtime.GOARCH); err != nil {
		return size, "", err
	}
	return size, native, nil
}

// served is a started emulator.
type served struct {
	cmd    *exec.Cmd
	exited chan error
	stderr *bytes.Buffer
}

// StartError says why the emulator did not answer /health with 200.
type StartError struct {
	Reason string
	Stderr string
}

func (e *StartError) Error() string {
	return fmt.Sprintf("the emulator did not become ready: %s\nits stderr:\n%s", e.Reason, e.Stderr)
}

// readyTimeout bounds the wait for the first 200; far above the 250 ms
// ceiling, so a slow start is measured rather than cut off.
const readyTimeout = 30 * time.Second

// startServer runs bin serve on a free loopback port without Docker and
// returns the time from process start to the first 200 from /health.
func startServer(ctx context.Context, bin, dir string) (*served, time.Duration, error) {
	ready := filepath.Join(dir, "ready.json")
	cmd := exec.CommandContext(ctx, bin, "serve", "--listen", "127.0.0.1:0", "--ready-file", ready, "--log-level", "error")
	cmd.Env = append(os.Environ(), "DOCKER_HOST=unix://"+filepath.Join(dir, "no-docker.sock"))
	s := &served{cmd: cmd, exited: make(chan error, 1), stderr: &bytes.Buffer{}}
	cmd.Stdout, cmd.Stderr = io.Discard, s.stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	go func() { s.exited <- cmd.Wait() }()
	client := &http.Client{Timeout: time.Second}
	deadline := time.NewTimer(readyTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if healthy(ctx, client, ready) {
			return s, time.Since(start), nil
		}
		select {
		case err := <-s.exited:
			return nil, 0, &StartError{Reason: fmt.Sprintf("it exited: %v", err), Stderr: s.stderr.String()}
		case <-deadline.C:
			reason := fmt.Sprintf("no 200 from /_understudy/v1/health within %s", readyTimeout)
			return nil, 0, errors.Join(&StartError{Reason: reason, Stderr: s.stderr.String()}, s.stop())
		case <-ctx.Done():
			return nil, 0, errors.Join(ctx.Err(), s.stop())
		case <-tick.C:
		}
	}
}

// healthy reports whether the ready file names an endpoint whose health
// check answers 200.
func healthy(ctx context.Context, client *http.Client, ready string) bool {
	raw, err := os.ReadFile(ready)
	if err != nil {
		return false
	}
	var doc struct {
		Endpoint string `json:"endpoint"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Endpoint == "" {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, doc.Endpoint+"/_understudy/v1/health", http.NoBody)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_, drainErr := io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()
	return drainErr == nil && closeErr == nil && resp.StatusCode == http.StatusOK
}

// stop interrupts the emulator and kills it when it has not exited after
// five seconds. A process that already exited is stopped.
func (s *served) stop() error {
	if err := s.cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("interrupt the emulator: %w", err)
	}
	select {
	case <-s.exited:
		return nil
	case <-time.After(5 * time.Second):
	}
	if err := s.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("the emulator ignored an interrupt for 5 s and cannot be killed: %w", err)
	}
	<-s.exited
	return nil
}

// idleRSS reads the resident set of pid after it idled for idle.
func idleRSS(ctx context.Context, pid int, idle time.Duration) (Bytes, error) {
	select {
	case <-time.After(idle):
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, err
	}
	return parseVMRSS(bytes.NewReader(raw))
}

// parseVMRSS reads the VmRSS line of a /proc/<pid>/status document.
func parseVMRSS(r io.Reader) (Bytes, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		rest, ok := strings.CutPrefix(sc.Text(), "VmRSS:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) != 2 || fields[1] != "kB" {
			return 0, fmt.Errorf("VmRSS line %q is not \"<n> kB\"", sc.Text())
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("VmRSS line %q: %w", sc.Text(), err)
		}
		return Bytes(kb << 10), nil
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, errors.New("the status document has no VmRSS line")
}

// coldBuild times go build ./... with a fresh, empty build cache.
func coldBuild(ctx context.Context, root, dir string) Result {
	took, err := command(ctx, root, []string{"GOCACHE=" + filepath.Join(dir, "gocache")}, "go", "build", "./...")
	if err != nil {
		return Failed{err}
	}
	return Measured{Duration(took)}
}

// lintWallTime times golangci-lint run ./.... Exit status 1 is a complete
// run that reported issues; whether the tree is lint-clean is just lint's
// to say, not a budget.
func lintWallTime(ctx context.Context, root string) Result {
	took, err := command(ctx, root, nil, "golangci-lint", "run", "./...")
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return Failed{err}
	}
	return Measured{Duration(took)}
}
