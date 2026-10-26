package journal

import (
	"errors"
	"strings"
	"testing"
)

// advisoryKindNames are the wire names of every advisory kind: NDJSON
// journal files carry them in each advisory event (docs/guide/journal.md),
// so a file written by an earlier build must still read back. A kind added
// later is appended after them.
var advisoryKindNames = []struct {
	k    AdvisoryKind
	name string
}{
	{AdvisoryPolicyWouldDeny, "policy-would-deny"},
	{AdvisoryUnmintedCredentials, "unminted-credentials"},
	{AdvisoryShadowedCodeUpdate, "shadowed-code-update"},
	{AdvisoryCrossRegionServed, "cross-region-served"},
	{AdvisoryFilterUndecided, "filter-undecided"},
	{AdvisoryScheduleFiresSkipped, "schedule-fires-skipped"},
}

// declaredAdvisoryKinds walks the kinds from the first until String falls
// back to its numeric form.
func declaredAdvisoryKinds() (named []AdvisoryKind, next AdvisoryKind) {
	k := AdvisoryPolicyWouldDeny
	for ; !strings.HasPrefix(k.String(), "AdvisoryKind("); k++ {
		named = append(named, k)
	}
	return named, k
}

func TestAdvisoryKindWireNamesAreStable(t *testing.T) {
	named, next := declaredAdvisoryKinds()
	if len(named) < len(advisoryKindNames) {
		t.Fatalf("declared kinds %v stop before the persisted ones end", named)
	}
	for i, c := range advisoryKindNames {
		if named[i] != c.k {
			t.Errorf("declared kind %d is %d, want %s: persisted kinds keep their place", i, named[i], c.name)
		}
		if got, err := c.k.MarshalText(); err != nil || string(got) != c.name {
			t.Errorf("AdvisoryKind %d MarshalText() = %q, %v; want %q", c.k, got, err, c.name)
		}
		var back AdvisoryKind
		if err := back.UnmarshalText([]byte(c.name)); err != nil || back != c.k {
			t.Errorf("UnmarshalText(%q) = %v, %v; want %v", c.name, back, err, c.k)
		}
	}
	// The value after the last declared one is no advisory kind, and
	// neither is the zero value.
	for _, k := range []AdvisoryKind{0, next} {
		if _, err := k.MarshalText(); err == nil {
			t.Errorf("MarshalText of %v succeeded, want an error", k)
		}
		if got, err := ParseAdvisoryKind(k.String()); err == nil {
			t.Errorf("ParseAdvisoryKind(%q) = %v, want an error", k.String(), got)
		}
	}
}

// The error lists every declared name, the last after "and"; for the
// persisted set that is the text understudy has always written.
func TestAdvisoryKindErrorListsTheKnownNames(t *testing.T) {
	_, err := ParseAdvisoryKind("policy-would-allow")
	if _, ok := errors.AsType[*AdvisoryKindError](err); !ok {
		t.Fatalf("ParseAdvisoryKind(policy-would-allow) error = %v, want *AdvisoryKindError", err)
	}
	named, _ := declaredAdvisoryKinds()
	names := make([]string, 0, len(named))
	for _, k := range named {
		names = append(names, k.String())
	}
	want := `"policy-would-allow" names no advisory kind; known names are ` +
		strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	if got := err.Error(); got != want {
		t.Errorf("error = %q\nwant    %q", got, want)
	}
	const persisted = "policy-would-deny, unminted-credentials, shadowed-code-update, cross-region-served, filter-undecided"
	if !strings.Contains(err.Error(), "known names are "+persisted) {
		t.Errorf("error = %q, want it to list the persisted names in order", err)
	}
}
