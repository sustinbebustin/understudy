package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
	"github.com/sustinbebustin/understudy/internal/snapshot"
	"github.com/sustinbebustin/understudy/internal/store"
)

// fixedRuntime is a snapshot runtime with nothing around the cells.
type fixedRuntime struct{ now clock.Instant }

func (r fixedRuntime) Head() (snapshot.Head, error) {
	return snapshot.Head{Clock: snapshot.Clock{Mode: clock.Frozen{}, At: r.now}}, nil
}
func (fixedRuntime) RestoreClock(snapshot.Clock) error    { return nil }
func (fixedRuntime) RestoreUnderGate(snapshot.Head) error { return nil }
func (r fixedRuntime) Now() clock.Instant                 { return r.now }

// handler builds the admin handler over k with no services, marked ready.
func handler(k *kerneltest.K, token string) *serveradmin.Handler {
	k.Helper()
	return handlerWith(k, func(o *serveradmin.Options) { o.Token = token })
}

// handlerWith builds the admin handler over k, marked ready, with the
// options set adjusts.
func handlerWith(k *kerneltest.K, set func(*serveradmin.Options)) *serveradmin.Handler {
	k.Helper()
	blobs, err := blob.NewMemory(k.TempDir(), 0)
	if err != nil {
		k.Fatal(err)
	}
	k.Cleanup(func() {
		if err := blobs.Close(); err != nil {
			k.Error(err)
		}
	})
	coord, err := snapshot.NewCoordinator(snapshot.Config{Gate: k.Kernel.Gate(), Blobs: blobs, Runtime: fixedRuntime{now: k.Clock.Now()}, Effects: k.Kernel, Writer: "test"})
	if err != nil {
		k.Fatal(err)
	}
	o := serveradmin.Options{
		Kernel: k.Kernel, Faults: k.Faults, Access: k.Access, Snapshots: coord, Blobs: blobs, Regions: aws.KnownRegions(),
		ClockStart: func() (clock.Instant, clock.Mode) { return k.Clock.Now(), clock.Frozen{} },
		Uptime:     func() time.Duration { return time.Second },
	}
	set(&o)
	h, err := serveradmin.New(o)
	if err != nil {
		k.Fatal(err)
	}
	h.SetReady(netip.MustParseAddrPort("127.0.0.1:4566"))
	return h
}

func serve(k *kerneltest.K, h http.Handler, method, path string) *httptest.ResponseRecorder {
	k.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(k.Context(), method, path, http.NoBody))
	return rec
}

// reset sends POST /reset with a JSON body.
func reset(k *kerneltest.K, h http.Handler, body string) *httptest.ResponseRecorder {
	k.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(k.Context(), http.MethodPost, admin.Prefix+"/reset", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

// lateService is a Stateful whose data also lives outside the cells, as
// DynamoDB's items do; it records what resets ask of it.
type lateService struct {
	resets      []kernel.ResetScope
	lateImports int
}

func (*lateService) Name() ref.ServiceID                                 { return "late" }
func (*lateService) SchemaVersion() int                                  { return 1 }
func (*lateService) Export() ([]store.Section, error)                    { return nil, nil }
func (*lateService) Import([]store.Section) error                        { return nil }
func (s *lateService) Reset(rs kernel.ResetScope)                        { s.resets = append(s.resets, rs) }
func (*lateService) Rearm(clock.Instant)                                 {}
func (*lateService) Resources(aws.Scope) []kernel.Resource               { return nil }
func (*lateService) Scopes() []aws.Scope                                 { return nil }
func (*lateService) ExportLate(context.Context) ([]store.Section, error) { return nil, nil }
func (s *lateService) ImportLate(context.Context, []store.Section) error {
	s.lateImports++
	return nil
}

// scopedLateService can also drop its late data by account and region.
type scopedLateService struct {
	lateService
	lateResets []kernel.ResetScope
}

func (s *scopedLateService) ResetLate(_ context.Context, rs kernel.ResetScope) error {
	s.lateResets = append(s.lateResets, rs)
	return nil
}

// A reset by account or region refuses, before dropping anything, a Late
// service that can only drop its data whole, rather than answer success
// over data it kept; one with ResetLate is asked to drop that scope.
func TestScopedResetOfLateData(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		whole := &lateService{}
		h := handlerWith(k, func(o *serveradmin.Options) {
			o.Services, o.Late = []serveradmin.Service{whole}, []snapshot.Late{whole}
		})
		rec := reset(k, h, `{"regions":["eu-west-1"]}`)
		var env admin.Envelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error == nil || env.Error.Code != admin.Unavailable ||
			!strings.Contains(env.Error.Message, "late") || len(whole.resets) != 0 || whole.lateImports != 0 {
			k.Fatalf("scoped reset of whole-only late data = %d %s; resets %v, late imports %d", rec.Code, rec.Body, whole.resets, whole.lateImports)
		}
		if rec := reset(k, h, `{}`); rec.Code != http.StatusOK || len(whole.resets) != 1 || whole.lateImports != 1 {
			k.Fatalf("unscoped reset = %d %s; resets %v, late imports %d", rec.Code, rec.Body, whole.resets, whole.lateImports)
		}

		scoped := &scopedLateService{}
		h = handlerWith(k, func(o *serveradmin.Options) {
			o.Services, o.Late = []serveradmin.Service{scoped}, []snapshot.Late{scoped}
		})
		euWest1, err := aws.KnownRegions().Parse("eu-west-1")
		if err != nil {
			k.Fatal(err)
		}
		want := []kernel.ResetScope{{Regions: []aws.Region{euWest1}}}
		if rec := reset(k, h, `{"regions":["eu-west-1"]}`); rec.Code != http.StatusOK ||
			!reflect.DeepEqual(scoped.lateResets, want) || !reflect.DeepEqual(scoped.resets, want) || scoped.lateImports != 0 {
			k.Fatalf("scoped reset with ResetLate = %d %s; late resets %v, resets %v", rec.Code, rec.Body, scoped.lateResets, scoped.resets)
		}
	})
}

// blobService holds, per scope, state that pins blob references, as S3
// objects and Lambda code do.
type blobService struct {
	refs map[aws.Scope][]string
}

func (*blobService) Name() ref.ServiceID { return "blobs" }
func (*blobService) SchemaVersion() int  { return 1 }
func (s *blobService) Export() ([]store.Section, error) {
	out := make([]store.Section, 0, len(s.refs))
	for scope, refs := range s.refs {
		out = append(out, store.Section{Path: scope.Region.String() + ".json", Version: 1, Data: []byte("{}"), Blobs: refs})
	}
	return out, nil
}
func (*blobService) Import([]store.Section) error { return nil }
func (s *blobService) Reset(rs kernel.ResetScope) {
	for scope := range s.refs {
		if rs.Covers(scope) {
			delete(s.refs, scope)
		}
	}
}
func (*blobService) Rearm(clock.Instant)                   {}
func (*blobService) Resources(aws.Scope) []kernel.Resource { return nil }
func (s *blobService) Scopes() []aws.Scope                 { return slices.Collect(maps.Keys(s.refs)) }

// releases records the blob references a reset releases.
type releases struct{ refs []string }

func (r *releases) Release(b blob.Ref) error {
	r.refs = append(r.refs, b.String())
	return nil
}

// A reset releases exactly the blob references the state it drops held: a
// scoped reset keeps the references of the scopes it leaves, including a
// blob both scopes share, which loses only the dropped scope's reference.
func TestResetReleasesTheBlobsOfDroppedState(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		scopeIn := func(region string) aws.Scope {
			reg, err := aws.KnownRegions().Parse(region)
			if err != nil {
				k.Fatal(err)
			}
			return aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: reg}
		}
		east, west, shared := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
		svc := &blobService{refs: map[aws.Scope][]string{
			scopeIn("us-east-1"): {east, shared},
			scopeIn("eu-west-1"): {west, shared},
		}}
		released := &releases{}
		h := handlerWith(k, func(o *serveradmin.Options) {
			o.Services, o.Blobs = []serveradmin.Service{svc}, released
		})
		if rec := reset(k, h, `{"regions":["eu-west-1"]}`); rec.Code != http.StatusOK {
			k.Fatalf("scoped reset = %d %s", rec.Code, rec.Body)
		}
		if got, want := sorted(released.refs), []string{west, shared}; !reflect.DeepEqual(got, want) {
			k.Fatalf("scoped reset released %v, want %v", got, want)
		}
		released.refs = nil
		if rec := reset(k, h, `{}`); rec.Code != http.StatusOK {
			k.Fatalf("reset = %d %s", rec.Code, rec.Body)
		}
		if got, want := sorted(released.refs), []string{east, shared}; !reflect.DeepEqual(got, want) {
			k.Fatalf("reset released %v, want %v", got, want)
		}
	})
}

// failingPrune is a blob store whose Prune fails, as a lost disk would.
type failingPrune struct{ *blob.Store }

func (failingPrune) Prune() error { return errors.New("the disk is gone") }

// rearmCounter is a service that counts the rearms the handler asks of it.
type rearmCounter struct {
	lateService
	rearms int
}

func (c *rearmCounter) Rearm(clock.Instant) { c.rearms++ }

// A load that replaced the state but could not free the replaced state's
// blobs succeeded: it answers 200 with the archive's summary, and the
// handler does not rearm the services Load already rearmed.
func TestSnapshotLoadSucceedsWhenFreeingBlobsFails(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		blobs, err := blob.NewMemory(k.TempDir(), 0)
		if err != nil {
			k.Fatal(err)
		}
		k.Cleanup(func() {
			if err := blobs.Close(); err != nil {
				k.Error(err)
			}
		})
		coord, err := snapshot.NewCoordinator(snapshot.Config{Gate: k.Kernel.Gate(), Blobs: failingPrune{blobs}, Runtime: fixedRuntime{now: k.Clock.Now()}, Effects: k.Kernel, Writer: "test"})
		if err != nil {
			k.Fatal(err)
		}
		svc := &rearmCounter{}
		h := handlerWith(k, func(o *serveradmin.Options) {
			o.Snapshots, o.Services = coord, []serveradmin.Service{svc}
		})
		saved := serve(k, h, http.MethodPost, admin.Prefix+"/snapshots")
		if saved.Code != http.StatusOK {
			k.Fatalf("save = %d %s", saved.Code, saved.Body)
		}
		rec := load(k, h, bytes.NewReader(saved.Body.Bytes()))
		var info admin.SnapshotInfo
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &info) != nil || info.Writer != "test" {
			k.Fatalf("load with a failing prune = %d %s, want 200 with the archive's summary", rec.Code, rec.Body)
		}
		if svc.rearms != 0 {
			k.Errorf("the handler rearmed %d times after a load that rearmed itself", svc.rearms)
		}
	})
}

// load sends PUT /snapshots with the archive body.
func load(k *kerneltest.K, h http.Handler, body io.Reader) *httptest.ResponseRecorder {
	k.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(k.Context(), http.MethodPut, admin.Prefix+"/snapshots", body)
	req.Header.Set("Content-Type", admin.Tar)
	h.ServeHTTP(rec, req)
	return rec
}

// One snapshot load runs at a time, since each holds its whole archive in
// memory: a load that arrives while another is still reading its upload is
// refused with conflict before reading anything, and the next load after
// the first answers is served.
func TestSnapshotLoadsRunOneAtATime(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "")
		saved := serve(k, h, http.MethodPost, admin.Prefix+"/snapshots")
		if saved.Code != http.StatusOK {
			k.Fatalf("save = %d %s", saved.Code, saved.Body)
		}
		archive := saved.Body.Bytes()
		upload, uploading := io.Pipe()
		first := make(chan *httptest.ResponseRecorder)
		go func() { first <- load(k, h, upload) }()
		// The handler has read this prefix, so the first load is admitted
		// and waits for the rest of its upload.
		if _, err := uploading.Write(archive[:len(archive)/2]); err != nil {
			k.Fatal(err)
		}
		rec := load(k, h, bytes.NewReader(archive))
		var env admin.Envelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || rec.Code != http.StatusConflict || env.Error == nil || env.Error.Code != admin.Conflict {
			k.Errorf("load during another load = %d %s, want 409 conflict", rec.Code, rec.Body)
		}
		uploading.CloseWithError(errors.New("the client went away"))
		if rec := <-first; rec.Code == http.StatusOK {
			k.Errorf("load whose upload broke off = %d %s, want a failure", rec.Code, rec.Body)
		}
		if rec := load(k, h, bytes.NewReader(archive)); rec.Code != http.StatusOK {
			k.Errorf("load after the first answered = %d %s, want 200", rec.Code, rec.Body)
		}
	})
}

// pollerCounter counts the poller reconciliations the handler asks for.
type pollerCounter struct{ rearms int }

func (p *pollerCounter) RearmPollers() { p.rearms++ }

// A reset and a snapshot load each reconcile the pollers once with the
// state they leave.
func TestResetAndSnapshotLoadRearmThePollers(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		pollers := &pollerCounter{}
		h := handlerWith(k, func(o *serveradmin.Options) { o.Pollers = pollers })
		if rec := reset(k, h, `{}`); rec.Code != http.StatusOK {
			k.Fatalf("reset = %d %s", rec.Code, rec.Body)
		}
		if pollers.rearms != 1 {
			k.Fatalf("pollers rearmed %d times after a reset, want once", pollers.rearms)
		}
		saved := serve(k, h, http.MethodPost, admin.Prefix+"/snapshots")
		if saved.Code != http.StatusOK {
			k.Fatalf("save = %d %s", saved.Code, saved.Body)
		}
		if rec := load(k, h, bytes.NewReader(saved.Body.Bytes())); rec.Code != http.StatusOK {
			k.Fatalf("load = %d %s", rec.Code, rec.Body)
		}
		if pollers.rearms != 2 {
			k.Fatalf("pollers rearmed %d times after a reset and a load, want twice", pollers.rearms)
		}
	})
}

// failingLateService fails to drop its late data.
type failingLateService struct{ lateService }

func (*failingLateService) ImportLate(context.Context, []store.Section) error {
	return errors.New("the late store is gone")
}

// A reset that fails after draining still lifts the drain's block on
// invocations, so functions do not stay blocked until the next reset.
func TestFailedResetRearmsThePollers(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		pollers := &pollerCounter{}
		late := &failingLateService{}
		h := handlerWith(k, func(o *serveradmin.Options) {
			o.Pollers = pollers
			o.Services, o.Late = []serveradmin.Service{late}, []snapshot.Late{late}
		})
		if rec := reset(k, h, `{}`); rec.Code != http.StatusInternalServerError {
			k.Fatalf("reset whose late data cannot be dropped = %d %s, want 500", rec.Code, rec.Body)
		}
		if pollers.rearms != 1 {
			k.Fatalf("pollers rearmed %d times after a failed reset, want once", pollers.rearms)
		}
	})
}

// failingDrainer is the drained service, whose drain fails part way.
type failingDrainer struct{}

func (failingDrainer) Name() ref.ServiceID { return "late" }
func (failingDrainer) Drain(context.Context, kernel.ResetScope) error {
	return errors.New("a container would not stop")
}

// A reset or snapshot load whose drain fails replaces nothing but still
// lifts the block the drain may have put on invocations, once.
func TestFailedDrainRearmsThePollers(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		pollers := &pollerCounter{}
		h := handlerWith(k, func(o *serveradmin.Options) {
			o.Pollers, o.Drainer = pollers, failingDrainer{}
			o.Services = []serveradmin.Service{&lateService{}}
		})
		saved := serve(k, h, http.MethodPost, admin.Prefix+"/snapshots")
		if saved.Code != http.StatusOK {
			k.Fatalf("save = %d %s", saved.Code, saved.Body)
		}
		if rec := reset(k, h, `{}`); rec.Code != http.StatusInternalServerError {
			k.Fatalf("reset whose drain fails = %d %s, want 500", rec.Code, rec.Body)
		}
		if pollers.rearms != 1 {
			k.Fatalf("pollers rearmed %d times after a reset whose drain failed, want once", pollers.rearms)
		}
		if rec := load(k, h, bytes.NewReader(saved.Body.Bytes())); rec.Code != http.StatusInternalServerError {
			k.Fatalf("load whose drain fails = %d %s, want 500", rec.Code, rec.Body)
		}
		if pollers.rearms != 2 {
			k.Fatalf("pollers rearmed %d times after a reset and a load whose drains failed, want twice", pollers.rearms)
		}
	})
}

// A reset whose clock restore fails still reset the state and still clears
// the fault rules it was asked to; the error says what was reset and that
// the clock was not restored, and adds the blob release failure too.
func TestResetReportsAClockRestoreFailure(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := &rearmCounter{}
		pinned := &blobService{refs: map[aws.Scope][]string{k.Scope: {strings.Repeat("a", 64)}}}
		h := handlerWith(k, func(o *serveradmin.Options) {
			o.Services, o.Blobs = []serveradmin.Service{svc, pinned}, failingRelease{}
			// An instant beyond clock.MaxSkew, which Restore refuses.
			o.ClockStart = func() (clock.Instant, clock.Mode) { return k.Clock.Now().Add(2 * clock.MaxSkew), clock.Frozen{} }
		})
		if _, err := k.Faults.Add(fault.Rule{Phase: fault.Delivery, Action: fault.Latency{Wall: time.Second}, Trigger: fault.Always{}}); err != nil {
			k.Fatal(err)
		}
		rec := reset(k, h, `{"clock":true,"faults":true}`)
		var env admin.Envelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error == nil || env.Error.Code != admin.Internal {
			k.Fatalf("reset with a failing clock restore = %d %s, want internal", rec.Code, rec.Body)
		}
		for _, want := range []string{"[late blobs] was reset", "restoring the clock", "the clock was not restored", "also, ", "the disk is gone"} {
			if !strings.Contains(env.Error.Message, want) {
				k.Errorf("message %q lacks %q", env.Error.Message, want)
			}
		}
		if len(svc.resets) != 1 || svc.rearms == 0 {
			k.Errorf("resets %v, rearms %d; want the state reset and its timers rebuilt", svc.resets, svc.rearms)
		}
		if rules := k.Faults.List(); len(rules) != 0 {
			k.Errorf("fault rules after the reset = %+v, want none", rules)
		}
	})
}

// A reset asked to return the clock and remove the fault rules answers the
// mode the clock returned to and how many rules it removed; one that keeps
// them answers neither.
func TestResetAnswersTheClockModeAndRulesRemoved(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handlerWith(k, func(o *serveradmin.Options) {
			o.Services = []serveradmin.Service{&rearmCounter{}}
			o.ClockStart = func() (clock.Instant, clock.Mode) { return k.Clock.Now(), clock.Frozen{} }
		})
		for range 2 {
			if _, err := k.Faults.Add(fault.Rule{Phase: fault.Delivery, Action: fault.Latency{Wall: time.Second}, Trigger: fault.Always{}}); err != nil {
				k.Fatal(err)
			}
		}
		for body, want := range map[string]admin.ResetResult{
			`{"clock":true,"faults":true}`: {Clock: admin.Frozen, Faults: 2},
			`{}`:                           {},
		} {
			rec := reset(k, h, body)
			var got admin.ResetResult
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Clock != want.Clock || got.Faults != want.Faults {
				k.Errorf("reset %s = %d %s, want clock %q and %d rules removed", body, rec.Code, rec.Body, want.Clock, want.Faults)
			}
		}
	})
}

// failingRelease is a blob store that cannot release any blob.
type failingRelease struct{}

func (failingRelease) Release(blob.Ref) error { return errors.New("the disk is gone") }

func sorted(s []string) []string {
	return slices.Sorted(slices.Values(s))
}

// A service is enabled once it binds an operation; one that binds none
// answers 501 to every call and is reported degraded, with the reason.
func TestHealthReportsServicesWithoutOperationsDegraded(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		k.Stub(sqsapi.OpListQueues, func(context.Context, *kernel.Call, *sqsapi.ListQueuesInput) (*sqsapi.ListQueuesOutput, *awserr.Error) {
			return &sqsapi.ListQueuesOutput{}, nil
		})
		var h admin.Health
		if err := json.Unmarshal(serve(k, handler(k, ""), http.MethodGet, admin.Prefix+"/health").Body.Bytes(), &h); err != nil {
			k.Fatal(err)
		}
		if st := h.Services["sqs"]; st.Status != admin.Enabled {
			k.Errorf("health of a service with a bound operation = %+v, want enabled", st)
		}
		if st := h.Services["sns"]; st.Status != admin.Degraded || st.Reason == "" {
			k.Errorf("health of a service with no operations = %+v, want degraded with a reason", st)
		}
	}, kerneltest.Services(sqsapi.Service(), snsapi.Service()))
}

// healthOf reads GET /health.
func healthOf(k *kerneltest.K, h http.Handler) admin.Health {
	k.Helper()
	var out admin.Health
	if err := json.Unmarshal(serve(k, h, http.MethodGet, admin.Prefix+"/health").Body.Bytes(), &out); err != nil {
		k.Fatal(err)
	}
	return out
}

// clockEvent is an event any journal accepts.
func clockEvent() journal.Event {
	return journal.Event{Detail: journal.ClockChanged{From: clock.Frozen{}, To: clock.Frozen{}}}
}

// Health leaves the journal out while it records normally and reports it
// degraded once its file fails: here an event recorded after shutdown,
// which the closed file did not write.
func TestHealthReportsAJournalFileFailure(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "")
		if st := healthOf(k, h).Journal; st != (admin.ServiceStatus{}) {
			k.Fatalf("journal health while recording = %+v, want absent", st)
		}
		if err := k.Journal.Close(); err != nil {
			k.Fatal(err)
		}
		if _, err := k.Journal.Append(clockEvent()); err != nil {
			k.Fatal(err)
		}
		synctest.Wait()
		st := healthOf(k, h).Journal
		if st.Status != admin.Degraded || !strings.Contains(st.Reason, "events recorded after shutdown were not written") {
			k.Errorf("journal health after a write past Close = %+v, want degraded naming the shutdown", st)
		}
	}, kerneltest.JournalFile(filepath.Join(t.TempDir(), "journal.ndjson")))
}

// Once every event id is issued the journal records nothing more, which
// health reports once instead of per refused event.
func TestHealthReportsJournalIDsExhausted(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		if err := k.Journal.Seek(math.MaxUint64 - 1); err != nil {
			k.Fatal(err)
		}
		if _, err := k.Journal.Append(clockEvent()); err != nil {
			k.Fatal(err)
		}
		st := healthOf(k, handler(k, "")).Journal
		if st.Status != admin.Degraded || !strings.Contains(st.Reason, "every journal event id has been issued") {
			k.Errorf("journal health with every id issued = %+v, want degraded", st)
		}
	})
}

// The container listener's handler serves GET health and GET clock without
// the token, so function code can read emulator time, and nothing else.
func TestContainerServesOnlyHealthAndClock(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "s3cret").Container()
		for _, path := range []string{admin.Prefix + "/health", admin.Prefix + "/clock"} {
			if rec := serve(k, h, http.MethodGet, path); rec.Code != http.StatusOK {
				k.Errorf("GET %s on the container listener = %d %s", path, rec.Code, rec.Body)
			}
		}
		var st admin.ClockState
		if err := json.Unmarshal(serve(k, h, http.MethodGet, admin.Prefix+"/clock").Body.Bytes(), &st); err != nil || !st.Now.Equal(k.Clock.Now().Wire()) {
			k.Errorf("clock on the container listener = %+v, %v", st, err)
		}
		for _, req := range []struct{ method, path string }{
			{http.MethodPost, admin.Prefix + "/clock"},
			{http.MethodGet, admin.Prefix + "/faults"},
			{http.MethodPost, admin.Prefix + "/reset"},
		} {
			if rec := serve(k, h, req.method, req.path); rec.Code < 400 {
				k.Errorf("%s %s on the container listener = %d, want refused", req.method, req.path, rec.Code)
			}
		}
		if rec := serve(k, handler(k, "s3cret"), http.MethodGet, admin.Prefix+"/clock"); rec.Code != http.StatusUnauthorized {
			k.Errorf("GET clock on the main listener without the token = %d", rec.Code)
		}
	})
}

// tokens is a SubscriptionTokens hook that answers err for every
// subscription.
type tokens struct{ err *awserr.Error }

func (t tokens) SubscriptionToken(aws.ARN) (string, *awserr.Error) { return "", t.err }

// A hook's AWS-shaped error maps onto the admin vocabulary: a 501, a hook
// its service has but does not implement yet, is unavailable like a hook
// the build lacks, not an internal failure.
func TestHookErrorsMapOntoAdminCodes(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		for _, tc := range []struct {
			err  *awserr.Error
			code admin.ErrorCode
		}{
			{&awserr.Error{ID: awserr.NotImplemented(), Message: "not yet"}, admin.Unavailable},
			{snsapi.ErrNotFoundException("Subscription does not exist"), admin.NotFound},
			{snsapi.ErrInvalidParameterException("Invalid parameter"), admin.InvalidRequest},
			{awserr.Internalf(errors.New("bug")), admin.Internal},
		} {
			h := handlerWith(k, func(o *serveradmin.Options) { o.Subscriptions = tokens{err: tc.err} })
			rec := serve(k, h, http.MethodGet, admin.Prefix+"/sns/subscriptions/arn:aws:sns:us-east-1:000000000000:t:1/token")
			var got struct{ Error admin.Error }
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Error.Code != tc.code {
				k.Errorf("%s (%d) = %d %s, want code %s", tc.err.ID.Shape, tc.err.ID.Status, rec.Code, rec.Body, tc.code)
			}
		}
	})
}

// Settle that times out answers condition_not_met naming the pending work
// by kind, exit code 4.
func TestSettleTimeoutNamesPendingWork(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		release := make(chan struct{})
		k.Kernel.Go(kernel.InvocationRunning, func(ctx context.Context) {
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
		rec := serve(k, handler(k, ""), http.MethodGet, admin.Prefix+"/settle?timeout=2s")
		close(release)
		var env admin.Envelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			k.Fatal(err)
		}
		if rec.Code != http.StatusPreconditionFailed || env.Error.Code != admin.ConditionNotMet ||
			env.Error.Pending["invocation-running"] != 1 || env.Error.Code.ExitCode() != admin.ExitConditionNotMet {
			k.Fatalf("settle with running work = %d %s", rec.Code, rec.Body)
		}
	})
}

// faultRule draws admin rules whose every member the internal form reads.
func faultRule() *rapid.Generator[admin.FaultRule] {
	shape := rapid.SampledFrom([]string{"com.amazonaws.sqs#QueueDoesNotExist", "QueueDoesNotExist", "com.amazonaws.s3#NoSuchKey"})
	action := rapid.OneOf(
		rapid.Map(shape, func(s string) admin.FaultAction { return admin.InjectError{Shape: s, Message: "injected"} }),
		rapid.Just[admin.FaultAction](admin.Throttle{}),
		rapid.Map(rapid.Int64Range(1, int64(time.Minute)), func(n int64) admin.FaultAction { return admin.Latency{Wall: time.Duration(n)} }),
		rapid.Map(shape, func(s string) admin.FaultAction { return admin.AfterCommit{Shape: s} }),
		rapid.Map(shape, func(s string) admin.FaultAction { return admin.StatusOKError{Shape: s} }),
		rapid.Just[admin.FaultAction](admin.DropDelivery{}),
	)
	trigger := rapid.OneOf(
		rapid.Just[admin.FaultTrigger](admin.Always{}),
		rapid.Map(rapid.IntRange(1, 50), func(n int) admin.FaultTrigger { return admin.NextN{N: n} }),
		rapid.Map(rapid.Float64Range(0, 1), func(p float64) admin.FaultTrigger { return admin.Probability{P: p} }),
	)
	some := func(values ...string) *rapid.Generator[[]string] {
		return rapid.SliceOfNDistinct(rapid.SampledFrom(values), 0, 2, rapid.ID)
	}
	return rapid.Custom(func(t *rapid.T) admin.FaultRule {
		return admin.FaultRule{
			ID:    rapid.StringMatching(`[a-z][a-z0-9-]{0,10}`).Draw(t, "id"),
			Phase: rapid.SampledFrom([]admin.FaultPhase{admin.RequestPhase, admin.DeliveryPhase}).Draw(t, "phase"),
			Match: admin.FaultMatch{
				Services:   some("sqs", "sns", "lambda").Draw(t, "services"),
				Operations: some("sqs.SendMessage", "sns.Publish").Draw(t, "operations"),
				Mechanisms: some("sns-to-sqs", "s3-to-lambda").Draw(t, "mechanisms"),
				Resource:   rapid.SampledFrom([]string{"", "arn:aws:sqs:*:*:orders-*"}).Draw(t, "resource"),
				Accounts:   some("000000000000", "123456789012").Draw(t, "accounts"),
				Regions:    some("us-east-1", "eu-west-1").Draw(t, "regions"),
				Origin:     rapid.SampledFrom([]admin.FaultOrigin{"", admin.ExternalOrigin, admin.InternalOrigin}).Draw(t, "origin"),
			},
			Action:  action.Draw(t, "action"),
			Trigger: trigger.Draw(t, "trigger"),
		}
	})
}

// The public rule form mirrors fault.Rule's JSON: every admin rule decodes
// as a fault.Rule whose own encoding reads back as the same admin rule.
func TestFaultRuleMirrorsTheInternalForm(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		want := faultRule().Draw(t, "rule")
		data, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		var internal fault.Rule
		if err := json.Unmarshal(data, &internal); err != nil {
			t.Fatalf("fault.Rule does not read %s: %v", data, err)
		}
		back, err := internal.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var got admin.FaultRule
		if err := json.Unmarshal(back, &got); err != nil {
			t.Fatalf("admin.FaultRule does not read %s: %v", back, err)
		}
		if !reflect.DeepEqual(nilEmpty(got), nilEmpty(want)) {
			t.Fatalf("mirror of %s = %s (%#v)", data, back, got)
		}
	})
}

// nilEmpty makes empty and nil lists compare equal, as JSON omits both.
func nilEmpty(r admin.FaultRule) admin.FaultRule {
	for _, s := range []*[]string{&r.Match.Services, &r.Match.Operations, &r.Match.Mechanisms, &r.Match.Accounts, &r.Match.Regions} {
		if len(*s) == 0 {
			*s = nil
		}
	}
	return r
}
