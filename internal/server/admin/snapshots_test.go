package admin_test

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
// is refused as a conflict that says nothing was saved and how to raise the
// cap, leaving no file behind and the state as it was. The cap counts the
// archive decompressed, as a load does, so a gzipped save is refused too
// although its compressed bytes would fit.
func TestSnapshotSaveRefusesAnArchiveNoLoadCanRead(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		before := save(k, handler(k, ""), false)
		size := int64(len(before))
		fits := handlerWith(k, func(o *serveradmin.Options) { o.MaxSnapshotBytes = size })
		if rec := load(k, fits, bytes.NewReader(save(k, fits, false))); rec.Code != http.StatusOK {
			k.Fatalf("load of a save at the cap = %d %s, want 200", rec.Code, rec.Body)
		}
		if compressed := int64(len(save(k, fits, true))); compressed >= size-1 {
			k.Fatalf("gzipped archive is %d bytes, not under the cap %d; the case below proves nothing", compressed, size-1)
		}
		dataDir, spool := k.TempDir(), k.TempDir()
		root, err := os.OpenRoot(dataDir)
		if err != nil {
			k.Fatal(err)
		}
		k.Cleanup(func() {
			if err := root.Close(); err != nil {
				k.Error(err)
			}
		})
		over := handlerWith(k, func(o *serveradmin.Options) {
			o.MaxSnapshotBytes, o.DataDir, o.TempDir = size-1, root, spool
		})
		refusal := (&snapshot.WriteLimitError{Max: size - 1}).Error()
		for _, path := range []string{admin.Prefix + "/snapshots", admin.Prefix + "/snapshots?gzip=true"} {
			for _, body := range []string{"", `{"path": "over.tar"}`} {
				req := httptest.NewRequestWithContext(k.Context(), http.MethodPost, path, strings.NewReader(body))
				if body != "" {
					req.Header.Set("Content-Type", "application/json")
				}
				rec := httptest.NewRecorder()
				over.ServeHTTP(rec, req)
				e := envelope(k, rec)
				if rec.Code != http.StatusConflict || e.Code != admin.Conflict || !strings.Contains(e.Message, refusal) ||
					!strings.Contains(e.Message, "nothing was saved and the emulator state is unchanged") || !strings.Contains(e.Hint, "--snapshot-max-bytes") {
					k.Errorf("POST %s %s over the cap = %d %+v; want 409 conflict with %q, saying nothing was saved, hinting --snapshot-max-bytes", path, body, rec.Code, e, refusal)
				}
			}
		}
		for _, dir := range []string{dataDir, spool} {
			if left, err := os.ReadDir(dir); err != nil || len(left) != 0 {
				k.Errorf("refused saves left %v in %s (%v); want nothing", left, dir, err)
			}
		}
		if after := save(k, fits, false); !bytes.Equal(after, before) {
			k.Error("the state saved after the refused saves differs from the state before them")
		}
	})
}

// An archive over the cap a load reads with is refused naming the setting
// that raises it, whether the archive or the upload is what passes it.
func TestSnapshotLoadOverTheCapHintsTheSetting(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		archive := save(k, handler(k, ""), false)
		size := int64(len(archive))
		// A gzipped archive passes the cap only once decompressed; the plain
		// one passes it as uploaded. want is the text only that case's answer
		// carries.
		for name, cfg := range map[string]struct {
			gz   bool
			max  int64
			want string
		}{
			"decompressed": {true, size - 1, "once decompressed"},
			"uploaded":     {false, size - 1, fmt.Sprintf("the archive is larger than %d bytes", size-1)},
		} {
			body := archive
			if cfg.gz {
				body = save(k, handler(k, ""), true)
			}
			h := handlerWith(k, func(o *serveradmin.Options) { o.MaxSnapshotBytes = cfg.max })
			rec := load(k, h, bytes.NewReader(body))
			e := envelope(k, rec)
			if rec.Code != http.StatusBadRequest || !strings.Contains(e.Message, cfg.want) || !strings.Contains(e.Message, "nothing was loaded") || !strings.Contains(e.Hint, "snapshotMaxBytes (--snapshot-max-bytes)") {
				k.Errorf("load of an archive over the cap, %s = %d %+v; want 400 saying %q and that nothing was loaded, hinting snapshotMaxBytes (--snapshot-max-bytes)", name, rec.Code, e, cfg.want)
			}
		}
	})
}

// A gzipped save is the plain save compressed: snapshot.Write gzips the
// capped tar and loses nothing.
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
