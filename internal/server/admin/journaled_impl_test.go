package admin_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
	"github.com/sustinbebustin/understudy/internal/snapshot"
)

// A page that follows the stream loads the newest events first: after a
// reset, the newest admin action the tail answers is that reset (spec: a UI
// that relies on the stream sees resets).
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
