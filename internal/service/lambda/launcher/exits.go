package launcher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/docker"
)

// Exit is one function container that stopped on its own: the runtime
// process ended, was killed for memory, or could not start.
type Exit struct {
	ContainerID string
	Cause       ExitCause
}

// ExitCause says why a container stopped.
//
//sumtype:decl
type ExitCause interface{ isExitCause() }

// Exited is a process exit with the container's exit code.
type Exited struct{ Code int }

// OOMKilled is a container killed for exceeding its memory limit.
type OOMKilled struct{}

// ArchMismatch is a container whose code was built for another
// architecture: its output says "exec format error", or it exited 126
// while declaring an architecture the host does not run.
type ArchMismatch struct{ Declared, Host Architecture }

func (Exited) isExitCause()       {}
func (OOMKilled) isExitCause()    {}
func (ArchMismatch) isExitCause() {}

const (
	// maxDiagnosisLog bounds how much of an exited container's output is
	// read looking for "exec format error".
	maxDiagnosisLog = 1 << 20
	// sigkillExit is the exit code of a process killed by SIGKILL.
	sigkillExit = 128 + 9
	// Reconnects of the events stream back off from minReconnect, doubling
	// up to maxReconnect; once more than maxBareStreams streams in a row end
	// without delivering an event, the watch falls back to per-container
	// waits.
	minReconnect   = 100 * time.Millisecond
	maxReconnect   = 5 * time.Second
	maxBareStreams = 5
)

// watcher delivers exits to the one consumer of Exits.
type watcher struct {
	out chan Exit
	// ctx is the watch's lifetime: waits, diagnoses and sends end with it.
	ctx context.Context //nolint:containedctx // waits registered later, from Start, must outlive Start's context
	// waitMode: the events stream is unavailable, so every container is
	// waited on individually.
	waitMode bool
	closing  bool
	senders  sync.WaitGroup
}

// Exits reports every container this launcher started that stops on its
// own, until ctx ends; then the channel closes, and Exits may be called
// again. Containers removed through Remove are not reported. Exits come
// from one Docker events subscription filtered by this instance's label,
// replaying the daemon's buffer so no exit before the subscription is
// missed; when the stream fails and cannot be reopened, each container is
// waited on instead (ARCHITECTURE.md 14.2).
func (l *Launcher) Exits(ctx context.Context) (<-chan Exit, error) {
	w := &watcher{out: make(chan Exit, 64), ctx: ctx}
	l.mu.Lock()
	if l.watcher != nil {
		l.mu.Unlock()
		return nil, &WatchError{}
	}
	l.watcher = w
	w.senders.Add(1)
	l.mu.Unlock()
	go func() {
		<-ctx.Done()
		l.mu.Lock()
		w.closing = true
		l.mu.Unlock()
		w.senders.Wait()
		// Free the slot before the close, so a consumer that sees the
		// channel close can watch again at once.
		l.mu.Lock()
		if l.watcher == w {
			l.watcher = nil
		}
		l.mu.Unlock()
		close(w.out)
	}()
	stream, err := l.subscribe(ctx, replayAll())
	if err != nil {
		l.log.WarnContext(ctx, "docker events unavailable; waiting on each Lambda container instead", slog.String("error", err.Error()))
		l.fallBackToWaits(ctx, w)
		w.senders.Done()
		return w.out, nil
	}
	go l.watchEvents(ctx, w, stream)
	return w.out, nil
}

// replayAll is the since of the first subscription: the daemon replays its
// buffered events, and the instance label limits them to this process.
func replayAll() time.Time { return time.Unix(1, 0) }

func (l *Launcher) subscribe(ctx context.Context, since time.Time) (*docker.EventStream, error) {
	return l.client.Events(ctx, docker.EventsOptions{
		Filters: docker.Filters{
			"type":  {"container"},
			"event": {"die", "oom", "kill"},
			"label": docker.LabelFilter(map[string]string{docker.LabelManaged: "true", docker.LabelInstance: l.ownership.Instance}),
		},
		Since: since,
	})
}

type eventKey struct {
	id, action string
	at         int64
}

// dedup drops events a reconnect replays: since is inclusive, so the
// events at the latest instant seen are remembered.
type dedup struct {
	last int64
	seen map[eventKey]bool
}

func (d *dedup) fresh(ev docker.Event) bool {
	key := eventKey{id: ev.Actor.ID, action: ev.Action, at: ev.TimeNano}
	if d.seen[key] {
		return false
	}
	if ev.TimeNano != d.last {
		clear(d.seen)
		d.last = ev.TimeNano
	}
	d.seen[key] = true
	return true
}

// resume is where a reconnect replays from.
func (d *dedup) resume() time.Time {
	if d.last == 0 {
		return replayAll()
	}
	return time.Unix(0, d.last)
}

// signs is what the events stream said about a container before its die.
type signs struct {
	// oom: the kernel reported an out-of-memory kill.
	oom bool
	// killed: someone sent it a signal through the daemon (docker kill,
	// stop or rm -f).
	killed bool
}

func (l *Launcher) watchEvents(ctx context.Context, w *watcher, stream *docker.EventStream) {
	defer w.senders.Done()
	seen := map[string]signs{}
	d := dedup{seen: map[eventKey]bool{}}
	delay, bare, delivered := minReconnect, 0, false
	for {
		ev, err := stream.Next()
		if err != nil {
			_ = stream.Close()
			if delivered {
				delay, bare = minReconnect, 0
			}
			bare++
			if bare > maxBareStreams || !l.backOff(ctx, delay) {
				l.loseStream(ctx, w, err)
				return
			}
			delay = min(2*delay, maxReconnect)
			if stream, err = l.subscribe(ctx, d.resume()); err != nil {
				l.loseStream(ctx, w, err)
				return
			}
			delivered = false
			continue
		}
		delivered = true
		if !d.fresh(ev) {
			continue
		}
		s := seen[ev.Actor.ID]
		switch ev.Action {
		case "oom":
			s.oom = true
			seen[ev.Actor.ID] = s
		case "kill":
			s.killed = true
			seen[ev.Actor.ID] = s
		case "die":
			code, err := strconv.Atoi(ev.Actor.Attributes["exitCode"])
			if err != nil {
				code = -1
			}
			l.report(ctx, w, ev.Actor.ID, code, s)
			delete(seen, ev.Actor.ID)
		}
	}
}

// backOff waits delay on monotonic time before a reconnect. It reports
// false when ctx ended first.
func (l *Launcher) backOff(ctx context.Context, delay time.Duration) bool {
	wait, cancel := context.WithTimeout(ctx, delay)
	defer cancel()
	<-wait.Done()
	return ctx.Err() == nil
}

// loseStream gives up on the events stream: unless the watch is ending,
// every container is waited on from now on.
func (l *Launcher) loseStream(ctx context.Context, w *watcher, err error) {
	if ctx.Err() != nil {
		return
	}
	l.log.WarnContext(ctx, "docker events stream lost; waiting on each Lambda container instead", slog.String("error", err.Error()))
	l.fallBackToWaits(ctx, w)
}

// track registers a container before it starts. Without an events stream
// its wait is registered here, for the next exit, before start.
//
// Which watch waits on a container is decided under l.mu, against the same
// live set fallBackToWaits and markStarted read, so each container gets
// exactly one wait per watch. The wait itself is a daemon round trip and
// is registered outside the lock, so a slow daemon does not serialize
// every Start and Remove behind it.
func (l *Launcher) track(id string, arch Architecture) error {
	l.mu.Lock()
	t := tracked{arch: arch}
	w := l.watcher
	if w != nil && w.reserveWaitsLocked(1) {
		t.waiter = w
	}
	l.live[id] = t
	l.mu.Unlock()
	if t.waiter == nil {
		return nil
	}
	return l.wait(w, id, docker.WaitNextExit)
}

// markStarted records that the container started. A watch that fell back
// to waits between track and the start skipped it, since a not-running
// wait on a created container returns at once; it is waited on now. A
// container removed meanwhile is left alone.
func (l *Launcher) markStarted(id string) error {
	l.mu.Lock()
	t, live := l.live[id]
	if !live {
		l.mu.Unlock()
		return nil
	}
	t.started = true
	w := l.watcher
	waits := w != nil && t.waiter != w && w.reserveWaitsLocked(1)
	if waits {
		t.waiter = w
	}
	l.live[id] = t
	l.mu.Unlock()
	if !waits {
		return nil
	}
	return l.wait(w, id, docker.WaitNotRunning)
}

// fallBackToWaits switches to per-container waits for every started
// container not yet waited on and every later one. Containers created but
// not started are waited on by markStarted.
func (l *Launcher) fallBackToWaits(ctx context.Context, w *watcher) {
	l.mu.Lock()
	w.waitMode = true
	var ids []string
	for id, t := range l.live {
		if t.started && t.waiter != w {
			ids = append(ids, id)
		}
	}
	if w.reserveWaitsLocked(len(ids)) {
		for _, id := range ids {
			t := l.live[id]
			t.waiter = w
			l.live[id] = t
		}
	} else {
		ids = nil
	}
	l.mu.Unlock()
	for _, id := range ids {
		if err := l.wait(w, id, docker.WaitNotRunning); err != nil {
			l.log.WarnContext(ctx, "cannot watch Lambda container for exit", slog.String("container", id), slog.String("error", err.Error()))
		}
	}
}

// reserveWaitsLocked claims n senders for per-container waits when the
// watch is in wait mode and not ending. Claiming under l.mu, where closing
// is set, keeps every senders.Add ahead of the closer's senders.Wait.
// Callers hold l.mu and pass each claimed sender to wait.
func (w *watcher) reserveWaitsLocked(n int) bool {
	if !w.waitMode || w.closing {
		return false
	}
	w.senders.Add(n)
	return true
}

// wait registers a wait on a sender reserveWaitsLocked claimed and reports
// its result; the sender is released when the wait ends or fails.
func (l *Launcher) wait(w *watcher, id string, cond docker.WaitCondition) error {
	wait, err := l.client.ContainerWait(w.ctx, id, cond)
	if err != nil {
		w.senders.Done()
		return err
	}
	go func() {
		defer w.senders.Done()
		code, err := wait.Result()
		if err != nil {
			// The watch ended, which needs no notice, or the daemon failed
			// the wait: a container that exits unseen is still swept, and
			// its next invocation times out.
			if w.ctx.Err() == nil {
				l.log.WarnContext(w.ctx, "lost the wait on a Lambda container", slog.String("container", id), slog.String("error", err.Error()))
			}
			return
		}
		// A wait sees no events, so an outside docker kill is not told
		// apart from the kernel's OOM kill.
		l.report(w.ctx, w, id, code, signs{})
	}()
	return nil
}

// report delivers the exit of a live container once.
func (l *Launcher) report(ctx context.Context, w *watcher, id string, code int, s signs) {
	l.mu.Lock()
	t, live := l.live[id]
	delete(l.live, id)
	l.mu.Unlock()
	if !live {
		return
	}
	exit := Exit{ContainerID: id, Cause: l.diagnose(ctx, id, t.arch, code, s)}
	select {
	case w.out <- exit:
	case <-ctx.Done():
	}
}

// diagnose turns an exit code into a cause: an out-of-memory kill, then
// the architecture mismatch signs, then the plain code.
//
// The oom event and State.OOMKilled are positive OOM signals, but on
// cgroup v2 hosts the daemon often reports neither. So a SIGKILL exit (137)
// nobody sent through the daemon is an OOM kill too: every tracked
// container has a memory limit (Spec requires one), PID 1 cannot be
// SIGKILLed from inside its own PID namespace, understudy's own kills go
// through Remove, which untracks the container first, and docker kill,
// stop and rm -f emit a kill event.
func (l *Launcher) diagnose(ctx context.Context, id string, arch Architecture, code int, s signs) ExitCause {
	if s.oom {
		return OOMKilled{}
	}
	if code == 0 {
		return Exited{Code: code}
	}
	if c, err := l.client.ContainerInspect(ctx, id); err == nil && c.State.OOMKilled {
		return OOMKilled{}
	}
	if code == sigkillExit && !s.killed {
		return OOMKilled{}
	}
	host, known := l.hostArchitecture(ctx)
	if known && (code == 126 && host != arch || l.outputSays(ctx, id, "exec format error")) {
		return ArchMismatch{Declared: arch, Host: host}
	}
	return Exited{Code: code}
}

// hostArchitecture reads the daemon's architecture. known is false when
// the daemon cannot say or runs a machine Lambda does not support; no
// architecture mismatch is claimed then.
func (l *Launcher) hostArchitecture(ctx context.Context) (arch Architecture, known bool) {
	info, err := l.client.Info(ctx)
	if err != nil {
		return "", false
	}
	return machineArchitecture(info.Architecture)
}

// outputSays reports whether the container's output contains text,
// ignoring case. It reads at most maxDiagnosisLog bytes.
func (l *Launcher) outputSays(ctx context.Context, id, text string) bool {
	stream, err := l.client.ContainerLogs(ctx, id, docker.LogsOptions{})
	if err != nil {
		return false
	}
	defer stream.Close()
	needle := bytes.ToLower([]byte(text))
	read := 0
	for read < maxDiagnosisLog {
		f, err := stream.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				l.log.DebugContext(ctx, "reading an exited container's output stopped", slog.String("container", id), slog.String("error", err.Error()))
			}
			return false
		}
		read += len(f.Payload)
		if bytes.Contains(bytes.ToLower(f.Payload), needle) {
			return true
		}
	}
	return false
}
