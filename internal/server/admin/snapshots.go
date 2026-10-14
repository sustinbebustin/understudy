package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/snapshot"
)

// Snapshot query parameters.
const (
	pGzip    = "gzip"
	pJournal = "journal"
)

// snapshotSave cuts the state and writes the archive: in the response, or
// with a JSON {"path"} body to that path under the snapshots directory. The
// archive embeds its blobs and carries the late data, as `snapshot save`
// needs. Either form reports the effects the cut was taken without: the
// path form in its JSON body, the response form in
// admin.PendingEffectsHeader.
func (h *Handler) snapshotSave(w http.ResponseWriter, r *http.Request, _ []string) {
	v := r.URL.Query()
	gz, aerr := boolParam(v, pGzip)
	if aerr != nil {
		h.fail(w, r, aerr)
		return
	}
	withJournal, aerr := boolParam(v, pJournal)
	if aerr != nil {
		h.fail(w, r, aerr)
		return
	}
	target, aerr := h.snapshotPath(w, r)
	if aerr != nil {
		h.fail(w, r, aerr)
		return
	}
	var journalBytes []byte
	if withJournal {
		var err error
		if journalBytes, err = h.journalNDJSON(); err != nil {
			h.fail(w, r, internal("encoding the journal", err))
			return
		}
	}
	opts := snapshot.WriteOptions{Gzip: gz, Blobs: snapshot.EmbedBlobs, MaxBytes: h.o.MaxSnapshotBytes}
	if target == "" {
		h.saveToResponse(w, r, journalBytes, opts)
		return
	}
	info, aerr := h.saveToPath(r, target, journalBytes, opts)
	if aerr != nil {
		h.fail(w, r, aerr)
		return
	}
	h.ok(w, r, info)
}

// saveToResponse spools the archive to a temporary file in Options.TempDir,
// so a failure part way answers an error instead of a truncated archive,
// then sends it.
func (h *Handler) saveToResponse(w http.ResponseWriter, r *http.Request, journalBytes []byte, opts snapshot.WriteOptions) {
	ctx := r.Context()
	tmp, err := os.CreateTemp(h.o.TempDir, "understudy-snapshot-*")
	if err != nil {
		h.fail(w, r, internal("creating a temporary file for the archive", err))
		return
	}
	defer func() {
		if err := errors.Join(tmp.Close(), os.Remove(tmp.Name())); err != nil {
			h.log.LogAttrs(ctx, slog.LevelWarn, "the temporary snapshot file was not removed", slog.String("path", tmp.Name()), slog.String("error", err.Error()))
		}
	}()
	_, pending, err := h.writeArchive(ctx, tmp, journalBytes, opts)
	if err != nil {
		h.fail(w, r, saveError("saving the snapshot", err))
		return
	}
	size, err := tmp.Seek(0, io.SeekCurrent)
	if err == nil {
		_, err = tmp.Seek(0, io.SeekStart)
	}
	if err != nil {
		h.fail(w, r, internal("rewinding the archive", err))
		return
	}
	contentType := admin.Tar
	if opts.Gzip {
		contentType = admin.Gzip
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set(admin.PendingEffectsHeader, strconv.Itoa(pending))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, tmp); err != nil {
		h.log.LogAttrs(ctx, slog.LevelDebug, "snapshot archive not sent; the client went away", slog.String("error", err.Error()))
	}
}

// saveToPath writes the archive atomically to target under the data
// directory. Each save writes its own temporary file, so two saves to one
// path never share one: the last rename wins with a whole archive.
func (h *Handler) saveToPath(r *http.Request, target string, journalBytes []byte, opts snapshot.WriteOptions) (*admin.SnapshotInfo, *admin.Error) {
	ctx := r.Context()
	root := h.o.DataDir
	if dir := filepath.Dir(target); dir != "." {
		if err := root.MkdirAll(dir, 0o750); err != nil {
			return nil, internal("creating "+dir+" under the snapshots directory", err)
		}
	}
	f, tmpName, err := createTemp(root, target)
	if err != nil {
		return nil, internal("creating a temporary file next to "+target+" under the snapshots directory", err)
	}
	counted := &countingWriter{w: f}
	head, pending, werr := h.writeArchive(ctx, counted, journalBytes, opts)
	if werr == nil {
		werr = f.Sync()
	}
	if err := errors.Join(werr, f.Close()); err != nil {
		// saveError tells the caller the archive was discarded, which holds
		// only once the temporary file is gone.
		if rerr := root.Remove(tmpName); rerr != nil {
			return nil, internal("writing "+target+"; the incomplete archive "+tmpName+" was left under the snapshots directory, delete it by hand; the emulator state is unchanged", errors.Join(err, rerr))
		}
		return nil, saveError("writing "+target, err)
	}
	if err := root.Rename(tmpName, target); err != nil {
		return nil, internal("moving the archive into place at "+target, errors.Join(err, root.Remove(tmpName)))
	}
	// The rename is durable only once its directory is synced.
	if err := syncDir(root, filepath.Dir(target)); err != nil {
		return nil, internal("syncing the directory of "+target+" after moving the archive into place; the archive is there but may not survive a power loss", err)
	}
	info := describe(head)
	info.Path, info.Bytes, info.PendingEffects = target, counted.n, pending
	return &info, nil
}

// syncDir syncs the directory dir under root, making the renames in it
// durable.
func syncDir(root *os.Root, dir string) error {
	d, err := root.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// createTemp creates a new file next to target in root, named
// <target>.<random>.tmp, that no other caller has opened.
func createTemp(root *os.Root, target string) (*os.File, string, error) {
	const attempts = 10
	for range attempts {
		name := target + "." + rand.Text() + ".tmp"
		f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return f, name, err
	}
	return nil, "", fmt.Errorf("%d random names next to %s were all taken", attempts, target)
}

// writeArchive cuts the state and writes the archive with the late data to
// dst, returning the cut's manifest and how many effects of committed
// writes it was cut without, which it also logs (ADR 0009: a save names
// what it did not capture). It holds the state lock from the cut through
// the late exports, so no reset or load runs between them. An archive that
// would pass opts.MaxBytes fails part-way with *snapshot.WriteLimitError,
// and the caller discards what was written.
func (h *Handler) writeArchive(ctx context.Context, dst io.Writer, journalBytes []byte, opts snapshot.WriteOptions) (snapshot.Manifest, int, error) {
	h.stateOps.Lock()
	defer h.stateOps.Unlock()
	cut, err := h.o.Snapshots.Cut(ctx)
	if err != nil {
		return snapshot.Manifest{}, 0, fmt.Errorf("cutting the state: %w", err)
	}
	head, pending := cut.Archive().Manifest, cut.PendingEffects()
	if err := h.o.Snapshots.WriteAfterCut(ctx, dst, cut, snapshot.WithLateData, journalBytes, opts); err != nil {
		return snapshot.Manifest{}, 0, fmt.Errorf("writing the archive: %w", err)
	}
	if pending > 0 {
		h.log.LogAttrs(ctx, slog.LevelWarn, "the snapshot holds writes whose effects, such as deliveries, had not run; loading it does not run them",
			slog.Int("pending_effects", pending))
	}
	return head, pending, nil
}

// saveError maps a failed save: an archive over the size cap is the
// state's, a conflict the caller can act on; anything else is internal,
// failing op.
func saveError(op string, err error) *admin.Error {
	if big, ok := errors.AsType[*snapshot.WriteLimitError](err); ok {
		return &admin.Error{
			Code:    admin.Conflict,
			Message: big.Error() + "; it was discarded, so nothing was saved and the emulator state is unchanged",
			Hint:    "reset or delete state the snapshot does not need, or raise " + SnapshotMaxBytesSetting,
		}
	}
	return internal(op, err)
}

// SnapshotMaxBytesSetting names the serve setting MaxSnapshotBytes comes
// from, as hints and errors spell it here and in internal/server.
const SnapshotMaxBytesSetting = "snapshotMaxBytes (--snapshot-max-bytes)"

// raiseCapHint is the next step for an archive over the cap a load reads
// with.
const raiseCapHint = "if this is the archive you meant to load, restart serve with a larger " + SnapshotMaxBytesSetting + " and load it again"

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// snapshotLoad replaces the state with an archive: the request body, or
// with a JSON {"path"} body the archive at that path under the data
// directory. The archive is read and checked first; only then are the
// environments drained and the scheduler cleared, as Load requires. A load
// that arrives while another runs is refused before it reads anything.
func (h *Handler) snapshotLoad(w http.ResponseWriter, r *http.Request, _ []string) {
	if !h.loading.TryLock() {
		h.fail(w, r, &admin.Error{
			Code:    admin.Conflict,
			Message: "another snapshot load is in progress; this archive was not read and nothing was loaded",
			Hint:    "send it again once the load in progress has answered",
		})
		return
	}
	defer h.loading.Unlock()
	target, aerr := h.snapshotPath(w, r)
	if aerr != nil {
		h.fail(w, r, aerr)
		return
	}
	var src io.Reader = http.MaxBytesReader(w, r.Body, h.o.MaxSnapshotBytes)
	if target != "" {
		f, aerr := h.openArchive(target)
		if aerr != nil {
			h.fail(w, r, aerr)
			return
		}
		defer f.Close()
		src = f
	}
	a, err := snapshot.Read(src, snapshot.ReadOptions{MaxBytes: h.o.MaxSnapshotBytes, Schemas: h.o.Snapshots.Schemas()})
	if err != nil {
		h.fail(w, r, readError(err))
		return
	}
	if aerr := h.loadState(r.Context(), a); aerr != nil {
		h.fail(w, r, aerr)
		return
	}
	info := describe(a.Manifest)
	info.Path = target
	h.ok(w, r, info)
}

// loadState replaces the state with a's inside the drain bracket. It
// returns only once the drain is lifted, so the answer never reaches a
// caller whose next invocation the drain would still refuse.
func (h *Handler) loadState(ctx context.Context, a *snapshot.Archive) *admin.Error {
	h.stateOps.Lock()
	defer h.stateOps.Unlock()
	undrain, aerr := h.drain(ctx, h.stateful(), kernel.ResetScope{})
	if aerr != nil {
		return aerr
	}
	defer undrain()
	h.o.Kernel.Timers().Scheduler().Clear()
	err := h.o.Snapshots.Load(ctx, a)
	if released, ok := errors.AsType[*snapshot.BlobReleaseError](err); ok {
		// The state is loaded and Load rearmed it; only blobs of the
		// replaced state stay stored, which the caller cannot act on.
		h.log.LogAttrs(ctx, slog.LevelWarn, "snapshot loaded; some blobs of the replaced state stay stored until restart", slog.String("error", released.Error()))
		return nil
	}
	if err != nil {
		// Load rearms only the state it finished loading.
		h.rearm()
		return loadError(err)
	}
	return nil
}

// openArchive opens the archive at target under the snapshots directory. A
// missing file is not_found, a directory an invalid request, and anything
// else, such as a permission error, internal; nothing has been loaded yet.
func (h *Handler) openArchive(target string) (*os.File, *admin.Error) {
	f, err := h.o.DataDir.Open(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, &admin.Error{Code: admin.NotFound, Field: "path", Message: "no archive at " + target + " under the snapshots directory", Hint: "save one there first with 'understudy snapshot save " + target + " --on-server' (POST " + admin.Prefix + "/snapshots with {\"path\": ...})"}
	case err != nil:
		return nil, internal("opening the archive "+target+" under the snapshots directory; nothing was loaded", err)
	}
	info, err := f.Stat()
	if err == nil && !info.IsDir() {
		return f, nil
	}
	if cerr := f.Close(); cerr != nil {
		err = errors.Join(err, cerr)
	}
	if err != nil {
		return nil, internal("reading the archive "+target+" under the snapshots directory; nothing was loaded", err)
	}
	return nil, &admin.Error{Code: admin.InvalidRequest, Field: "path", Message: target + " under the snapshots directory is a directory, not an archive"}
}

// snapshotPath reads the path form's body, which needs persistence. It
// returns "" when the request carries no JSON body.
func (h *Handler) snapshotPath(w http.ResponseWriter, r *http.Request) (string, *admin.Error) {
	if !isJSON(r) {
		return "", nil
	}
	var in admin.SnapshotRequest
	if aerr := readJSON(w, r, &in, false); aerr != nil {
		return "", aerr
	}
	if h.o.DataDir == nil {
		return "", &admin.Error{
			Code:    admin.Unavailable,
			Message: "the path form of /snapshots needs persistence, and this server has none",
			Hint:    "start serve with --persist DIR, or send and receive the archive in the request body",
		}
	}
	if !filepath.IsLocal(in.Path) {
		return "", &admin.Error{Code: admin.InvalidRequest, Field: "path", Message: fmt.Sprintf("%q is not a path inside the snapshots directory (<data dir>/snapshots)", in.Path), Hint: "give a relative path without .., such as before.tar"}
	}
	return filepath.Clean(in.Path), nil
}

// isJSON reports whether the request body is JSON by its media type.
func isJSON(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}

// journalNDJSON encodes every retained journal event in id order.
func (h *Handler) journalNDJSON() ([]byte, error) {
	j := h.o.Kernel.Journal()
	var buf bytes.Buffer
	f := journal.Filter{Limit: 1000}
	for {
		p := j.Query(f)
		for i := range p.Events {
			if err := journal.EncodeNDJSON(&buf, p.Events[i]); err != nil {
				return nil, err
			}
		}
		if p.Next == 0 {
			return buf.Bytes(), nil
		}
		f.Cursor = p.Next
	}
}

// describe summarizes a manifest.
func describe(m snapshot.Manifest) admin.SnapshotInfo {
	info := admin.SnapshotInfo{
		Writer: m.Writer, ModelPin: m.ModelPin, Clock: m.Clock.At.Wire(),
		Mode: modeName(m.Clock.Mode), Sections: len(m.Sections),
	}
	seen := map[string]bool{}
	for _, s := range m.Sections {
		svc := sectionService(s.Path)
		if svc != "" && !seen[svc] {
			seen[svc] = true
			info.Services = append(info.Services, svc)
		}
	}
	return info
}

// sectionService is the service a "services/<svc>/..." section belongs to.
func sectionService(path string) string {
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 3 || parts[0] != "services" {
		return ""
	}
	return parts[1]
}

func modeName(m clock.Mode) admin.ClockMode {
	switch m.(type) {
	case clock.Frozen:
		return admin.Frozen
	case clock.Scaled:
		return admin.Scaled
	case clock.Running:
	}
	return admin.Running
}

// readError maps a failure to read an archive. One this build cannot run
// and a damaged one are conflicts, since the archive is at fault rather
// than how it was sent, and an oversized one an invalid request; any other
// failure, such as the request body breaking off, is internal. Nothing was
// loaded in every case.
func readError(err error) *admin.Error {
	if refused, ok := archiveError(err); ok {
		return refused
	}
	return internal("reading the archive; nothing was loaded", err)
}

// inspectHint is the next step for a damaged archive.
const inspectHint = "check the file with 'understudy snapshot inspect FILE', or save a new archive with 'understudy snapshot save FILE'"

// archiveError maps the errors that blame the archive itself.
func archiveError(err error) (*admin.Error, bool) {
	var (
		format   *snapshot.FormatError
		schema   *snapshot.SchemaError
		manifest *snapshot.ManifestError
		entry    *snapshot.EntryError
		size     *snapshot.SizeError
		stream   *snapshot.StreamError
		tooBig   *http.MaxBytesError
	)
	switch {
	case errors.As(err, &format), errors.As(err, &schema):
		return &admin.Error{Code: admin.Conflict, Message: err.Error(), Hint: "load the archive with the understudy build that wrote it"}, true
	case errors.As(err, &tooBig):
		return &admin.Error{Code: admin.InvalidRequest, Message: fmt.Sprintf("the archive is larger than %d bytes; nothing was loaded", tooBig.Limit), Hint: raiseCapHint}, true
	case errors.As(err, &size):
		return &admin.Error{Code: admin.InvalidRequest, Message: err.Error(), Hint: raiseCapHint}, true
	case errors.As(err, &manifest):
		// The one archive error whose text does not say nothing was loaded.
		return &admin.Error{Code: admin.Conflict, Message: err.Error() + "; nothing was loaded", Hint: inspectHint}, true
	case errors.As(err, &entry), errors.As(err, &stream):
		return &admin.Error{Code: admin.Conflict, Message: err.Error(), Hint: inspectHint}, true
	}
	return nil, false
}

// loadError maps a failed Load other than a *snapshot.BlobReleaseError,
// which is a success. After it began replacing state the failure is
// internal and the state partial; before it, a defect of the archive is
// the caller's, and anything else (waiting for a save in progress,
// making the archive's blobs available) is internal with the state
// unchanged.
func loadError(err error) *admin.Error {
	if partial, ok := errors.AsType[*snapshot.LoadError](err); ok {
		return &admin.Error{Code: admin.Internal, Message: partial.Error(), Hint: "the state is partially loaded; run 'understudy reset' (POST " + admin.Prefix + "/reset) and load again"}
	}
	if refused, ok := archiveError(err); ok {
		return refused
	}
	return &admin.Error{Code: admin.Internal, Message: "the snapshot load failed before it replaced any state, which is unchanged: " + err.Error(), Hint: "load the archive again"}
}
