//go:build docker

package distribution_test

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
)

const (
	testLabel   = "understudy.dev/test=1"
	maxImage    = 60 << 20
	socketPath  = "/var/run/docker.sock"
	readyWait   = 30 * time.Second
	attemptWait = 2 * time.Second
	pollEvery   = 100 * time.Millisecond
	healthPath  = "/_understudy/v1/health"
	imageUser   = 65532
)

// imageConfig is the part of `docker image inspect` the image contract names.
type imageConfig struct {
	User         string
	Entrypoint   []string
	Cmd          []string
	ExposedPorts map[string]struct{}
	Volumes      map[string]struct{}
	Labels       map[string]string
	Healthcheck  struct {
		Test        []string
		Interval    time.Duration
		Timeout     time.Duration
		StartPeriod time.Duration
	}
}

// imageFS is the exported filesystem of a container created from the image.
type imageFS struct {
	// Size is the length of the export, which is what a pull writes to
	// disk; `docker image inspect` reports compressed bytes under the
	// containerd image store.
	Size    int64
	Entries map[string]*tar.Header
}

// docker runs the docker CLI and returns its trimmed stdout.
func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := dockerOutput(t.Context(), args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func dockerOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(string(out)), nil
}

// removeOnCleanup removes the container name when the test ends. It is
// registered before the container is created, so a run that creates the
// container and then fails to start it does not leak it; removing a
// container that does not exist succeeds.
func removeOnCleanup(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := dockerOutput(context.WithoutCancel(t.Context()), "rm", "--force", "--volumes", name); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
}

// uniqueName is a name no other run's containers or images share. It is
// lowercase because image repository names must be.
func uniqueName() string {
	return "understudy-test-" + strings.ToLower(rand.Text())
}

// repoRoot is the directory holding go.mod above the test's working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// buildImage builds the repository's Dockerfile under a unique tag and
// removes the image when the test ends.
func buildImage(t *testing.T) string {
	t.Helper()
	tag := uniqueName() + ":test"
	t.Cleanup(func() {
		if _, err := dockerOutput(context.WithoutCancel(t.Context()), "image", "rm", "--force", tag); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	docker(t, "build", "--label", testLabel, "--tag", tag, repoRoot(t))
	return tag
}

func inspectImage(t *testing.T, tag string) imageConfig {
	t.Helper()
	var cfg imageConfig
	if err := json.Unmarshal([]byte(docker(t, "image", "inspect", "--format", "{{json .Config}}", tag)), &cfg); err != nil {
		t.Fatalf("decode the image config: %v", err)
	}
	return cfg
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// exportImage exports the filesystem of a container created from tag.
func exportImage(t *testing.T, tag string) imageFS {
	t.Helper()
	ctx := t.Context()
	name := uniqueName()
	removeOnCleanup(t, name)
	docker(t, "create", "--label", testLabel, "--name", name, tag)
	cmd := exec.CommandContext(ctx, "docker", "export", name)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	counted := &countingReader{r: stdout}
	entries, readErr := tarEntries(counted)
	_, drainErr := io.Copy(io.Discard, counted)
	if err := errors.Join(readErr, drainErr, cmd.Wait()); err != nil {
		t.Fatalf("docker export %s: %v\n%s", name, err, stderr.String())
	}
	return imageFS{Size: counted.n, Entries: entries}
}

func tarEntries(r io.Reader) (map[string]*tar.Header, error) {
	entries := map[string]*tar.Header{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return entries, fmt.Errorf("read the export: %w", err)
		}
		entries[strings.TrimSuffix(hdr.Name, "/")] = hdr
	}
}

// runContainer starts the image with its port published on an ephemeral
// loopback port and returns the container name and its base URL.
func runContainer(t *testing.T, tag string, extra ...string) (name, endpoint string) {
	t.Helper()
	name = uniqueName()
	removeOnCleanup(t, name)
	args := append([]string{"run", "--detach", "--label", testLabel, "--name", name, "--publish", "127.0.0.1:0:4566"}, extra...)
	docker(t, append(args, tag)...)
	published := docker(t, "port", name, "4566/tcp")
	first, _, _ := strings.Cut(published, "\n")
	return name, "http://" + first
}

// containerLogs is everything the container wrote: `docker logs` replays
// the container's stderr, where serve logs, on its own stderr.
func containerLogs(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.CommandContext(context.WithoutCancel(t.Context()), "docker", "logs", name).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("(docker logs %s failed: %v)\n%s", name, err, out)
	}
	return string(out)
}

// awaitHealth polls the health endpoint until it answers 200, failing with
// the container's logs when it exits, demands a token, or is not ready
// within readyWait. A 401 or 403 is final rather than "not yet": the
// published port, not a token, guards the admin API of an
// Exposed{ContainerPublished} listener.
func awaitHealth(t *testing.T, name, endpoint string) admin.Health {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), readyWait)
	defer cancel()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	last := "no answer yet"
	for {
		if running := docker(t, "inspect", "--format", "{{.State.Running}}", name); running != "true" {
			t.Fatalf("container %s stopped before it was ready:\n%s", name, containerLogs(t, name))
		}
		h, status, err := getHealth(ctx, endpoint)
		switch {
		case err != nil && ctx.Err() != nil:
			// The readiness deadline cut this attempt short; keep the
			// previous attempt's answer, which says why serve is not ready.
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			t.Fatalf("container %s answered health with status %d: the admin API demanded a token on a ContainerPublished listener:\n%s", name, status, containerLogs(t, name))
		case err != nil:
			last = err.Error()
		case status == http.StatusOK && h.Ready:
			return h
		default:
			last = fmt.Sprintf("status %d, ready %v, waiting %v", status, h.Ready, h.Waiting)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("container %s was not ready within %s (last answer: %s):\n%s", name, readyWait, last, containerLogs(t, name))
		case <-tick.C:
		}
	}
}

// healthyWait bounds the wait for Docker's healthcheck: the image's start
// period and two intervals, plus readyWait for serve itself.
const healthyWait = readyWait + 13*time.Second

// awaitHealthy waits until Docker's own run of the image's HEALTHCHECK
// reports the container healthy, failing with the check's output and the
// container's logs when it reports unhealthy, the container stops, or
// healthyWait passes.
func awaitHealthy(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(healthyWait)
	for {
		state := docker(t, "inspect", "--format", "{{.State.Running}} {{.State.Health.Status}}", name)
		switch state {
		case "true healthy":
			return
		case "true starting":
		default:
			t.Fatalf("container %s is %q, want running and healthy; healthcheck runs: %s\n%s", name, state,
				docker(t, "inspect", "--format", "{{json .State.Health.Log}}", name), containerLogs(t, name))
		}
		if time.Now().After(deadline) {
			t.Fatalf("container %s was not healthy within %s; healthcheck runs: %s\n%s", name, healthyWait,
				docker(t, "inspect", "--format", "{{json .State.Health.Log}}", name), containerLogs(t, name))
		}
		time.Sleep(10 * pollEvery)
	}
}

// getHealth makes one health request, bounded by attemptWait so a hung
// connection cannot use up the whole readiness wait.
func getHealth(ctx context.Context, endpoint string) (admin.Health, int, error) {
	ctx, cancel := context.WithTimeout(ctx, attemptWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+healthPath, http.NoBody)
	if err != nil {
		return admin.Health{}, 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return admin.Health{}, 0, err
	}
	defer resp.Body.Close()
	var h admin.Health
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return admin.Health{}, resp.StatusCode, fmt.Errorf("decode the health body: %w", err)
	}
	return h, resp.StatusCode, nil
}

// socketGroup is the group that owns the Docker socket. A missing socket
// fails rather than skips: the docker tag already requires a daemon, and a
// skip would hide that the "Docker reachable" check never ran.
func socketGroup(t *testing.T) string {
	t.Helper()
	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("the socket check mounts the Docker daemon's socket from %s, which is not there (%v); run the docker-tagged tests against a daemon listening at %s (rootless and remote daemons are unsupported in phase 1)", socketPath, err, socketPath)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s has no unix ownership", socketPath)
	}
	return strconv.FormatUint(uint64(st.Gid), 10)
}

// noSocket is the cause ARCHITECTURE.md 24 gives for Docker and DynamoDB
// being disabled in an image started without the Docker socket.
const noSocket = "no Docker socket"

// wantNoSocket fails unless s is disabled because no Docker socket is
// mounted. Matching the cause keeps a status disabled for another reason,
// such as DynamoDB turned off by configuration, from passing.
func wantNoSocket(t *testing.T, what string, s admin.ServiceStatus) {
	t.Helper()
	if s.Status != admin.Disabled || !strings.Contains(s.Reason, noSocket) {
		t.Errorf("%s = %+v without a mounted socket, want disabled with a reason naming %q", what, s, noSocket)
	}
}

// TestImage builds the image once and checks what it declares and carries,
// then what serve reports from inside it (ARCHITECTURE.md 14.5 InContainer,
// 24).
func TestImage(t *testing.T) {
	tag := buildImage(t)

	t.Run("declares the contract the documentation promises", func(t *testing.T) {
		cfg := inspectImage(t, tag)
		if want := fmt.Sprintf("%d:%d", imageUser, imageUser); cfg.User != want {
			t.Errorf("user = %q, want %s", cfg.User, want)
		}
		if !slices.Equal(cfg.Entrypoint, []string{"/understudy"}) || !slices.Equal(cfg.Cmd, []string{"serve"}) {
			t.Errorf("entrypoint %q, cmd %q; want /understudy and serve", cfg.Entrypoint, cfg.Cmd)
		}
		if _, ok := cfg.ExposedPorts["4566/tcp"]; !ok {
			t.Errorf("exposed ports = %v, want 4566/tcp", cfg.ExposedPorts)
		}
		if _, ok := cfg.Volumes["/data"]; !ok {
			t.Errorf("volumes = %v, want /data", cfg.Volumes)
		}
		wantCheck := []string{"CMD", "/understudy", "health"}
		hc := cfg.Healthcheck
		if !slices.Equal(hc.Test, wantCheck) || hc.Interval != 5*time.Second || hc.Timeout != 2*time.Second || hc.StartPeriod != 3*time.Second {
			t.Errorf("healthcheck = %+v, want %q every 5s with a 2s timeout and 3s start period", hc, wantCheck)
		}
		for _, key := range []string{"title", "description", "source", "licenses", "version"} {
			if cfg.Labels["org.opencontainers.image."+key] == "" {
				t.Errorf("label org.opencontainers.image.%s is missing; labels = %v", key, cfg.Labels)
			}
		}
	})

	t.Run("filesystem", func(t *testing.T) {
		testFilesystem(t, exportImage(t, tag))
	})

	t.Run("serves in-container on a published port without a token", func(t *testing.T) {
		name, endpoint := runContainer(t, tag)
		h := awaitHealth(t, name, endpoint)
		if h.Topology != "in-container" {
			t.Errorf("topology = %q, want in-container", h.Topology)
		}
		if h.Listener != "0.0.0.0:4566" {
			t.Errorf("listener = %q, want 0.0.0.0:4566", h.Listener)
		}
		wantNoSocket(t, "docker", h.Docker)
		wantNoSocket(t, "dynamodb", h.Services["dynamodb"])
		// Lambda's control plane works without Docker; only Invoke cannot
		// run functions, and Docker's own row names the missing socket.
		if s := h.Services["lambda"]; s.Status != admin.Degraded || !strings.Contains(s.Reason, "control plane only") || !strings.Contains(s.Reason, "Docker is disabled") {
			t.Errorf("lambda = %+v without a mounted socket, want degraded to its control plane, naming Docker as disabled", s)
		}
	})

	// The healthcheck runs inside the container, so it must find serve on
	// whatever port the container's own settings move it to.
	for _, tc := range []struct {
		name string
		env  []string
	}{
		{"is healthy on a port set with UNDERSTUDY_LISTEN and UNDERSTUDY_ENDPOINT", []string{"UNDERSTUDY_LISTEN=0.0.0.0:8080", "UNDERSTUDY_ENDPOINT=http://127.0.0.1:8080"}},
		{"is healthy on a port set with UNDERSTUDY_LISTEN alone", []string{"UNDERSTUDY_LISTEN=0.0.0.0:8080"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra := make([]string, 0, 2*len(tc.env))
			for _, kv := range tc.env {
				extra = append(extra, "--env", kv)
			}
			name, _ := runContainer(t, tag, extra...)
			awaitHealthy(t, name)
		})
	}

	t.Run("reaches Docker through a mounted socket", func(t *testing.T) {
		group := socketGroup(t)
		name, endpoint := runContainer(t, tag, "--volume", socketPath+":"+socketPath, "--group-add", group)
		h := awaitHealth(t, name, endpoint)
		if h.Docker.Status != admin.Enabled {
			t.Errorf("docker = %+v with %s mounted, want enabled", h.Docker, socketPath)
		}
	})
}

// testFilesystem checks the exported root filesystem of the image.
func testFilesystem(t *testing.T, root imageFS) {
	t.Helper()
	t.Run("is small", func(t *testing.T) {
		if root.Size >= maxImage {
			t.Errorf("unpacked image is %d bytes, want under %d", root.Size, maxImage)
		}
	})

	t.Run("carries a writable /data, a sticky /tmp and the notices", func(t *testing.T) {
		for _, want := range []struct {
			path     string
			mode     fs.FileMode
			uid, gid int
		}{
			{path: "data", mode: fs.ModeDir | 0o755, uid: imageUser, gid: imageUser},
			{path: "tmp", mode: fs.ModeDir | fs.ModeSticky | 0o777, uid: 0, gid: 0},
			{path: "etc/ssl/certs/ca-certificates.crt", mode: 0o644, uid: 0, gid: 0},
			{path: "usr/share/doc/understudy", mode: fs.ModeDir | 0o755, uid: 0, gid: 0},
			{path: "usr/share/doc/understudy/LICENSE", mode: 0o644, uid: 0, gid: 0},
			{path: "usr/share/doc/understudy/NOTICE", mode: 0o644, uid: 0, gid: 0},
		} {
			hdr, ok := root.Entries[want.path]
			if !ok {
				t.Errorf("/%s is missing from the image", want.path)
				continue
			}
			if got := hdr.FileInfo().Mode(); got != want.mode || hdr.Uid != want.uid || hdr.Gid != want.gid {
				t.Errorf("/%s is %v %d:%d, want %v %d:%d", want.path, got, hdr.Uid, hdr.Gid, want.mode, want.uid, want.gid)
			}
		}
	})
}
