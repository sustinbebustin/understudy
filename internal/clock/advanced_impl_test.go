package clock

import (
	"testing"
	"testing/synctest"
)

// An advance by nothing still reports the modes, unchanged, so the admin
// handler journals a zero advance as a clock change from a mode to itself.
func TestAdvanceByNothingReportsUnchangedModes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, err := New(Options{Start: epoch, Mode: Frozen{}})
		if err != nil {
			t.Fatal(err)
		}
		before := c.Mode()
		adv, err := c.Advance(0)
		if err != nil {
			t.Fatal(err)
		}
		if want := (ModeChange[Mode]{From: before, To: before}); adv.Modes != want {
			t.Errorf("Advance(0) Modes = %#v, want %#v", adv.Modes, want)
		}
	})
}
