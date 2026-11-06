package journal

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/ref"
)

// An admin action without an op has no wire form, so Append refuses it
// without using an id, as it refuses every nil sum value (doc.go).
func TestAppendRefusesAnAdminActionWithoutOp(t *testing.T) {
	j := newTestJournal(t, Options{})
	id, err := j.Append(Event{Detail: AdminAction{}})
	if recErr, ok := errors.AsType[*RecordError](err); !ok || recErr.Reason != NoWireForm || recErr.Kind != KindAdminAction || id != 0 {
		t.Fatalf("Append(AdminAction{}) = %d, %v; want a *RecordError for admin-action with reason NoWireForm", id, err)
	}
	if next := j.NextID(); next != 1 {
		t.Errorf("NextID after the refusal = %d, want 1", next)
	}
}

// An admin action's text is recorded as JSON carries it, each byte that is
// not UTF-8 as U+FFFD, so the held event equals its file record (doc.go)
// whatever a request named.
func TestAdminActionTextIsRecordedAsJSONCarriesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.ndjson")
	j := newTestJournal(t, Options{File: &FileOptions{Path: path}})
	for _, op := range []AdminOp{
		Reset{Services: []string{"sqs\xff"}, Accounts: []string{"\xfe1"}, Regions: []string{"us-east-1\xff"}},
		SnapshotSave{Path: "a\xffb.tar"},
		SnapshotLoad{Path: "a\xffb.tar"},
		FaultRuleAdded{Rule: "rule\xff"},
		FaultRuleRemoved{Rules: []ref.RuleID{"ok", "bad\xff"}},
	} {
		mustAppend(t, j, Event{Detail: AdminAction{Op: op}})
	}
	for _, e := range j.Query(Filter{}).Events {
		a, ok := e.Detail.(AdminAction)
		if !ok {
			t.Fatalf("event %d is %s, want an admin action", e.ID, detailName(e.Detail))
		}
		if text := strings.Join(adminOpTexts(a.Op), " "); !utf8.ValidString(text) || !strings.ContainsRune(text, utf8.RuneError) {
			t.Errorf("event %d held as %#v, want its invalid bytes as U+FFFD", e.ID, e.Detail)
		}
	}
	heldEqualsFile(t, j, path)
}

// adminOpTexts is every string op carries.
func adminOpTexts(op AdminOp) []string {
	switch op := op.(type) {
	case Reset:
		return append(append(append([]string(nil), op.Services...), op.Accounts...), op.Regions...)
	case SnapshotSave:
		return []string{op.Path}
	case SnapshotLoad:
		return []string{op.Path}
	case FaultRuleAdded:
		return []string{string(op.Rule)}
	case FaultRuleRemoved:
		out := make([]string, 0, len(op.Rules))
		for _, r := range op.Rules {
			out = append(out, string(r))
		}
		return out
	}
	return nil
}

// The memory estimate charges an admin action for its text, so eviction by
// bytes counts a reset naming many services as more than one naming none.
func TestEstimateChargesAnAdminActionsText(t *testing.T) {
	long := strings.Repeat("x", 1000)
	for _, tc := range []struct{ short, long AdminOp }{
		{Reset{}, Reset{Services: []string{long}, Accounts: []string{long}, Regions: []string{long}}},
		{SnapshotSave{}, SnapshotSave{Path: long}},
		{SnapshotLoad{}, SnapshotLoad{Path: long}},
		{FaultRuleAdded{}, FaultRuleAdded{Rule: ref.RuleID(long)}},
		{FaultRuleRemoved{}, FaultRuleRemoved{Rules: []ref.RuleID{ref.RuleID(long)}}},
	} {
		short, grown := Estimate(Event{Detail: AdminAction{Op: tc.short}}), Estimate(Event{Detail: AdminAction{Op: tc.long}})
		if grown-short < len(long) {
			t.Errorf("Estimate of %T grows by %d bytes for %d bytes of text, want at least the text", tc.long, grown-short, len(long))
		}
	}
}

// A clock op the decoder does not know is refused with every known name,
// so the reader of a damaged file sees what would have been accepted.
func TestClockOpErrorNamesTheKnownOps(t *testing.T) {
	var op ClockOp
	err := op.UnmarshalText([]byte("rewind"))
	if _, ok := errors.AsType[*ClockOpError](err); !ok {
		t.Fatalf("UnmarshalText(rewind) = %v, want a *ClockOpError", err)
	}
	for _, c := range clockOpNames {
		if !strings.Contains(err.Error(), c.name) {
			t.Errorf("ClockOpError %q does not name %s", err, c.name)
		}
	}
}
