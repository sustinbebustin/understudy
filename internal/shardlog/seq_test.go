package shardlog_test

import (
	"errors"
	"math/big"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// decimal draws a sequence number as decimal digits without a leading zero,
// up to digits long.
func decimal(t *rapid.T, label string, digits int) string {
	n := rapid.IntRange(1, digits).Draw(t, label+"Len")
	if n == 1 {
		return rapid.StringMatching(`[0-9]`).Draw(t, label)
	}
	return rapid.StringMatching(`[1-9][0-9]{`+strconv.Itoa(n-1)+`}`).Draw(t, label)
}

func bigOf(t interface{ Fatalf(string, ...any) }, s shardlog.Seq) *big.Int {
	n, ok := new(big.Int).SetString(string(s), 10)
	if !ok {
		t.Fatalf("sequence number %q is not decimal", s)
	}
	return n
}

// Sequence numbers vary in length (21 to 25 digits in AWS's examples), so
// they compare as integers, never as strings (dynamodb-streams.md 4.3,
// aws-doc). Compare agrees with integer order on every pair.
func TestSeqCompareIsNumericOrder(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		a := decimal(t, "a", 40)
		b := rapid.OneOf(rapid.Just(a), rapid.Custom(func(t *rapid.T) string { return decimal(t, "b", 40) })).Draw(t, "b")
		want := bigOf(t, shardlog.Seq(a)).Cmp(bigOf(t, shardlog.Seq(b)))
		if got := shardlog.Seq(a).Compare(shardlog.Seq(b)); got != want {
			t.Fatalf("Seq(%q).Compare(%q) = %d, want %d", a, b, got, want)
		}
	})
}

// The documented examples disagree in string and numeric order
// (dynamodb-streams.md 4.3: 820400000000000001192334 is a child of a shard
// whose numbers are shorter).
func TestSeqCompareDocumentedExamples(t *testing.T) {
	t.Parallel()
	// dynamodb-streams.md 4.3, the DescribeStream example's child shards.
	short, long := shardlog.Seq("820400000000000001192334"), shardlog.Seq("1683700000000000001135967")
	if got := short.Compare(long); got != -1 {
		t.Errorf("Compare(%s, %s) = %d, want -1", short, long, got)
	}
	if got := long.Compare(short); got != 1 {
		t.Errorf("Compare(%s, %s) = %d, want 1", long, short, got)
	}
	if same := shardlog.Seq(string(long)); long.Compare(same) != 0 {
		t.Errorf("Compare(%s, itself) = %d, want 0", long, long.Compare(same))
	}
}

// ParseSeq accepts 1 to 129 decimal digits without a leading zero, or "0"
// (kinesis.md 3.4 sets the 129-digit bound; dynamodb-streams.md 3.4 the
// digits-only rule), and refuses everything else with a *SeqError whose text
// is understudy's (contract: it names the text and the rule).
func TestParseSeq(t *testing.T) {
	t.Parallel()
	valid := []string{"0", "1", "100000000000000000000", "1" + strings.Repeat("0", 128)}
	for _, s := range valid {
		got, err := shardlog.ParseSeq(s)
		if err != nil || string(got) != s {
			t.Errorf("ParseSeq(%q) = %q, %v; want it back, nil", s, got, err)
		}
	}
	invalid := []string{"", "01", "00", "-1", "+1", "1a", " 1", "1.0", "1" + strings.Repeat("0", 129)}
	for _, s := range invalid {
		_, err := shardlog.ParseSeq(s)
		var se *shardlog.SeqError
		if !errors.As(err, &se) {
			t.Errorf("ParseSeq(%q) error = %v, want *SeqError", s, err)
			continue
		}
		want := `"` + s + `" is not a sequence number: it must be decimal digits without a leading zero`
		if se.Error() != want {
			t.Errorf("ParseSeq(%q) error text %q, want %q", s, se.Error(), want)
		}
	}
}

// Every decimal of 1 to 129 digits without a leading zero parses as itself.
func TestParseSeqAcceptsDecimals(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		s := decimal(t, "s", 129)
		if got, err := shardlog.ParseSeq(s); err != nil || string(got) != s {
			t.Fatalf("ParseSeq(%q) = %q, %v", s, got, err)
		}
	})
}

var shard0 = shardlog.ShardInfo{ID: "shardId-00000001792962817102-d2065312", Index: 0, Opened: clock.FromWire(time.Date(2026, 10, 25, 21, 13, 37, 0, time.UTC))}

// decimal-21, the default of dynamodb.streams.sequence-number (inferred,
// DDBS-09; dynamodb-streams.md 4.3): a 21-digit start and a first record one
// above it, as AWS's first record is strictly above StartingSequenceNumber
// (third-party-recorded, DDB-TEST:L1993-L1994). start-equals-first is
// DynamoDB Local's form (dynamodb-streams.md 8.3 row 7).
func TestDecimalCounterSwitchValues(t *testing.T) {
	t.Parallel()
	at := shard0.Opened
	for _, tc := range []struct {
		name  string
		c     shardlog.DecimalCounter
		first shardlog.Seq
	}{
		{"decimal-21", shardlog.DecimalCounter{Digits: 21}, "100000000000000000001"},
		{"start-equals-first", shardlog.DecimalCounter{Digits: 21, FirstAtStart: true}, "100000000000000000000"},
	} {
		start := tc.c.Start(shard0)
		if start != "100000000000000000000" {
			t.Errorf("%s: Start = %q, want 10^20", tc.name, start)
		}
		first := tc.c.Next(shard0, "", at)
		if first != tc.first {
			t.Errorf("%s: first record %q, want %q", tc.name, first, tc.first)
		}
		if second := tc.c.Next(shard0, first, at); bigOf(t, second).Cmp(new(big.Int).Add(bigOf(t, first), big.NewInt(1))) != 0 {
			t.Errorf("%s: second record %q, want %q + 1", tc.name, second, first)
		}
	}
}

// Under both counter settings the numbers a shard mints strictly increase
// numerically, by one, and grow past Digits rather than wrapping (spec A3;
// dynamodb-streams.md 11.9).
func TestDecimalCounterIncreases(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		c := shardlog.DecimalCounter{Digits: rapid.IntRange(1, 25).Draw(t, "digits"), FirstAtStart: rapid.Bool().Draw(t, "firstAtStart")}
		start := c.Start(shard0)
		if want := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(c.Digits-1)), nil); bigOf(t, start).Cmp(want) != 0 {
			t.Fatalf("Start = %q, want 10^%d", start, c.Digits-1)
		}
		at := shard0.Opened
		prev := shardlog.Seq("")
		for i := range rapid.IntRange(1, 200).Draw(t, "n") {
			next := c.Next(shard0, prev, at)
			if _, err := shardlog.ParseSeq(string(next)); err != nil {
				t.Fatalf("record %d: %v", i, err)
			}
			if prev == "" {
				if c.FirstAtStart && next != start {
					t.Fatalf("first record %q, want the start %q", next, start)
				}
				if !c.FirstAtStart && next.Compare(start) != 1 {
					t.Fatalf("first record %q is not above the start %q", next, start)
				}
			} else {
				if next.Compare(prev) != 1 {
					t.Fatalf("record %d: %q is not above %q", i, next, prev)
				}
				if bigOf(t, next).Cmp(new(big.Int).Add(bigOf(t, prev), big.NewInt(1))) != 0 {
					t.Fatalf("record %d: %q is not %q + 1", i, next, prev)
				}
			}
			prev = next
			at = at.Add(time.Duration(rapid.IntRange(0, 5000).Draw(t, "step")) * time.Millisecond)
		}
	})
}
