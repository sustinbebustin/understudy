package launcher_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/topology"
)

const (
	image    = "public.ecr.aws/lambda/python@sha256:0fddf7542198843bd5a488b17e35473659fdf69f0788c7b3563f0231edf3384d"
	instance = "inst0001"
	owner    = "owner-a"
)

// tmpVolume is /tmp as every function container gets it: an anonymous
// volume, on disk like Lambda's /tmp.
var tmpVolume = docker.Mount{Type: "volume", Target: "/tmp"}

func bridge() topology.Topology {
	return topology.LinuxBridge{Gateway: netip.MustParseAddr("172.17.0.1")}
}

func newLauncher(t *testing.T, d *dockertest.Daemon, top topology.Topology) *launcher.Launcher {
	t.Helper()
	l, err := launcher.New(d.Client(t), launcher.Options{
		Ownership:    docker.Ownership{Instance: instance, Owner: owner},
		Test:         true,
		Topology:     top,
		EndpointPort: 4566,
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func newDaemon(t *testing.T) *dockertest.Daemon {
	t.Helper()
	d := dockertest.New(t, dockertest.Options{})
	d.AddImage(image, docker.Image{Architecture: "amd64", OS: "linux"})
	return d
}

func spec(t *testing.T) launcher.Spec {
	t.Helper()
	account, err := aws.ParseAccountID("123456789012")
	if err != nil {
		t.Fatal(err)
	}
	region, err := aws.KnownRegions().Parse("eu-west-1")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("def handler(e, c): return e\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return launcher.Spec{
		Function:           launcher.Function{Name: "worker", Version: "$LATEST", Account: account, Region: region},
		Generation:         3,
		Image:              image,
		Architecture:       launcher.X86,
		Code:               launcher.ZipCode{Dir: dir, Handler: "handler.handler"},
		MemoryMB:           256,
		EphemeralStorageMB: 1024,
		Environment:        map[string]string{"STAGE": "test", "AWS_REGION": "us-east-1", "AWS_ENDPOINT_URL": "http://override:1"},
		Credentials:        launcher.Credentials{AccessKeyID: "ASIAQAAAAAAAAEXAMPLE", SecretAccessKey: "secret", SessionToken: "token"},
		LogGroup:           "/aws/lambda/worker",
	}
}

func zipDir(t *testing.T, s launcher.Spec) string {
	t.Helper()
	code, ok := s.Code.(launcher.ZipCode)
	if !ok {
		t.Fatalf("spec code = %#v, want ZipCode", s.Code)
	}
	return code.Dir
}

func slot() launcher.Slot {
	return launcher.Slot{
		ID:         "0123456789abcdef0123456789abcdef",
		RuntimeAPI: netip.MustParseAddrPort("172.17.0.1:41234"),
		LogStream:  "2026/10/09/[$LATEST]0123456789abcdef0123456789abcdef",
	}
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestStartCreatesTheContainerSpecOfArchitecture14_3(t *testing.T) {
	d := newDaemon(t)
	l := newLauncher(t, d, bridge())
	s := spec(t)
	c, err := l.Start(t.Context(), s, slot())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, ok := d.Container(c.ID)
	if !ok {
		t.Fatal("container not on the daemon")
	}
	if !got.State.Running {
		t.Errorf("container state = %+v, want running", got.State)
	}
	if c.Name != "understudy-inst0001-worker-0123456789ab" || got.Name != "/"+c.Name {
		t.Errorf("name = %q (daemon %q), want understudy-<instance>-<function>-<slot prefix>", c.Name, got.Name)
	}
	h := got.HostConfig
	wantMount := []docker.Mount{tmpVolume, {Type: "bind", Source: zipDir(t, s), Target: "/var/task", ReadOnly: true}}
	if !slices.Equal(h.Mounts, wantMount) {
		t.Errorf("Mounts = %+v, want /tmp on an anonymous volume and the code tree read-only at /var/task", h.Mounts)
	}
	if h.Memory != 256<<20 || h.MemorySwap != h.Memory {
		t.Errorf("Memory = %d, MemorySwap = %d, want both 256 MB (no swap)", h.Memory, h.MemorySwap)
	}
	if h.PidsLimit != 1024 || !slices.Equal(h.Ulimits, []docker.Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}}) {
		t.Errorf("PidsLimit = %d, Ulimits = %+v, want Lambda's 1024 processes and file descriptors", h.PidsLimit, h.Ulimits)
	}
	if h.LogConfig.Type != "json-file" || h.LogConfig.Config["max-size"] == "" || h.LogConfig.Config["max-file"] == "" {
		t.Errorf("LogConfig = %+v, want json-file with rotation", h.LogConfig)
	}
	if !slices.Equal(h.CapDrop, []string{"ALL"}) || !slices.Equal(h.SecurityOpt, []string{"no-new-privileges"}) {
		t.Errorf("CapDrop = %v, SecurityOpt = %v, want ALL and no-new-privileges", h.CapDrop, h.SecurityOpt)
	}
	if !h.ReadonlyRootfs || len(h.Tmpfs) != 0 {
		t.Errorf("ReadonlyRootfs = %v, Tmpfs = %v, want a read-only root and no tmpfs: tmpfs pages count against the memory limit", h.ReadonlyRootfs, h.Tmpfs)
	}
	if !slices.Equal(h.ExtraHosts, []string{"localhost:host-gateway"}) {
		t.Errorf("ExtraHosts = %v, want the Linux bridge's localhost:host-gateway", h.ExtraHosts)
	}
	if !slices.Equal(got.Config.Cmd, []string{"handler.handler"}) || len(got.Config.Entrypoint) != 0 {
		t.Errorf("Cmd = %v, Entrypoint = %v, want the handler as the one argument of the image's entrypoint", got.Config.Cmd, got.Config.Entrypoint)
	}
	wantLabels := map[string]string{
		docker.LabelManaged: "true", docker.LabelInstance: instance, docker.LabelOwner: owner,
		docker.LabelFunction: "worker", docker.LabelAccount: "123456789012", docker.LabelRegion: "eu-west-1",
		docker.LabelGeneration: "3", docker.LabelTest: "1",
	}
	for k, v := range wantLabels {
		if got.Config.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, got.Config.Labels[k], v)
		}
	}
	env := envMap(got.Config.Env)
	wantEnv := map[string]string{
		"AWS_LAMBDA_RUNTIME_API":          "172.17.0.1:41234",
		"AWS_LAMBDA_FUNCTION_NAME":        "worker",
		"AWS_LAMBDA_FUNCTION_VERSION":     "$LATEST",
		"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": "256",
		"AWS_LAMBDA_INITIALIZATION_TYPE":  "on-demand",
		"AWS_LAMBDA_LOG_GROUP_NAME":       "/aws/lambda/worker",
		"AWS_LAMBDA_LOG_STREAM_NAME":      slot().LogStream,
		"AWS_REGION":                      "eu-west-1", // reserved: the function's value is ignored
		"AWS_DEFAULT_REGION":              "eu-west-1",
		"AWS_ACCESS_KEY_ID":               "ASIAQAAAAAAAAEXAMPLE",
		"AWS_SECRET_ACCESS_KEY":           "secret",
		"AWS_SESSION_TOKEN":               "token",
		"AWS_EC2_METADATA_DISABLED":       "true",
		"TZ":                              ":UTC",              // Lambda's zone, not the base images' :/etc/localtime
		"AWS_ENDPOINT_URL":                "http://override:1", // not reserved: the function's value wins
		"STAGE":                           "test",
	}
	for k, v := range wantEnv {
		if env[k] != v {
			t.Errorf("Env %s = %q, want %q", k, env[k], v)
		}
	}
	platform := ""
	for _, r := range d.Requests() {
		if strings.HasSuffix(r.Path, "/containers/create") {
			platform = r.Query.Get("platform")
		}
	}
	if platform != "linux/amd64" {
		t.Errorf("create platform = %q, want linux/amd64 from Architectures x86_64", platform)
	}
}

func TestStartDefaultsTheEndpointToTheTopologyAddress(t *testing.T) {
	d := newDaemon(t)
	l := newLauncher(t, d, bridge())
	s := spec(t)
	s.Environment = nil
	s.Architecture = launcher.ARM64
	c, err := l.Start(t.Context(), s, slot())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := d.Container(c.ID)
	if env := envMap(got.Config.Env); env["AWS_ENDPOINT_URL"] != "http://172.17.0.1:4566" {
		t.Errorf("AWS_ENDPOINT_URL = %q, want the IPv4 literal of the container listener", env["AWS_ENDPOINT_URL"])
	}
	for _, r := range d.Requests() {
		if strings.HasSuffix(r.Path, "/containers/create") && r.Query.Get("platform") != "linux/arm64" {
			t.Errorf("create platform = %q, want linux/arm64", r.Query.Get("platform"))
		}
	}
}

func TestStartInsideAContainerCopiesTheCodeAndJoinsUnderstudysNetwork(t *testing.T) {
	d := newDaemon(t)
	if _, err := d.Client(t).NetworkCreate(t.Context(), "understudy-net", docker.NetworkOptions{}); err != nil {
		t.Fatal(err)
	}
	l := newLauncher(t, d, topology.InContainer{Network: "understudy-net", Self: netip.MustParseAddr("10.9.0.2")})
	s := spec(t)
	dir := zipDir(t, s)
	if err := os.Mkdir(filepath.Join(dir, "lib"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "util.py"), []byte("X = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := l.Start(t.Context(), s, slot())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := d.Container(c.ID)
	if !slices.Equal(got.HostConfig.Mounts, []docker.Mount{tmpVolume}) || got.HostConfig.ReadonlyRootfs {
		t.Errorf("Mounts = %+v, ReadonlyRootfs = %v; tar copy needs no code mount and a writable root", got.HostConfig.Mounts, got.HostConfig.ReadonlyRootfs)
	}
	if got.HostConfig.NetworkMode != "understudy-net" || len(got.HostConfig.ExtraHosts) != 0 {
		t.Errorf("NetworkMode = %q, ExtraHosts = %v; want understudy's network and no extra hosts", got.HostConfig.NetworkMode, got.HostConfig.ExtraHosts)
	}
	uploads := d.Uploads(c.ID)
	if len(uploads) != 1 || uploads[0].Path != "/var/task" {
		t.Fatalf("uploads = %+v, want one archive at /var/task", uploads)
	}
	files := map[string]int64{}
	tr := tar.NewReader(bytes.NewReader(uploads[0].Data))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Uid != 0 || hdr.Gid != 0 {
			t.Errorf("%s owned by %d:%d, want root", hdr.Name, hdr.Uid, hdr.Gid)
		}
		files[hdr.Name] = hdr.Size
	}
	for _, name := range []string{"handler.py", "lib", "lib/util.py"} {
		if _, ok := files[name]; !ok {
			t.Errorf("archive lacks %s: %v", name, files)
		}
	}
}

// A remote daemon cannot see this host's paths, so the code is copied in,
// and functions join the default bridge with no extra hosts: host-gateway
// would name the daemon's host.
func TestStartOnARemoteDaemonCopiesTheCode(t *testing.T) {
	d := newDaemon(t)
	l := newLauncher(t, d, topology.Remote{Daemon: netip.MustParseAddr("172.30.0.3"), Self: netip.MustParseAddr("172.30.0.2")})
	c, err := l.Start(t.Context(), spec(t), slot())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := d.Container(c.ID)
	if !slices.Equal(got.HostConfig.Mounts, []docker.Mount{tmpVolume}) || len(got.HostConfig.ExtraHosts) != 0 {
		t.Errorf("Mounts = %+v, ExtraHosts = %v; want no code mount and no extra hosts", got.HostConfig.Mounts, got.HostConfig.ExtraHosts)
	}
	if uploads := d.Uploads(c.ID); len(uploads) != 1 || uploads[0].Path != "/var/task" {
		t.Errorf("uploads = %+v, want one archive at /var/task", uploads)
	}
}

func TestStartAppliesImageConfigOverrides(t *testing.T) {
	d := newDaemon(t)
	l := newLauncher(t, d, bridge())
	s := spec(t)
	s.Code = launcher.ImageCode{EntryPoint: []string{"/app/run"}, Command: []string{"app.handler"}, WorkingDirectory: "/app"}
	c, err := l.Start(t.Context(), s, slot())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := d.Container(c.ID)
	if !slices.Equal(got.Config.Entrypoint, []string{"/app/run"}) || !slices.Equal(got.Config.Cmd, []string{"app.handler"}) || got.Config.WorkingDir != "/app" {
		t.Errorf("Entrypoint %v Cmd %v WorkingDir %q, want the ImageConfig overrides", got.Config.Entrypoint, got.Config.Cmd, got.Config.WorkingDir)
	}
	if !slices.Equal(got.HostConfig.Mounts, []docker.Mount{tmpVolume}) || !got.HostConfig.ReadonlyRootfs {
		t.Errorf("image function: Mounts %+v ReadonlyRootfs %v, want only the /tmp volume and a read-only root", got.HostConfig.Mounts, got.HostConfig.ReadonlyRootfs)
	}
}

func TestStartRemovesTheContainerWhenStartFails(t *testing.T) {
	d := newDaemon(t)
	l := newLauncher(t, d, bridge())
	d.FailNext("POST /containers/{id}/start", http.StatusInternalServerError, "driver failed programming external connectivity")
	if _, err := l.Start(t.Context(), spec(t), slot()); err == nil || !strings.Contains(err.Error(), "driver failed") {
		t.Fatalf("Start = %v, want the daemon's error", err)
	}
	for _, c := range d.Created() {
		if _, live := d.Container(c.ID); live {
			t.Errorf("container %s left behind after a failed start", c.Name)
		}
	}
}

func TestStartCopiesTheCodeWhenTheDaemonCannotMountIt(t *testing.T) {
	const denied = "Mounts denied: \nThe path /srv/understudy/code is not shared from the host and is not known to Docker.\nYou can configure shared paths from Docker -> Preferences... -> Resources -> File Sharing."
	for _, route := range []string{"POST /containers/create", "POST /containers/{id}/start"} {
		t.Run(route, func(t *testing.T) {
			d := newDaemon(t)
			l := newLauncher(t, d, bridge())
			d.FailNext(route, http.StatusBadRequest, denied)
			s := spec(t)
			c, err := l.Start(t.Context(), s, slot())
			if err != nil {
				t.Fatalf("Start = %v, want a retry with the code copied in", err)
			}
			got, _ := d.Container(c.ID)
			if !slices.Equal(got.HostConfig.Mounts, []docker.Mount{tmpVolume}) || got.HostConfig.ReadonlyRootfs {
				t.Errorf("Mounts = %+v, ReadonlyRootfs = %v; the retry needs no code mount and a writable root", got.HostConfig.Mounts, got.HostConfig.ReadonlyRootfs)
			}
			if uploads := d.Uploads(c.ID); len(uploads) != 1 || uploads[0].Path != "/var/task" {
				t.Errorf("uploads = %+v, want one archive at /var/task", uploads)
			}
			live := 0
			for _, created := range d.Created() {
				if _, ok := d.Container(created.ID); ok {
					live++
				}
			}
			if live != 1 {
				t.Errorf("%d containers live after the retry, want 1: the refused one must be removed", live)
			}

			// The code store does not move, so later starts copy at once.
			s.Function.Name = "other"
			before := len(d.Created())
			c2, err := l.Start(t.Context(), s, slot())
			if err != nil {
				t.Fatal(err)
			}
			if n := len(d.Created()) - before; n != 1 {
				t.Errorf("second Start created %d containers, want 1", n)
			}
			if got, _ := d.Container(c2.ID); !slices.Equal(got.HostConfig.Mounts, []docker.Mount{tmpVolume}) {
				t.Errorf("second Start Mounts = %+v, want tar copy", got.HostConfig.Mounts)
			}
		})
	}
}

func TestStartRefusesAnInvalidSpec(t *testing.T) {
	d := newDaemon(t)
	l := newLauncher(t, d, bridge())
	cases := map[string]func(*launcher.Spec){
		"memory":    func(s *launcher.Spec) { s.MemoryMB = 64 },
		"storage":   func(s *launcher.Spec) { s.EphemeralStorageMB = 20_000 },
		"handler":   func(s *launcher.Spec) { s.Code = launcher.ZipCode{Dir: "/abs", Handler: ""} },
		"relative":  func(s *launcher.Spec) { s.Code = launcher.ZipCode{Dir: "rel", Handler: "h"} },
		"arch":      func(s *launcher.Spec) { s.Architecture = "mips" },
		"image":     func(s *launcher.Spec) { s.Image = "" },
		"code":      func(s *launcher.Spec) { s.Code = nil },
		"identity":  func(s *launcher.Spec) { s.Function.Name = "" },
		"principal": func(s *launcher.Spec) { s.Credentials = launcher.Credentials{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := spec(t)
			mutate(&s)
			var specErr *launcher.SpecError
			if _, err := l.Start(t.Context(), s, slot()); !errors.As(err, &specErr) {
				t.Errorf("Start = %v, want *SpecError", err)
			}
		})
	}
	if len(d.Created()) != 0 {
		t.Errorf("an invalid spec created %d containers", len(d.Created()))
	}
}

func watchExits(t *testing.T, l *launcher.Launcher) <-chan launcher.Exit {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	exits, err := l.Exits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return exits
}

func nextExit(t *testing.T, exits <-chan launcher.Exit) launcher.Exit {
	t.Helper()
	select {
	case e := <-exits:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no exit reported within 5s")
		return launcher.Exit{}
	}
}

func TestExitsReportWhyAContainerStopped(t *testing.T) {
	execFormat := []docker.Frame{{Stream: docker.Stderr, Payload: []byte("exec /lambda-entrypoint.sh: exec format error\n")}}
	cases := []struct {
		name    string
		host    string // the daemon's Info.Architecture; empty for x86_64
		program dockertest.Program
		arch    launcher.Architecture
		want    launcher.ExitCause
	}{
		{"exit code", "", dockertest.Exit{Code: 3}, launcher.X86, launcher.Exited{Code: 3}},
		{"oom", "", dockertest.Exit{Code: 137, OOMKilled: true}, launcher.X86, launcher.OOMKilled{}},
		// cgroup v2 hosts often send no oom event and leave OOMKilled false.
		{"137 without an oom event", "", dockertest.Exit{Code: 137}, launcher.X86, launcher.OOMKilled{}},
		{"126 on another architecture", "", dockertest.Exit{Code: 126}, launcher.ARM64, launcher.ArchMismatch{Declared: launcher.ARM64, Host: launcher.X86}},
		{"126 on the host architecture", "", dockertest.Exit{Code: 126}, launcher.X86, launcher.Exited{Code: 126}},
		{"exec format error", "", dockertest.Exit{Code: 255, Output: execFormat}, launcher.ARM64, launcher.ArchMismatch{Declared: launcher.ARM64, Host: launcher.X86}},
		{"exec format error in another case", "", dockertest.Exit{Code: 255, Output: []docker.Frame{{Stream: docker.Stderr, Payload: []byte("Exec Format Error\n")}}}, launcher.ARM64, launcher.ArchMismatch{Declared: launcher.ARM64, Host: launcher.X86}},
		{"126 on a host Lambda does not run", "s390x", dockertest.Exit{Code: 126}, launcher.ARM64, launcher.Exited{Code: 126}},
		{"exec format error on a host Lambda does not run", "s390x", dockertest.Exit{Code: 255, Output: execFormat}, launcher.ARM64, launcher.Exited{Code: 255}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := dockertest.New(t, dockertest.Options{Info: docker.Info{Architecture: tc.host}})
			d.AddImage(image, docker.Image{Architecture: "amd64", OS: "linux"})
			d.SetProgram(image, tc.program)
			l := newLauncher(t, d, bridge())
			exits := watchExits(t, l)
			s := spec(t)
			s.Architecture = tc.arch
			c, err := l.Start(t.Context(), s, slot())
			if err != nil {
				t.Fatal(err)
			}
			if e := nextExit(t, exits); e.ContainerID != c.ID || e.Cause != tc.want {
				t.Errorf("exit = %+v, want %s with %#v", e, c.ID, tc.want)
			}
		})
	}
}

func TestExitsSkipRemovedContainersAndReplayEarlierExits(t *testing.T) {
	d := newDaemon(t)
	l := newLauncher(t, d, bridge())
	first, err := l.Start(t.Context(), spec(t), slot())
	if err != nil {
		t.Fatal(err)
	}
	// The exit happens before anyone watches: the subscription replays it.
	d.Exit(first.ID, 2)
	exits := watchExits(t, l)
	if e := nextExit(t, exits); e.ContainerID != first.ID || e.Cause != (launcher.Exited{Code: 2}) {
		t.Errorf("exit = %+v, want the earlier exit of %s", e, first.ID)
	}
	second := slot()
	second.ID = "fedcba9876543210fedcba9876543210"
	c, err := l.Start(t.Context(), spec(t), second)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Remove(t.Context(), c.ID); err != nil {
		t.Fatal(err)
	}
	// Exits arrive in event order, so once a later container's exit is
	// reported, the removed container's die has been seen and dropped.
	third := slot()
	third.ID = "0000000000000000ffffffffffffffff"
	later, err := l.Start(t.Context(), spec(t), third)
	if err != nil {
		t.Fatal(err)
	}
	d.Exit(later.ID, 5)
	if e := nextExit(t, exits); e.ContainerID != later.ID {
		t.Errorf("exit %+v reported; want none for the container removed through Remove, then %s's", e, later.ID)
	}
}

// docker kill emits a kill event, so its 137 is not mistaken for the OOM
// kill a 137 without one is.
func TestExitsTellAnOutsideKillFromAnOOMKill(t *testing.T) {
	d := newDaemon(t)
	l := newLauncher(t, d, bridge())
	exits := watchExits(t, l)
	c, err := l.Start(t.Context(), spec(t), slot())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Client(t).ContainerKill(t.Context(), c.ID, "KILL"); err != nil {
		t.Fatal(err)
	}
	if e := nextExit(t, exits); e.ContainerID != c.ID || e.Cause != (launcher.Exited{Code: 137}) {
		t.Errorf("exit = %+v, want Exited 137 for a docker kill", e)
	}
}

func TestExitsFallBackToWaitsWithoutAnEventsStream(t *testing.T) {
	d := newDaemon(t)
	d.FailNext("GET /events", http.StatusInternalServerError, "events are not supported")
	l := newLauncher(t, d, bridge())
	exits := watchExits(t, l)
	c, err := l.Start(t.Context(), spec(t), slot())
	if err != nil {
		t.Fatal(err)
	}
	d.Exit(c.ID, 4)
	if e := nextExit(t, exits); e.ContainerID != c.ID || e.Cause != (launcher.Exited{Code: 4}) {
		t.Errorf("exit = %+v, want Exited 4 through the wait fallback", e)
	}
}

// The events stream can be lost while a Start is between create and
// start. The daemon then reports the created container as not running, which
// must not pass for its exit: the container is waited on once it starts.
func TestExitsFallBackWithoutMistakingACreatedContainerForAnExit(t *testing.T) {
	d := newDaemon(t)
	launched := make(chan *launcher.Launcher, 1)
	watched := make(chan (<-chan launcher.Exit), 1)
	client := proxyBeforeStart(t, d, func() {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		// The stream fails now, while the container exists but has not
		// started: the watch falls back to waits at once.
		d.FailNext("GET /events", http.StatusInternalServerError, "events are not supported")
		exits, err := (<-launched).Exits(ctx)
		if err != nil {
			t.Errorf("Exits: %v", err)
		}
		watched <- exits
	})
	l, err := launcher.New(client, launcher.Options{
		Ownership:    docker.Ownership{Instance: instance, Owner: owner},
		Test:         true,
		Topology:     bridge(),
		EndpointPort: 4566,
	})
	if err != nil {
		t.Fatal(err)
	}
	launched <- l
	c, err := l.Start(t.Context(), spec(t), slot())
	if err != nil {
		t.Fatal(err)
	}
	var exits <-chan launcher.Exit
	select {
	case exits = <-watched:
	default:
		t.Fatal("the proxy never saw the container start")
	}
	d.Exit(c.ID, 4)
	if e := nextExit(t, exits); e.ContainerID != c.ID || e.Cause != (launcher.Exited{Code: 4}) {
		t.Errorf("exit = %+v, want only the running container's own Exited 4", e)
	}
}

// proxyBeforeStart returns a client for d whose requests pass through a
// proxy that runs hook before it forwards the first container start.
func proxyBeforeStart(t *testing.T, d *dockertest.Daemon, hook func()) *docker.Client {
	t.Helper()
	// t.TempDir paths can exceed the 108-byte unix socket path limit.
	dir, err := os.MkdirTemp("", "launcher-proxy-") //nolint:usetesting // see above
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "docker.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	target := &url.URL{Scheme: "http", Host: "docker"}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", d.Socket())
	}}
	proxy.FlushInterval = -1
	var once sync.Once
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start") {
			once.Do(hook)
		}
		proxy.ServeHTTP(w, r)
	})}
	go func() { _ = srv.Serve(ln) }() //nolint:errcheck // Serve returns ErrServerClosed at cleanup
	t.Cleanup(func() { _ = srv.Close() })
	client, err := docker.New(docker.Options{Endpoint: docker.Unix{Path: socket}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func TestExitsHasOneConsumer(t *testing.T) {
	l := newLauncher(t, newDaemon(t), bridge())
	watchExits(t, l)
	var watchErr *launcher.WatchError
	if _, err := l.Exits(t.Context()); !errors.As(err, &watchErr) {
		t.Errorf("second Exits = %v, want *WatchError", err)
	}
}

// Once a watch's channel has closed, the launcher can be watched again:
// a pool whose Run returned can run again.
func TestExitsCanBeWatchedAgainAfterTheWatchEnds(t *testing.T) {
	d := newDaemon(t)
	l := newLauncher(t, d, bridge())
	ctx, cancel := context.WithCancel(t.Context())
	first, err := l.Exits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	for range first {
	}
	second := watchExits(t, l)
	c, err := l.Start(t.Context(), spec(t), slot())
	if err != nil {
		t.Fatal(err)
	}
	d.Exit(c.ID, 5)
	if e := nextExit(t, second); e.ContainerID != c.ID || e.Cause != (launcher.Exited{Code: 5}) {
		t.Errorf("exit = %+v, want Exited 5 on the second watch", e)
	}
}

func TestLogsReassembleTimestampedLinesPerStream(t *testing.T) {
	d := newDaemon(t)
	d.SetProgram(image, dockertest.Exit{Code: 0, Output: []docker.Frame{
		{Stream: docker.Stdout, Payload: []byte("START of a long ")},
		{Stream: docker.Stderr, Payload: []byte("warning\n")},
		{Stream: docker.Stdout, Payload: []byte("line\nsecond\n")},
		{Stream: docker.Stdout, Payload: []byte("no newline")},
	}})
	l := newLauncher(t, d, bridge())
	c, err := l.Start(t.Context(), spec(t), slot())
	if err != nil {
		t.Fatal(err)
	}
	f, err := l.Logs(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []launcher.Line
	for line := range f.Lines() {
		got = append(got, line)
	}
	if err := f.Err(); err != nil {
		t.Fatalf("Err = %v", err)
	}
	want := []struct {
		stream docker.Stream
		text   string
	}{{docker.Stderr, "warning"}, {docker.Stdout, "START of a long line"}, {docker.Stdout, "second"}, {docker.Stdout, "no newline"}}
	if len(got) != len(want) {
		t.Fatalf("lines = %+v, want %d", got, len(want))
	}
	for i, w := range want {
		if got[i].Stream != w.stream || got[i].Text != w.text || got[i].At.IsZero() {
			t.Errorf("line %d = %+v, want %v %q with a timestamp", i, got[i], w.stream, w.text)
		}
	}
	if !got[1].At.Before(got[2].At) {
		t.Errorf("a split line is stamped %v, want its first frame's time, before the next line's %v", got[1].At, got[2].At)
	}
}

// Output with no newline is cut into lines no longer than the largest
// CloudWatch Logs event, losing nothing, and never inside a UTF-8 character.
func TestLogsCapLinesWithoutANewline(t *testing.T) {
	const tail = "tail"
	for _, tc := range []struct {
		name  string
		long  string
		first int // the length of the first line
	}{
		{"ascii", strings.Repeat("x", 70*16<<10), launcher.MaxLineBytes},
		// A 3-byte character takes bytes MaxLineBytes-1 to MaxLineBytes+1, so
		// the cut moves back to its start.
		{"multibyte character across the cut", strings.Repeat("x", launcher.MaxLineBytes-1) + "世" + strings.Repeat("y", 100), launcher.MaxLineBytes - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDaemon(t)
			output := tc.long + tail + "\n"
			var frames []docker.Frame
			for rest := output; rest != ""; {
				n := min(len(rest), 16<<10) // the daemon's frame size
				frames = append(frames, docker.Frame{Stream: docker.Stdout, Payload: []byte(rest[:n])})
				rest = rest[n:]
			}
			d.SetProgram(image, dockertest.Exit{Code: 0, Output: frames})
			l := newLauncher(t, d, bridge())
			c, err := l.Start(t.Context(), spec(t), slot())
			if err != nil {
				t.Fatal(err)
			}
			f, err := l.Logs(t.Context(), c.ID)
			if err != nil {
				t.Fatal(err)
			}
			var got strings.Builder
			var lengths []int
			for line := range f.Lines() {
				if !utf8.ValidString(line.Text) {
					t.Errorf("line %d is not valid UTF-8", len(lengths))
				}
				got.WriteString(line.Text)
				lengths = append(lengths, len(line.Text))
			}
			if err := f.Err(); err != nil {
				t.Fatalf("Err = %v", err)
			}
			want := tc.long + tail
			if wantLengths := []int{tc.first, len(want) - tc.first}; !slices.Equal(lengths, wantLengths) {
				t.Errorf("line lengths = %v, want %v: one line of at most MaxLineBytes, then the rest", lengths, wantLengths)
			}
			if got.String() != want {
				t.Errorf("lines joined differ from the output (%d bytes, want %d)", got.Len(), len(want))
			}
		})
	}
}

func TestEnsureImageUsesALocalImageAndRecordsItsDigest(t *testing.T) {
	d := dockertest.New(t, dockertest.Options{})
	d.AddImage("public.ecr.aws/lambda/provided:al2023", docker.Image{
		Architecture: "amd64",
		RepoDigests:  []string{"public.ecr.aws/lambda/provided@sha256:e10c4765865b384b15eacc855a3f25ac61745d8dd1f510217deecf23b88fdbd5"},
	})
	l := newLauncher(t, d, bridge())
	img, err := l.EnsureImage(t.Context(), "public.ecr.aws/lambda/provided:al2023", launcher.X86, nil)
	if err != nil {
		t.Fatal(err)
	}
	if img.Digest != "sha256:e10c4765865b384b15eacc855a3f25ac61745d8dd1f510217deecf23b88fdbd5" {
		t.Errorf("Digest = %q, want the repository digest", img.Digest)
	}
	for _, r := range d.Requests() {
		if strings.HasSuffix(r.Path, "/images/create") {
			t.Error("a present image was pulled")
		}
	}
}

func TestEnsureImagePullsOnceForConcurrentCallers(t *testing.T) {
	d := dockertest.New(t, dockertest.Options{})
	ref := "public.ecr.aws/lambda/nodejs:22"
	d.AddRemoteImage(ref, dockertest.Remote{Image: docker.Image{Architecture: "arm64"}, Layers: []string{"l1", "l2"}})
	l := newLauncher(t, d, bridge())
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := l.EnsureImage(t.Context(), ref, launcher.ARM64, nil)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	pulls := 0
	for _, r := range d.Requests() {
		if strings.HasSuffix(r.Path, "/images/create") {
			pulls++
			if r.Query.Get("platform") != "linux/arm64" {
				t.Errorf("pull platform = %q, want linux/arm64", r.Query.Get("platform"))
			}
		}
	}
	// Overlapping callers share the pull; a caller arriving after it
	// finished finds the image present. The sharing itself is pinned down
	// deterministically by the flights tests.
	if pulls != 1 {
		t.Errorf("pulls = %d for 8 concurrent callers, want 1", pulls)
	}
}

// An image present only for another architecture is never replaced by a
// pull that could only come from AWS or from nowhere.
func TestEnsureImageNeverPullsOverALocalImageItCannotReplace(t *testing.T) {
	const (
		ecrRef   = "123456789012.dkr.ecr.eu-west-1.amazonaws.com/app:latest"
		localRef = "my-function:dev"
	)
	d := dockertest.New(t, dockertest.Options{})
	d.AddImage(ecrRef, docker.Image{Architecture: "arm64"})
	d.AddImage(localRef, docker.Image{Architecture: "arm64"})
	l := newLauncher(t, d, bridge())

	_, err := l.EnsureImage(t.Context(), ecrRef, launcher.X86, nil)
	if ecr, ok := errors.AsType[*launcher.ECRImageError](err); !ok || ecr.LocalArchitecture != "arm64" || !strings.Contains(ecr.Error(), "only for arm64") {
		t.Errorf("EnsureImage of an ECR image present for arm64 only = %v, want *ECRImageError naming arm64", err)
	}
	_, err = l.EnsureImage(t.Context(), localRef, launcher.X86, nil)
	if arch, ok := errors.AsType[*launcher.ImageArchitectureError](err); !ok || arch.Declared != launcher.X86 || arch.Present != "arm64" {
		t.Errorf("EnsureImage of a locally built arm64 image for x86_64 = %v, want *ImageArchitectureError", err)
	}
	for _, r := range d.Requests() {
		if strings.HasSuffix(r.Path, "/images/create") {
			t.Errorf("pulled %s; want no pull", r.Query.Get("fromImage"))
		}
	}
	if img, err := l.EnsureImage(t.Context(), localRef, launcher.ARM64, nil); err != nil || img.Architecture != launcher.ARM64 {
		t.Errorf("EnsureImage for the image's own architecture = %+v, %v; want it used as is", img, err)
	}
}

func TestEnsureImageFailures(t *testing.T) {
	d := dockertest.New(t, dockertest.Options{})
	d.AddRemoteImage("registry.example/broken:1", dockertest.Remote{Error: "toomanyrequests: rate limit"})
	l := newLauncher(t, d, bridge())
	_, err := l.EnsureImage(t.Context(), "123456789012.dkr.ecr.eu-west-1.amazonaws.com/app:latest", launcher.X86, nil)
	if ecr, ok := errors.AsType[*launcher.ECRImageError](err); !ok || !strings.Contains(ecr.Error(), "ECR is not emulated") {
		t.Errorf("EnsureImage of an absent ECR image = %v, want *ECRImageError", err)
	}
	_, err = l.EnsureImage(t.Context(), "registry.example/broken:1", launcher.X86, nil)
	if pull, ok := errors.AsType[*launcher.PullError](err); !ok || !strings.Contains(pull.Error(), "rate limit") || !strings.Contains(pull.Error(), "registry.example/broken:1") {
		t.Errorf("EnsureImage of a failing pull = %v, want *PullError naming the image and the registry error", err)
	}
	for _, r := range d.Requests() {
		if strings.Contains(r.Query.Get("fromImage"), "amazonaws.com") {
			t.Error("an ECR image was pulled")
		}
	}
}

func TestSweepRemovesEarlierInstancesOfTheOwner(t *testing.T) {
	d := newDaemon(t)
	earlier, err := launcher.New(d.Client(t), launcher.Options{
		Ownership: docker.Ownership{Instance: "crashed1", Owner: owner}, Test: true, Topology: bridge(), EndpointPort: 4566,
	})
	if err != nil {
		t.Fatal(err)
	}
	leftover, err := earlier.Start(t.Context(), spec(t), slot())
	if err != nil {
		t.Fatal(err)
	}
	l := newLauncher(t, d, bridge())
	other := slot()
	other.ID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mine, err := l.Start(t.Context(), spec(t), other)
	if err != nil {
		t.Fatal(err)
	}
	swept, err := l.Sweep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(swept.Containers, []string{leftover.ID}) {
		t.Errorf("swept %v, want only the earlier instance's %s", swept.Containers, leftover.ID)
	}
	if _, ok := d.Container(mine.ID); !ok {
		t.Error("Sweep removed the live instance's container")
	}
}

func TestRuntimeImagesArePinned(t *testing.T) {
	want := []string{"provided.al2023", "python3.13", "python3.12", "nodejs22.x", "nodejs20.x", "java21", "dotnet8", "ruby3.3"}
	for _, runtime := range want {
		img, ok := launcher.ImageForRuntime(runtime)
		if !ok {
			t.Errorf("no pinned image for %s", runtime)
			continue
		}
		digest, found := strings.CutPrefix(img.Digest, "sha256:")
		if !found || len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
			t.Errorf("%s digest %q is not a sha256 digest", runtime, img.Digest)
		}
		if !strings.HasPrefix(img.Repository, "public.ecr.aws/lambda/") || img.Tag == "" {
			t.Errorf("%s = %+v, want an AWS base image with its tag", runtime, img)
		}
		if img.Ref() != img.Repository+"@"+img.Digest {
			t.Errorf("Ref = %q, want repository@digest", img.Ref())
		}
	}
	if len(launcher.RuntimeImages()) != len(want) {
		t.Errorf("table has %d runtimes, want %d", len(launcher.RuntimeImages()), len(want))
	}
	if _, ok := launcher.ImageForRuntime("go1.x"); ok {
		t.Error("a deprecated runtime has an image")
	}
}
