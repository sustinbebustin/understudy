package shardlog_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// A shard's State survives the JSON a snapshot writes: restored from the
// decoded state, the shard holds the same state, reads the same records and
// numbers its next record where the original would (DynamoDB's snapshot
// section persists ShardState).
func TestShardStateJSONRoundTrip(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		s := shardlog.NewShard[item](shard0, shardlog.DecimalCounter{Digits: 21})
		now := epoch
		for i := range rapid.IntRange(0, 30).Draw(t, "ops") {
			now = now.Add(time.Duration(rapid.Int64Range(0, int64(3*time.Second)).Draw(t, "step")))
			switch rapid.IntRange(0, 9).Draw(t, "op") {
			case 0:
				s.Trim(now.Add(-time.Duration(rapid.Int64Range(0, int64(10*time.Second)).Draw(t, "age"))))
			case 1:
				s.Close(now)
			default:
				// A closed shard refuses the append; that is not under test here.
				if _, err := s.Append(now, item{Key: "k", N: i, Size: 1}); err != nil {
					if _, ok := errors.AsType[*shardlog.ClosedError](err); !ok {
						t.Fatalf("Append: %v", err)
					}
				}
			}
		}
		st := s.State()
		b, err := json.Marshal(st)
		if err != nil {
			t.Fatalf("marshal State: %v", err)
		}
		var decoded shardlog.ShardState[item]
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatalf("unmarshal State %s: %v", b, err)
		}
		r := shardlog.RestoreShard(decoded, shardlog.DecimalCounter{Digits: 21})
		if got, want := fmt.Sprint(r.State()), fmt.Sprint(st); got != want {
			t.Fatalf("restored State %s, want %s (json %s)", got, want, b)
		}
		if r.Range() != s.Range() {
			t.Fatalf("restored Range %#v, want %#v", r.Range(), s.Range())
		}
		got, _, err := r.Read(shardlog.TrimHorizon{}, 1<<20, 1<<20, sizeOf)
		if err != nil {
			t.Fatal(err)
		}
		want, _, err := s.Read(shardlog.TrimHorizon{}, 1<<20, 1<<20, sizeOf)
		if err != nil {
			t.Fatal(err)
		}
		if !sameEntries(got, want) {
			t.Fatalf("restored shard reads %v, want %v", got, want)
		}
		now = now.Add(time.Second)
		e1, err1 := s.Append(now, item{Key: "k", Size: 1})
		e2, err2 := r.Append(now, item{Key: "k", Size: 1})
		if (err1 == nil) != (err2 == nil) || e1.Seq != e2.Seq {
			t.Fatalf("next append: original %s (%v), restored %s (%v)", e1.Seq, err1, e2.Seq, err2)
		}
	})
}

// A trimmed read names the shard and the position by the iterator type a
// caller asked for, not by Go syntax.
func TestTrimmedErrorText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		from shardlog.Position
		want string
	}{
		{shardlog.At{Seq: "100000000000000000001"}, "shard " + shard0.ID + ": the records from AT_SEQUENCE_NUMBER 100000000000000000001 on have been trimmed"},
		{shardlog.After{Seq: "7"}, "shard " + shard0.ID + ": the records from AFTER_SEQUENCE_NUMBER 7 on have been trimmed"},
		{shardlog.TrimHorizon{}, "shard " + shard0.ID + ": the records from TRIM_HORIZON on have been trimmed"},
		{shardlog.Latest{}, "shard " + shard0.ID + ": the records from LATEST on have been trimmed"},
		{shardlog.AtTime{At: epoch}, "shard " + shard0.ID + ": the records from AT_TIMESTAMP " + epoch.String() + " on have been trimmed"},
	} {
		if got := (&shardlog.TrimmedError{Shard: shard0.ID, From: tc.from}).Error(); got != tc.want {
			t.Errorf("TrimmedError{%#v} = %q, want %q", tc.from, got, tc.want)
		}
	}
}
