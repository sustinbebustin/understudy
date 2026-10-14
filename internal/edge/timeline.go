package edge

import (
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/clock"
)

// ClockTimeline is the emulator clock's history as the edge saw it: each
// mode in force and the wall time it took effect. A SigV4 presigned URL
// names the wall time it was signed at (X-Amz-Date); the timeline places
// that instant on the emulator timeline, so a URL signed after
// `clock advance 1h` lives its full lifetime, and one signed before an
// advance expires once the clock passes its lifetime, frozen or not.
//
// The clock changes only through the admin API, which the edge serves, so
// the edge reads the mode before every request and as it answers every
// admin call (Options.Verify only). Whichever request sees a new mode
// first, its era begins at the next whole wall second, and every admin
// answer is held until the latest era has begun (settledWriter):
// X-Amz-Date has whole-second precision, so a URL signed before the change
// was asked for names an earlier second, and one signed after the answer
// that second or a later one, and each is placed on its own side of the
// change. Only a URL signed while the call was in flight can be placed on
// the wrong side of it. Handlers of one server share one timeline
// (Options.Timeline), since the admin API is served on the main listener
// and presigned URLs arrive on either.
type ClockTimeline struct {
	mu sync.Mutex
	// eras are in strictly increasing wall order; the first covers every
	// earlier wall time.
	eras []era
}

// era is a clock mode in force from wall time from on.
type era struct {
	from time.Time
	mode clock.Mode
}

const (
	// timelineSpan is how far back eras are kept: the longest presigned
	// lifetime (X-Amz-Expires 604800) plus the skew window. A URL signed
	// earlier has expired in any era, so the oldest era kept stands in.
	timelineSpan = 7*24*time.Hour + 15*time.Minute
	// maxEras bounds the history a script that changes the clock in a
	// tight loop can grow; the oldest eras go first.
	maxEras = 1024
)

// observe records c's mode when it differs from the latest era, as an era
// from the next whole wall second, and returns the wall time the latest
// era begins, which may still be ahead of now. The mode and wall time are
// read under t.mu, so eras follow the clock's own order of changes: a
// request that read a mode before a change cannot append it after the
// change's era. An era seen within the second the latest one begins at
// replaces it, so eras stay in strictly increasing wall order.
func (t *ClockTimeline) observe(c *clock.Clock) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, wall := c.Mode(), c.Wall()
	n := len(t.eras)
	switch from := wall.Truncate(time.Second).Add(time.Second); {
	case n > 0 && t.eras[n-1].mode == m:
		return t.eras[n-1].from
	case n > 0 && !from.After(t.eras[n-1].from):
		t.eras[n-1].mode = m
		return t.eras[n-1].from
	default:
		t.eras = append(t.eras, era{from: from, mode: m})
	}
	drop := 0
	for drop < len(t.eras)-1 && (len(t.eras)-drop > maxEras || t.eras[drop+1].from.Before(wall.Add(-timelineSpan))) {
		drop++
	}
	t.eras = t.eras[drop:]
	t.eras[0].from = time.Time{}
	return t.eras[len(t.eras)-1].from
}

// settledWriter holds an admin answer until the latest era has begun
// (ClockTimeline.observe), whichever request recorded it, so the client
// cannot sign a URL in the wall second a change is placed before.
type settledWriter struct {
	http.ResponseWriter
	// r is the admin request, whose end stops the wait.
	r        *http.Request
	clock    *clock.Clock
	timeline *ClockTimeline
	settled  bool
}

// noEmulatorDeadline stands for "no emulator deadline" in a wall-time
// wait.
const noEmulatorDeadline = 100 * 365 * 24 * time.Hour

func (s *settledWriter) WriteHeader(status int) {
	if !s.settled && status >= http.StatusOK {
		s.settled = true
		if from := s.timeline.observe(s.clock); from.After(s.clock.Wall()) {
			<-s.clock.Until(s.r.Context(), clock.Deadline{Emu: s.clock.Now().Add(noEmulatorDeadline), WallCap: from})
		}
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *settledWriter) Write(b []byte) (int, error) {
	if !s.settled {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the connection's writer, for
// a streamed admin answer.
func (s *settledWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// at is the emulator instant of wall time w: w under the mode in force
// then. With no era seen yet it reads c's mode now.
func (t *ClockTimeline) at(c *clock.Clock, w time.Time) clock.Instant {
	t.mu.Lock()
	m := clock.Mode(nil)
	for _, e := range t.eras {
		if e.from.After(w) {
			break
		}
		m = e.mode
	}
	t.mu.Unlock()
	if m == nil {
		m = c.Mode()
	}
	return emulatorAt(m, w)
}

// emulatorAt is emulator time under m when the wall clock reads w, as the
// clock computes it.
func emulatorAt(m clock.Mode, w time.Time) clock.Instant {
	switch m := m.(type) {
	case clock.Running:
		return clock.FromWire(w.Add(m.Offset))
	case clock.Frozen:
		return m.At
	case clock.Scaled:
		ns := float64(w.Sub(m.AnchorWall)) * m.Factor
		return m.AnchorEmu.Add(time.Duration(max(min(ns, math.MaxInt64), math.MinInt64)))
	}
	return clock.FromWire(w)
}
