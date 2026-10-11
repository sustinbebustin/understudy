package ddblocal_test

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
)

const (
	hubRef = "amazon/dynamodb-local@" + ddblocal.Digest
	ecrRef = "public.ecr.aws/aws-dynamodb-local/aws-dynamodb-local@" + ddblocal.Digest
)

// daemon is a fake daemon whose bridge is TEST-NET-1, so no address it
// hands out reaches anything on the host.
func daemon(t *testing.T) *dockertest.Daemon {
	t.Helper()
	return dockertest.New(t, dockertest.Options{BridgeSubnet: netip.MustParsePrefix("192.0.2.0/24")})
}

// pulled lists the images the daemon was asked to pull, in order.
func pulled(d *dockertest.Daemon) []string {
	var out []string
	for _, r := range d.Requests() {
		if strings.HasSuffix(r.Path, "/images/create") {
			out = append(out, r.Query.Get("fromImage"))
		}
	}
	return out
}

// errorIs reports whether err's chain holds a T.
func errorIs[T error](err error) bool {
	e, ok := errors.AsType[T](err)
	return ok && error(e) != nil
}

func manager(t *testing.T, d *dockertest.Daemon, cfg ddblocal.Config) *ddblocal.Manager {
	t.Helper()
	if cfg.Docker == nil {
		cfg.Docker = d.Client(t)
	}
	cfg.Labels = docker.Labels{Ownership: docker.Ownership{Instance: "inst1", Owner: "owner1"}, Test: true}
	if cfg.Licence == nil {
		cfg.Licence = ddblocal.Accepted{Digest: ddblocal.Digest}
	}
	m, err := ddblocal.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Stop(t.Context()); err != nil {
			t.Error(err)
		}
	})
	return m
}

// TestStartWithoutLicencePullsNothing: until the licence is accepted for
// the pinned digest, the daemon is never asked for anything (ADR 0014).
func TestStartWithoutLicencePullsNothing(t *testing.T) {
	for _, l := range []ddblocal.Licence{ddblocal.AwaitingLicence{}, ddblocal.Disabled{}, ddblocal.Accepted{Digest: "sha256:0000"}} {
		d := daemon(t)
		m := manager(t, d, ddblocal.Config{Licence: l})
		err := m.Start(t.Context())
		le, ok := errors.AsType[*ddblocal.LicenceError](err)
		if !ok {
			t.Fatalf("Start with %#v = %v, want *LicenceError", l, err)
		}
		if le.Licence != l {
			t.Errorf("LicenceError.Licence = %#v, want %#v", le.Licence, l)
		}
		if reqs := d.Requests(); len(reqs) != 0 {
			t.Errorf("Start with %#v sent %d daemon requests, want none: %v", l, len(reqs), reqs)
		}
		if _, ok := m.Endpoint(); ok {
			t.Errorf("Endpoint after a refused Start reports a running container")
		}
	}
}

// TestStartFallsBackToECRAndRemovesAContainerThatExits: Docker Hub fails,
// the mirror serves the same digest; the container runs understudy's exact
// spec, and one that exits before answering is removed and reported.
func TestStartFallsBackToECRAndRemovesAContainerThatExits(t *testing.T) {
	d := daemon(t)
	d.AddRemoteImage(hubRef, dockertest.Remote{Error: "toomanyrequests: You have reached your pull rate limit"})
	d.AddRemoteImage(ecrRef, dockertest.Remote{Image: docker.Image{ID: ddblocal.Digest}})
	d.SetProgram(ecrRef, dockertest.Exit{Code: 3, Output: []docker.Frame{
		{Stream: docker.Stdout, Payload: []byte("Initializing DynamoDB Local with the following configuration:\n")},
		{Stream: docker.Stderr, Payload: []byte("Error: Unable to access jarfile DynamoDBLocal.jar\n")},
	}})
	m := manager(t, d, ddblocal.Config{Memory: 512 << 20})

	err := m.Start(t.Context())
	exited, ok := errors.AsType[*ddblocal.ExitedError](err)
	if !ok {
		t.Fatalf("Start = %v, want *ExitedError", err)
	}
	if exited.Code != 3 {
		t.Errorf("exit code = %d, want 3", exited.Code)
	}
	for _, want := range []string{"exited with code 3", "the next DynamoDB call starts it again", "Unable to access jarfile DynamoDBLocal.jar"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if got, want := pulled(d), []string{hubRef, ecrRef}; !slices.Equal(got, want) {
		t.Errorf("pulls = %v, want %v", got, want)
	}
	created := d.Created()
	if len(created) != 1 {
		t.Fatalf("created %d containers, want 1", len(created))
	}
	c := created[0]
	if _, ok := d.Container(c.ID); ok {
		t.Error("the exited container was not removed")
	}
	wantArgs := []string{"-jar", "DynamoDBLocal.jar", "-port", "8000", "-disableTelemetry", "-inMemory"}
	if !slices.Equal(c.Config.Cmd, wantArgs) {
		t.Errorf("Cmd = %v, want %v", c.Config.Cmd, wantArgs)
	}
	if !slices.Equal(c.Config.Env, []string{"DDB_LOCAL_TELEMETRY=0"}) {
		t.Errorf("Env = %v, want the literal 0 telemetry opt-out", c.Config.Env)
	}
	for k, v := range map[string]string{docker.LabelManaged: "true", docker.LabelInstance: "inst1", docker.LabelOwner: "owner1", docker.LabelTest: "1"} {
		if c.Config.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, c.Config.Labels[k], v)
		}
	}
	h := c.HostConfig
	if h.Memory != 512<<20 || h.MemorySwap != h.Memory {
		t.Errorf("Memory, MemorySwap = %d, %d; want 512 MiB and no swap", h.Memory, h.MemorySwap)
	}
	if h.NetworkMode != "bridge" || len(h.Mounts) != 0 {
		t.Errorf("NetworkMode = %q, Mounts = %v; want the default bridge and no mounts", h.NetworkMode, h.Mounts)
	}
	if c.Name != "/understudy-dynamodb-local-inst1" {
		t.Errorf("container name = %q", c.Name)
	}
}

// TestStartUsesAPresentImage: a pinned image already present in either
// repository is used without pulling.
func TestStartUsesAPresentImage(t *testing.T) {
	d := daemon(t)
	d.AddImage(ecrRef, docker.Image{ID: ddblocal.Digest})
	d.SetProgram(ecrRef, dockertest.Exit{Code: 1})
	m := manager(t, d, ddblocal.Config{})
	if !errorIs[*ddblocal.ExitedError](m.Start(t.Context())) {
		t.Fatal("Start did not run the present image")
	}
	if got := pulled(d); len(got) != 0 {
		t.Errorf("Start pulled %v although the image is present", got)
	}
	if img := d.Created()[0].Config.Image; img != ecrRef {
		t.Errorf("container image = %q, want the present %q", img, ecrRef)
	}
}

// TestPullFailingEverywhereNamesEachRegistry: the error lists both
// attempts and how to recover.
func TestPullFailingEverywhereNamesEachRegistry(t *testing.T) {
	d := daemon(t)
	m := manager(t, d, ddblocal.Config{})
	err := m.Start(t.Context())
	pe, ok := errors.AsType[*ddblocal.PullError](err)
	if !ok {
		t.Fatalf("Start = %v, want *PullError", err)
	}
	if len(pe.Attempts) != 2 {
		t.Errorf("attempts = %v, want one per registry", pe.Attempts)
	}
	for _, want := range []string{hubRef, ecrRef, "pull " + ddblocal.Reference() + " by hand"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if len(d.Created()) != 0 {
		t.Error("a container was created without an image")
	}
}

// TestPersistentStorageOnUnderstudysNetwork: -dbPath on a bind mount, and
// understudy's own network when it runs in a container.
func TestPersistentStorageOnUnderstudysNetwork(t *testing.T) {
	d := daemon(t)
	d.AddImage(hubRef, docker.Image{ID: ddblocal.Digest})
	d.SetProgram(hubRef, dockertest.Exit{Code: 1})
	if _, err := d.Client(t).NetworkCreate(t.Context(), "understudy-net", docker.NetworkOptions{}); err != nil {
		t.Fatal(err)
	}
	m := manager(t, d, ddblocal.Config{Storage: ddblocal.DBPath{Dir: "/var/lib/understudy/dynamodb"}, Network: ddblocal.Attached{Network: "understudy-net"}})
	if !errorIs[*ddblocal.ExitedError](m.Start(t.Context())) {
		t.Fatal("Start did not run the container")
	}
	c := d.Created()[0]
	if got := c.Config.Cmd[len(c.Config.Cmd)-2:]; !slices.Equal(got, []string{"-dbPath", "/data"}) {
		t.Errorf("storage args = %v, want -dbPath /data", got)
	}
	want := []docker.Mount{{Type: "bind", Source: "/var/lib/understudy/dynamodb", Target: "/data"}}
	if !slices.Equal(c.HostConfig.Mounts, want) {
		t.Errorf("Mounts = %v, want %v", c.HostConfig.Mounts, want)
	}
	if c.HostConfig.NetworkMode != "understudy-net" {
		t.Errorf("NetworkMode = %q, want understudy-net", c.HostConfig.NetworkMode)
	}
}

// TestNotReadyInTime: a container that never answers is removed after
// ReadyTimeout.
func TestNotReadyInTime(t *testing.T) {
	d := daemon(t)
	d.AddImage(hubRef, docker.Image{ID: ddblocal.Digest})
	m := manager(t, d, ddblocal.Config{ReadyTimeout: 300 * time.Millisecond})
	answering(t, m, false)
	err := m.Start(t.Context())
	if !errorIs[*ddblocal.NotReadyError](err) {
		t.Fatalf("Start = %v, want *NotReadyError", err)
	}
	if _, ok := d.Container(d.Created()[0].ID); ok {
		t.Error("the unready container was not removed")
	}
}

func TestNewRefusesAnIncompleteConfig(t *testing.T) {
	d := daemon(t)
	if _, err := ddblocal.New(ddblocal.Config{Labels: docker.Labels{Ownership: docker.Ownership{Instance: "i", Owner: "o"}}}); err == nil {
		t.Error("New without Docker succeeded")
	}
	if _, err := ddblocal.New(ddblocal.Config{Docker: d.Client(t)}); err == nil {
		t.Error("New without an ownership succeeded; the container would escape the sweep")
	}
}
