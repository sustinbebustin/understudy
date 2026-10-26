package journal_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/journal"
)

// The advisories iam.enforce writes (contract 7) are appended after the
// kinds journal files already carry, in this order, so a file written by an
// earlier build still reads back (docs/guide/journal.md), and their wire
// names round-trip.
func TestEnforcementAdvisoryKinds(t *testing.T) {
	kinds := []struct {
		k    journal.AdvisoryKind
		name string
	}{
		{journal.AdvisoryAccessDenied, "access-denied"},
		{journal.AdvisoryAccessWouldDeny, "access-would-deny"},
		{journal.AdvisoryAccessUnevaluated, "access-unevaluated"},
		{journal.AdvisoryConditionKeyUnsupplied, "condition-key-unsupplied"},
	}
	previous := journal.AdvisoryScheduleFiresSkipped
	for _, c := range kinds {
		if c.k <= previous {
			t.Errorf("%s = %d, want it after %d", c.name, c.k, previous)
		}
		previous = c.k
		if got, err := c.k.MarshalText(); err != nil || string(got) != c.name {
			t.Errorf("MarshalText(%d) = %q, %v; want %q", c.k, got, err, c.name)
		}
		if got, err := journal.ParseAdvisoryKind(c.name); err != nil || got != c.k {
			t.Errorf("ParseAdvisoryKind(%q) = %d, %v; want %d", c.name, got, err, c.k)
		}
	}
}
