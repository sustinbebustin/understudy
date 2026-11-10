package shardlog_test

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// gapSequencer numbers records three apart from a start of 7, so that
// positions can fall between records and numbers cross digit lengths
// (dynamodb-streams.md 4.3: numbers are not contiguous and vary in length).
// It keeps the shard's properties independent of DecimalCounter.
type gapSequencer struct{}

func (gapSequencer) Start(shardlog.ShardInfo) shardlog.Seq { return "7" }

func (gapSequencer) Next(_ shardlog.ShardInfo, prev shardlog.Seq, _ clock.Instant) shardlog.Seq {
	if prev == "" {
		prev = "7"
	}
	n, _ := new(big.Int).SetString(string(prev), 10)
	return shardlog.Seq(n.Add(n, big.NewInt(3)).String())
}

// item is a record value: the item key it belongs to, its write number and
// its size for maxBytes.
type item struct {
	Key  string
	N    int
	Size int
}

func sizeOf(v item) int { return v.Size }

// model is the reference list a shard must agree with.
type model struct {
	entries []shardlog.Entry[item] // untrimmed, in append order
	start   shardlog.Seq
	last    shardlog.Seq // "" when nothing was appended
	trimmed shardlog.Seq // highest trimmed, "" when none
	closed  bool
	end     shardlog.Seq
}

func seqNum(s shardlog.Seq) *big.Int {
	n, _ := new(big.Int).SetString(string(s), 10)
	return n
}

func cmpSeq(a, b shardlog.Seq) int { return seqNum(a).Cmp(seqNum(b)) }

// resolve is Shard.Resolve over the model.
func (m *model) resolve(p shardlog.Position) shardlog.Position {
	switch p := p.(type) {
	case shardlog.Latest:
		if m.last == "" {
			return shardlog.TrimHorizon{}
		}
		return shardlog.After{Seq: m.last}
	case shardlog.AtTime:
		if len(m.entries) > 0 && p.At.Compare(m.entries[0].Arrival) <= 0 {
			return shardlog.TrimHorizon{}
		}
		for _, e := range m.entries {
			if e.Arrival.Compare(p.At) >= 0 {
				return shardlog.At{Seq: e.Seq}
			}
		}
		return m.resolve(shardlog.Latest{})
	case shardlog.TrimHorizon, shardlog.At, shardlog.After:
	}
	return p
}

// read is Shard.Read over the model; trimmed reports a
// *TrimmedError.
func (m *model) read(from shardlog.Position, limit, maxBytes int) (out []shardlog.Entry[item], next shardlog.Position, trimmed bool) {
	p := m.resolve(from)
	if limit < 1 || maxBytes < 1 {
		return nil, p, false
	}
	var rest []shardlog.Entry[item]
	switch p := p.(type) {
	case shardlog.TrimHorizon:
		rest = m.entries
	case shardlog.At:
		if m.trimmed != "" && cmpSeq(p.Seq, m.trimmed) <= 0 {
			return nil, nil, true
		}
		for i, e := range m.entries {
			if cmpSeq(e.Seq, p.Seq) >= 0 {
				rest = m.entries[i:]
				break
			}
		}
	case shardlog.After:
		if m.trimmed != "" && cmpSeq(p.Seq, m.trimmed) < 0 {
			return nil, nil, true
		}
		for i, e := range m.entries {
			if cmpSeq(e.Seq, p.Seq) > 0 {
				rest = m.entries[i:]
				break
			}
		}
	case shardlog.Latest, shardlog.AtTime:
		// resolve never returns these.
	}
	sum := 0
	for _, e := range rest {
		if len(out) == limit || (len(out) > 0 && sum+e.Value.Size > maxBytes) {
			break
		}
		sum += e.Value.Size
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, p, false
	}
	return out, shardlog.After{Seq: out[len(out)-1].Seq}, false
}

func (m *model) rangeOf() shardlog.SeqRange {
	if m.closed {
		return shardlog.Closed{Start: m.start, End: m.end}
	}
	return shardlog.Open{Start: m.start}
}

var epoch = clock.FromWire(time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC))

func samePos(a, b shardlog.Position) bool {
	if a, ok := a.(shardlog.AtTime); ok {
		b, ok := b.(shardlog.AtTime)
		return ok && a.At.Compare(b.At) == 0
	}
	return a == b
}

func sameEntries(a, b []shardlog.Entry[item]) bool {
	return slices.EqualFunc(a, b, func(x, y shardlog.Entry[item]) bool {
		return x.Seq == y.Seq && x.Arrival.Compare(y.Arrival) == 0 && x.Value == y.Value
	})
}

// positionGen draws a position anywhere around the shard's numbers and
// arrivals, including before, between and past the records.
func positionGen(m *model, now clock.Instant) *rapid.Generator[shardlog.Position] {
	return rapid.Custom(func(t *rapid.T) shardlog.Position {
		seq := func() shardlog.Seq {
			hi := new(big.Int).Add(seqNum(m.start), big.NewInt(3*int64(len(m.entries))+30))
			if m.last != "" {
				hi.Add(seqNum(m.last), big.NewInt(6))
			}
			n := rapid.Int64Range(0, hi.Int64()).Draw(t, "seq")
			return shardlog.Seq(big.NewInt(n).String())
		}
		switch rapid.IntRange(0, 4).Draw(t, "kind") {
		case 0:
			return shardlog.TrimHorizon{}
		case 1:
			return shardlog.Latest{}
		case 2:
			return shardlog.At{Seq: seq()}
		case 3:
			return shardlog.After{Seq: seq()}
		default:
			span := now.Sub(epoch) + 2*time.Second
			return shardlog.AtTime{At: epoch.Add(time.Duration(rapid.Int64Range(-int64(time.Second), int64(span)).Draw(t, "at")))}
		}
	})
}

// A shard driven by appends, trims, closes and reads agrees with a
// reference list model: Resolve, Read (entries, next position, trimmed
// refusals), Range and Exhausted, under the rules for TRIM_HORIZON, LATEST,
// AT_ and AFTER_SEQUENCE_NUMBER and AT_TIMESTAMP (dynamodb-streams.md 6.3
// and 11.9; kinesis.md 3.5). A shard restored from
// its State agrees with it afterwards.
func TestShardAgreesWithReferenceModel(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		s := shardlog.NewShard[item](shard0, gapSequencer{})
		m := &model{start: gapSequencer{}.Start(shard0)}
		now := epoch
		writes := map[string]int{}
		check := func(t *rapid.T, s *shardlog.Shard[item]) {
			if got, want := s.Range(), m.rangeOf(); got != want {
				t.Fatalf("Range = %#v, want %#v", got, want)
			}
			from := positionGen(m, now).Draw(t, "from")
			if got, want := s.Resolve(from), m.resolve(from); !samePos(got, want) {
				t.Fatalf("Resolve(%#v) = %#v, want %#v", from, got, want)
			}
			limit := rapid.IntRange(0, 8).Draw(t, "limit")
			maxBytes := rapid.IntRange(0, 40).Draw(t, "maxBytes")
			got, next, err := s.Read(from, limit, maxBytes, sizeOf)
			want, wantNext, trimmed := m.read(from, limit, maxBytes)
			if trimmed {
				if _, ok := errors.AsType[*shardlog.TrimmedError](err); !ok {
					t.Fatalf("Read(%#v) error %v, want *TrimmedError (trimmed through %s)", from, err, m.trimmed)
				}
				return
			}
			if err != nil {
				t.Fatalf("Read(%#v, %d, %d): %v", from, limit, maxBytes, err)
			}
			if !sameEntries(got, want) {
				t.Fatalf("Read(%#v, %d, %d) = %v, want %v", from, limit, maxBytes, got, want)
			}
			if !samePos(next, wantNext) {
				t.Fatalf("Read(%#v) next = %#v, want %#v", from, next, wantNext)
			}
			if limit > 0 && maxBytes > 0 {
				rest, _, _ := m.read(from, 1<<30, 1<<30)
				if got, want := s.Exhausted(from), m.closed && len(rest) == 0; got != want {
					t.Fatalf("Exhausted(%#v) = %v, want %v (closed %v, %d records left)", from, got, want, m.closed, len(rest))
				}
			}
		}
		t.Repeat(map[string]func(*rapid.T){
			"append": func(t *rapid.T) {
				now = now.Add(time.Duration(rapid.IntRange(0, 3000).Draw(t, "step")) * time.Millisecond)
				key := rapid.SampledFrom([]string{"a", "b", "c"}).Draw(t, "key")
				writes[key]++
				v := item{Key: key, N: writes[key], Size: rapid.IntRange(1, 12).Draw(t, "size")}
				e, err := s.Append(now, v)
				if m.closed {
					if _, ok := errors.AsType[*shardlog.ClosedError](err); !ok {
						t.Fatalf("Append to a closed shard: %v, want *ClosedError", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("Append: %v", err)
				}
				want := gapSequencer{}.Next(shard0, m.last, now)
				if e.Seq != want || e.Arrival.Compare(now) != 0 || e.Value != v {
					t.Fatalf("Append = %+v, want seq %s at %s value %+v", e, want, now, v)
				}
				m.entries = append(m.entries, e)
				m.last = e.Seq
			},
			"trim": func(t *rapid.T) {
				through := now.Add(-time.Duration(rapid.IntRange(0, 6000).Draw(t, "age")) * time.Millisecond)
				s.Trim(through)
				for len(m.entries) > 0 && m.entries[0].Arrival.Compare(through) <= 0 {
					m.trimmed = m.entries[0].Seq
					m.entries = m.entries[1:]
				}
			},
			"close": func(*rapid.T) {
				s.Close(now)
				if !m.closed {
					m.closed = true
					m.end = m.last
					if m.end == "" {
						m.end = m.start
					}
				}
			},
			"read": func(t *rapid.T) { check(t, s) },
			"restore": func(t *rapid.T) {
				st := s.State()
				r := shardlog.RestoreShard(st, gapSequencer{})
				if r.Info() != s.Info() {
					t.Fatalf("restored Info %+v, want %+v", r.Info(), s.Info())
				}
				if got := r.State(); fmt.Sprint(got) != fmt.Sprint(st) {
					t.Fatalf("restored State %+v, want %+v", got, st)
				}
				s = r
				check(t, s)
			},
		})
	})
}

// Records sharing an item key read back in the order they were written,
// paging from TRIM_HORIZON with any page size (dynamodb-streams.md 7.7,
// aws-doc: "the stream records appear in the same sequence as the actual
// modifications to the item").
func TestShardPerKeyOrderIsAppendOrder(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		s := shardlog.NewShard[item](shard0, shardlog.DecimalCounter{Digits: 21})
		now := epoch
		writes := map[string]int{}
		for range rapid.IntRange(1, 60).Draw(t, "n") {
			key := rapid.SampledFrom([]string{"a", "b", "c", "d"}).Draw(t, "key")
			writes[key]++
			if _, err := s.Append(now, item{Key: key, N: writes[key], Size: 1}); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Duration(rapid.IntRange(0, 2).Draw(t, "step")) * time.Second)
		}
		page := rapid.IntRange(1, 7).Draw(t, "page")
		seen := map[string]int{}
		var prev shardlog.Seq
		from := shardlog.Position(shardlog.TrimHorizon{})
		for {
			got, next, err := s.Read(from, page, 1<<20, sizeOf)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) == 0 {
				break
			}
			for _, e := range got {
				if prev != "" && e.Seq.Compare(prev) != 1 {
					t.Fatalf("sequence %s follows %s", e.Seq, prev)
				}
				prev = e.Seq
				seen[e.Value.Key]++
				if e.Value.N != seen[e.Value.Key] {
					t.Fatalf("key %s: write %d read as its %dth record", e.Value.Key, e.Value.N, seen[e.Value.Key])
				}
			}
			from = next
		}
		for k, n := range writes {
			if seen[k] != n {
				t.Fatalf("key %s: %d writes, %d records read", k, n, seen[k])
			}
		}
	})
}

// streamRetention is DynamoDB Streams' 24-hour lifetime (dynamodb-streams.md
// 5.5, aws-doc), which the owner applies as Trim(now - retention).
const streamRetention = 24 * time.Hour

// Trimming falls exactly at retention on the stepped clock: a record that
// arrived at t is still read at t+24h-1ns and gone at t+24h, where a read
// at its number is a *TrimmedError and TRIM_HORIZON starts after it
// (dynamodb-streams.md 5.5 and 11.1 item 5).
func TestShardTrimsExactlyAtRetention(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		s := shardlog.NewShard[item](shard0, shardlog.DecimalCounter{Digits: 21})
		first := epoch.Add(time.Duration(rapid.Int64Range(0, int64(time.Hour)).Draw(t, "first")))
		gap := time.Duration(rapid.Int64Range(1, int64(time.Hour)).Draw(t, "gap"))
		old, err := s.Append(first, item{Key: "a", N: 1, Size: 1})
		if err != nil {
			t.Fatal(err)
		}
		young, err := s.Append(first.Add(gap), item{Key: "a", N: 2, Size: 1})
		if err != nil {
			t.Fatal(err)
		}
		s.Trim(first.Add(streamRetention - time.Nanosecond).Add(-streamRetention))
		got, _, err := s.Read(shardlog.TrimHorizon{}, 10, 100, sizeOf)
		if err != nil || len(got) != 2 {
			t.Fatalf("1ns before retention: %d records (%v), want 2", len(got), err)
		}
		s.Trim(first.Add(streamRetention).Add(-streamRetention))
		got, _, err = s.Read(shardlog.TrimHorizon{}, 10, 100, sizeOf)
		if err != nil || len(got) != 1 || got[0].Seq != young.Seq {
			t.Fatalf("at retention: %v (%v), want only %s", got, err, young.Seq)
		}
		var te *shardlog.TrimmedError
		if _, _, err := s.Read(shardlog.At{Seq: old.Seq}, 10, 100, sizeOf); !errors.As(err, &te) {
			t.Fatalf("At the trimmed record: %v, want *TrimmedError", err)
		}
		if got, _, err := s.Read(shardlog.After{Seq: old.Seq}, 10, 100, sizeOf); err != nil || len(got) != 1 {
			t.Fatalf("After the trimmed record: %v (%v), want the next record: its next record is not gone", got, err)
		}
	})
}

// A shard that never held a record closes at its start, and reading it is
// exhausted at once (dynamodb-streams.md 5.3: a disabled stream's shard is
// closed).
func TestShardCloseEmpty(t *testing.T) {
	t.Parallel()
	s := shardlog.NewShard[item](shard0, shardlog.DecimalCounter{Digits: 21})
	s.Close(epoch)
	start := shardlog.DecimalCounter{Digits: 21}.Start(shard0)
	if got, want := s.Range(), shardlog.SeqRange(shardlog.Closed{Start: start, End: start}); got != want {
		t.Errorf("Range = %#v, want %#v", got, want)
	}
	if !s.Exhausted(shardlog.TrimHorizon{}) {
		t.Error("an empty closed shard is not exhausted from TRIM_HORIZON")
	}
	var ce *shardlog.ClosedError
	if _, err := s.Append(epoch, item{}); !errors.As(err, &ce) || ce.Error() != "shard "+shard0.ID+" is closed: no record can be appended" {
		t.Errorf("Append after Close: %v, want *ClosedError with its text", err)
	}
}
