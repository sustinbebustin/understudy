package logs

import (
	"context"
	"log/slog"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// retentionDays returns the values PutRetentionPolicy accepts
// (secrets-logs-events-alarms.md 3.2).
func retentionDays() []int32 {
	return []int32{1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653}
}

// nextExpiry is when the group's oldest event expires: its timestamp plus
// the retention period. ok is false when nothing in the group expires.
func nextExpiry(g *Group) (clock.Instant, bool) {
	ret, ok := g.retention()
	if !ok {
		return clock.Instant{}, false
	}
	oldest, ok := g.oldestEvent()
	if !ok {
		return clock.Instant{}, false
	}
	return instantOf(oldest).Add(ret), true
}

// expired reports whether an event of timestamp ts has outlived the
// group's retention at now; never for a group without one.
func expired(g *Group, ts int64, now clock.Instant) bool {
	ret, ok := g.retention()
	return ok && instantOf(ts).Add(ret).Compare(now) <= 0
}

// expire drops the group's events that have outlived its retention at now
// and reports whether any were dropped. Each stream's events are in
// timestamp order, so the expired ones are a prefix.
func expire(g *Group, now clock.Instant) bool {
	dropped := false
	for _, name := range g.streamNames() {
		st := g.Streams[name]
		n, _ := slices.BinarySearchFunc(st.Events, true, func(e Event, _ bool) int {
			if expired(g, e.Timestamp, now) {
				return -1
			}
			return 1
		})
		if n == 0 {
			continue
		}
		st.Events = st.Events[n:]
		g.Streams[name] = st
		dropped = true
	}
	return dropped
}

// armExpiry arms the group's retention timer at its next expiry, or
// disarms it when nothing in the group expires.
func (s *Service) armExpiry(tx *kernel.Tx, ref groupRef, g *Group) {
	id := ref.arn().String()
	if due, ok := nextExpiry(g); ok {
		tx.Arm(s.retention, id, due)
		return
	}
	tx.Disarm(s.retention, id)
}

// onRetention removes the expired events of the group the timer names and
// arms the timer for the next expiry. A group deleted or changed since the
// timer was armed is re-read; nothing expires early.
func (s *Service) onRetention(c *kernel.Call, key clock.TimerKey, _ clock.Instant) {
	ref, ok := groupOfARN(key.ID)
	if !ok {
		return
	}
	if _, ok := s.cells.Lookup(ref.scope); !ok {
		return
	}
	_, err := kernel.Write(c, s.cells, ref.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		g, ok := st.Groups[ref.name]
		if !ok {
			tx.Unchanged()
			return struct{}{}, nil
		}
		g.Streams = maps.Clone(g.Streams)
		if !expire(&g, now) {
			tx.Unchanged()
		}
		st.Groups[ref.name] = g
		s.armExpiry(tx, ref, &g)
		return struct{}{}, nil
	})
	if err != nil {
		s.k.Log().LogAttrs(context.Background(), slog.LevelError, "log retention sweep failed; the group's events stop expiring until the next PutLogEvents, PutRetentionPolicy or DeleteLogStream on the group, or a snapshot load, re-arms its retention timer",
			slog.String("log_group", ref.arn().String()), slog.String("error", err.Message))
	}
}
