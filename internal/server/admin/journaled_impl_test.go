package admin_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
	"github.com/sustinbebustin/understudy/internal/snapshot"
)

// A page that follows the stream loads the newest events first: after a
// reset, the newest admin action the tail answers is that reset (ADR 0007,
// implementation amendment 2026-10-26).
func TestJournalTailAnswersTheLatestAdminAction(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := &lateService{}
		h := handlerWith(k, func(o *serveradmin.Options) {
			o.Services, o.Late = []serveradmin.Service{svc}, []snapshot.Late{svc}
		})
		if status, body := jsonCall(k, h, http.MethodPost, admin.Prefix+"/reset", `{"faults":true}`); status != http.StatusOK {
			k.Fatalf("reset = %d %s", status, body)
		}
		rec := serve(k, h, http.MethodGet, admin.Prefix+"/journal/events?kind=admin-action&tail=1")
		var page admin.JournalPage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK || len(page.Events) != 1 {
			k.Fatalf("GET journal/events?kind=admin-action&tail=1 = %d %s, want one event", rec.Code, rec.Body)
		}
		var detail struct {
			Op string `json:"op"`
		}
		if err := json.Unmarshal(page.Events[0].Detail, &detail); err != nil || page.Events[0].Kind != "admin-action" || detail.Op != "reset" {
			k.Errorf("newest admin action = %s %s, want the reset", page.Events[0].Kind, page.Events[0].Detail)
		}
	})
}

// A reset that fails after it dropped state journals what it reset, since
// the state changed: one whose blob release fails after the clock's return
// records the restore and then its admin action (ADR 0007, implementation
// amendment 2026-10-26).
func TestResetWhoseBlobReleaseFailsIsJournaled(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		start := k.Clock.Now()
		pinned := &blobService{refs: map[aws.Scope][]string{k.Scope: {strings.Repeat("a", 64)}}}
		h := handlerWith(k, func(o *serveradmin.Options) {
			o.Services, o.Blobs = []serveradmin.Service{pinned}, failingRelease{}
			o.ClockStart = func() (clock.Instant, clock.Mode) { return start, clock.Frozen{} }
		})
		if _, err := k.Clock.Advance(time.Hour); err != nil {
			k.Fatal(err)
		}
		moved := k.Clock.Mode()
		before := k.Journal.NextID()
		if rec := reset(k, h, `{"clock":true}`); rec.Code != http.StatusInternalServerError {
			k.Fatalf("reset whose blob release fails = %d %s, want 500", rec.Code, rec.Body)
		}
		got := recordedSince(k, before)
		if len(got) != 2 {
			k.Fatalf("reset whose blob release fails recorded %v, want the clock's return then an admin action", got)
		}
		wantAdminEvent(k, "failed reset's clock return", got[0], journal.ClockChanged{From: moved, To: clock.Frozen{At: start}, Op: journal.ClockRestore})
		wantAdminEvent(k, "reset whose blob release fails", got[1], journal.AdminAction{Op: journal.Reset{Services: []string{"blobs"}, Clock: true}})
	})
}

// A reset whose clock restore fails journals the state and fault rules it
// dropped, without the clock, which it did not restore.
func TestResetWhoseClockRestoreFailsIsJournaled(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handlerWith(k, func(o *serveradmin.Options) {
			o.Services = []serveradmin.Service{&rearmCounter{}}
			// An instant beyond clock.MaxSkew, which Restore refuses.
			o.ClockStart = func() (clock.Instant, clock.Mode) { return k.Clock.Now().Add(2 * clock.MaxSkew), clock.Frozen{} }
		})
		if _, err := k.Faults.Add(fault.Rule{Phase: fault.Delivery, Action: fault.Latency{Wall: time.Second}, Trigger: fault.Always{}}); err != nil {
			k.Fatal(err)
		}
		before := k.Journal.NextID()
		if rec := reset(k, h, `{"clock":true,"faults":true}`); rec.Code != http.StatusInternalServerError {
			k.Fatalf("reset whose clock restore fails = %d %s, want 500", rec.Code, rec.Body)
		}
		wantOneAdminEvent(k, "reset whose clock restore fails", before, journal.AdminAction{Op: journal.Reset{Services: []string{"late"}, Faults: 1}})
	})
}

// A reset that fails at its first step, before it dropped anything,
// journals nothing.
func TestResetThatFailsBeforeDroppingStateJournalsNothing(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		late := &failingLateService{}
		h := handlerWith(k, func(o *serveradmin.Options) {
			o.Services, o.Late = []serveradmin.Service{late}, []snapshot.Late{late}
		})
		before := k.Journal.NextID()
		if rec := reset(k, h, `{}`); rec.Code != http.StatusInternalServerError {
			k.Fatalf("reset whose late data cannot be dropped = %d %s, want 500", rec.Code, rec.Body)
		}
		if got := recordedSince(k, before); len(got) != 0 {
			k.Errorf("reset that dropped nothing recorded %v, want nothing", got)
		}
	})
}

// A snapshot load that fails after it began replacing state, which leaves
// the state partially loaded, journals its admin action.
func TestPartialSnapshotLoadIsJournaled(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		late := &failingLateService{}
		h := handlerWith(k, func(o *serveradmin.Options) {
			blobs, err := blob.NewMemory(k.TempDir(), 0)
			if err != nil {
				k.Fatal(err)
			}
			k.Cleanup(func() {
				if err := blobs.Close(); err != nil {
					k.Error(err)
				}
			})
			coord, err := snapshot.NewCoordinator(snapshot.Config{
				Gate: k.Kernel.Gate(), Blobs: blobs, Runtime: fixedRuntime{now: k.Clock.Now()}, Effects: k.Kernel, Late: []snapshot.Late{late}, Writer: "test",
			})
			if err != nil {
				k.Fatal(err)
			}
			o.Snapshots, o.Blobs = coord, blobs
			o.Services, o.Late = []serveradmin.Service{late}, []snapshot.Late{late}
		})
		saved := serve(k, h, http.MethodPost, admin.Prefix+"/snapshots")
		if saved.Code != http.StatusOK {
			k.Fatalf("save = %d %s", saved.Code, saved.Body)
		}
		before := k.Journal.NextID()
		rec := load(k, h, bytes.NewReader(saved.Body.Bytes()))
		if e := envelope(k, rec); rec.Code != http.StatusInternalServerError || !strings.Contains(e.Message, "partially loaded") {
			k.Fatalf("load whose late import fails = %d %+v, want 500 naming a partial load", rec.Code, e)
		}
		wantOneAdminEvent(k, "partial load", before, journal.AdminAction{Op: journal.SnapshotLoad{}})
	})
}

// A HEAD answer carries the GET answer's headers, Content-Length included,
// and no body, on every JSON route.
func TestHeadAnswersCarryTheGetHeadersWithoutABody(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "")
		for _, path := range []string{"/clock", "/faults", "/journal/events?tail=5", "/ui/session"} {
			get := serve(k, h, http.MethodGet, admin.Prefix+path)
			head := serve(k, h, http.MethodHead, admin.Prefix+path)
			if get.Code != http.StatusOK || head.Code != http.StatusOK || head.Body.Len() != 0 ||
				head.Header().Get("Content-Type") != get.Header().Get("Content-Type") ||
				head.Header().Get("Content-Length") != strconv.Itoa(get.Body.Len()) {
				k.Errorf("HEAD %s = %d %v with %d body bytes; GET = %d with %d bytes; want the GET headers, its length and no body",
					path, head.Code, head.Header(), head.Body.Len(), get.Code, get.Body.Len())
			}
		}
	})
}

// A page's call that carries no session value is refused with today's
// hint, which names the tools that send neither header; Sec-Fetch-Site
// sent twice is not same-origin, the secret notwithstanding (ADR 0020).
func TestBrowserRefusalsWithoutTheUIPass(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := uiHandler(k, &lateService{}, nil)
		rec := pageCall(k, h, http.MethodGet, admin.Prefix+"/clock", "", http.Header{"Sec-Fetch-Site": {"same-origin"}})
		if e := envelope(k, rec); rec.Code != http.StatusForbidden || !strings.Contains(e.Hint, "curl") || strings.Contains(e.Hint, "/_understudy/ui/") {
			k.Errorf("page call without a session = %d %+v, want 403 with the hint naming curl", rec.Code, e)
		}
		twice := http.Header{"Sec-Fetch-Site": {"same-origin", "same-origin"}, admin.UISessionHeader: {uiSecret}}
		wantRefused(k, h, "GET clock with Sec-Fetch-Site twice", http.MethodGet, admin.Prefix+"/clock", twice)
	})
}

// Only a save's exact temporary name, <target>.<26 characters of A-Z and
// 2-7>.tmp, is left out of the list; a name merely like it is an archive.
func TestSnapshotListLeavesOutOnlyTheExactTemporaryName(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		dir, h := snapshotsDir(k)
		mod := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
		listed := []string{
			".ABCDEFGHIJKLMNOPQRSTUVWXYZ.tmp",     // no target before the random part
			"a.tar.ABCDEFGHIJKLMNOPQRSTUVWXY.tmp", // 25 characters
			"a.tar.ABCDEFGHIJKLMNOPQRSTUVWXYZ.tar",
			"a.tar.abcdefghijklmnopqrstuvwxyz.tmp", // outside rand.Text's alphabet
			"a.tar.ABCDEFGHIJKLMNOPQRSTUVWXY1.tmp",
		}
		for _, name := range append(slices.Clone(listed), "a.tar.ABCDEFGHIJKLMNOPQRSTUVWXYZ.tmp") {
			writeFile(k, dir, name, 1, mod)
		}
		rec := serve(k, h, http.MethodGet, admin.Prefix+"/snapshots")
		var got admin.SnapshotList
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
			k.Fatalf("GET snapshots = %d %s", rec.Code, rec.Body)
		}
		names := make([]string, 0, len(got.Snapshots))
		for _, f := range got.Snapshots {
			names = append(names, f.Path)
		}
		slices.Sort(listed)
		if !slices.Equal(names, listed) {
			k.Errorf("GET snapshots = %v, want %v", names, listed)
		}
	})
}

// An archive a save writes to a path is listed at that path with the size
// the save reported, and its temporary file is gone.
func TestSavedArchiveIsListed(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		_, h := snapshotsDir(k)
		status, body := jsonCall(k, h, http.MethodPost, admin.Prefix+"/snapshots", `{"path":"snaps/one.tar"}`)
		var info admin.SnapshotInfo
		if status != http.StatusOK || json.Unmarshal([]byte(body), &info) != nil {
			k.Fatalf("save to a path = %d %s", status, body)
		}
		rec := serve(k, h, http.MethodGet, admin.Prefix+"/snapshots")
		var got admin.SnapshotList
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
			k.Fatalf("GET snapshots = %d %s", rec.Code, rec.Body)
		}
		if len(got.Snapshots) != 1 || got.Snapshots[0].Path != "snaps/one.tar" || got.Snapshots[0].Bytes != info.Bytes {
			k.Errorf("GET snapshots after a save = %+v, want snaps/one.tar of %d bytes", got.Snapshots, info.Bytes)
		}
	})
}

// A fault rule put again under its id replaces it and journals another
// addition.
func TestReplacingAFaultRuleJournalsAnAddition(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "")
		rule, err := json.Marshal(admin.FaultRule{Phase: admin.DeliveryPhase, Action: admin.Latency{Wall: time.Second}, Trigger: admin.Always{}})
		if err != nil {
			k.Fatal(err)
		}
		for range 2 {
			before := k.Journal.NextID()
			if status, body := jsonCall(k, h, http.MethodPut, admin.Prefix+"/faults/same", string(rule)); status != http.StatusOK {
				k.Fatalf("PUT faults/same = %d %s", status, body)
			}
			wantOneAdminEvent(k, "PUT faults/same", before, journal.AdminAction{Op: journal.FaultRuleAdded{Rule: "same"}})
		}
		if len(k.Faults.List()) != 1 {
			k.Errorf("rules after putting one id twice = %v, want one", k.Faults.List())
		}
	})
}
