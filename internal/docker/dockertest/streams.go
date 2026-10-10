package dockertest

import (
	"cmp"
	"encoding/binary"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/docker"
)

// MuxFrame encodes one frame of the multiplexed stream format: the stream
// byte, three zero bytes, the payload length as uint32 big endian, then the
// payload. Stream 3 is the daemon's systemerr frame.
func MuxFrame(stream byte, payload []byte) []byte {
	out := make([]byte, 8, 8+len(payload))
	out[0] = stream
	binary.BigEndian.PutUint32(out[4:], uint32(len(payload))) //nolint:gosec // frames in tests are far below 4 GiB
	return append(out, payload...)
}

// emit records an event and wakes waiters. Callers hold d.mu.
func (d *Daemon) emit(kind, action, id string, attrs map[string]string) {
	at := d.now()
	d.events = append(d.events, docker.Event{
		Type:     kind,
		Action:   action,
		Actor:    docker.Actor{ID: id, Attributes: attrs},
		Scope:    "local",
		Time:     at.Unix(),
		TimeNano: at.UnixNano(),
	})
	d.notify()
}

func parseFilters(r *http.Request) (docker.Filters, error) {
	raw := r.URL.Query().Get("filters")
	if raw == "" {
		return docker.Filters{}, nil
	}
	var f docker.Filters
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return nil, errors.New("invalid filter: " + err.Error())
	}
	return f, nil
}

// labelsMatch reports whether labels satisfies every "key" or "key=value"
// selector.
func labelsMatch(selectors []string, labels map[string]string) bool {
	for _, s := range selectors {
		k, v, hasValue := strings.Cut(s, "=")
		got, ok := labels[k]
		if !ok || hasValue && got != v {
			return false
		}
	}
	return true
}

func anyOrEmpty(values []string, got string) bool {
	return len(values) == 0 || slices.Contains(values, got)
}

func eventMatches(f docker.Filters, e docker.Event) bool {
	if !anyOrEmpty(f["type"], e.Type) || !anyOrEmpty(f["event"], e.Action) {
		return false
	}
	if cs := f["container"]; len(cs) > 0 {
		if e.Type != "container" || !slices.ContainsFunc(cs, func(c string) bool {
			return strings.HasPrefix(e.Actor.ID, c) || e.Actor.Attributes["name"] == c
		}) {
			return false
		}
	}
	return labelsMatch(f["label"], e.Actor.Attributes)
}

func sortedContainers(m map[string]*container) []*container {
	return slices.SortedFunc(maps.Values(m), func(a, b *container) int { return cmp.Compare(a.name, b.name) })
}

func sortedNetworks(m map[string]*network) []*network {
	return slices.SortedFunc(maps.Values(m), func(a, b *network) int { return cmp.Compare(a.name, b.name) })
}

// parseSince reads the "seconds[.nanoseconds]" form of since.
func parseSince(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	secs, frac, _ := strings.Cut(raw, ".")
	s, err := strconv.ParseInt(secs, 10, 64)
	if err != nil {
		return time.Time{}, errors.New("invalid since timestamp " + raw)
	}
	var ns int64
	if frac != "" {
		frac = (frac + "000000000")[:9]
		if ns, err = strconv.ParseInt(frac, 10, 64); err != nil {
			return time.Time{}, errors.New("invalid since timestamp " + raw)
		}
	}
	return time.Unix(s, ns).UTC(), nil
}

func (d *Daemon) eventStream(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	since, err := parseSince(r.URL.Query().Get("since"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Like moby's getEvents, the headers go out before the subscription is
	// taken, so an event emitted just after the client sees them can be
	// missed. Only a since replays past events (moby's loadBufferedEvents).
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if !writeBody(w, nil) {
		return
	}
	next := 0
	if since.IsZero() {
		d.mu.Lock()
		next = len(d.events)
		d.mu.Unlock()
	}
	for {
		d.mu.Lock()
		pending := d.events[next:]
		next = len(d.events)
		changed := d.changed
		d.mu.Unlock()
		for _, e := range pending {
			if !since.IsZero() && e.TimeNano < since.UnixNano() || !eventMatches(f, e) {
				continue
			}
			raw, err := json.Marshal(e)
			if err != nil || !writeBody(w, append(raw, '\n')) {
				return
			}
		}
		if !waitChange(r.Context(), changed) {
			return
		}
	}
}

type waitResult struct {
	StatusCode int        `json:"StatusCode"`
	Error      *waitError `json:"Error"`
}

type waitError struct {
	Message string `json:"Message"`
}

// done reports whether cond holds for c, given the exit count when the wait
// was registered. Callers hold d.mu.
func (c *container) done(cond docker.WaitCondition, exitsAtStart int) bool {
	switch cond {
	case docker.WaitNotRunning:
		return c.removed || c.status != statusRunning
	case docker.WaitNextExit:
		return c.removed || c.exits > exitsAtStart
	case docker.WaitRemoved:
		return c.removed
	}
	return false
}

func (d *Daemon) containerWait(w http.ResponseWriter, r *http.Request) {
	cond := docker.WaitCondition(r.URL.Query().Get("condition"))
	if cond == "" {
		cond = docker.WaitNotRunning
	}
	if !slices.Contains([]docker.WaitCondition{docker.WaitNotRunning, docker.WaitNextExit, docker.WaitRemoved}, cond) {
		writeError(w, http.StatusBadRequest, "invalid condition: "+string(cond))
		return
	}
	var c *container
	var exitsAtStart, failuresAtStart int
	d.withContainer(w, r, func(found *container) {
		c, exitsAtStart, failuresAtStart = found, found.exits, len(found.waitFailures)
		// The headers go out once the waiter is registered, so a client that
		// waits for next-exit before start cannot miss the exit.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	})
	if c == nil || !writeBody(w, nil) {
		return
	}
	for {
		d.mu.Lock()
		finished, code, changed := c.done(cond, exitsAtStart), c.exitCode, d.changed
		var result *waitResult
		switch {
		case len(c.waitFailures) > failuresAtStart:
			result = &waitResult{StatusCode: -1, Error: &waitError{Message: c.waitFailures[failuresAtStart]}}
		case finished:
			result = &waitResult{StatusCode: code}
		}
		d.mu.Unlock()
		if result != nil {
			raw, err := json.Marshal(result)
			if err == nil {
				writeBody(w, append(raw, '\n'))
			}
			return
		}
		if !waitChange(r.Context(), changed) {
			return
		}
	}
}

type logsQuery struct {
	stdout, stderr     bool
	follow, timestamps bool
	since              time.Time
}

func parseLogsQuery(r *http.Request) (logsQuery, error) {
	q := r.URL.Query()
	on := func(k string) bool { return q.Get(k) == "1" || q.Get(k) == "true" }
	since, err := parseSince(q.Get("since"))
	if err != nil {
		return logsQuery{}, err
	}
	lq := logsQuery{stdout: on("stdout"), stderr: on("stderr"), follow: on("follow"), timestamps: on("timestamps"), since: since}
	if !lq.stdout && !lq.stderr {
		return logsQuery{}, errors.New("bad parameters: you must choose at least one stream")
	}
	return lq, nil
}

func (q logsQuery) wants(e logEntry) bool {
	if e.at.Before(q.since) {
		return false
	}
	switch e.stream {
	case docker.Stdout:
		return q.stdout
	case docker.Stderr:
		return q.stderr
	}
	return false
}

func (q logsQuery) encode(e logEntry, tty bool) []byte {
	payload := e.data
	if q.timestamps {
		payload = append([]byte(e.at.Format("2006-01-02T15:04:05.000000000Z07:00")+" "), payload...)
	}
	if tty {
		return payload
	}
	return MuxFrame(byte(e.stream), payload)
}

func (d *Daemon) containerLogs(w http.ResponseWriter, r *http.Request) {
	q, err := parseLogsQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var c *container
	d.withContainer(w, r, func(found *container) {
		if found.host.LogConfig.Type == "none" {
			writeError(w, http.StatusNotImplemented, "configured logging driver does not support reading")
			return
		}
		c = found
		// Like moby, the multiplexed content type is only declared from API
		// 1.42; older requests are labeled raw-stream even when framed.
		// Podman's compat handler declares no content type at all.
		if _, podman := d.opts.Engine.(docker.Podman); !podman {
			contentType := "application/vnd.docker.multiplexed-stream"
			if c.config.Tty || requestedBelow(r, docker.APIVersion{Major: 1, Minor: 42}) {
				contentType = "application/vnd.docker.raw-stream"
			}
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(http.StatusOK)
	})
	if c == nil || !writeBody(w, nil) {
		return
	}
	d.streamLogs(w, r, c, q)
}

func (d *Daemon) streamLogs(w http.ResponseWriter, r *http.Request, c *container, q logsQuery) {
	next := 0
	for {
		d.mu.Lock()
		pending := c.logs[next:]
		next = len(c.logs)
		running, changed, tty := c.status == statusRunning && !c.removed, d.changed, c.config.Tty
		d.mu.Unlock()
		for _, e := range pending {
			if q.wants(e) && !writeBody(w, q.encode(e, tty)) {
				return
			}
		}
		if !q.follow || !running {
			return
		}
		if !waitChange(r.Context(), changed) {
			return
		}
	}
}
