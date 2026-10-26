package admin_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
	"github.com/sustinbebustin/understudy/internal/snapshot"
)

// recordedSince returns the events the journal holds with ids from before
// on, in id order.
func recordedSince(k *kerneltest.K, before ref.EventID) []journal.Event {
	k.Helper()
	return k.Journal.Query(journal.Filter{IDRange: journal.IDRange{From: before}, Limit: 1000}).Events
}

// ofKind returns the events of kind among es.
func ofKind(es []journal.Event, kind journal.Kind) []journal.Event {
	var out []journal.Event
	for i := range es {
		if journal.KindOf(es[i].Detail) == kind {
			out = append(out, es[i])
		}
	}
	return out
}

// wantAdminEvent checks e is an event the admin handler wrote: no cause,
// no links, the zero scope (ADR 0007 pre-tag amendment: written by the
// admin handler like ClockChanged), with detail want.
func wantAdminEvent(k *kerneltest.K, what string, e journal.Event, want journal.Detail) {
	k.Helper()
	if e.Cause.Parent != 0 || e.Cause.Trace != e.ID || e.Links != nil || e.Scope != (aws.Scope{}) {
		k.Errorf("%s recorded event %d with cause %+v, links %v, scope %+v; want no cause, no links and the zero scope", what, e.ID, e.Cause, e.Links, e.Scope)
	}
	if !reflect.DeepEqual(e.Detail, want) {
		k.Errorf("%s recorded %#v, want %#v", what, e.Detail, want)
	}
}

// wantOneAdminEvent checks the call since before recorded exactly one event,
// the admin handler's, with detail want.
func wantOneAdminEvent(k *kerneltest.K, what string, before ref.EventID, want journal.Detail) {
	k.Helper()
	got := recordedSince(k, before)
	if len(got) != 1 {
		k.Errorf("%s recorded %d events %v, want exactly one", what, len(got), got)
		return
	}
	wantAdminEvent(k, what, got[0], want)
}

// jsonCall sends method path with a JSON body ("" sends none).
func jsonCall(k *kerneltest.K, h http.Handler, method, path, body string) (status int, answer string) {
	k.Helper()
	rec := pageCall(k, h, method, path, body, nil)
	return rec.Code, rec.Body.String()
}

// Freeze, resume and scale each journal one clock change naming its op, as
// they did before without one (ADR 0007: ClockChanged).
func TestClockModeChangesJournalTheirOp(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "")
		for _, tc := range []struct {
			body string
			op   journal.ClockOp
			to   string
		}{
			{`{"op": "resume"}`, journal.ClockResume, "clock.Running"},
			{`{"op": "scale", "factor": 60}`, journal.ClockScale, "clock.Scaled"},
			{`{"op": "freeze"}`, journal.ClockFreeze, "clock.Frozen"},
		} {
			before := k.Journal.NextID()
			from := k.Clock.Mode()
			if status, body := jsonCall(k, h, http.MethodPost, admin.Prefix+"/clock", tc.body); status != http.StatusOK {
				k.Fatalf("POST clock %s = %d %s", tc.body, status, body)
			}
			got := recordedSince(k, before)
			if len(got) != 1 {
				k.Fatalf("POST clock %s recorded %v, want one clock change", tc.body, got)
			}
			c, ok := got[0].Detail.(journal.ClockChanged)
			if !ok || c.Op != tc.op || typeName(c.To) != tc.to || typeName(c.From) != typeName(from) {
				k.Errorf("POST clock %s recorded %#v, want op %s from a %s to a %s", tc.body, got[0].Detail, tc.op, typeName(from), tc.to)
			}
			wantAdminEvent(k, "POST clock "+tc.body, got[0], got[0].Detail)
		}
	})
}

// typeName names m's dynamic type, such as clock.Frozen.
func typeName(m clock.Mode) string {
	if m == nil {
		return "<nil>"
	}
	return reflect.TypeOf(m).String()
}

// An advance journals one clock change with op advance whose from and to
// are the mode it replaced and the mode it left, recorded after the advance
// returns: above every event the timers it fired recorded (ADR 0007
// pre-tag amendment; spec: a UI that relies on the stream sees clock moves).
func TestClockAdvanceJournalsAClockChangeAfterTheTimersItFired(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "")
		tick := k.Kernel.Timers().Handle("test.tick", func(*kernel.Call, clock.TimerKey, clock.Instant) {})
		k.Kernel.Timers().Scheduler().Arm(tick.Key("a"), k.Clock.Now().Add(time.Minute))
		before, from := k.Journal.NextID(), k.Clock.Mode()
		if status, body := jsonCall(k, h, http.MethodPost, admin.Prefix+"/clock", `{"op": "advance", "by": "90s"}`); status != http.StatusOK {
			k.Fatalf("POST clock advance = %d %s", status, body)
		}
		got := recordedSince(k, before)
		changes, fired := ofKind(got, journal.KindClockChanged), ofKind(got, journal.KindTimerFired)
		if len(changes) != 1 || len(fired) != 1 {
			k.Fatalf("advance recorded %v; want one timer fired and one clock change", got)
		}
		wantAdminEvent(k, "advance", changes[0], journal.ClockChanged{From: from, To: k.Clock.Mode(), Op: journal.ClockAdvance})
		frozen, ok := from.(clock.Frozen)
		if !ok {
			k.Fatalf("kerneltest clock mode = %#v, want frozen", from)
		}
		if want := (clock.Frozen{At: frozen.At.Add(90 * time.Second)}); k.Clock.Mode() != want {
			k.Errorf("clock after advancing 90s = %#v, want %#v", k.Clock.Mode(), want)
		}
		if last := got[len(got)-1]; last.ID != changes[0].ID {
			k.Errorf("clock change %d is not the last event the advance recorded (%d): it comes after the fired timers", changes[0].ID, last.ID)
		}
	})
}

// A set journals one clock change with op set, as an advance does.
func TestClockSetJournalsAClockChange(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "")
		before, from := k.Journal.NextID(), k.Clock.Mode()
		to := k.Clock.Now().Add(time.Hour).Wire().Format(time.RFC3339Nano)
		if status, body := jsonCall(k, h, http.MethodPost, admin.Prefix+"/clock", `{"op": "set", "to": "`+to+`"}`); status != http.StatusOK {
			k.Fatalf("POST clock set = %d %s", status, body)
		}
		wantOneAdminEvent(k, "set", before, journal.ClockChanged{From: from, To: k.Clock.Mode(), Op: journal.ClockSet})
	})
}

// A reset journals one admin action naming what it reset; with clock true
// the clock's return comes first, as a clock change with op restore.
func TestResetJournalsAnAdminAction(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		start := k.Clock.Now()
		svc := &scopedLateService{}
		h := handlerWith(k, func(o *serveradmin.Options) {
			o.Services, o.Late = []serveradmin.Service{svc}, []snapshot.Late{svc}
			o.ClockStart = func() (clock.Instant, clock.Mode) { return start, clock.Frozen{} }
		})
		for range 2 {
			if _, err := k.Faults.Add(fault.Rule{Phase: fault.Delivery, Action: fault.Latency{Wall: time.Second}, Trigger: fault.Always{}}); err != nil {
				k.Fatal(err)
			}
		}
		if _, err := k.Clock.Advance(time.Hour); err != nil {
			k.Fatal(err)
		}
		moved := k.Clock.Mode()

		before := k.Journal.NextID()
		status, body := jsonCall(k, h, http.MethodPost, admin.Prefix+"/reset",
			`{"services":["late"],"accounts":["111111111111"],"regions":["us-east-1"],"clock":true,"faults":true}`)
		var res admin.ResetResult
		if status != http.StatusOK || json.Unmarshal([]byte(body), &res) != nil {
			k.Fatalf("reset = %d %s", status, body)
		}
		got := recordedSince(k, before)
		if len(got) != 2 {
			k.Fatalf("reset with clock recorded %v, want a clock change then an admin action", got)
		}
		wantAdminEvent(k, "reset's clock return", got[0], journal.ClockChanged{From: moved, To: clock.Frozen{At: start}, Op: journal.ClockRestore})
		wantAdminEvent(k, "reset", got[1], journal.AdminAction{Op: journal.Reset{
			Services: res.Services, Accounts: []string{"111111111111"}, Regions: []string{"us-east-1"}, Clock: true, Faults: 2,
		}})

		before = k.Journal.NextID()
		status, body = jsonCall(k, h, http.MethodPost, admin.Prefix+"/reset", `{}`)
		if status != http.StatusOK || json.Unmarshal([]byte(body), &res) != nil {
			k.Fatalf("reset {} = %d %s", status, body)
		}
		wantOneAdminEvent(k, "reset {}", before, journal.AdminAction{Op: journal.Reset{Services: nilIfEmpty(res.Services)}})
	})
}

// nilIfEmpty is s, or nil when s is empty: the journal records empty lists
// as nil.
func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

// Snapshot saves and loads each journal one admin action, with the
// archive's path under the snapshots directory in the path form and none
// in the body form; a load's clock restore writes no clock change of its
// own.
func TestSnapshotsJournalAnAdminAction(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		root, err := os.OpenRoot(k.TempDir())
		if err != nil {
			k.Fatal(err)
		}
		k.Cleanup(func() {
			if err := root.Close(); err != nil {
				k.Error(err)
			}
		})
		h := handlerWith(k, func(o *serveradmin.Options) { o.DataDir = root })

		before := k.Journal.NextID()
		archive := save(k, h, false)
		wantOneAdminEvent(k, "save to the body", before, journal.AdminAction{Op: journal.SnapshotSave{}})

		before = k.Journal.NextID()
		if rec := load(k, h, bytes.NewReader(archive)); rec.Code != http.StatusOK {
			k.Fatalf("load from the body = %d %s", rec.Code, rec.Body)
		}
		wantOneAdminEvent(k, "load from the body", before, journal.AdminAction{Op: journal.SnapshotLoad{}})

		before = k.Journal.NextID()
		if status, body := jsonCall(k, h, http.MethodPost, admin.Prefix+"/snapshots", `{"path":"snaps/one.tar"}`); status != http.StatusOK {
			k.Fatalf("save to a path = %d %s", status, body)
		}
		wantOneAdminEvent(k, "save to a path", before, journal.AdminAction{Op: journal.SnapshotSave{Path: "snaps/one.tar"}})

		before = k.Journal.NextID()
		if status, body := jsonCall(k, h, http.MethodPut, admin.Prefix+"/snapshots", `{"path":"snaps/one.tar"}`); status != http.StatusOK {
			k.Fatalf("load from a path = %d %s", status, body)
		}
		wantOneAdminEvent(k, "load from a path", before, journal.AdminAction{Op: journal.SnapshotLoad{Path: "snaps/one.tar"}})
	})
}

// Adding a fault rule, removing one and removing them all each journal one
// admin action naming the rules, the removal of all in evaluation order and
// with none when there were none.
func TestFaultRuleChangesJournalAnAdminAction(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "")
		rule, err := json.Marshal(admin.FaultRule{Phase: admin.DeliveryPhase, Action: admin.Latency{Wall: time.Second}, Trigger: admin.Always{}})
		if err != nil {
			k.Fatal(err)
		}
		for _, id := range []string{"b", "a", "c"} {
			before := k.Journal.NextID()
			if status, body := jsonCall(k, h, http.MethodPut, admin.Prefix+"/faults/"+id, string(rule)); status != http.StatusOK {
				k.Fatalf("PUT faults/%s = %d %s", id, status, body)
			}
			wantOneAdminEvent(k, "PUT faults/"+id, before, journal.AdminAction{Op: journal.FaultRuleAdded{Rule: ref.RuleID(id)}})
		}

		before := k.Journal.NextID()
		if status, body := jsonCall(k, h, http.MethodDelete, admin.Prefix+"/faults/c", ""); status >= 300 {
			k.Fatalf("DELETE faults/c = %d %s", status, body)
		}
		wantOneAdminEvent(k, "DELETE faults/c", before, journal.AdminAction{Op: journal.FaultRuleRemoved{Rules: []ref.RuleID{"c"}}})

		before = k.Journal.NextID()
		if status, body := jsonCall(k, h, http.MethodDelete, admin.Prefix+"/faults", ""); status >= 300 {
			k.Fatalf("DELETE faults = %d %s", status, body)
		}
		wantOneAdminEvent(k, "DELETE faults", before, journal.AdminAction{Op: journal.FaultRuleRemoved{Rules: []ref.RuleID{"b", "a"}}})

		before = k.Journal.NextID()
		if status, body := jsonCall(k, h, http.MethodDelete, admin.Prefix+"/faults", ""); status >= 300 {
			k.Fatalf("DELETE faults with none = %d %s", status, body)
		}
		wantOneAdminEvent(k, "DELETE faults with none", before, journal.AdminAction{Op: journal.FaultRuleRemoved{}})
	})
}

// A call that answers an error, and a call that changes nothing, journal
// nothing: only a state change that happened is a signal.
func TestRefusedAndReadOnlyCallsJournalNothing(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		whole := &lateService{}
		h := handlerWith(k, func(o *serveradmin.Options) {
			o.Services, o.Late = []serveradmin.Service{whole}, []snapshot.Late{whole}
		})
		before := k.Journal.NextID()
		for _, req := range []struct{ method, path, body string }{
			{http.MethodPost, admin.Prefix + "/clock", `{"op": "advance", "by": "-1s"}`},
			{http.MethodPost, admin.Prefix + "/clock", `{"op": "scale", "factor": 0}`},
			{http.MethodPost, admin.Prefix + "/clock", `{"op": "set", "to": "` + k.Clock.Now().Add(-time.Hour).Wire().Format(time.RFC3339Nano) + `"}`},
			{http.MethodPost, admin.Prefix + "/reset", `{"regions":["eu-west-1"]}`},
			{http.MethodPut, admin.Prefix + "/faults/bad", `{"phase": "request"}`},
			{http.MethodDelete, admin.Prefix + "/faults/missing", ""},
			{http.MethodPut, admin.Prefix + "/snapshots", `{"path":"x.tar"}`},
			{http.MethodPost, admin.Prefix + "/snapshots", `{"path":"x.tar"}`},
		} {
			if status, body := jsonCall(k, h, req.method, req.path, req.body); status < 400 {
				k.Fatalf("%s %s %s = %d %s, want an error", req.method, req.path, req.body, status, body)
			}
		}
		for _, path := range []string{"/health", "/clock", "/faults", "/journal/events"} {
			if rec := serve(k, h, http.MethodGet, admin.Prefix+path); rec.Code != http.StatusOK {
				k.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body)
			}
		}
		if got := recordedSince(k, before); len(got) != 0 {
			kinds := make([]string, 0, len(got))
			for i := range got {
				kinds = append(kinds, journal.KindOf(got[i].Detail).String())
			}
			k.Errorf("refused and read-only calls recorded %s, want nothing", strings.Join(kinds, ", "))
		}
	})
}
