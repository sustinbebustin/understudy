package journal

import (
	"errors"
	"strings"
	"testing"
)

// kindNames are the wire names of every event kind: NDJSON journal files
// carry them and `understudy journal --kind` takes them
// (docs/guide/journal.md), so a file written by an earlier build must still
// read back. A kind added later is appended after them.
var kindNames = []struct {
	k    Kind
	name string
}{
	{KindAPICall, "api-call"},
	{KindDelivery, "delivery"},
	{KindInvocation, "invocation"},
	{KindTimerFired, "timer-fired"},
	{KindStateChange, "state-change"},
	{KindFaultInjected, "fault-injected"},
	{KindClockChanged, "clock-changed"},
	{KindAdvisory, "advisory"},
	{KindUnclaimed, "unclaimed"},
	{KindGap, "gap"},
}

// declaredKinds walks the kinds from the first until String falls back to
// its numeric form.
func declaredKinds() (named []Kind, next Kind) {
	k := KindAPICall
	for ; !strings.HasPrefix(k.String(), "Kind("); k++ {
		named = append(named, k)
	}
	return named, k
}

func TestKindWireNamesAreStable(t *testing.T) {
	named, next := declaredKinds()
	if len(named) < len(kindNames) {
		t.Fatalf("declared kinds %v stop before the persisted ones end", named)
	}
	for i, c := range kindNames {
		if named[i] != c.k {
			t.Errorf("declared kind %d is %d, want %s: persisted kinds keep their place", i, named[i], c.name)
		}
		if got := c.k.String(); got != c.name {
			t.Errorf("Kind %d String() = %q, want %q", c.k, got, c.name)
		}
		if got, err := c.k.MarshalText(); err != nil || string(got) != c.name {
			t.Errorf("Kind %d MarshalText() = %q, %v; want %q", c.k, got, err, c.name)
		}
		if got, err := ParseKind(c.name); err != nil || got != c.k {
			t.Errorf("ParseKind(%q) = %v, %v; want %v", c.name, got, err, c.k)
		}
	}
	if _, err := next.MarshalText(); err == nil {
		t.Errorf("MarshalText of %v, after the last declared kind, succeeded", next)
	}
	if k, err := ParseKind(next.String()); err == nil {
		t.Errorf("ParseKind(%q) = %v, want an error", next.String(), k)
	}
}

// The error lists every declared name, the last after "and"; for the
// persisted set that is the text understudy has always written.
func TestKindErrorListsTheKnownNames(t *testing.T) {
	_, err := ParseKind("api-calls")
	if _, ok := errors.AsType[*KindError](err); !ok {
		t.Fatalf("ParseKind(api-calls) error = %v, want *KindError", err)
	}
	named, _ := declaredKinds()
	names := make([]string, 0, len(named))
	for _, k := range named {
		names = append(names, k.String())
	}
	want := `"api-calls" names no journal event kind; known names are ` +
		strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	if got := err.Error(); got != want {
		t.Errorf("error = %q\nwant    %q", got, want)
	}
	const persisted = "api-call, delivery, invocation, timer-fired, state-change, fault-injected, clock-changed, advisory, unclaimed"
	if !strings.Contains(err.Error(), "known names are "+persisted) {
		t.Errorf("error = %q, want it to list the persisted names in order", err)
	}
}
