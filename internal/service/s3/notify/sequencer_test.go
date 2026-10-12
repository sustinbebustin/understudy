package notify_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/s3/notify"
)

// Next strictly increases after any last sequencer, at times that may stand
// still (a frozen clock) or step back (a snapshot load), compared as S3
// tells consumers to: as hex strings left-padded to one length. The
// per-key property of the records the s3 package raises is
// TestSequencersIncreasePerKey there.
func TestNextStrictlyIncreasesAfterAnyLast(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		at := time.Unix(rapid.Int64Range(0, 4e9).Draw(t, "start"), 0)
		var last notify.Sequencer
		for i := range rapid.IntRange(1, 200).Draw(t, "changes") {
			at = at.Add(time.Duration(rapid.Int64Range(-int64(time.Second), int64(time.Second)).Draw(t, "step")))
			next := notify.Next(last, at)
			if prev, got := last.String(), next.String(); padded(prev, got) >= padded(got, prev) {
				t.Fatalf("change %d: sequencer %s does not follow %s", i, got, prev)
			}
			last = next
		}
	})
}

// A sequencer tracks the time of its change while time moves forward.
func TestSequencerFollowsTime(t *testing.T) {
	at := time.Date(2026, time.October, 11, 0, 0, 0, 0, time.UTC)
	if got, want := notify.Next(0, at), notify.Sequencer(at.UnixNano()); got != want {
		t.Fatalf("Next(0, %v) = %s, want %s", at, got, want)
	}
	if got := notify.Next(math.MaxUint64-1, at); got != math.MaxUint64 {
		t.Fatalf("Next after MaxUint64-1 = %s, want MaxUint64", got)
	}
}

// padded is a left-padded with zeros to the length of the longer of a
// and b.
func padded(a, b string) string {
	if len(b) > len(a) {
		return strings.Repeat("0", len(b)-len(a)) + a
	}
	return a
}
