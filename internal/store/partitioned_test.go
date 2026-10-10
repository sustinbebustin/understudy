package store_test

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/store"
)

type queues struct {
	Queues map[string]int `json:"queues,omitempty"`
	Bodies []string       `json:"bodies,omitempty"`
}

func (q *queues) IsZero() bool { return len(q.Queues) == 0 && len(q.Bodies) == 0 }

func (q *queues) BlobRefs() []string { return q.Bodies }

func newQueues[K comparable]() *store.Partitioned[K, queues] {
	return store.NewPartitioned(&store.Gate{}, jsonCodec(), func(K) queues { return queues{} })
}

// newQueuesWithEmptyMap starts every cell with a non-nil, empty map, the
// pattern NewPartitioned allows so handlers can write without a nil check.
func newQueuesWithEmptyMap[K comparable]() *store.Partitioned[K, queues] {
	return store.NewPartitioned(&store.Gate{}, jsonCodec(), func(K) queues { return queues{Queues: map[string]int{}} })
}

func drawQueues(t *rapid.T) queues {
	return queues{
		Queues: rapid.MapOf(rapid.StringMatching(`[a-z]{1,8}`), rapid.IntRange(0, 1000)).Draw(t, "queues"),
		Bodies: rapid.SliceOf(rapid.StringMatching(`[0-9a-f]{4}`)).Draw(t, "bodies"),
	}
}

func fill[K comparable](t *rapid.T, p *store.Partitioned[K, queues], keys []K) {
	for _, k := range keys {
		s := drawQueues(t)
		store.Update(p.For(k), func(q *queues) (struct{}, bool) { *q = s; return struct{}{}, true })
	}
}

// exportImportExport exports src, imports the sections into a Partitioned
// made by newP and exports that again. It checks the two exports are
// identical and that the import recreated exactly the non-zero cells under
// the same keys, so For on an original key finds its imported state. It
// returns the imported Partitioned.
func exportImportExport[K comparable](t *rapid.T, newP func() *store.Partitioned[K, queues], kc store.KeyCodec[K], src *store.Partitioned[K, queues]) *store.Partitioned[K, queues] {
	first, err := src.Export(kc, 3)
	if err != nil {
		t.Fatalf("first Export: %v", err)
	}
	dst := newP()
	if err := dst.Import(kc, 3, first); err != nil {
		t.Fatalf("Import: %v", err)
	}
	second, err := dst.Export(kc, 3)
	if err != nil {
		t.Fatalf("second Export: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("export after import differs:\nfirst  %+v\nsecond %+v", first, second)
	}
	if !slices.IsSortedFunc(first, func(a, b store.Section) int { return strings.Compare(a.Path, b.Path) }) {
		t.Fatalf("sections are not sorted by path: %+v", first)
	}
	nonZero := slices.DeleteFunc(src.Keys(kc), func(k K) bool {
		c, _ := src.Lookup(k)
		return store.Read(c, func(q *queues) bool { return q.IsZero() })
	})
	if got := dst.Keys(kc); !slices.Equal(got, nonZero) {
		t.Fatalf("Import recreated keys %v, want the non-zero source keys %v", got, nonZero)
	}
	return dst
}

func TestPartitionedExportImportExportIsByteIdentical(t *testing.T) {
	t.Parallel()
	t.Run("string keys", func(t *testing.T) {
		t.Parallel()
		rapid.Check(t, func(t *rapid.T) {
			src := newQueues[string]()
			fill(t, src, rapid.SliceOf(rapid.StringMatching(`[a-z0-9][a-z0-9.-]{2,20}`)).Draw(t, "keys"))
			exportImportExport(t, newQueues[string], store.StringKeys(), src)
		})
	})
	// The state removes entries from the map init made, which Export omits;
	// Import must not bring them back and must leave the map non-nil.
	t.Run("init makes an empty map the state then edits", func(t *testing.T) {
		t.Parallel()
		rapid.Check(t, func(t *rapid.T) {
			src := newQueuesWithEmptyMap[string]()
			for _, k := range rapid.SliceOf(rapid.StringMatching(`[a-z]{1,4}`)).Draw(t, "keys") {
				add := rapid.MapOf(rapid.StringMatching(`[a-z]{1,2}`), rapid.IntRange(0, 9)).Draw(t, "add")
				remove := rapid.SliceOf(rapid.StringMatching(`[a-z]{1,2}`)).Draw(t, "remove")
				store.Update(src.For(k), func(q *queues) (struct{}, bool) {
					maps.Copy(q.Queues, add)
					for _, name := range remove {
						delete(q.Queues, name)
					}
					return struct{}{}, true
				})
			}
			dst := exportImportExport(t, newQueuesWithEmptyMap[string], store.StringKeys(), src)
			for _, k := range dst.Keys(store.StringKeys()) {
				c, _ := dst.Lookup(k)
				if store.Read(c, func(q *queues) bool { return q.Queues == nil }) {
					t.Fatalf("imported cell %q has a nil map although init makes one", k)
				}
			}
		})
	})
}

func TestPartitionedExportSkipsZeroCellsAndCollectsBlobs(t *testing.T) {
	t.Parallel()
	p := newQueues[aws.Scope]()
	p.For(scope(t, "000000000000", "us-east-1"))
	store.Update(p.For(scope(t, "111111111111", "eu-west-1")), func(q *queues) (struct{}, bool) {
		q.Bodies = []string{"bb", "aa", "bb"}
		return struct{}{}, true
	})
	got, err := p.Export(store.ScopeKeys(), 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.Section{{
		Path:    "111111111111/eu-west-1.json",
		Version: 1,
		Data:    []byte(`{"bodies":["bb","aa","bb"]}`),
		Blobs:   []string{"aa", "bb", "bb"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Export = %+v, want %+v", got, want)
	}
}

func TestPartitionedImportRefusesMismatchedVersionNamingBoth(t *testing.T) {
	t.Parallel()
	p := newQueues[string]()
	err := p.Import(store.StringKeys(), 2, []store.Section{{Path: "b.json", Version: 5, Data: []byte(`{}`)}})
	vm, ok := errors.AsType[*store.VersionMismatchError](err)
	if !ok {
		t.Fatalf("Import error = %v, want *VersionMismatchError", err)
	}
	if vm.Section != 5 || vm.Running != 2 || !strings.Contains(err.Error(), "5") || !strings.Contains(err.Error(), "2") {
		t.Fatalf("error %q does not name both versions", err)
	}
}

func TestPartitionedImportIsAllOrNothing(t *testing.T) {
	t.Parallel()
	valid := store.Section{Path: "a.json", Version: 1, Data: []byte(`{}`)}
	type badSection struct {
		sec    store.Section
		defect store.SectionDefect
	}
	cases := map[string]badSection{
		"bad data":      {store.Section{Path: "b.json", Version: 1, Data: []byte(`{`)}, store.UndecodableState},
		"no extension":  {store.Section{Path: "b", Version: 1, Data: []byte(`{}`)}, store.MissingExtension},
		"duplicate key": {valid, store.DuplicateKey},
		// Paths Export refuses: loading them would block every later save.
		"parent escape":  {store.Section{Path: "../x.json", Version: 1, Data: []byte(`{}`)}, store.NonLocalPath},
		"empty key":      {store.Section{Path: ".json", Version: 1, Data: []byte(`{}`)}, store.NoEncoding},
		"empty element":  {store.Section{Path: "a//b.json", Version: 1, Data: []byte(`{}`)}, store.NonLocalPath},
		"absolute":       {store.Section{Path: "/abs.json", Version: 1, Data: []byte(`{}`)}, store.NonLocalPath},
		"trailing slash": {store.Section{Path: "a/.json", Version: 1, Data: []byte(`{}`)}, store.NonLocalPath},
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newQueues[string]()
			err := p.Import(store.StringKeys(), 1, []store.Section{valid, bad.sec})
			wantSectionError(t, err, bad.sec.Path, bad.defect)
			if keys := p.Keys(store.StringKeys()); len(keys) != 0 {
				t.Fatalf("Import created cells %v despite failing", keys)
			}
		})
	}
}

func TestPartitionedImportRefusesNonCanonicalPath(t *testing.T) {
	t.Parallel()
	lower := store.KeyCodec[string]{
		Encode: strings.ToLower,
		Decode: func(s string) (string, error) { return strings.ToLower(s), nil },
	}
	p := newQueues[string]()
	err := p.Import(lower, 1, []store.Section{{Path: "Bucket.json", Version: 1, Data: []byte(`{}`)}})
	wantSectionError(t, err, "Bucket.json", store.NonCanonicalPath)
}

func wantSectionError(t *testing.T, err error, path string, defect store.SectionDefect) {
	t.Helper()
	se, ok := errors.AsType[*store.SectionError](err)
	if !ok {
		t.Fatalf("error = %v, want *SectionError", err)
	}
	if se.Path != path || se.Defect != defect {
		t.Fatalf("SectionError names path %q and defect %q, want %q and %q", se.Path, se.Defect, path, defect)
	}
}

func TestPartitionedExportRefusesKeysThatEscapeTheServiceDirectory(t *testing.T) {
	t.Parallel()
	cases := map[string]store.SectionDefect{
		"../etc": store.NonLocalPath,
		"/abs":   store.NonLocalPath,
		"a//b":   store.NonLocalPath,
		"a/":     store.NonLocalPath,
		"":       store.NoEncoding,
	}
	for key, defect := range cases {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			p := newQueues[string]()
			store.Update(p.For(key), func(q *queues) (struct{}, bool) { q.Bodies = []string{"x"}; return struct{}{}, true })
			_, err := p.Export(store.StringKeys(), 1)
			wantSectionError(t, err, key+".json", defect)
		})
	}
}

// A key codec that maps two keys to one path breaks its contract; Export
// refuses it rather than write an archive that Import would refuse.
func TestPartitionedExportRefusesTwoKeysEncodingToOnePath(t *testing.T) {
	t.Parallel()
	folding := store.KeyCodec[string]{
		Encode: strings.ToLower,
		Decode: func(s string) (string, error) { return s, nil },
	}
	p := newQueues[string]()
	for _, key := range []string{"Bucket", "bucket"} {
		store.Update(p.For(key), func(q *queues) (struct{}, bool) { q.Bodies = []string{"x"}; return struct{}{}, true })
	}
	_, err := p.Export(folding, 1)
	wantSectionError(t, err, "bucket.json", store.DuplicateKey)
}

// A handler that decides inside its write to delete the cell, and deletes it
// there, never removes a cell a concurrent load put at the same key: either
// the delete runs first and the load replaces nothing, or the load retires
// the cell first and the write sees Retired and leaves the key alone.
func TestDeleteInsideUpdateNeverDropsALoadedCell(t *testing.T) {
	t.Parallel()
	const rounds = 200
	for range rounds {
		g := &store.Gate{}
		p := store.NewPartitioned(g, jsonCodec(), func(string) queues { return queues{} })
		cell := p.For("k")
		var wg sync.WaitGroup
		wg.Go(func() {
			store.Update(cell, func(*queues) (struct{}, bool) {
				if !cell.Retired() {
					p.Delete("k")
				}
				return struct{}{}, false
			})
		})
		wg.Go(func() {
			release := g.Exclusive()
			defer release()
			if err := p.Import(store.StringKeys(), 1, []store.Section{{Path: "k.json", Version: 1, Data: []byte(`{"bodies":["loaded"]}`)}}); err != nil {
				t.Error(err)
			}
		})
		wg.Wait()
		c, ok := p.Lookup("k")
		if !ok {
			t.Fatal("the delete removed the cell the load put at the key")
		}
		if got := store.Read(c, func(q *queues) []string { return slices.Clone(q.Bodies) }); !slices.Equal(got, []string{"loaded"}) {
			t.Fatalf("live cell holds %v, want the loaded state [loaded]", got)
		}
	}
}

func TestPartitionedRetiresTheCellsItDropsOrReplaces(t *testing.T) {
	t.Parallel()
	ops := map[string]func(p *store.Partitioned[string, queues]) error{
		"delete": func(p *store.Partitioned[string, queues]) error { p.Delete("k"); return nil },
		"reset": func(p *store.Partitioned[string, queues]) error {
			p.Reset(func(k string) bool { return k == "other" })
			return nil
		},
		"import": func(p *store.Partitioned[string, queues]) error {
			return p.Import(store.StringKeys(), 1, []store.Section{{Path: "k.json", Version: 1, Data: []byte(`{}`)}})
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newQueues[string]()
			old, other := p.For("k"), p.For("other")
			if err := op(p); err != nil {
				t.Fatal(err)
			}
			if !old.Retired() {
				t.Fatal("the dropped or replaced cell does not report Retired")
			}
			if other.Retired() {
				t.Fatal("a cell the operation kept reports Retired")
			}
			if live := p.For("k"); live == old || live.Retired() {
				t.Fatal("For returned the retired cell")
			}
		})
	}
}

// writeLive appends id to the cell for "k" the way a long poll does: it keeps
// *cell across calls and re-resolves it with For only when it was retired.
func writeLive(p *store.Partitioned[string, queues], cell **store.Cell[queues], id string) {
	for {
		c := *cell
		landed := store.Update(c, func(q *queues) (bool, bool) {
			if c.Retired() {
				return false, false
			}
			q.Bodies = append(q.Bodies, id)
			return true, true
		})
		if landed {
			return
		}
		*cell = p.For("k")
	}
}

// Writers that keep their cell across Resets and Imports under the exclusive
// gate never lose a write acknowledged after the last of them: each write
// either lands in the live cell or sees Retired and re-resolves.
func TestUpdatesRacingResetAndImportLandInTheLiveCell(t *testing.T) {
	t.Parallel()
	const writers, rounds, after = 4, 200, 50
	g := &store.Gate{}
	p := store.NewPartitioned(g, jsonCodec(), func(string) queues { return queues{} })
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		defer close(done)
		for i := range rounds {
			release := g.Exclusive()
			if i%2 == 0 {
				p.Reset(func(string) bool { return false })
			} else if err := p.Import(store.StringKeys(), 1, []store.Section{{Path: "k.json", Version: 1, Data: []byte(`{}`)}}); err != nil {
				t.Error(err)
			}
			release()
		}
	})
	for w := range writers {
		wg.Go(func() {
			cell := p.For("k")
			for {
				select {
				case <-done:
					for i := range after {
						writeLive(p, &cell, fmt.Sprintf("%d-%d", w, i))
					}
					return
				default:
					writeLive(p, &cell, "racing")
				}
			}
		})
	}
	wg.Wait()
	got := store.Read(p.For("k"), func(q *queues) []string { return slices.Clone(q.Bodies) })
	for w := range writers {
		for i := range after {
			if id := fmt.Sprintf("%d-%d", w, i); !slices.Contains(got, id) {
				t.Fatalf("write %s, acknowledged after the last reset, is missing from the live cell", id)
			}
		}
	}
}

func TestPartitionedLookupNeverCreates(t *testing.T) {
	t.Parallel()
	p := newQueues[string]()
	if _, ok := p.Lookup("a"); ok {
		t.Fatal("Lookup found a cell nobody created")
	}
	if keys := p.Keys(store.StringKeys()); len(keys) != 0 {
		t.Fatalf("Lookup created cells %v", keys)
	}
	c := p.For("a")
	got, ok := p.Lookup("a")
	if !ok || got != c {
		t.Fatal("Lookup did not return the cell For created")
	}
}

func TestPartitionedKeysSortByEncoding(t *testing.T) {
	t.Parallel()
	p := newQueues[aws.Scope]()
	a, b, c := scope(t, "111111111111", "eu-west-1"), scope(t, "111111111111", "us-west-2"), scope(t, "222222222222", "us-east-1")
	for _, k := range []aws.Scope{c, b, a} {
		p.For(k)
	}
	want := []aws.Scope{a, b, c}
	if got := p.Keys(store.ScopeKeys()); !slices.Equal(got, want) {
		t.Fatalf("Keys = %v, want %v", got, want)
	}
}

func TestPartitionedDeleteAndResetDropCells(t *testing.T) {
	t.Parallel()
	p := newQueues[string]()
	for _, k := range []string{"a", "b", "c", "d"} {
		store.Update(p.For(k), func(q *queues) (struct{}, bool) { q.Bodies = []string{k}; return struct{}{}, true })
	}
	p.Delete("a")
	p.Reset(func(k string) bool { return k == "c" })
	if got := p.Keys(store.StringKeys()); !slices.Equal(got, []string{"c"}) {
		t.Fatalf("Keys = %v, want [c]", got)
	}
	fresh := store.Read(p.For("a"), func(q *queues) bool { return q.IsZero() })
	if !fresh {
		t.Fatal("For after Delete did not start from init")
	}
}
