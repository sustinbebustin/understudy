package docker_test

import (
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
)

func daemonWithImage(t *testing.T, opts dockertest.Options) (*dockertest.Daemon, *docker.Client) {
	t.Helper()
	d := dockertest.New(t, opts)
	d.AddImage(providedImage, docker.Image{Config: docker.ImageConfig{Labels: map[string]string{"com.amazonaws.lambda.platform.kernel": "k6.1"}}})
	c := d.Client(t)
	return d, c
}

func create(t *testing.T, c *docker.Client, name string, cfg docker.Config, host docker.HostConfig) string {
	t.Helper()
	if cfg.Image == "" {
		cfg.Image = providedImage
	}
	created, err := c.ContainerCreate(t.Context(), name, cfg, host, docker.NetworkingConfig{})
	if err != nil {
		t.Fatalf("ContainerCreate %s: %v", name, err)
	}
	return created.ID
}

// labelMap renders l, failing the test on an incomplete ownership.
func labelMap(t *testing.T, l docker.Labels) map[string]string {
	t.Helper()
	m, err := l.Map()
	if err != nil {
		t.Fatalf("Labels.Map: %v", err)
	}
	return m
}

// readAll drains a log stream into per-stream text.
func readAll(t *testing.T, s *docker.LogStream) (stdout, stderr string) {
	t.Helper()
	defer s.Close()
	var out, errOut strings.Builder
	for {
		f, err := s.Next()
		if errors.Is(err, io.EOF) {
			return out.String(), errOut.String()
		}
		if err != nil {
			t.Fatalf("LogStream.Next: %v", err)
		}
		switch f.Stream {
		case docker.Stdout:
			out.Write(f.Payload)
		case docker.Stderr:
			errOut.Write(f.Payload)
		}
	}
}

func TestContainerRunsToExitAndReportsItsCodeAndOutput(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	d.SetProgram(providedImage, dockertest.Exit{Code: 7, Output: []docker.Frame{
		{Stream: docker.Stdout, Payload: []byte("hello\n")},
		{Stream: docker.Stderr, Payload: []byte("warn\n")},
	}})
	ctx := t.Context()
	id := create(t, c, "fn-1", docker.Config{
		Platform: docker.Platform{OS: "linux", Architecture: "arm64"},
		Cmd:      []string{"bootstrap"},
		Labels:   labelMap(t, docker.Labels{Ownership: docker.Ownership{Instance: "i1", Owner: "o1"}, Test: true}),
	}, docker.HostConfig{LogConfig: docker.LogConfig{Type: "json-file"}})
	if q := lastRequest(d, http.MethodPost).Query; q.Get("name") != "fn-1" || q.Get("platform") != "linux/arm64" {
		t.Errorf("create query = %v, want name and platform", q)
	}

	wait, err := c.ContainerWait(ctx, id, docker.WaitNextExit)
	if err != nil {
		t.Fatalf("ContainerWait: %v", err)
	}
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatalf("ContainerStart: %v", err)
	}
	if code, err := wait.Result(); err != nil || code != 7 {
		t.Fatalf("Wait.Result = %d, %v; want 7", code, err)
	}
	logs, err := c.ContainerLogs(ctx, id, docker.LogsOptions{})
	if err != nil {
		t.Fatalf("ContainerLogs: %v", err)
	}
	if stdout, stderr := readAll(t, logs); stdout != "hello\n" || stderr != "warn\n" {
		t.Errorf("logs = %q / %q, want hello / warn", stdout, stderr)
	}
	got, err := c.ContainerInspect(ctx, "fn-1")
	if err != nil {
		t.Fatalf("ContainerInspect by name: %v", err)
	}
	if got.ID != id || got.State.Running || got.State.ExitCode != 7 || got.Config.Labels[docker.LabelTest] != "1" {
		t.Errorf("inspect = %+v, want exited with 7 and the test label", got)
	}
	if err := c.ContainerRemove(ctx, id, docker.RemoveOptions{Force: true, Volumes: true}); err != nil {
		t.Fatalf("ContainerRemove: %v", err)
	}
	if _, err := c.ContainerInspect(ctx, id); !docker.IsNotFound(err) {
		t.Errorf("inspect after remove = %v, want not found", err)
	}
}

// ContainerMemory reads the daemon's memory_stats once; Used is the usage
// without reclaimable page cache, under either cgroup version's name.
func TestContainerMemory(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	id := create(t, c, "mem", docker.Config{}, docker.HostConfig{})
	var m docker.Memory
	m.Usage, m.Stats.InactiveFile = 40<<20, 8<<20
	if !d.SetMemory(id, m) {
		t.Fatal("SetMemory found no container")
	}
	got, err := c.ContainerMemory(t.Context(), id)
	if err != nil || got.Usage != 40<<20 || got.Used() != 32<<20 {
		t.Fatalf("ContainerMemory = %+v (used %d), %v; want usage 40 MiB, used 32 MiB", got, got.Used(), err)
	}
	var v1 docker.Memory
	v1.Usage, v1.Stats.TotalInactiveFile = 10, 20
	if v1.Used() != 0 {
		t.Fatalf("Used with more cache than usage = %d, want 0", v1.Used())
	}
}

func TestContainerStartAndStopAreIdempotent(t *testing.T) {
	_, c := daemonWithImage(t, dockertest.Options{})
	ctx := t.Context()
	id := create(t, c, "svc", docker.Config{}, docker.HostConfig{})
	for range 2 {
		if err := c.ContainerStart(ctx, id); err != nil {
			t.Fatalf("ContainerStart (a running container answers 304): %v", err)
		}
	}
	for range 2 {
		if err := c.ContainerStop(ctx, id, 0); err != nil {
			t.Fatalf("ContainerStop (a stopped container answers 304): %v", err)
		}
	}
	if st, err := c.ContainerInspect(ctx, id); err != nil || st.State.Running {
		t.Errorf("inspect after stop = %+v, %v; want not running", st.State, err)
	}
}

func TestContainerStopRoundsTheTimeoutUpToSeconds(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	id := create(t, c, "svc", docker.Config{}, docker.HostConfig{})
	if err := c.ContainerStop(t.Context(), id, 1500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got := lastRequest(d, http.MethodPost).Query.Get("t"); got != "2" {
		t.Errorf("stop t = %q, want 2", got)
	}
}

func TestContainerErrorsCarryTheDaemonStatusAndMessage(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	ctx := t.Context()
	running := create(t, c, "running", docker.Config{}, docker.HostConfig{ReadonlyRootfs: true})
	if err := c.ContainerStart(ctx, running); err != nil {
		t.Fatal(err)
	}
	stopped := create(t, c, "stopped", docker.Config{}, docker.HostConfig{LogConfig: docker.LogConfig{Type: "none"}})
	d.FailNext("POST /containers/{id}/start", http.StatusInternalServerError, `failed to create task for container: exec: "/nonexistent": stat /nonexistent: no such file or directory`)

	_, logsErr := c.ContainerLogs(ctx, stopped, docker.LogsOptions{})
	_, createErr := c.ContainerCreate(ctx, "running", docker.Config{Image: providedImage}, docker.HostConfig{}, docker.NetworkingConfig{})
	_, missingImageErr := c.ContainerCreate(ctx, "x1", docker.Config{Image: "nope:1"}, docker.HostConfig{}, docker.NetworkingConfig{})
	cases := []struct {
		name     string
		err      error
		status   int
		contains string
	}{
		{"kill a stopped container", c.ContainerKill(ctx, stopped, ""), http.StatusConflict, "is not running"},
		{"remove a running container without force", c.ContainerRemove(ctx, running, docker.RemoveOptions{}), http.StatusConflict, "container is running"},
		{"remove a missing container", c.ContainerRemove(ctx, "ghost", docker.RemoveOptions{Force: true}), http.StatusNotFound, "No such container: ghost"},
		{"create with a name in use", createErr, http.StatusConflict, `The container name "/running" is already in use`},
		{"create from an absent image", missingImageErr, http.StatusNotFound, "No such image: nope:1"},
		{"logs with the none driver", logsErr, http.StatusNotImplemented, "does not support reading"},
		{"upload into a read-only rootfs", c.ContainerArchiveUpload(ctx, running, "/var/task", strings.NewReader("tar")), http.StatusBadRequest, "rootfs is marked read-only"},
		{"start failing in the runtime", c.ContainerStart(ctx, stopped), http.StatusInternalServerError, "no such file or directory"},
	}
	for _, tc := range cases {
		var derr *docker.Error
		if !errors.As(tc.err, &derr) || derr.Status != tc.status || !strings.Contains(derr.Message, tc.contains) {
			t.Errorf("%s: error = %v, want HTTP %d containing %q", tc.name, tc.err, tc.status, tc.contains)
		}
	}
	if !docker.IsConflict(cases[0].err) || !docker.IsNotFound(cases[2].err) || docker.IsNotFound(cases[0].err) {
		t.Error("IsConflict and IsNotFound disagree with the status")
	}
}

// The daemon resolves a reference as a full ID, then an exact name, then an
// ID prefix, so a container named like another's ID prefix is found by its
// name and its name stays taken.
func TestContainerNameWinsOverAnIDPrefix(t *testing.T) {
	_, c := daemonWithImage(t, dockertest.Options{})
	ctx := t.Context()
	first := create(t, c, "aaa", docker.Config{}, docker.HostConfig{})
	prefix := first[:4]
	named := create(t, c, prefix, docker.Config{}, docker.HostConfig{})
	for range 20 {
		got, err := c.ContainerInspect(ctx, prefix)
		if err != nil {
			t.Fatalf("ContainerInspect %s: %v", prefix, err)
		}
		if got.ID != named {
			t.Fatalf("ContainerInspect %s = %s, want the container named %s (%s), not the ID-prefix match %s", prefix, got.ID, prefix, named, first)
		}
	}
	_, err := c.ContainerCreate(ctx, prefix, docker.Config{Image: providedImage}, docker.HostConfig{}, docker.NetworkingConfig{})
	if !docker.IsConflict(err) {
		t.Errorf("second ContainerCreate %s = %v, want conflict", prefix, err)
	}
}

func TestContainerKillEndsARunningContainer(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	ctx := t.Context()
	id := create(t, c, "svc", docker.Config{}, docker.HostConfig{})
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := c.ContainerKill(ctx, id, ""); err != nil {
		t.Fatalf("ContainerKill: %v", err)
	}
	if st, ok := d.Container(id); !ok || st.State.Running || st.State.ExitCode != 137 {
		t.Errorf("state after SIGKILL = %+v, want exited 137", st.State)
	}
}

func TestContainerWaitNotRunningReturnsAtOnceForAStoppedContainer(t *testing.T) {
	_, c := daemonWithImage(t, dockertest.Options{})
	id := create(t, c, "svc", docker.Config{}, docker.HostConfig{})
	w, err := c.ContainerWait(t.Context(), id, docker.WaitNotRunning)
	if err != nil {
		t.Fatal(err)
	}
	if code, err := w.Result(); err != nil || code != 0 {
		t.Errorf("Result = %d, %v; want 0 at once", code, err)
	}
}

func TestContainerWaitRemovedReturnsWhenTheContainerIsDeleted(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	ctx := t.Context()
	id := create(t, c, "svc", docker.Config{}, docker.HostConfig{})
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatal(err)
	}
	w, err := c.ContainerWait(ctx, id, docker.WaitRemoved)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Exit(id, 3) {
		t.Fatal("Exit: container not running")
	}
	if err := c.ContainerRemove(ctx, id, docker.RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if code, err := w.Result(); err != nil || code != 3 {
		t.Errorf("Result = %d, %v; want 3", code, err)
	}
}

func TestContainerWaitReportsAFailedWaitAsAStreamError(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	ctx := t.Context()
	id := create(t, c, "svc", docker.Config{}, docker.HostConfig{})
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatal(err)
	}
	w, err := c.ContainerWait(ctx, id, docker.WaitNextExit)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if !d.FailWait(id, "container wait interrupted") {
		t.Fatal("FailWait: no such container")
	}
	code, err := w.Result()
	se, ok := errors.AsType[*docker.StreamError](err)
	if !ok || se.Message != "container wait interrupted" || se.Op != "ContainerWait "+id || code != -1 {
		t.Errorf("Result = %d, %v; want -1 and a *StreamError for ContainerWait %s carrying the daemon's message", code, err, id)
	}
}

func TestContainerLogsFollowStreamsUntilExit(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	d.SetProgram(providedImage, dockertest.Run{Output: []docker.Frame{{Stream: docker.Stdout, Payload: []byte("START\n")}}})
	ctx := t.Context()
	id := create(t, c, "fn", docker.Config{}, docker.HostConfig{})
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatal(err)
	}
	logs, err := c.ContainerLogs(ctx, id, docker.LogsOptions{Follow: true, Timestamps: true})
	if err != nil {
		t.Fatal(err)
	}
	first, err := logs.Next()
	if err != nil || !strings.HasSuffix(string(first.Payload), "Z START\n") || !strings.HasPrefix(string(first.Payload), "2026-01-01T") {
		t.Fatalf("first frame = %q, %v; want a timestamped START", first.Payload, err)
	}
	d.WriteLog(id, docker.Frame{Stream: docker.Stderr, Payload: []byte("later\n")})
	d.Exit(id, 0)
	stdout, stderr := readAll(t, logs)
	if stdout != "" || !strings.HasSuffix(stderr, " later\n") {
		t.Errorf("rest of the follow = %q / %q, want only the later stderr line", stdout, stderr)
	}
}

func TestContainerLogsSinceSkipsOlderOutput(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	ctx := t.Context()
	id := create(t, c, "fn", docker.Config{}, docker.HostConfig{})
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatal(err)
	}
	d.WriteLog(id, docker.Frame{Stream: docker.Stdout, Payload: []byte("old\n")})
	logs, err := c.ContainerLogs(ctx, id, docker.LogsOptions{Timestamps: true})
	if err != nil {
		t.Fatal(err)
	}
	stamped, _ := readAll(t, logs)
	stamp, _, _ := strings.Cut(stamped, " ")
	cut, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		t.Fatal(err)
	}
	d.WriteLog(id, docker.Frame{Stream: docker.Stdout, Payload: []byte("new\n")})
	logs, err = c.ContainerLogs(ctx, id, docker.LogsOptions{Since: cut.Add(time.Nanosecond)})
	if err != nil {
		t.Fatal(err)
	}
	if stdout, _ := readAll(t, logs); stdout != "new\n" {
		t.Errorf("logs since = %q, want only new", stdout)
	}
}

func TestContainerLogsOfATtyContainerAreUnframed(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	d.SetProgram(providedImage, dockertest.Exit{Output: []docker.Frame{{Stream: docker.Stdout, Payload: []byte("raw text\n")}}})
	id := create(t, c, "tty", docker.Config{Tty: true}, docker.HostConfig{})
	if err := c.ContainerStart(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	logs, err := c.ContainerLogs(t.Context(), id, docker.LogsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if stdout, _ := readAll(t, logs); stdout != "raw text\n" {
		t.Errorf("tty logs = %q", stdout)
	}
}

// Below API 1.42 Docker labels every logs response raw-stream, framed or
// not, and Podman declares no content type at any version, so the
// container's Tty setting decides.
func TestContainerLogsWithoutATrustworthyContentTypeFollowTheContainersTty(t *testing.T) {
	daemons := []struct {
		name string
		opts dockertest.Options
	}{
		{name: "docker 1.41", opts: dockertest.Options{APIVersion: "1.41"}},
		{name: "podman 1.44", opts: dockertest.Options{APIVersion: "1.44", Engine: docker.Podman{LibpodAPIVersion: "5.0.0"}}},
	}
	cases := []struct {
		name               string
		tty                bool
		wantOut, wantError string
	}{
		{name: "framed", wantOut: "out\n", wantError: "err\n"},
		{name: "tty", tty: true, wantOut: "out\nerr\n"},
	}
	for _, daemon := range daemons {
		t.Run(daemon.name, func(t *testing.T) {
			d, c := daemonWithImage(t, daemon.opts)
			d.SetProgram(providedImage, dockertest.Exit{Output: []docker.Frame{
				{Stream: docker.Stdout, Payload: []byte("out\n")},
				{Stream: docker.Stderr, Payload: []byte("err\n")},
			}})
			for _, tc := range cases {
				id := create(t, c, tc.name, docker.Config{Tty: tc.tty}, docker.HostConfig{})
				if err := c.ContainerStart(t.Context(), id); err != nil {
					t.Fatal(err)
				}
				logs, err := c.ContainerLogs(t.Context(), id, docker.LogsOptions{})
				if err != nil {
					t.Fatalf("%s: ContainerLogs: %v", tc.name, err)
				}
				if stdout, stderr := readAll(t, logs); stdout != tc.wantOut || stderr != tc.wantError {
					t.Errorf("%s: logs = %q / %q, want %q / %q", tc.name, stdout, stderr, tc.wantOut, tc.wantError)
				}
			}
		})
	}
}

func TestContainerListSelectsOnEveryLabel(t *testing.T) {
	_, c := daemonWithImage(t, dockertest.Options{})
	ours := docker.Labels{Ownership: docker.Ownership{Instance: "i1", Owner: "o1"}}
	theirs := docker.Labels{Ownership: docker.Ownership{Instance: "i2", Owner: "o2"}}
	a := create(t, c, "ours", docker.Config{Labels: labelMap(t, ours)}, docker.HostConfig{})
	create(t, c, "theirs", docker.Config{Labels: labelMap(t, theirs)}, docker.HostConfig{})
	create(t, c, "unmanaged", docker.Config{}, docker.HostConfig{})
	got, err := c.ContainerList(t.Context(), map[string]string{docker.LabelManaged: "true", docker.LabelOwner: "o1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != a || got[0].Names[0] != "/ours" {
		t.Errorf("ContainerList = %+v, want only ours", got)
	}
	all, err := c.ContainerList(t.Context(), nil)
	if err != nil || len(all) != 3 {
		t.Errorf("ContainerList(nil) = %d containers, %v; want all 3 including stopped ones", len(all), err)
	}
}

func TestContainerArchiveUploadDeliversTheTar(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	id := create(t, c, "fn", docker.Config{}, docker.HostConfig{})
	if err := c.ContainerArchiveUpload(t.Context(), id, "/var/task", strings.NewReader("tar bytes")); err != nil {
		t.Fatalf("ContainerArchiveUpload: %v", err)
	}
	if up := d.Uploads(id); len(up) != 1 || up[0].Path != "/var/task" || string(up[0].Data) != "tar bytes" {
		t.Errorf("uploads = %+v", up)
	}
	if ct := lastRequest(d, http.MethodPut); ct.Path != "/v1.44/containers/"+id+"/archive" {
		t.Errorf("upload path = %s", ct.Path)
	}
}

// TestContainerPublishesAPortOnLoopback: a port exposed and bound to
// 127.0.0.1 with host port 0 gets an ephemeral host port when the container
// starts, which inspect reports (containers.md 3.2, ExposedPorts +
// PortBindings).
func TestContainerPublishesAPortOnLoopback(t *testing.T) {
	_, c := daemonWithImage(t, dockertest.Options{})
	ctx := t.Context()
	id := create(t, c, "published",
		docker.Config{ExposedPorts: docker.PortSet{"8000/tcp": {}}},
		docker.HostConfig{PortBindings: docker.PortMap{"8000/tcp": {{HostIP: "127.0.0.1", HostPort: "0"}}}})
	created, err := c.ContainerInspect(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := created.NetworkSettings.HostPort("8000/tcp"); ok {
		t.Errorf("HostPort before start = %v, want none: the daemon binds on start", got)
	}
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatal(err)
	}
	running, err := c.ContainerInspect(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := running.NetworkSettings.HostPort("8000/tcp")
	if !ok || got.Addr() != netip.MustParseAddr("127.0.0.1") || got.Port() == 0 {
		t.Errorf("HostPort = %v, %t; want 127.0.0.1 and an ephemeral port", got, ok)
	}
	if _, ok := running.NetworkSettings.HostPort("9000/tcp"); ok {
		t.Error("HostPort reports a port that was never published")
	}
}

func TestContainerCreateOnPodmanRefusesSeveralEndpoints(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{APIVersion: "1.44", Engine: docker.Podman{LibpodAPIVersion: "5.4.0"}})
	net := docker.NetworkingConfig{EndpointsConfig: map[string]docker.EndpointSettings{"a": {}, "b": {}}}
	_, err := c.ContainerCreate(t.Context(), "x1", docker.Config{Image: providedImage}, docker.HostConfig{}, net)
	if lerr, ok := errors.AsType[*docker.EngineLimitError](err); !ok || lerr.Op != "ContainerCreate" {
		t.Fatalf("ContainerCreate on Podman with two endpoints = %v, want *EngineLimitError", err)
	}
	if r := lastRequest(d, http.MethodPost); r.Path != "" {
		t.Errorf("a create request reached the daemon: %+v", r)
	}
}
