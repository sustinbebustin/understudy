package cli_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
)

const fakeImage = "public.ecr.aws/lambda/provided:al2023"

// sweepResult is cleanup's JSON result.
type sweepResult struct {
	Owner      string   `json:"owner"`
	Containers []string `json:"containers"`
	Networks   []string `json:"networks"`
}

// sweep runs cleanup with args in JSON against the fake daemon at socket
// and returns what it removed, its containers sorted.
func sweep(t *testing.T, socket string, args ...string) sweepResult {
	t.Helper()
	r := runCLI(t.Context(), closedEndpoint(t), []string{"UNDERSTUDY_DOCKER=unix://" + socket}, append([]string{"cleanup", "--output", "json"}, args...)...)
	if r.code != admin.ExitOK {
		t.Fatalf("cleanup %q = %d, stderr %s", args, r.code, r.stderr)
	}
	var res sweepResult
	if err := json.Unmarshal([]byte(r.stdout), &res); err != nil {
		t.Fatal(err)
	}
	slices.Sort(res.Containers)
	return res
}

// leftovers creates containers on a fake daemon, as a crashed run leaves
// them.
type leftovers struct {
	t *testing.T
	d *dockertest.Daemon
	c *docker.Client
}

func newLeftovers(t *testing.T) leftovers {
	t.Helper()
	d := dockertest.New(t, dockertest.Options{})
	d.AddImage(fakeImage, docker.Image{})
	return leftovers{t: t, d: d, c: d.Client(t)}
}

// labels is the label set of an object of instance and owner, marked as a
// test harness's when test is set.
func (l leftovers) labels(instance, owner string, test bool) map[string]string {
	l.t.Helper()
	m, err := docker.Labels{Ownership: docker.Ownership{Instance: instance, Owner: owner}, Test: test}.Map()
	if err != nil {
		l.t.Fatal(err)
	}
	return m
}

func (l leftovers) container(name string, labels map[string]string) string {
	l.t.Helper()
	created, err := l.c.ContainerCreate(l.t.Context(), name, docker.Config{Image: fakeImage, Labels: labels}, docker.HostConfig{}, docker.NetworkingConfig{})
	if err != nil {
		l.t.Fatal(err)
	}
	return created.ID
}

func (l leftovers) exists(id string) bool {
	_, ok := l.d.Container(id)
	return ok
}

// cleanup removes the managed objects of the deployment its persistence
// directory names, leaving another owner's alone; --all removes every
// owner's; unmanaged and test harness objects stay under --all. Without a
// persistence directory or --all it refuses, naming --all.
func TestCleanupSweepsOneOwnerUnlessAll(t *testing.T) {
	l := newLeftovers(t)
	dir := t.TempDir()
	refused := runCLI(t.Context(), closedEndpoint(t), []string{"UNDERSTUDY_DOCKER=unix://" + l.d.Socket()}, "cleanup")
	if refused.code != admin.ExitUsage || !strings.Contains(refused.stderr, "--all") {
		t.Errorf("cleanup without persistence = %d, stderr %q; want 2 naming --all", refused.code, refused.stderr)
	}
	owner := sweep(t, l.d.Socket(), "--persist", dir).Owner
	if !strings.HasPrefix(owner, "dir-") {
		t.Fatalf("cleanup --persist reported owner %q, want a dir- owner", owner)
	}
	mine := l.labels("a", owner, false)
	left, other := l.container("left", mine), l.container("other", l.labels("b", "run-b", false))
	kept := []string{l.container("harness", l.labels("c", "run-c", true)), l.container("unmanaged", nil)}
	net, err := l.c.NetworkCreate(t.Context(), "left-net", docker.NetworkOptions{Labels: mine})
	if err != nil {
		t.Fatal(err)
	}

	res := sweep(t, l.d.Socket(), "--persist", dir)
	if !slices.Equal(res.Containers, []string{left}) || !slices.Equal(res.Networks, []string{net}) {
		t.Errorf("cleanup --persist removed %v and %v, want only its owner's %s and %s", res.Containers, res.Networks, left, net)
	}
	if !l.exists(other) {
		t.Errorf("cleanup --persist removed another owner's container %s", other)
	}
	if res := sweep(t, l.d.Socket(), "--all"); !slices.Equal(res.Containers, []string{other}) {
		t.Errorf("cleanup --all removed %v, want the other owner's %s", res.Containers, other)
	}
	for _, id := range kept {
		if !l.exists(id) {
			t.Errorf("container %s was removed; cleanup --all must leave unmanaged and test harness containers", id)
		}
	}
}

// cleanup --persist sweeps the owner serve --persist labels its objects
// with: serve's own start-up sweep removes a leftover labeled with the
// owner cleanup reports. While that serve runs, on whatever port, cleanup
// refuses and removes nothing; once it stops, cleanup sweeps.
func TestCleanupMatchesServeAndRefusesWhileItRuns(t *testing.T) {
	l := newLeftovers(t)
	dir := t.TempDir()
	owner := sweep(t, l.d.Socket(), "--persist", dir).Owner
	crashed := l.container("crashed", l.labels("crashed", owner, false))

	s := serve(t, "--persist", dir, "--docker", "unix://"+l.d.Socket())
	// serve logs its start-up sweep once it removed something.
	changed := s.stderr.changes()
	timeout := time.NewTimer(waitTimeout)
	defer timeout.Stop()
	for !strings.Contains(s.stderr.String(), "removed what an earlier run left behind") {
		select {
		case <-changed:
		case <-timeout.C:
			s.stop()
			t.Fatalf("serve --persist swept nothing in %s, so it labels its objects with an owner other than cleanup's %s:\n%s", waitTimeout, owner, s.stderr)
		}
	}
	if l.exists(crashed) {
		t.Fatalf("serve --persist swept, but not the leftover %s labeled with cleanup's owner %s", crashed, owner)
	}
	later := l.container("later", l.labels("later", owner, false))
	r := runCLI(t.Context(), closedEndpoint(t), []string{"UNDERSTUDY_DOCKER=unix://" + l.d.Socket()}, "cleanup", "--persist", dir)
	if r.code != admin.ExitFailed || !strings.Contains(r.stderr, "serve is running with --persist") || !strings.Contains(r.stderr, "Stop that serve") {
		t.Errorf("cleanup --persist while serve holds it = %d, stderr %q; want 1 naming the running serve and the next step", r.code, r.stderr)
	}
	if !l.exists(later) {
		t.Errorf("cleanup --persist removed %s while serve ran", later)
	}
	if code := s.stop(); code != admin.ExitOK {
		t.Fatalf("serve = %d:\n%s", code, s.stderr)
	}
	if res := sweep(t, l.d.Socket(), "--persist", dir); !slices.Equal(res.Containers, []string{later}) {
		t.Errorf("cleanup --persist after serve stopped removed %v, want %s", res.Containers, later)
	}
}

// doctor reports a reachable Docker and its topology, and fails naming the
// socket when none answers there.
func TestDoctorChecksDocker(t *testing.T) {
	d := dockertest.New(t, dockertest.Options{})
	r := runCLI(t.Context(), closedEndpoint(t), []string{"UNDERSTUDY_DOCKER=unix://" + d.Socket()}, "doctor", "--output", "json")
	var report struct {
		Checks []struct {
			Name, Status, Detail, Fix string
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &report); err != nil {
		t.Fatalf("doctor = %d %q: %v", r.code, r.stdout, err)
	}
	status := map[string]string{}
	for _, c := range report.Checks {
		status[c.Name] = c.Status
	}
	if status["docker"] != "ok" || status["topology"] != "ok" || status["server"] != "warn" {
		t.Errorf("doctor checks %+v, want docker and topology ok and the server warned", report.Checks)
	}
	missing := runCLI(t.Context(), closedEndpoint(t), []string{"UNDERSTUDY_DOCKER=unix://" + t.TempDir() + "/none.sock"}, "doctor")
	if missing.code != admin.ExitFailed || !strings.Contains(missing.stdout, "none.sock") || !strings.Contains(missing.stderr, "docker check failed") {
		t.Errorf("doctor without a socket = %d\n%s\n%s", missing.code, missing.stdout, missing.stderr)
	}
}
