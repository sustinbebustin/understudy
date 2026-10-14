package edge

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/clock"
)

// manualWall is a wall clock a test moves by hand.
type manualWall struct{ now time.Time }

func (w *manualWall) read() time.Time { return w.now }

// The timeline places a wall instant in the clock mode the edge saw in
// force then: before an advance the old offset, after it the new one,
// while frozen the frozen instant, while scaled the scaled reading.
func TestClockTimelinePlacesWallInstantsInTheirEra(t *testing.T) {
	start := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	wall := &manualWall{now: start}
	clk, err := clock.New(clock.Options{Wall: wall.read})
	if err != nil {
		t.Fatal(err)
	}
	var tl ClockTimeline
	tl.observe(clk)
	running := start.Add(time.Minute)

	wall.now = start.Add(2 * time.Minute)
	if _, err := clk.Advance(time.Hour); err != nil {
		t.Fatal(err)
	}
	tl.observe(clk)
	advanced := start.Add(3 * time.Minute)

	wall.now = start.Add(4 * time.Minute)
	frozenAt := clk.Freeze().To.At
	tl.observe(clk)
	frozen := start.Add(5 * time.Minute)

	wall.now = start.Add(6 * time.Minute)
	if _, err := clk.Scale(60); err != nil {
		t.Fatal(err)
	}
	tl.observe(clk)
	scaled := start.Add(7 * time.Minute)
	wall.now = start.Add(8 * time.Minute)

	for _, tc := range []struct {
		name string
		w    time.Time
		want clock.Instant
	}{
		{"before any change", running, clock.FromWire(running)},
		{"before the first observation", start.Add(-time.Hour), clock.FromWire(start.Add(-time.Hour))},
		{"in the wall second of the advance", start.Add(2*time.Minute + 999*time.Millisecond), clock.FromWire(start.Add(2*time.Minute + 999*time.Millisecond))},
		{"after the advance", advanced, clock.FromWire(advanced.Add(time.Hour))},
		{"while frozen", frozen, frozenAt},
		{"while scaled", scaled, frozenAt.Add(60 * time.Minute)},
	} {
		if got := tl.at(clk, tc.w); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

// Eras that ended longer ago than the longest presigned lifetime are
// dropped, the oldest kept standing in for every earlier instant, and the
// history never holds more than maxEras.
func TestClockTimelineForgetsErasNoURLCanNeed(t *testing.T) {
	start := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	wall := &manualWall{now: start}
	clk, err := clock.New(clock.Options{Wall: wall.read})
	if err != nil {
		t.Fatal(err)
	}
	var tl ClockTimeline
	tl.observe(clk)
	for _, day := range []int{8, 16} {
		wall.now = start.Add(time.Duration(day) * 24 * time.Hour)
		if _, err := clk.Advance(time.Minute); err != nil {
			t.Fatal(err)
		}
		tl.observe(clk)
	}
	// The first era ended on day 8, more than a week before day 16; the
	// second ended just now, so a URL signed in it may still be live.
	if len(tl.eras) != 2 {
		t.Fatalf("kept %d eras, want the two a live URL can need", len(tl.eras))
	}
	if got, want := tl.at(clk, start), clock.FromWire(start.Add(time.Minute)); got != want {
		t.Errorf("an instant before the kept era: %s, want it read in that era, %s", got, want)
	}
	for range maxEras + 10 {
		wall.now = wall.now.Add(time.Second)
		if _, err := clk.Advance(time.Second); err != nil {
			t.Fatal(err)
		}
		tl.observe(clk)
	}
	if len(tl.eras) != maxEras {
		t.Errorf("kept %d eras, want at most %d", len(tl.eras), maxEras)
	}
}

// Another request can see a clock change before its admin answer is
// written; the answer is still held until the new era begins, so a URL
// the client signs on the answer is placed after the change.
func TestAdminAnswerWaitsForAnEraAnotherRequestRecorded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clk, err := clock.New(clock.Options{})
		if err != nil {
			t.Fatal(err)
		}
		var tl ClockTimeline
		tl.observe(clk)
		time.Sleep(200 * time.Millisecond)
		changed := time.Now()
		if _, err := clk.Advance(time.Hour); err != nil {
			t.Fatal(err)
		}
		tl.observe(clk) // a concurrent request, before the admin answer

		w := &settledWriter{ResponseWriter: httptest.NewRecorder(), r: httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/_understudy/v1/clock", http.NoBody), clock: clk, timeline: &tl}
		w.WriteHeader(http.StatusOK)
		answered := time.Now()
		if want := changed.Truncate(time.Second).Add(time.Second); answered.Before(want) {
			t.Fatalf("answered at %s, want held until the era begins at %s", answered, want)
		}
		signed := answered.Truncate(time.Second)
		if got, want := tl.at(clk, signed), clock.FromWire(signed.Add(time.Hour)); got != want {
			t.Errorf("a URL signed on the answer: %s, want it placed after the advance, %s", got, want)
		}
		if got, want := tl.at(clk, changed.Truncate(time.Second)), clock.FromWire(changed.Truncate(time.Second)); got != want {
			t.Errorf("a URL signed in the second of the change: %s, want it placed before the advance, %s", got, want)
		}
	})
}

// Two modes seen within one wall second share one era, the later mode's,
// so eras stay in strictly increasing wall order.
func TestClockTimelineKeepsTheLaterOfTwoChangesInOneSecond(t *testing.T) {
	start := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	wall := &manualWall{now: start}
	clk, err := clock.New(clock.Options{Wall: wall.read})
	if err != nil {
		t.Fatal(err)
	}
	var tl ClockTimeline
	tl.observe(clk)
	wall.now = start.Add(100 * time.Millisecond)
	for range 2 {
		if _, err := clk.Advance(time.Hour); err != nil {
			t.Fatal(err)
		}
		tl.observe(clk)
	}
	if len(tl.eras) != 2 {
		t.Fatalf("kept %d eras, want the first and one for the second's changes", len(tl.eras))
	}
	after := start.Add(time.Second)
	if got, want := tl.at(clk, after), clock.FromWire(after.Add(2*time.Hour)); got != want {
		t.Errorf("after both changes: %s, want the later mode, %s", got, want)
	}
}
