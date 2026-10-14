package snapshot_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/snapshot"
	"github.com/sustinbebustin/understudy/internal/store"
)

// queues is a service state: queue attributes and message bodies held as
// blob references.
type queues struct {
	Attributes map[string]string `json:"attributes,omitempty"`
	Bodies     []string          `json:"bodies,omitempty"`
}

func (q *queues) IsZero() bool { return len(q.Attributes) == 0 && len(q.Bodies) == 0 }

func (q *queues) BlobRefs() []string { return q.Bodies }

// The fixture relies on both hooks, which Partitioned finds by type
// assertions.
var (
	_ store.Zeroer     = (*queues)(nil)
	_ store.BlobHolder = (*queues)(nil)
)

// recorder logs the calls the Coordinator makes, in order.
type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) log(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, fmt.Sprintf(format, args...))
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// service is a Stateful service over a Partitioned, the way real services
// are built.
type service struct {
	name    ref.ServiceID
	version int
	p       *store.Partitioned[string, queues]
	rec     *recorder
	// onExport, when set, runs at the start of Export.
	onExport func()
}

func newService(gate *store.Gate, name ref.ServiceID, rec *recorder) *service {
	codec := store.Codec{Marshal: jsonx.Marshal, Unmarshal: jsonx.Unmarshal}
	return &service{name: name, version: 1, rec: rec, p: store.NewPartitioned(gate, codec, func(string) queues { return queues{} })}
}

func (s *service) Name() ref.ServiceID { return s.name }
func (s *service) SchemaVersion() int  { return s.version }

func (s *service) Export() ([]store.Section, error) {
	if s.onExport != nil {
		s.onExport()
	}
	return s.p.Export(store.StringKeys(), s.version)
}

func (s *service) ResetAll() {
	s.rec.log("reset %s", s.name)
	s.p.Reset(func(string) bool { return false })
}

func (s *service) Import(ss []store.Section) error {
	s.rec.log("import %s %d sections", s.name, len(ss))
	return s.p.Import(store.StringKeys(), s.version, ss)
}

func (s *service) Rearm(now clock.Instant) { s.rec.log("rearm %s at %v", s.name, now) }

func (s *service) set(key string, q queues) {
	store.Update(s.p.For(key), func(st *queues) (struct{}, bool) { *st = q; return struct{}{}, true })
}

func (s *service) get(key string) queues {
	c, ok := s.p.Lookup(key)
	if !ok {
		return queues{}
	}
	return store.Read(c, func(st *queues) queues {
		return queues{Attributes: st.Attributes, Bodies: slices.Clone(st.Bodies)}
	})
}

// late is a Late service holding NDJSON items outside the cells, in one
// section when there are any.
type late struct {
	items []byte
	// pins are the blob references the items' section claims, which late
	// data must not have.
	pins         []string
	rec          *recorder
	exports      int
	onExportLate func()
	// onImportLate, when set, runs at the start of ImportLate; a non-nil
	// result fails the import.
	onImportLate func() error
}

func (l *late) Name() ref.ServiceID { return "dynamodb" }
func (l *late) SchemaVersion() int  { return 1 }

func (l *late) ExportLate(context.Context) ([]store.Section, error) {
	l.exports++
	if l.onExportLate != nil {
		l.onExportLate()
	}
	if l.items == nil {
		return nil, nil
	}
	return []store.Section{{Path: "000000000000/us-east-1/locks.ndjson", Version: 1, Data: l.items, Blobs: l.pins}}, nil
}

func (l *late) ImportLate(_ context.Context, ss []store.Section) error {
	l.rec.log("import late %d sections", len(ss))
	if l.onImportLate != nil {
		if err := l.onImportLate(); err != nil {
			return err
		}
	}
	l.items = nil
	for _, s := range ss {
		l.items = append(l.items, s.Data...)
	}
	return nil
}

// fixedRuntime is a fixed head. Its restores log whether the gate's
// exclusive side was held.
type fixedRuntime struct {
	head snapshot.Head
	gate *store.Gate
	rec  *recorder
	now  clock.Instant
}

func (r *fixedRuntime) Head() (snapshot.Head, error) { return r.head, nil }

func (r *fixedRuntime) RestoreClock(c snapshot.Clock) error {
	r.rec.log("restore clock %v (%s)", c.At, gateState(r.gate))
	r.head.Clock = c
	r.now = c.At
	return nil
}

func (r *fixedRuntime) RestoreUnderGate(h snapshot.Head) error {
	r.rec.log("restore journal %d (%s)", h.JournalNext, gateState(r.gate))
	h.Clock = r.head.Clock
	r.head = h
	return nil
}

func (r *fixedRuntime) Now() clock.Instant { return r.now }

func gateState(g *store.Gate) string {
	if g.TryEnter() {
		g.Leave()
		return "gate free"
	}
	return "gate held"
}

// effects is a hand-driven Effects counter. read is closed by the first
// PendingEffects call.
type effects struct {
	mu       sync.Mutex
	n        int
	changed  chan struct{}
	read     chan struct{}
	readOnce sync.Once
}

func newEffects() *effects {
	return &effects{changed: make(chan struct{}), read: make(chan struct{})}
}

func (f *effects) PendingEffects() (n int, changed <-chan struct{}) {
	f.readOnce.Do(func() { close(f.read) })
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n, f.changed
}

func (f *effects) add(d int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n += d
	close(f.changed)
	f.changed = make(chan struct{})
}

type emulator struct {
	gate    *store.Gate
	blobs   *blob.Store
	sqs     *service
	late    *late
	rt      *fixedRuntime
	effects *effects
	rec     *recorder
	coord   *snapshot.Coordinator
}

// newEmulator returns an emulator over a memory blob store.
func newEmulator(t *testing.T) *emulator {
	t.Helper()
	bs, err := blob.NewMemory(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := bs.Close(); err != nil {
			t.Error(err)
		}
	})
	return newEmulatorOver(t, bs)
}

// newDiskEmulator returns an emulator over a disk blob store in dir, as
// --persist runs with <dir>/blobs.
func newDiskEmulator(t *testing.T, dir string) *emulator {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	bs, err := blob.NewDisk(root)
	if err != nil {
		t.Fatal(err)
	}
	return newEmulatorOver(t, bs)
}

func newEmulatorOver(t *testing.T, bs *blob.Store) *emulator {
	t.Helper()
	var err error
	e := &emulator{gate: &store.Gate{}, blobs: bs, rec: &recorder{}}
	e.sqs = newService(e.gate, "sqs", e.rec)
	e.late = &late{rec: e.rec}
	e.rt = &fixedRuntime{gate: e.gate, rec: e.rec, head: snapshot.Head{
		Clock:       snapshot.Clock{Mode: clock.Frozen{}, At: instant(t, "2026-10-09T12:00:00Z")},
		JournalNext: 9,
		Faults:      []byte(`{"rules":[],"version":1}`),
	}}
	e.effects = newEffects()
	e.coord, err = snapshot.NewCoordinator(snapshot.Config{
		Gate: e.gate, Blobs: bs, Runtime: e.rt, Effects: e.effects,
		Services: []snapshot.Stateful{e.sqs}, Late: []snapshot.Late{e.late},
		Writer: "understudy v0.0.0-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *emulator) put(t *testing.T, body string) string {
	t.Helper()
	r, _, err := e.blobs.Put(context.Background(), strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	return r.String()
}

// save takes a full save, late data and blobs included.
func (e *emulator) save(t *testing.T) []byte {
	t.Helper()
	return e.saveWith(t, snapshot.WithLateData, snapshot.EmbedBlobs)
}

func (e *emulator) saveWith(t *testing.T, ld snapshot.LateData, placement snapshot.BlobPlacement) []byte {
	t.Helper()
	cut, err := e.coord.Cut(context.Background())
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	var buf bytes.Buffer
	if err := e.coord.WriteAfterCut(context.Background(), &buf, cut, ld, nil, snapshot.WriteOptions{Blobs: placement, MaxBytes: limit}); err != nil {
		t.Fatalf("WriteAfterCut: %v", err)
	}
	return buf.Bytes()
}

func (e *emulator) load(t *testing.T, saved []byte) error {
	t.Helper()
	a, err := snapshot.Read(bytes.NewReader(saved), snapshot.ReadOptions{MaxBytes: 1 << 20, Schemas: e.coord.Schemas()})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return e.coord.Load(context.Background(), a)
}

func ref256(t *testing.T, h string) blob.Ref {
	t.Helper()
	r, err := blob.ParseRef(h)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCutHoldsTheGateOnlyWhileExporting(t *testing.T) {
	t.Parallel()
	e := newEmulator(t)
	e.sqs.set("orders", queues{Attributes: map[string]string{"VisibilityTimeout": "30"}})

	exporting, resume := make(chan struct{}), make(chan struct{})
	e.sqs.onExport = func() {
		close(exporting)
		<-resume
	}
	type result struct {
		cut *snapshot.Cut
		err error
	}
	cutDone := make(chan result, 1)
	go func() {
		cut, err := e.coord.Cut(context.Background())
		cutDone <- result{cut, err}
	}()
	<-exporting

	// A write that arrives while Export runs waits for the cut.
	written := make(chan struct{})
	go func() {
		e.sqs.set("orders", queues{Attributes: map[string]string{"VisibilityTimeout": "60"}})
		close(written)
	}()
	if e.gate.TryEnter() {
		e.gate.Leave()
		t.Fatal("the gate's shared side was free while Export ran under the cut, so a write could proceed")
	}
	select {
	case <-written:
		t.Fatal("a write completed while Export ran under the cut")
	default:
	}
	close(resume)
	r := <-cutDone
	if r.err != nil {
		t.Fatalf("Cut: %v", r.err)
	}
	<-written
	if got := e.sqs.get("orders").Attributes["VisibilityTimeout"]; got != "60" {
		t.Errorf("VisibilityTimeout after the blocked write = %q; want 60", got)
	}

	// Late exports run outside the gate: a write completes during one. A
	// write that blocked here would hang the test until go test's -timeout.
	e.late.onExportLate = func() {
		done := make(chan struct{})
		go func() {
			e.sqs.set("audit", queues{Attributes: map[string]string{"x": "1"}})
			close(done)
		}()
		<-done
	}
	var buf bytes.Buffer
	if err := e.coord.WriteAfterCut(context.Background(), &buf, r.cut, snapshot.WithLateData, nil, snapshot.WriteOptions{Blobs: snapshot.EmbedBlobs, MaxBytes: limit}); err != nil {
		t.Fatalf("WriteAfterCut: %v", err)
	}
	// The archive holds the state of the cut, before either write.
	a := read(t, buf.Bytes())
	if len(a.Sections) != 1 || !strings.Contains(string(a.Sections[0].Data), `"VisibilityTimeout":"30"`) {
		t.Errorf("cut sections %+v; want the orders queue as it was at the cut", a.Sections)
	}
}

func TestSaveLoadSaveIsByteIdentical(t *testing.T) {
	t.Parallel()
	src := newEmulator(t)
	body := src.put(t, "message body")
	src.sqs.set("orders", queues{Attributes: map[string]string{"DelaySeconds": "5"}, Bodies: []string{body}})
	src.late.items = []byte("{\"LockID\":{\"S\":\"a\"}}\n")
	saved := src.save(t)

	dst := newEmulator(t)
	dst.sqs.set("stale", queues{Attributes: map[string]string{"x": "y"}})
	if err := dst.load(t, saved); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Load mirrors the save: late imports, then the clock outside the gate,
	// then the rest of the runtime head, reset and import under it, then
	// rearm. The ids, journal counter and faults move only with the gate
	// held, so no call against the old cells draws from the loaded head.
	want := []string{
		"import late 1 sections",
		"restore clock 2026-10-09T12:00:00Z (gate free)",
		"restore journal 9 (gate held)",
		"reset sqs",
		"import sqs 1 sections",
		"rearm sqs at 2026-10-09T12:00:00Z",
	}
	if got := dst.rec.list(); !slices.Equal(got, want) {
		t.Errorf("load calls\n got %q\nwant %q", got, want)
	}
	if got := dst.sqs.get("stale"); !got.IsZero() {
		t.Errorf("state from before the load survived: %+v", got)
	}
	if got := dst.sqs.get("orders"); got.Attributes["DelaySeconds"] != "5" || !slices.Equal(got.Bodies, []string{body}) {
		t.Errorf("orders queue after load %+v", got)
	}
	if again := dst.save(t); !bytes.Equal(again, saved) {
		t.Error("saving the loaded state gives different bytes")
	}

	// The load retained one reference per reference the state holds, and
	// the save released its pin.
	r := ref256(t, body)
	if err := dst.blobs.Release(r); err != nil {
		t.Errorf("the loaded state does not hold a reference to its blob: %v", err)
	}
	if _, err := dst.blobs.Size(r); !errors.As(err, new(*blob.NotFoundError)) {
		t.Errorf("after releasing the one loaded reference the blob is still stored (%v); the load or the save kept an extra reference", err)
	}
}

// A caller that drained work off the replaced state must lift the drain's
// hold before any service rearms: a Rearm can fire a due invocation or wake
// a polled queue at once, and the hold would refuse that work.
func TestLoadRunsBeforeRearmAfterTheImportAndBeforeAnyRearm(t *testing.T) {
	t.Parallel()
	src := newEmulator(t)
	src.sqs.set("orders", queues{Attributes: map[string]string{"DelaySeconds": "5"}})
	a := read(t, src.save(t))

	dst := newEmulator(t)
	err := dst.coord.LoadWith(context.Background(), a, snapshot.LoadOptions{BeforeRearm: func() {
		dst.rec.log("before rearm (%s)", gateState(dst.gate))
		if got := dst.sqs.get("orders"); got.Attributes["DelaySeconds"] != "5" {
			t.Errorf("BeforeRearm ran before the state was imported: orders = %+v", got)
		}
	}})
	if err != nil {
		t.Fatalf("LoadWith: %v", err)
	}
	want := []string{
		"import late 0 sections",
		"restore clock 2026-10-09T12:00:00Z (gate free)",
		"restore journal 9 (gate held)",
		"reset sqs",
		"import sqs 1 sections",
		"before rearm (gate free)",
		"rearm sqs at 2026-10-09T12:00:00Z",
	}
	if got := dst.rec.list(); !slices.Equal(got, want) {
		t.Errorf("load calls\n got %q\nwant %q", got, want)
	}
}

func TestFailedLoadDoesNotRunBeforeRearm(t *testing.T) {
	t.Parallel()
	src := newEmulator(t)
	a := read(t, src.save(t))

	dst := newEmulator(t)
	dst.late.onImportLate = func() error { return errors.New("dynamodb local is gone") }
	ran := false
	err := dst.coord.LoadWith(context.Background(), a, snapshot.LoadOptions{BeforeRearm: func() { ran = true }})
	if !errors.As(err, new(*snapshot.LoadError)) {
		t.Fatalf("LoadWith error %v; want *LoadError", err)
	}
	if ran {
		t.Error("BeforeRearm ran for a load that failed before importing the state; the caller's failure path owns the hold")
	}
}

func TestLoadRefusesAnotherSchemaVersionBeforeChangingAnything(t *testing.T) {
	t.Parallel()
	src := newEmulator(t)
	src.sqs.set("orders", queues{Attributes: map[string]string{"DelaySeconds": "5"}})
	a := read(t, src.save(t))

	dst := newEmulator(t)
	dst.sqs.version = 2
	dst.coord = mustCoordinator(t, dst)
	dst.sqs.set("kept", queues{Attributes: map[string]string{"x": "y"}})
	err := dst.coord.Load(context.Background(), a)
	var se *snapshot.SchemaError
	if !errors.As(err, &se) || se.Section != 1 || se.Running != 2 || se.Writer != "understudy v0.0.0-test" {
		t.Fatalf("Load error %v; want *SchemaError naming versions 1 and 2 and the writer", err)
	}
	if calls := dst.rec.list(); len(calls) != 0 {
		t.Errorf("a refused load still called %q", calls)
	}
	if got := dst.sqs.get("kept"); got.Attributes["x"] != "y" {
		t.Errorf("a refused load changed the state: %+v", got)
	}
}

func TestNewCoordinatorRefusesConflictingServices(t *testing.T) {
	t.Parallel()
	e := newEmulator(t)
	cases := map[string]snapshot.Config{
		"no gate":         {Blobs: e.blobs, Runtime: e.rt, Effects: e.effects, Writer: "w"},
		"no effects":      {Gate: e.gate, Blobs: e.blobs, Runtime: e.rt, Writer: "w"},
		"duplicate names": {Gate: e.gate, Blobs: e.blobs, Runtime: e.rt, Effects: e.effects, Writer: "w", Services: []snapshot.Stateful{e.sqs, e.sqs}},
		"bad name":        {Gate: e.gate, Blobs: e.blobs, Runtime: e.rt, Effects: e.effects, Writer: "w", Services: []snapshot.Stateful{&service{name: "a/b"}}},
		"dot name":        {Gate: e.gate, Blobs: e.blobs, Runtime: e.rt, Effects: e.effects, Writer: "w", Services: []snapshot.Stateful{&service{name: "."}}},
		"two versions":    {Gate: e.gate, Blobs: e.blobs, Runtime: e.rt, Effects: e.effects, Writer: "w", Services: []snapshot.Stateful{&service{name: "dynamodb", version: 2}}, Late: []snapshot.Late{e.late}},
	}
	for name, cfg := range cases {
		if _, err := snapshot.NewCoordinator(cfg); !errors.As(err, new(*snapshot.ConfigError)) {
			t.Errorf("%s: NewCoordinator error %v; want *ConfigError", name, err)
		}
	}
}

// A committed write's deliveries run after it releases the gate, and they
// write cells themselves (the outbox). The cut waits for them with the gate
// released, so the archive holds the delivery along with the state change.
func TestCutWaitsForTheEffectsOfCommittedWrites(t *testing.T) {
	t.Parallel()
	e := newEmulator(t)
	e.sqs.set("rules", queues{Attributes: map[string]string{"NextFire": "12:01"}})
	e.effects.add(1)
	delivered := make(chan struct{})
	go func() {
		defer close(delivered)
		// The cut finds the effect pending, under the gate, before it runs.
		<-e.effects.read
		e.sqs.set("outbox", queues{Attributes: map[string]string{"target": "worker"}})
		e.effects.add(-1)
	}()
	cut, err := e.coord.Cut(context.Background())
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	defer func() {
		if err := cut.Release(); err != nil {
			t.Error(err)
		}
	}()
	<-delivered
	if n := cut.PendingEffects(); n != 0 {
		t.Errorf("PendingEffects = %d; want 0", n)
	}
	var outbox bool
	for _, s := range cut.Archive().Sections {
		outbox = outbox || strings.Contains(string(s.Data), `"target":"worker"`)
	}
	if !outbox {
		t.Errorf("cut sections %+v; want the delivery the committed write enqueued", cut.Archive().Sections)
	}
}

// Effects that never run do not hold a save hostage: after EffectsWait the
// cut is taken anyway and reports what it misses.
func TestCutReportsEffectsStillPendingAfterTheWait(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		e := newEmulator(t)
		e.effects.add(2)
		start := time.Now()
		cut, err := e.coord.Cut(context.Background())
		if err != nil {
			t.Fatalf("Cut: %v", err)
		}
		if waited := time.Since(start); waited != snapshot.EffectsWait {
			t.Errorf("Cut returned after %v; want %v", waited, snapshot.EffectsWait)
		}
		if n := cut.PendingEffects(); n != 2 {
			t.Errorf("PendingEffects = %d; want 2", n)
		}
		if err := cut.Release(); err != nil {
			t.Fatal(err)
		}
	})
}

// A cut waiting for effects gives up when its context ends, saves nothing
// and frees the Coordinator.
func TestCutWaitingForEffectsHonorsTheContext(t *testing.T) {
	t.Parallel()
	e := newEmulator(t)
	e.effects.add(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if cut, err := e.coord.Cut(ctx); !errors.Is(err, context.Canceled) || cut != nil {
		t.Fatalf("Cut = %v, %v; want no cut and context.Canceled", cut, err)
	}
	if !e.gate.TryEnter() {
		t.Fatal("the gate is still held after the cut gave up")
	}
	e.gate.Leave()
	e.effects.add(-1)
	cut, err := e.coord.Cut(context.Background())
	if err != nil {
		t.Fatalf("Cut after the one that gave up: %v", err)
	}
	if err := cut.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadReleasesTheBlobsOfTheStateItReplaces(t *testing.T) {
	t.Parallel()
	src := newEmulator(t)
	shared := src.put(t, "shared body")
	src.sqs.set("orders", queues{Bodies: []string{shared}})
	saved := src.save(t)

	dst := newEmulator(t)
	gone, kept := dst.put(t, "body only the old state holds"), dst.put(t, "shared body")
	dst.sqs.set("orders", queues{Bodies: []string{gone, kept}})
	if err := dst.load(t, saved); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := dst.blobs.Size(ref256(t, gone)); !errors.As(err, new(*blob.NotFoundError)) {
		t.Errorf("a blob only the replaced state held is still stored (%v); the load leaked its reference", err)
	}
	r := ref256(t, kept)
	if err := dst.blobs.Release(r); err != nil {
		t.Fatalf("the loaded state does not hold the shared blob: %v", err)
	}
	if _, err := dst.blobs.Size(r); !errors.As(err, new(*blob.NotFoundError)) {
		t.Errorf("the shared blob outlived its one loaded reference (%v); the load kept the replaced state's reference", err)
	}
}

func TestLoadSurvivesAConcurrentReleaseOfABlobItNeeds(t *testing.T) {
	t.Parallel()
	src := newEmulator(t)
	body := src.put(t, "message body")
	src.sqs.set("orders", queues{Bodies: []string{body}})
	saved := src.save(t)

	// The current state holds the same blob; a client deletes it while the
	// slow late import runs.
	dst := newEmulator(t)
	dst.put(t, "message body")
	dst.sqs.set("orders", queues{Bodies: []string{body}})
	r := ref256(t, body)
	dst.late.onImportLate = func() error {
		dst.sqs.set("orders", queues{})
		return dst.blobs.Release(r)
	}
	if err := dst.load(t, saved); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := dst.sqs.get("orders"); !slices.Equal(got.Bodies, []string{body}) {
		t.Errorf("orders queue after load %+v; want the archived body", got)
	}
	if err := dst.blobs.Release(r); err != nil {
		t.Fatalf("the loaded state does not hold its blob: %v", err)
	}
	if _, err := dst.blobs.Size(r); !errors.As(err, new(*blob.NotFoundError)) {
		t.Errorf("the blob outlived its one loaded reference (%v); the load kept its pin", err)
	}
}

// The --persist round trip: the checkpoint names the blobs <dir>/blobs keeps,
// and the next start's load finds them in the store NewDisk indexed,
// retains the ones the state names and prunes the rest.
func TestCheckpointWithExternalBlobsLoadsThemFromTheStore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := newDiskEmulator(t, dir)
	body := src.put(t, "message body")
	orphan := src.put(t, "a body the state no longer names")
	src.sqs.set("orders", queues{Attributes: map[string]string{"DelaySeconds": "5"}, Bodies: []string{body}})
	saved := src.saveWith(t, snapshot.WithoutLateData, snapshot.ExternalBlobs)
	if a := read(t, saved); a.Manifest.Blobs != snapshot.ExternalBlobs || a.Blobs != nil {
		t.Fatalf("checkpoint placement %v with blob source %v; want ExternalBlobs and none", a.Manifest.Blobs, a.Blobs)
	}

	// The next start opens the same directory.
	dst := newDiskEmulator(t, dir)
	if err := dst.load(t, saved); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := dst.sqs.get("orders"); got.Attributes["DelaySeconds"] != "5" || !slices.Equal(got.Bodies, []string{body}) {
		t.Errorf("orders queue after load %+v", got)
	}
	if _, err := dst.blobs.Size(ref256(t, orphan)); !errors.As(err, new(*blob.NotFoundError)) {
		t.Errorf("a blob no loaded state names survived the load (%v); want it pruned", err)
	}
	r := ref256(t, body)
	if err := dst.blobs.Release(r); err != nil {
		t.Fatalf("the loaded state does not hold its blob: %v", err)
	}
	if _, err := dst.blobs.Size(r); !errors.As(err, new(*blob.NotFoundError)) {
		t.Errorf("the blob outlived its one loaded reference (%v); the load kept its pin", err)
	}
}

func TestLoadRefusesAnExternalBlobTheStoreLacks(t *testing.T) {
	t.Parallel()
	src := newEmulator(t)
	body := src.put(t, "message body")
	src.sqs.set("orders", queues{Bodies: []string{body}})
	saved := src.saveWith(t, snapshot.WithoutLateData, snapshot.ExternalBlobs)

	dst := newEmulator(t)
	dst.sqs.set("kept", queues{Attributes: map[string]string{"x": "y"}})
	err := dst.load(t, saved)
	if ee, ok := errors.AsType[*snapshot.EntryError](err); !ok || ee.Defect != snapshot.UnreadableBlob || ee.Name != "blobs/sha256/"+body {
		t.Fatalf("Load error %v; want an UnreadableBlob *EntryError naming the missing blob", err)
	}
	if calls := dst.rec.list(); len(calls) != 0 {
		t.Errorf("a refused load still called %q", calls)
	}
	if got := dst.sqs.get("kept"); got.Attributes["x"] != "y" {
		t.Errorf("a refused load changed the state: %+v", got)
	}
}

// No cut pins late data's blobs and no load retains them, so a late section
// that pins one fails the save.
func TestWriteAfterCutRefusesLateSectionsThatPinBlobs(t *testing.T) {
	t.Parallel()
	e := newEmulator(t)
	e.late.items = []byte("{\"LockID\":{\"S\":\"a\"}}\n")
	e.late.pins = []string{e.put(t, "item attachment")}
	cut, err := e.coord.Cut(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = e.coord.WriteAfterCut(context.Background(), &buf, cut, snapshot.WithLateData, nil, snapshot.WriteOptions{Blobs: snapshot.EmbedBlobs, MaxBytes: limit})
	if err == nil || !strings.Contains(err.Error(), "late data holds no blobs") || buf.Len() != 0 {
		t.Errorf("error %v after %d bytes; want a refusal naming the rule before writing", err, buf.Len())
	}
}

func TestFailedLoadPrunesTheBlobsItImported(t *testing.T) {
	t.Parallel()
	src := newEmulator(t)
	src.sqs.set("orders", queues{Bodies: []string{src.put(t, "message body")}})
	saved := src.save(t)

	dst := newEmulator(t)
	dst.late.onImportLate = func() error { return errors.New("the DynamoDB Local container is gone") }
	err := dst.load(t, saved)
	if !errors.As(err, new(*snapshot.LoadError)) {
		t.Fatalf("Load error %v; want *snapshot.LoadError", err)
	}
	if refs := dst.blobs.Refs(); len(refs) != 0 {
		t.Errorf("a failed load left the blobs it imported: %v", refs)
	}
}

func TestLoadReplacesOnlyTheLateDataTheArchiveCaptured(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		save      snapshot.LateData
		items     []byte
		services  []ref.ServiceID
		exports   int
		wantItems []byte
		wantCalls []string
	}{
		"full save": {
			save: snapshot.WithLateData, items: []byte("{\"LockID\":{\"S\":\"a\"}}\n"),
			services: []ref.ServiceID{"dynamodb"}, exports: 1,
			wantItems: []byte("{\"LockID\":{\"S\":\"a\"}}\n"), wantCalls: []string{"import late 1 sections"},
		},
		// A captured service with no data still replaces the current data.
		"full save of no items": {
			save: snapshot.WithLateData, services: []ref.ServiceID{"dynamodb"}, exports: 1,
			wantCalls: []string{"import late 0 sections"},
		},
		// The --persist checkpoint: DynamoDB Local keeps the items.
		"save without late data": {
			save: snapshot.WithoutLateData, items: []byte("{\"LockID\":{\"S\":\"a\"}}\n"),
			wantItems: []byte("current items\n"),
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			src := newEmulator(t)
			src.late.items = c.items
			saved := src.saveWith(t, c.save, snapshot.EmbedBlobs)
			if src.late.exports != c.exports {
				t.Errorf("ExportLate ran %d times; want %d", src.late.exports, c.exports)
			}
			if got := read(t, saved).Manifest.LateServices; !slices.Equal(got, c.services) {
				t.Errorf("manifest late services %q; want %q", got, c.services)
			}

			dst := newEmulator(t)
			dst.late.items = []byte("current items\n")
			if err := dst.load(t, saved); err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !bytes.Equal(dst.late.items, c.wantItems) {
				t.Errorf("late items after load %q; want %q", dst.late.items, c.wantItems)
			}
			var lateCalls []string
			for _, call := range dst.rec.list() {
				if strings.HasPrefix(call, "import late") {
					lateCalls = append(lateCalls, call)
				}
			}
			if !slices.Equal(lateCalls, c.wantCalls) {
				t.Errorf("late import calls %q; want %q", lateCalls, c.wantCalls)
			}
		})
	}
}

func TestWriteAfterCutRefusesAReleasedOrForeignCut(t *testing.T) {
	t.Parallel()
	e, other := newEmulator(t), newEmulator(t)
	var buf bytes.Buffer
	write := func(c *snapshot.Coordinator, cut *snapshot.Cut, ld snapshot.LateData) error {
		return c.WriteAfterCut(context.Background(), &buf, cut, ld, nil, snapshot.WriteOptions{Blobs: snapshot.EmbedBlobs, MaxBytes: limit})
	}
	wantCutError := func(what string, err error, d snapshot.CutDefect) {
		t.Helper()
		if ce, ok := errors.AsType[*snapshot.CutError](err); !ok || ce.Defect != d {
			t.Errorf("%s: error %v; want *CutError with defect %d", what, err, d)
		}
	}

	cut, err := e.coord.Cut(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantCutError("another Coordinator's cut", write(other.coord, cut, snapshot.WithLateData), snapshot.CutForeign)
	wantCutError("a nil cut", write(e.coord, nil, snapshot.WithLateData), snapshot.CutForeign)
	// The refusals left the cut to its owner.
	if err := write(e.coord, cut, snapshot.WithLateData); err != nil {
		t.Fatalf("WriteAfterCut by the owner after the refusals: %v", err)
	}
	wantCutError("a written cut", write(e.coord, cut, snapshot.WithLateData), snapshot.CutReleased)

	cut, err = e.coord.Cut(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := cut.Release(); err != nil {
		t.Fatal(err)
	}
	exports := e.late.exports
	buf.Reset()
	wantCutError("a released cut", write(e.coord, cut, snapshot.WithLateData), snapshot.CutReleased)
	if e.late.exports != exports || buf.Len() != 0 {
		t.Errorf("a refused cut still ran %d late exports and wrote %d bytes", e.late.exports-exports, buf.Len())
	}

	cut, err = e.coord.Cut(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !errors.As(write(e.coord, cut, 0), new(*snapshot.ConfigError)) {
		t.Error("WriteAfterCut accepted a zero LateData")
	}
	// The refused write still released the cut, so the Coordinator is free.
	if cut, err = e.coord.Cut(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := cut.Release(); err != nil {
		t.Fatal(err)
	}
}

// A save or load waiting behind an outstanding cut gives up when its context
// ends, without touching the state, and the cut's owner still finishes.
func TestWaitingForAnOutstandingCutHonorsTheContext(t *testing.T) {
	t.Parallel()
	e := newEmulator(t)
	saved := e.save(t)
	a := read(t, saved)
	cut, err := e.coord.Cut(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.coord.Load(ctx, a); !errors.Is(err, context.Canceled) {
		t.Errorf("Load with a cancelled context behind a cut: error %v; want context.Canceled", err)
	}
	if calls := e.rec.list(); len(calls) != 0 {
		t.Errorf("a load that gave up still called %q", calls)
	}
	if c, err := e.coord.Cut(ctx); !errors.Is(err, context.Canceled) || c != nil {
		t.Errorf("Cut with a cancelled context behind a cut: %v, error %v; want no cut and context.Canceled", c, err)
	}
	if err := cut.Release(); err != nil {
		t.Fatal(err)
	}
	if err := e.load(t, saved); err != nil {
		t.Errorf("Load after the cut was released: %v", err)
	}
}

func TestLoadRefusesANewerFormat(t *testing.T) {
	t.Parallel()
	src := newEmulator(t)
	a := read(t, src.save(t))
	a.Manifest.Format = snapshot.Format + 1

	dst := newEmulator(t)
	err := dst.coord.Load(context.Background(), a)
	if fe, ok := errors.AsType[*snapshot.FormatError](err); !ok || fe.Archive != snapshot.Format+1 {
		t.Fatalf("Load error %v; want *FormatError naming format %d", err, snapshot.Format+1)
	}
	if calls := dst.rec.list(); len(calls) != 0 {
		t.Errorf("a refused load still called %q", calls)
	}
}

func mustCoordinator(t *testing.T, e *emulator) *snapshot.Coordinator {
	t.Helper()
	c, err := snapshot.NewCoordinator(snapshot.Config{
		Gate: e.gate, Blobs: e.blobs, Runtime: e.rt, Effects: e.effects,
		Services: []snapshot.Stateful{e.sqs}, Late: []snapshot.Late{e.late},
		Writer: "understudy v0.0.0-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
