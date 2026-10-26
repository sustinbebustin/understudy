package clock

import (
	"testing"
	"testing/synctest"
	"time"
)

// movedBy is m moved d later as an advance moves it: a frozen instant, a
// running offset or a scaled clock's emulator anchor.
func movedBy(t *testing.T, m Mode, d time.Duration) Mode {
	t.Helper()
	switch m := m.(type) {
	case Frozen:
		return Frozen{At: m.At.Add(d)}
	case Running:
		return Running{Offset: m.Offset + d}
	case Scaled:
		m.AnchorEmu = m.AnchorEmu.Add(d)
		return m
	}
	t.Fatalf("mode %#v is no known mode", m)
	return nil
}

// Advance and AdvanceTo report the mode the shift replaced and the shifted
// mode, the same kind of mode moved by the advance, so the admin handler
// journals an advance or set as a clock change whose from and to are
// exactly that far apart (ADR 0007 pre-tag amendment: clock moves are
// journaled).
func TestAdvanceReportsTheModesItShifted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, start := range []Mode{Frozen{}, Running{}, Scaled{Factor: 60}} {
			c, err := New(Options{Start: epoch, Mode: start})
			if err != nil {
				t.Fatalf("New(%#v): %v", start, err)
			}
			before := c.Mode()
			adv, err := c.Advance(90 * time.Second)
			if err != nil {
				t.Fatalf("Advance on %#v: %v", before, err)
			}
			if want := (ModeChange[Mode]{From: before, To: movedBy(t, before, 90*time.Second)}); adv.Modes != want || c.Mode() != want.To {
				t.Errorf("Advance(90s) on %#v: Modes = %#v, clock now %#v; want %#v", before, adv.Modes, c.Mode(), want)
			}

			before = c.Mode()
			target := c.Now().Add(time.Hour)
			adv, err = c.AdvanceTo(target)
			if err != nil {
				t.Fatalf("AdvanceTo on %#v: %v", before, err)
			}
			if want := (ModeChange[Mode]{From: before, To: movedBy(t, before, time.Hour)}); adv.Modes != want || c.Mode() != want.To {
				t.Errorf("AdvanceTo(now+1h) on %#v: Modes = %#v, clock now %#v; want %#v", before, adv.Modes, c.Mode(), want)
			}
		}
	})
}
