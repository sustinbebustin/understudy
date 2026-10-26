package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// encodeLine encodes e and returns its one NDJSON line, without the newline.
func encodeLine(t *testing.T, e Event) string {
	t.Helper()
	var buf bytes.Buffer
	if err := EncodeNDJSON(&buf, e); err != nil {
		t.Fatalf("EncodeNDJSON(%s): %v", detailName(e.Detail), err)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// withDetail returns line with its detail member replaced by detail and its
// kind by kind, so a test can hand the decoder a line no encoder writes.
func withDetail(t *testing.T, line, kind, detail string) string {
	t.Helper()
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &members); err != nil {
		t.Fatalf("line %s is not a JSON object: %v", line, err)
	}
	members["kind"] = json.RawMessage(`"` + kind + `"`)
	members["detail"] = json.RawMessage(detail)
	out, err := json.Marshal(members)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// decodeOne decodes the one line and returns its event or error.
func decodeOne(t *testing.T, line string) (Event, error) {
	t.Helper()
	var got []Event
	for e, err := range DecodeNDJSON(strings.NewReader(line + "\n")) {
		if err != nil {
			return Event{}, err
		}
		got = append(got, e)
	}
	if len(got) != 1 {
		t.Fatalf("decoding %s gave %d events, want 1", line, len(got))
	}
	return got[0], nil
}

// sameJSON reports whether a and b hold the same JSON value, member order
// aside.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("%s: %v", a, err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return reflect.DeepEqual(va, vb)
}

// An admin action's NDJSON detail names the op and only the members that
// op carries, each omitted when empty, false or 0 (ADR 0007 pre-tag
// amendment: AdminAction, one per action; member names as ARCHITECTURE.md
// 10 records them).
func TestAdminActionWireForm(t *testing.T) {
	cases := []struct {
		op   AdminOp
		want string
	}{
		{
			Reset{Services: []string{"sqs"}, Accounts: []string{"111111111111"}, Regions: []string{"us-east-1"}, Clock: true, Faults: 2},
			`{"op":"reset","services":["sqs"],"accounts":["111111111111"],"regions":["us-east-1"],"clock":true,"faults":2}`,
		},
		{Reset{}, `{"op":"reset"}`},
		{SnapshotSave{Path: "before.tar"}, `{"op":"snapshot-save","path":"before.tar"}`},
		{SnapshotSave{}, `{"op":"snapshot-save"}`},
		{SnapshotLoad{Path: "before.tar"}, `{"op":"snapshot-load","path":"before.tar"}`},
		{SnapshotLoad{}, `{"op":"snapshot-load"}`},
		{FaultRuleAdded{Rule: "orders-throttle"}, `{"op":"fault-rule-added","rule":"orders-throttle"}`},
		{FaultRuleRemoved{Rules: []ref.RuleID{"a", "b"}}, `{"op":"fault-rule-removed","rules":["a","b"]}`},
		{FaultRuleRemoved{}, `{"op":"fault-rule-removed"}`},
	}
	for _, c := range cases {
		e := Event{ID: 7, Cause: ref.Cause{Trace: 7}, At: epoch, Wall: wall0, Detail: AdminAction{Op: c.op}}
		line := encodeLine(t, e)
		var got struct {
			Kind   string          `json:"kind"`
			Detail json.RawMessage `json:"detail"`
		}
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("%T: line %s: %v", c.op, line, err)
		}
		if got.Kind != "admin-action" || !sameJSON(t, got.Detail, []byte(c.want)) {
			t.Errorf("%#v encodes as kind %q detail %s, want kind admin-action detail %s", c.op, got.Kind, got.Detail, c.want)
		}
		back, err := decodeOne(t, line)
		if err != nil || !reflect.DeepEqual(back, e) {
			t.Errorf("%#v round trip = %#v, %v; want the event back", c.op, back, err)
		}
	}
}

// The decoder reads admin actions strictly: an op it does not know, or a
// member another op owns, is a malformed line, never a silently different
// event.
func TestAdminActionDecodeIsStrict(t *testing.T) {
	base := encodeLine(t, Event{ID: 7, Cause: ref.Cause{Trace: 7}, At: epoch, Wall: wall0, Detail: AdminAction{Op: FaultRuleAdded{Rule: "r"}}})
	for _, detail := range []string{
		`{"op":"rewind"}`,
		`{}`,
		`{"op":"fault-rule-added","rule":"r","path":"before.tar"}`,
		`{"op":"snapshot-save","path":"before.tar","rules":["a"]}`,
		`{"op":"reset","rule":"r"}`,
		`{"op":"fault-rule-removed","rules":["a"],"clock":true}`,
	} {
		line := withDetail(t, base, "admin-action", detail)
		if e, err := decodeOne(t, line); !errorIs[*DecodeError](err) {
			t.Errorf("decoding admin-action detail %s = %s, %v; want a *DecodeError", detail, detailName(e.Detail), err)
		}
	}
}

// errorIs reports whether err holds a T.
func errorIs[T error](err error) bool {
	_, ok := errors.AsType[T](err)
	return ok
}

// clockOpNames are the wire names of ClockOp, as the admin clock endpoint
// names its ops (ADR 0017: {"op": "freeze"|"resume"|"advance"|"set"|
// "scale"}), plus restore for a reset that returns the clock.
var clockOpNames = []struct {
	op   ClockOp
	name string
}{
	{ClockFreeze, "freeze"},
	{ClockResume, "resume"},
	{ClockScale, "scale"},
	{ClockAdvance, "advance"},
	{ClockSet, "set"},
	{ClockRestore, "restore"},
}

func TestClockOpWireNames(t *testing.T) {
	for i, c := range clockOpNames {
		if c.op != ClockOp(i+1) {
			t.Errorf("%s is ClockOp(%d), want %d: values start at 1 so 0 means not recorded", c.name, c.op, i+1)
		}
		if got := c.op.String(); got != c.name {
			t.Errorf("ClockOp(%d).String() = %q, want %q", c.op, got, c.name)
		}
		if got, err := c.op.MarshalText(); err != nil || string(got) != c.name {
			t.Errorf("ClockOp(%d).MarshalText() = %q, %v; want %q", c.op, got, err, c.name)
		}
		var back ClockOp
		if err := back.UnmarshalText([]byte(c.name)); err != nil || back != c.op {
			t.Errorf("UnmarshalText(%q) = %d, %v; want %d", c.name, back, err, c.op)
		}
	}
	for outside, name := range map[ClockOp]string{0: "ClockOp(0)", ClockRestore + 1: "ClockOp(7)"} {
		if got := outside.String(); got != name {
			t.Errorf("ClockOp(%d).String() = %q, want %q", outside, got, name)
		}
		if got, err := outside.MarshalText(); err == nil {
			t.Errorf("ClockOp(%d).MarshalText() = %q, want an error", outside, got)
		}
	}
	var op ClockOp
	if err := op.UnmarshalText([]byte("rewind")); err == nil {
		t.Errorf("UnmarshalText(rewind) = %d, want an error", op)
	}
}

// A clock change writes its op; one with op 0, as an earlier build wrote
// every clock change, writes none and such a line reads back with op 0
// (kind_wire_test.go: files written by an earlier build still read back).
// An op name the decoder does not know is a malformed line.
func TestClockChangedWireFormCarriesItsOp(t *testing.T) {
	at := epoch.Add(90 * time.Second)
	for _, c := range clockOpNames {
		e := Event{ID: 3, Cause: ref.Cause{Trace: 3}, At: at, Wall: wall0, Detail: ClockChanged{From: clock.Frozen{At: epoch}, To: clock.Frozen{At: at}, Op: c.op}}
		line := encodeLine(t, e)
		if !strings.Contains(line, `"op":"`+c.name+`"`) {
			t.Errorf("clock change with op %s encodes as %s, want \"op\":%q", c.name, line, c.name)
		}
		if back, err := decodeOne(t, line); err != nil || !reflect.DeepEqual(back, e) {
			t.Errorf("round trip of op %s = %#v, %v", c.name, back, err)
		}
	}

	old := Event{ID: 3, Cause: ref.Cause{Trace: 3}, At: at, Wall: wall0, Detail: ClockChanged{From: clock.Frozen{At: epoch}, To: clock.Frozen{At: at}}}
	line := encodeLine(t, old)
	if strings.Contains(line, `"op"`) {
		t.Errorf("clock change with op 0 encodes as %s, want no op member", line)
	}
	if back, err := decodeOne(t, line); err != nil || !reflect.DeepEqual(back, old) {
		t.Errorf("a clock-changed line without op = %#v, %v; want op 0", back, err)
	}

	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &members); err != nil {
		t.Fatal(err)
	}
	var detail map[string]json.RawMessage
	if err := json.Unmarshal(members["detail"], &detail); err != nil {
		t.Fatal(err)
	}
	detail["op"] = json.RawMessage(`"rewind"`)
	bad, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if e, err := decodeOne(t, withDetail(t, line, "clock-changed", string(bad))); !errorIs[*DecodeError](err) {
		t.Errorf("clock-changed with op rewind = %s, %v; want a *DecodeError", detailName(e.Detail), err)
	}
}

// Append refuses a clock change whose op is outside its values, as it
// refuses every enum outside its values (doc.go), without using an id.
func TestAppendRefusesAClockOpOutsideItsValues(t *testing.T) {
	j := newTestJournal(t, Options{})
	id, err := j.Append(Event{Detail: ClockChanged{From: clock.Frozen{At: epoch}, To: clock.Frozen{At: epoch}, Op: ClockRestore + 1}})
	if recErr, ok := errors.AsType[*RecordError](err); !ok || recErr.Reason != NoWireForm || id != 0 {
		t.Fatalf("Append of ClockOp(%d) = %d, %v; want a *RecordError with reason NoWireForm", ClockRestore+1, id, err)
	}
	if next := j.NextID(); next != 1 {
		t.Errorf("NextID after the refusal = %d, want 1", next)
	}
}

// Kinds lists every declared kind in value order, admin-action last
// (kind_wire_test.go: a kind added later is appended).
func TestKindsListsEveryDeclaredKind(t *testing.T) {
	named, _ := declaredKinds()
	if got := Kinds(); !slices.Equal(got, named) {
		t.Fatalf("Kinds() = %v, want %v", got, named)
	}
	if named[len(named)-1] != KindAdminAction || KindOf(AdminAction{Op: Reset{}}) != KindAdminAction {
		t.Errorf("last declared kind = %v, KindOf(AdminAction) = %v; want admin-action for both", named[len(named)-1], KindOf(AdminAction{Op: Reset{}}))
	}
}

// Append records an admin action in the form decoding produces: empty
// lists as nil, so the held event equals its file record (doc.go).
func TestAdminActionIsRecordedAsDecoded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.ndjson")
	j := newTestJournal(t, Options{File: &FileOptions{Path: path}})
	reset := mustAppend(t, j, Event{Detail: AdminAction{Op: Reset{Services: []string{}, Accounts: []string{}, Regions: []string{}}}})
	removed := mustAppend(t, j, Event{Detail: AdminAction{Op: FaultRuleRemoved{Rules: []ref.RuleID{}}}})
	held := j.Query(Filter{})
	want := map[ref.EventID]AdminOp{reset: Reset{}, removed: FaultRuleRemoved{}}
	for _, e := range held.Events {
		a, ok := e.Detail.(AdminAction)
		if !ok || !reflect.DeepEqual(a.Op, want[e.ID]) {
			t.Errorf("event %d held as %#v, want %#v", e.ID, e.Detail, AdminAction{Op: want[e.ID]})
		}
	}
	heldEqualsFile(t, j, path)
}

// An admin action touches no resource and has no result, so the resource
// and outcome filters never select it; the kind filter does (doc.go:
// filters).
func TestAdminActionHasNoResourceOrResult(t *testing.T) {
	j := newTestJournal(t, Options{})
	id := mustAppend(t, j, Event{Detail: AdminAction{Op: FaultRuleAdded{Rule: "orders-throttle"}}})
	if got := eventIDs(j.Query(Filter{Kind: KindAdminAction}).Events); !slices.Equal(got, []ref.EventID{id}) {
		t.Errorf("Query(kind admin-action) = %v, want [%d]", got, id)
	}
	if got := j.Query(Filter{ResourceGlob: "*"}).Events; len(got) != 0 {
		t.Errorf("Query(resource *) = %v, want nothing: an admin action has no resource", eventIDs(got))
	}
	for r := ResultOK; r <= ResultPending; r++ {
		if got := j.Query(Filter{Outcome: r}).Events; len(got) != 0 {
			t.Errorf("Query(outcome %v) = %v, want nothing: an admin action has no result", r, eventIDs(got))
		}
	}
}

// Canonical trees leave an admin action as recorded and keep a clock
// change's op (doc.go: Canonical normalizes clock modes, nothing else of
// them).
func TestCanonicalKeepsAdminActionsAndClockOps(t *testing.T) {
	j := newTestJournal(t, Options{})
	action := AdminAction{Op: Reset{Services: []string{"sqs"}, Clock: true, Faults: 1}}
	id := mustAppend(t, j, Event{Detail: action})
	tree, ok := j.Trace(id)
	if !ok {
		t.Fatal("Trace found no tree for the admin action")
	}
	if got := tree.Canonical().Event.Detail; !reflect.DeepEqual(got, action) {
		t.Errorf("canonical admin action = %#v, want %#v", got, action)
	}
	id = mustAppend(t, j, Event{Detail: ClockChanged{From: clock.Frozen{At: epoch}, To: clock.Frozen{At: epoch.Add(time.Minute)}, Op: ClockAdvance}})
	if tree, ok = j.Trace(id); !ok {
		t.Fatal("Trace found no tree for the clock change")
	}
	if got, ok := tree.Canonical().Event.Detail.(ClockChanged); !ok || got.Op != ClockAdvance {
		t.Errorf("canonical clock change = %#v, want op advance kept", tree.Canonical().Event.Detail)
	}
}
