package kinesis

import (
	"context"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/shardlog"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TimerRetention fires when the oldest record of a stream outlives the
// stream's retention period (contract 3.4). The key's ID is the stream's
// ARN.
const TimerRetention kernel.TimerKind = "kinesis.retention"

// retention is the stream's retention period.
func (s Stream) retention() time.Duration { return time.Duration(s.RetentionHours) * time.Hour }

// readAll reads a shard's untrimmed records from p, all of them.
func readAll(log *shardlog.Shard[Record], p shardlog.Position) ([]shardlog.Entry[Record], error) {
	es, _, err := log.Read(p, math.MaxInt, math.MaxInt, Record.bytes)
	return es, err
}

// oldest returns the shard's oldest untrimmed record; false when it holds
// none.
func oldest(log *shardlog.Shard[Record]) (shardlog.Entry[Record], bool) {
	es, _, err := log.Read(shardlog.TrimHorizon{}, 1, math.MaxInt, Record.bytes)
	if err != nil || len(es) == 0 {
		return shardlog.Entry[Record]{}, false
	}
	return es[0], true
}

// expire trims every record of the stream that arrived at t with
// now >= t + retention ("a record is gone when arrival plus retention is
// not after now", aws-doc kinesis.md 13.2 kinesis.retention.trim, 5.7) and
// returns the blob references the trimmed records held.
func expire(str Stream, now clock.Instant) []blob.Ref {
	through := now.Add(-str.retention())
	var released []blob.Ref
	for _, sh := range str.Shards {
		if first, ok := oldest(sh.log); !ok || first.Arrival.Compare(through) > 0 {
			continue
		}
		es, err := readAll(sh.log, shardlog.TrimHorizon{})
		if err != nil {
			// A read from TrimHorizon is never trimmed.
			continue
		}
		for _, e := range es {
			if e.Arrival.Compare(through) > 0 {
				break
			}
			released = append(released, e.Value.Data)
		}
		sh.log.Trim(through)
	}
	return released
}

// nextExpiry is when the stream's oldest record expires; false when it
// holds none.
func nextExpiry(str Stream) (clock.Instant, bool) {
	var (
		due   clock.Instant
		found bool
	)
	for _, sh := range str.Shards {
		first, ok := oldest(sh.log)
		if !ok {
			continue
		}
		if at := first.Arrival.Add(str.retention()); !found || at.Before(due) {
			due, found = at, true
		}
	}
	return due, found
}

// armRetention arms the stream's retention timer at its next expiry, or
// disarms it when the stream holds no record.
func (s *Service) armRetention(tx *kernel.Tx, scope aws.Scope, str Stream) {
	id := streamARN(scope, str.Name).String()
	if due, ok := nextExpiry(str); ok {
		tx.Arm(s.retention, id, due)
		return
	}
	tx.Disarm(s.retention, id)
}

// trimStream trims the named stream at the write's instant, as every call
// that reads a shard does first (contract 3.4), and rearms its retention
// timer. A stream that is gone is left alone: the caller's own lookup
// answers for it.
func (s *Service) trimStream(ctx context.Context, c *kernel.Call, scope aws.Scope, name string) *awserr.Error {
	if _, ok := s.cells.Lookup(scope); !ok {
		return nil
	}
	released, err := kernel.Write(c, s.cells, scope, func(st *State, now clock.Instant, tx *kernel.Tx) ([]blob.Ref, *awserr.Error) {
		str, ok := st.Streams[name]
		if !ok {
			tx.Unchanged()
			return nil, nil
		}
		released := expire(str, now)
		if len(released) == 0 {
			tx.Unchanged()
		}
		s.armRetention(tx, scope, str)
		return released, nil
	})
	s.release(ctx, released)
	return err
}

// onRetention trims the stream the timer names. A stream deleted since is
// left alone; one changed since is trimmed at the due instant and rearmed.
func (s *Service) onRetention(c *kernel.Call, key clock.TimerKey, _ clock.Instant) {
	a, ok := parseStreamARN(key.ID)
	if !ok {
		return
	}
	scope, err := aws.ScopeOf(a)
	if err != nil {
		return
	}
	name := strings.TrimPrefix(a.Resource, "stream/")
	ctx := context.Background()
	if aerr := s.trimStream(ctx, c, scope, name); aerr != nil {
		s.k.Log().LogAttrs(ctx, slog.LevelError, "kinesis retention trim failed; the stream's expired records stay until the next call that reads it, which trims them first",
			slog.String("stream", key.ID), slog.String("error", aerr.Message))
	}
}

// release drops blob references the state no longer holds. A failure is
// logged: the blob then outlives its record until the next restart prunes
// it.
func (s *Service) release(ctx context.Context, refs []blob.Ref) {
	for _, r := range refs {
		if err := s.blobs.Release(r); err != nil {
			s.k.Log().LogAttrs(ctx, slog.LevelWarn, "releasing a Kinesis record's data failed; the bytes stay in the blob store until the next restart prunes them",
				slog.String("blob", r.String()), slog.String("error", err.Error()))
		}
	}
}

// Rearm arms the retention timer of every stream that holds a record,
// under the cell lock as kernel.Write arms, so a concurrent write either
// commits first and is read here or commits after and rearms itself.
func (s *Service) Rearm(clock.Instant) {
	sched := s.k.Timers().Scheduler()
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		cell, ok := s.cells.Lookup(scope)
		if !ok {
			continue
		}
		store.Read(cell, func(st *State) struct{} {
			for _, str := range st.Streams {
				if due, ok := nextExpiry(str); ok {
					sched.Arm(clock.TimerKey{Kind: TimerRetention, ID: streamARN(scope, str.Name).String()}, due)
				}
			}
			return struct{}{}
		})
	}
}
