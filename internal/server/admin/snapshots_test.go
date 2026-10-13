package admin_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
	"github.com/sustinbebustin/understudy/internal/snapshot"
)

// save sends POST /snapshots, gzipped when gz, and fails unless it answers
// 200; it returns the archive.
func save(k *kerneltest.K, h http.Handler, gz bool) []byte {
	k.Helper()
	path := admin.Prefix + "/snapshots"
	if gz {
		path += "?gzip=true"
	}
	rec := serve(k, h, http.MethodPost, path)
	if rec.Code != http.StatusOK {
		k.Fatalf("save (gzip %t) = %d %s", gz, rec.Code, rec.Body)
	}
	return rec.Body.Bytes()
}

// A save never writes an archive a load with the same cap refuses: one
// that fits the cap exactly is saved and loads back, and one a byte over
// is refused as a conflict that says nothing was saved. The cap counts the
// archive decompressed, as a load does, so a gzipped save is refused too
// although its compressed bytes would fit.
func TestSnapshotSaveRefusesAnArchiveNoLoadCanRead(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		size := int64(len(save(k, handler(k, ""), false)))
		fits := handlerWith(k, func(o *serveradmin.Options) { o.MaxSnapshotBytes = size })
		if rec := load(k, fits, bytes.NewReader(save(k, fits, false))); rec.Code != http.StatusOK {
			k.Fatalf("load of a save at the cap = %d %s, want 200", rec.Code, rec.Body)
		}
		over := handlerWith(k, func(o *serveradmin.Options) { o.MaxSnapshotBytes = size - 1 })
		if compressed := int64(len(save(k, fits, true))); compressed >= size-1 {
			k.Fatalf("gzipped archive is %d bytes, not under the cap %d; the case below proves nothing", compressed, size-1)
		}
		for _, path := range []string{admin.Prefix + "/snapshots", admin.Prefix + "/snapshots?gzip=true"} {
			rec := serve(k, over, http.MethodPost, path)
			e := envelope(k, rec)
			if rec.Code != http.StatusConflict || e.Code != admin.Conflict || !strings.Contains(e.Message, "nothing was saved") || e.Hint == "" {
				k.Errorf("POST %s over the cap = %d %+v; want 409 conflict saying nothing was saved, with a hint", path, rec.Code, e)
			}
		}
	})
}

// A gzipped save is the plain save compressed: the gzip stream the handler
// writes around the capped tar loses nothing.
func TestGzippedSaveIsThePlainSaveCompressed(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "")
		plain := save(k, h, false)
		zr, err := gzip.NewReader(bytes.NewReader(save(k, h, true)))
		if err != nil {
			k.Fatal(err)
		}
		got, err := io.ReadAll(zr)
		if err != nil {
			k.Fatal(err)
		}
		if !bytes.Equal(got, plain) {
			k.Fatalf("gzipped save decompresses to %d bytes that differ from the plain save's %d", len(got), len(plain))
		}
	})
}

// stuckEffects reports one effect pending that never runs.
type stuckEffects struct{}

func (stuckEffects) PendingEffects() (n int, changed <-chan struct{}) { return 1, nil }

// A downloaded save reports the effects its cut was taken without in a
// header, as the path form does in its body, so the caller can warn that
// the archive lacks them.
func TestDownloadedSaveReportsPendingEffects(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		if got := serve(k, handler(k, ""), http.MethodPost, admin.Prefix+"/snapshots").Header().Get("Understudy-Pending-Effects"); got != "0" {
			k.Errorf("pending effects header of a settled save = %q, want 0", got)
		}
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
			coord, err := snapshot.NewCoordinator(snapshot.Config{Gate: k.Kernel.Gate(), Blobs: blobs, Runtime: fixedRuntime{now: k.Clock.Now()}, Effects: stuckEffects{}, Writer: "test"})
			if err != nil {
				k.Fatal(err)
			}
			o.Snapshots, o.Blobs = coord, blobs
		})
		rec := serve(k, h, http.MethodPost, admin.Prefix+"/snapshots")
		if got := rec.Header().Get("Understudy-Pending-Effects"); rec.Code != http.StatusOK || got != "1" {
			k.Errorf("save with an effect stuck = %d, pending effects header %q; want 200 and 1", rec.Code, got)
		}
	})
}

// A downloaded save spools its archive in Options.TempDir, the run's
// temporary directory the next start sweeps, and nowhere else.
func TestDownloadedSaveSpoolsInTempDir(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		missing := filepath.Join(k.TempDir(), "gone")
		h := handlerWith(k, func(o *serveradmin.Options) { o.TempDir = missing })
		rec := serve(k, h, http.MethodPost, admin.Prefix+"/snapshots")
		if e := envelope(k, rec); e.Code != admin.Internal || !strings.Contains(e.Message, missing) {
			k.Errorf("save spooling in a missing directory = %d %+v; want internal naming %s", rec.Code, e, missing)
		}
	})
}
