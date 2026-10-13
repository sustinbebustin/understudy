package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/sustinbebustin/understudy/internal/docker"
)

// A serve without persistence has no data directory to name what it
// leaves behind, so it names its containers' owner label and its temporary
// directory after its process (runID). A later run on the same machine
// removes them once that process is gone, which covers a run killed before
// its shutdown could. A run with persistence sweeps its own leftovers by
// the data directory's owner instead.

// process identifies one process on a machine: the boot it runs in, its
// PID namespace, its PID there and its start time, which together never
// name two processes.
type process struct {
	// boot is the kernel's boot_id with the dashes removed: 32 hex digits.
	boot  string
	pidNS uint64
	pid   int
	// start is the start time in clock ticks after boot.
	start uint64
}

// runID is the identity of one serve without persistence: its process and
// its random instance, which tells apart servers sharing one process, such
// as tests built on understudytest.
type runID struct {
	proc     process
	instance string
}

// runPrefix starts every runID's text form, the owner label of a run
// without persistence.
const runPrefix = "run-"

// runDirPrefix starts the name of a run's temporary directory, which
// continues with the runID.
const runDirPrefix = "understudy-"

// String is the owner label: run-<boot>-<pidns>-<pid>-<start>-<instance>.
func (r runID) String() string {
	return fmt.Sprintf("%s%s-%d-%d-%d-%s", runPrefix, r.proc.boot, r.proc.pidNS, r.proc.pid, r.proc.start, r.instance)
}

// parseRunID parses String's form; false for anything else, such as the
// random owner of a run on a machine without /proc.
func parseRunID(s string) (runID, bool) {
	rest, ok := strings.CutPrefix(s, runPrefix)
	if !ok {
		return runID{}, false
	}
	parts := strings.Split(rest, "-")
	if len(parts) != 5 || !isBootID(parts[0]) || !isInstance(parts[4]) {
		return runID{}, false
	}
	ns, err1 := strconv.ParseUint(parts[1], 10, 64)
	pid, err2 := strconv.Atoi(parts[2])
	start, err3 := strconv.ParseUint(parts[3], 10, 64)
	if err := errors.Join(err1, err2, err3); err != nil || pid <= 0 {
		return runID{}, false
	}
	r := runID{proc: process{boot: parts[0], pidNS: ns, pid: pid, start: start}, instance: parts[4]}
	// Only the canonical form names a run, so no other text, such as one
	// with leading zeros, maps onto the same identity.
	return r, r.String() == s
}

func isBootID(s string) bool {
	return len(s) == 32 && strings.Trim(s, "0123456789abcdef") == ""
}

// isInstance accepts the instance a serve draws: rand.Text's base32
// alphabet.
func isInstance(s string) bool {
	return s != "" && strings.Trim(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") == ""
}

// procFS reads process identities from a procfs, rooted at /proc in
// production.
type procFS struct{ fsys fs.FS }

// identify reads the identity of the process pid.
func (p procFS) identify(pid int) (process, error) {
	raw, err := fs.ReadFile(p.fsys, "sys/kernel/random/boot_id")
	if err != nil {
		return process{}, fmt.Errorf("reading the boot id: %w", err)
	}
	boot := strings.ReplaceAll(strings.TrimSpace(string(raw)), "-", "")
	if !isBootID(boot) {
		return process{}, fmt.Errorf("the boot id %q is not 32 hex digits", raw)
	}
	link, err := fs.ReadLink(p.fsys, strconv.Itoa(pid)+"/ns/pid")
	if err != nil {
		return process{}, fmt.Errorf("reading the PID namespace of process %d: %w", pid, err)
	}
	inode, ok := strings.CutPrefix(link, "pid:[")
	inode, ok2 := strings.CutSuffix(inode, "]")
	ns, err := strconv.ParseUint(inode, 10, 64)
	if !ok || !ok2 || err != nil {
		return process{}, fmt.Errorf("the PID namespace link %q of process %d is not pid:[<inode>]", link, pid)
	}
	start, err := p.start(pid)
	if err != nil {
		return process{}, err
	}
	return process{boot: boot, pidNS: ns, pid: pid, start: start}, nil
}

// start reads the start time of the process pid: field 22 of its stat
// file, counted after the command name, which may hold spaces and
// parentheses.
func (p procFS) start(pid int) (uint64, error) {
	raw, err := fs.ReadFile(p.fsys, strconv.Itoa(pid)+"/stat")
	if err != nil {
		return 0, fmt.Errorf("reading the start time of process %d: %w", pid, err)
	}
	_, after, ok := strings.CutLast(string(raw), ")")
	if !ok {
		return 0, fmt.Errorf("the stat file of process %d has no command name", pid)
	}
	// Fields 3 onwards follow the command name; starttime is field 22.
	fields := strings.Fields(after)
	const startField = 22 - 3
	if len(fields) <= startField {
		return 0, fmt.Errorf("the stat file of process %d has %d fields after the command name, want more than %d", pid, len(fields), startField)
	}
	start, err := strconv.ParseUint(fields[startField], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("the start time %q of process %d: %w", fields[startField], pid, err)
	}
	return start, nil
}

// runs is this process, the procfs that tells whether another run's
// process is gone, and signal, which tells when the procfs cannot.
type runs struct {
	self process
	proc procFS
	// signal sends signal 0 to a PID of this PID namespace: ESRCH when no
	// process holds it, nil or EPERM when one does, whether or not the
	// procfs shows it.
	signal func(pid int) error
}

// currentRuns identifies this process; false where there is no procfs to
// read, such as macOS, where a run's leftovers are removed only by its own
// shutdown or understudy cleanup.
func currentRuns(fsys fs.FS) (runs, error) {
	p := procFS{fsys: fsys}
	self, err := p.identify(os.Getpid())
	if err != nil {
		return runs{}, err
	}
	return runs{self: self, proc: p, signal: func(pid int) error { return syscall.Kill(pid, 0) }}, nil
}

// gone reports whether the process of r has certainly exited: it ran in
// this boot and PID namespace, and its PID is free or held by a process
// started at another time. A run of another boot or namespace may be live
// on another machine or in another container sharing the daemon or the
// temporary directory, so it is never gone. A procfs mounted with hidepid
// hides another user's live process as if it had exited, so a PID missing
// from it is free only when signal finds no process holding it either.
func (rs runs) gone(r runID) bool {
	if r.proc.boot != rs.self.boot || r.proc.pidNS != rs.self.pidNS || r.proc == rs.self {
		return false
	}
	start, err := rs.proc.start(r.proc.pid)
	switch {
	case err == nil:
		return start != r.proc.start
	case errors.Is(err, fs.ErrNotExist):
		return errors.Is(rs.signal(r.proc.pid), syscall.ESRCH)
	}
	return false
}

// newRun is this serve's identity with instance.
func (rs runs) newRun(instance string) runID { return runID{proc: rs.self, instance: instance} }

// identify reads this process's identity from /proc into w.runs; where
// there is no procfs w.runs stays nil.
func (w *wiring) identify(log *slog.Logger) {
	rs, err := currentRuns(os.DirFS("/proc"))
	if err != nil {
		log.LogAttrs(context.Background(), slog.LevelDebug, "this process has no identity a later run can check, so only its own shutdown or understudy cleanup removes what it leaves behind",
			slog.String("error", err.Error()))
		return
	}
	w.runs = &rs
}

// makeTemp creates the run's temporary directory under the system
// temporary directory: named after the run's identity when it has one, so
// a later run removes it once this process is gone, else a random name.
func (w *wiring) makeTemp(instance string) (string, error) {
	if w.runs == nil {
		dir, err := os.MkdirTemp("", runDirPrefix)
		if err != nil {
			return "", fmt.Errorf("server: creating the run's temporary directory: %w", err)
		}
		return dir, nil
	}
	dir := filepath.Join(os.TempDir(), runDirPrefix+w.runs.newRun(instance).String())
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("server: creating the run's temporary directory: %w", err)
	}
	return dir, nil
}

// sweepRunDirs removes the temporary directories under tmp of runs that
// are gone and belonged to this user, returning how many it removed. A
// symbolic link is never followed or removed.
func sweepRunDirs(tmp string, rs runs) (int, error) {
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return 0, fmt.Errorf("listing %s: %w", tmp, err)
	}
	removed := 0
	var errs []error
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), runDirPrefix)
		if !ok || !e.IsDir() {
			continue
		}
		id, ok := parseRunID(rest)
		if !ok || !rs.gone(id) {
			continue
		}
		path := filepath.Join(tmp, e.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || !ownedByMe(info) {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// ownedByMe reports whether info is of a file this process's user owns.
func ownedByMe(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// sweepGoneRuns removes the containers and networks of runs without
// persistence that are gone, returning what it removed.
func sweepGoneRuns(ctx context.Context, c *docker.Client, rs runs) (docker.Swept, error) {
	var total docker.Swept
	managed := map[string]string{docker.LabelManaged: "true"}
	containers, err := c.ContainerList(ctx, managed)
	if err != nil {
		return total, fmt.Errorf("listing understudy's containers: %w", err)
	}
	networks, err := c.NetworkList(ctx, managed)
	if err != nil {
		return total, fmt.Errorf("listing understudy's networks: %w", err)
	}
	owners := map[string]bool{}
	for _, ct := range containers {
		owners[ct.Labels[docker.LabelOwner]] = true
	}
	for _, n := range networks {
		owners[n.Labels[docker.LabelOwner]] = true
	}
	var errs []error
	for _, owner := range slices.Sorted(maps.Keys(owners)) {
		id, ok := parseRunID(owner)
		if !ok || !rs.gone(id) {
			continue
		}
		// The process is gone, which stands in for the data directory's
		// lock Sweep asks for: no live instance has this owner.
		swept, err := c.Sweep(ctx, docker.Ownership{Owner: owner, Instance: sweepInstance})
		total.Containers = append(total.Containers, swept.Containers...)
		total.Networks = append(total.Networks, swept.Networks...)
		errs = append(errs, err)
	}
	return total, errors.Join(errs...)
}

// sweepGone removes what runs without persistence that are gone left
// behind: their temporary directories and, with Docker, their containers
// and networks.
func (w *wiring) sweepGone(ctx context.Context, log *slog.Logger) {
	if w.runs == nil {
		return
	}
	tmp := os.TempDir()
	dirs, err := sweepRunDirs(tmp, *w.runs)
	if err != nil {
		log.LogAttrs(ctx, slog.LevelWarn, "removing temporary directories that exited runs left behind failed; remove them by hand", slog.String("dir", tmp), slog.String("error", err.Error()))
	}
	if dirs > 0 {
		log.LogAttrs(ctx, slog.LevelInfo, "removed temporary directories that exited runs left behind", slog.Int("directories", dirs), slog.String("dir", tmp))
	}
	if w.host.missing() != "" {
		return
	}
	swept, err := sweepGoneRuns(ctx, w.host.client, *w.runs)
	if err != nil && ctx.Err() == nil {
		log.LogAttrs(ctx, slog.LevelWarn, "sweeping containers that exited runs left behind failed; run understudy cleanup --all to remove them", slog.String("error", err.Error()))
	}
	if n := len(swept.Containers) + len(swept.Networks); n > 0 {
		log.LogAttrs(ctx, slog.LevelInfo, "removed what exited runs left behind",
			slog.Int("containers", len(swept.Containers)), slog.Int("networks", len(swept.Networks)))
	}
}
