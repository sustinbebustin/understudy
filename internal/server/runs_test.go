package server

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"testing/fstest"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
)

const (
	testBoot  = "0123456789abcdef0123456789abcdef"
	otherBoot = "fedcba9876543210fedcba9876543210"
	testNS    = 4026531836
)

// fakeProc is a procfs holding the boot id and, per PID, a namespace link
// and a stat file with the given start time.
func fakeProc(starts map[int]uint64) fstest.MapFS {
	fsys := fstest.MapFS{
		"sys/kernel/random/boot_id": {Data: []byte("01234567-89ab-cdef-0123-456789abcdef\n")},
	}
	for pid, start := range starts {
		p := strconv.Itoa(pid)
		fsys[p+"/ns/pid"] = &fstest.MapFile{Data: []byte("pid:[" + strconv.Itoa(testNS) + "]"), Mode: fs.ModeSymlink}
		// The command name holds spaces and parentheses.
		fsys[p+"/stat"] = &fstest.MapFile{Data: []byte(p + " (under (stud) y) S 1 4242 4242 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 9 0 " + strconv.FormatUint(start, 10) + " 1000 200\n")}
	}
	return fsys
}

// testRuns is this process as pid 100 started at 500, with a live sibling
// process 200 started at 700, and another user's live process 400 that the
// procfs hides, as one mounted with hidepid does.
func testRuns(t *testing.T) runs {
	t.Helper()
	starts := map[int]uint64{100: 500, 200: 700}
	p := procFS{fsys: fakeProc(starts)}
	self, err := p.identify(100)
	if err != nil {
		t.Fatal(err)
	}
	signal := func(pid int) error {
		if _, ok := starts[pid]; ok {
			return nil
		}
		if pid == hiddenPID {
			return syscall.EPERM
		}
		return syscall.ESRCH
	}
	return runs{self: self, proc: p, signal: signal}
}

// hiddenPID is the live process of another user testRuns' procfs hides.
const hiddenPID = 400

func TestIdentifyReadsBootNamespaceAndStart(t *testing.T) {
	got := testRuns(t).self
	want := process{boot: testBoot, pidNS: testNS, pid: 100, start: 500}
	if got != want {
		t.Errorf("identify = %+v, want %+v", got, want)
	}
}

// Where there is a procfs this process identifies itself from it, and is
// never gone to itself.
func TestCurrentRunsReadsTheRealProcfs(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skipf("no procfs to read a run's identity from: %v", err)
	}
	rs, err := currentRuns(os.DirFS("/proc"))
	if err != nil {
		t.Fatal(err)
	}
	if rs.self.pid != os.Getpid() || rs.self.start == 0 {
		t.Errorf("self = %+v, want this process with its start time", rs.self)
	}
	if rs.gone(rs.newRun("SELF")) {
		t.Error("this process is gone to itself")
	}
}

// A run's owner label parses back to the same identity; nothing else does.
func TestRunIDRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		id := runID{
			proc: process{
				boot:  rapid.StringMatching(`[0-9a-f]{32}`).Draw(t, "boot"),
				pidNS: rapid.Uint64().Draw(t, "ns"),
				pid:   rapid.IntRange(1, 1<<22).Draw(t, "pid"),
				start: rapid.Uint64().Draw(t, "start"),
			},
			instance: rapid.StringMatching(`[A-Z2-7]{12}`).Draw(t, "instance"),
		}
		got, ok := parseRunID(id.String())
		if !ok || got != id {
			t.Fatalf("parseRunID(%q) = %+v, %v; want %+v", id.String(), got, ok, id)
		}
	})
	for _, s := range []string{
		"run-ABCDEFGHJKLM",
		"dir-0123456789abcdef",
		"run-" + testBoot + "-1-2-3",
		"run-" + testBoot + "-1-02-3-ABC",
		"run-" + testBoot + "-1-0-3-ABC",
		"run-" + testBoot + "-1-2-3-abc",
		"run-" + otherBoot[:31] + "-1-2-3-ABC",
	} {
		if id, ok := parseRunID(s); ok {
			t.Errorf("parseRunID(%q) = %+v, want no run", s, id)
		}
	}
}

// A run is gone when its PID is free or reused, and only when it ran in
// this boot and PID namespace.
func TestGoneOnlyForExitedProcessesHere(t *testing.T) {
	rs := testRuns(t)
	run := func(boot string, ns uint64, pid int, start uint64) runID {
		return runID{proc: process{boot: boot, pidNS: ns, pid: pid, start: start}, instance: "ABC"}
	}
	for _, tc := range []struct {
		name string
		id   runID
		gone bool
	}{
		{"this process", run(testBoot, testNS, 100, 500), false},
		{"a live process", run(testBoot, testNS, 200, 700), false},
		{"a free PID", run(testBoot, testNS, 300, 900), true},
		{"a reused PID", run(testBoot, testNS, 200, 650), true},
		{"a live process the procfs hides", run(testBoot, testNS, hiddenPID, 900), false},
		{"another boot", run(otherBoot, testNS, 300, 900), false},
		{"another namespace", run(testBoot, testNS+1, 300, 900), false},
	} {
		if got := rs.gone(tc.id); got != tc.gone {
			t.Errorf("gone(%s) = %v, want %v", tc.name, got, tc.gone)
		}
	}
}

// The startup sweep removes only the temporary directories of gone runs:
// never a live run's, another program's, or a symbolic link.
func TestSweepRunDirsRemovesOnlyGoneRuns(t *testing.T) {
	rs := testRuns(t)
	tmp := t.TempDir()
	dead := runDirPrefix + runID{proc: process{boot: testBoot, pidNS: testNS, pid: 300, start: 1}, instance: "DEAD"}.String()
	live := runDirPrefix + rs.newRun("LIVE").String()
	sibling := runDirPrefix + runID{proc: process{boot: testBoot, pidNS: testNS, pid: 200, start: 700}, instance: "SIB"}.String()
	for _, d := range []string{dead, live, sibling, "understudy-code-123", "unrelated"} {
		if err := os.MkdirAll(filepath.Join(tmp, d, "blobs"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(tmp, "unrelated")
	link := runDirPrefix + runID{proc: process{boot: testBoot, pidNS: testNS, pid: 301, start: 1}, instance: "LINK"}.String()
	if err := os.Symlink(target, filepath.Join(tmp, link)); err != nil {
		t.Fatal(err)
	}
	removed, err := sweepRunDirs(tmp, rs)
	if err != nil || removed != 1 {
		t.Fatalf("sweepRunDirs = %d, %v; want the one gone run's directory", removed, err)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	left := make([]string, 0, len(entries))
	for _, e := range entries {
		left = append(left, e.Name())
	}
	want := slices.Sorted(slices.Values([]string{link, live, sibling, "understudy-code-123", "unrelated"}))
	if !slices.Equal(left, want) {
		t.Errorf("left = %v, want %v", left, want)
	}
}

// The startup sweep removes the containers and networks of gone runs and
// leaves every other owner's alone.
func TestSweepGoneRunsRemovesOnlyGoneOwners(t *testing.T) {
	rs := testRuns(t)
	d := dockertest.New(t, dockertest.Options{})
	const image = "public.ecr.aws/lambda/provided:al2023"
	d.AddImage(image, docker.Image{})
	c := d.Client(t)
	ctx := t.Context()
	owners := map[string]string{
		"gone":      runID{proc: process{boot: testBoot, pidNS: testNS, pid: 300, start: 1}, instance: "GONE"}.String(),
		"live":      runID{proc: process{boot: testBoot, pidNS: testNS, pid: 200, start: 700}, instance: "LIVE"}.String(),
		"elsewhere": runID{proc: process{boot: otherBoot, pidNS: testNS, pid: 300, start: 1}, instance: "ELSE"}.String(),
		"dir":       "dir-0123456789abcdef",
	}
	containers := map[string]string{}
	networks := map[string]string{}
	for name, owner := range owners {
		labels, err := docker.Labels{Ownership: docker.Ownership{Owner: owner, Instance: "I" + name}}.Map()
		if err != nil {
			t.Fatal(err)
		}
		created, err := c.ContainerCreate(ctx, "c-"+name, docker.Config{Image: image, Labels: labels}, docker.HostConfig{}, docker.NetworkingConfig{})
		if err != nil {
			t.Fatal(err)
		}
		containers[name] = created.ID
		if networks[name], err = c.NetworkCreate(ctx, "n-"+name, docker.NetworkOptions{Labels: labels}); err != nil {
			t.Fatal(err)
		}
	}
	swept, err := sweepGoneRuns(ctx, c, rs)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(swept.Containers, []string{containers["gone"]}) || !slices.Equal(swept.Networks, []string{networks["gone"]}) {
		t.Errorf("swept = %+v, want only the gone run's container %s and network %s", swept, containers["gone"], networks["gone"])
	}
	for name, id := range containers {
		if _, ok := d.Container(id); ok == (name == "gone") {
			t.Errorf("container of %s: present = %v after the sweep", name, ok)
		}
	}
}
