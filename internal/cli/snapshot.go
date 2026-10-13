package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/snapshot"
)

// maxArchiveBytes caps an archive inspect and diff read; Read holds the
// whole archive in memory.
const maxArchiveBytes = 4 << 30

func snapshotCmd() *command {
	return group("snapshot", "Save, load, inspect and compare snapshot archives of the emulator's state",
		leaf("save", "Save the emulator's state to an archive", "snapshot save FILE [--gzip] [--include-journal] [--on-server]",
			[]string{"understudy snapshot save state.tar", "understudy snapshot save state.tar.gz --include-journal", "understudy snapshot save fixtures/a.tar --on-server"},
			snapshotSave),
		leaf("load", "Replace the emulator's state with an archive", "snapshot load FILE [--on-server]",
			[]string{"understudy snapshot load state.tar", "understudy snapshot load fixtures/a.tar --on-server"},
			snapshotLoad),
		leaf("inspect", "Print an archive's manifest without loading it", "snapshot inspect FILE",
			[]string{"understudy snapshot inspect state.tar", "understudy snapshot inspect state.tar --output json"},
			func(*flag.FlagSet) action { return snapshotInspect }),
		leaf("diff", "Compare two archives section by section", "snapshot diff A B [--normalize]",
			[]string{"understudy snapshot diff before.tar after.tar", "understudy snapshot diff run1.tar run2.tar --normalize --output json"},
			snapshotDiff),
	)
}

func snapshotSave(fs *flag.FlagSet) action {
	var o admin.SaveOptions
	fs.BoolVar(&o.Gzip, "gzip", false, "gzip the archive (default when FILE ends in .gz or .tgz)")
	fs.BoolVar(&o.IncludeJournal, "include-journal", false, "add the retained journal as journal.ndjson")
	onServer := fs.Bool("on-server", false, "write FILE under the server's <data dir>/snapshots instead (needs serve --persist)")
	return func(ctx context.Context, r *runner, args []string) error {
		if err := wantArgs(args, 1, "one argument, the archive path"); err != nil {
			return err
		}
		gzipSet := false
		fs.Visit(func(f *flag.Flag) { gzipSet = gzipSet || f.Name == "gzip" })
		if gzipName(args[0]) {
			if gzipSet && !o.Gzip {
				return &usageError{msg: args[0] + " names a gzipped archive, and --gzip=false asks for a plain one; drop --gzip=false, or name the file .tar"}
			}
			o.Gzip = true
		}
		c, err := r.slowClient()
		if err != nil {
			return err
		}
		if *onServer {
			info, err := c.SaveSnapshotTo(ctx, args[0], o)
			if err != nil {
				return err
			}
			return r.emit(info, func(w io.Writer) error { return writeSnapshotInfo(w, "saved", args[0], info) })
		}
		saved, err := saveArchive(ctx, c, args[0], o)
		if err != nil {
			return err
		}
		info := struct {
			Path string `json:"path"`
			*admin.SavedArchive
		}{args[0], saved}
		return r.emit(info, func(w io.Writer) error { return writeSaved(w, args[0], saved) })
	}
}

// writeSaved reports an archive saved to the local file path.
func writeSaved(w io.Writer, path string, saved *admin.SavedArchive) error {
	if _, err := fmt.Fprintf(w, "saved %s (%d bytes)\n", path, saved.Bytes); err != nil {
		return err
	}
	return writePendingEffects(w, saved.PendingEffects)
}

// gzipName reports whether path names a gzipped file, by its extension.
func gzipName(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".gz" || ext == ".tgz"
}

// saveArchive writes the archive to path through a temporary file in the
// same directory, so a failed save leaves no partial archive.
func saveArchive(ctx context.Context, c *admin.Client, path string, o admin.SaveOptions) (*admin.SavedArchive, error) {
	retry := "check that the directory of " + path + " exists and is writable, then run 'understudy snapshot save " + path + "' again"
	tmp, err := os.CreateTemp(filepath.Dir(path), ".understudy-snapshot-*")
	if err != nil {
		return nil, &localError{op: "creating the archive next to " + path, err: err, next: retry}
	}
	saved, saveErr := c.SaveSnapshot(ctx, tmp, o)
	closeErr := tmp.Close()
	if closeErr != nil {
		closeErr = &localError{op: "writing the archive " + tmp.Name(), err: closeErr, next: retry}
	}
	if err := errors.Join(saveErr, closeErr); err != nil {
		return nil, errors.Join(err, os.Remove(tmp.Name()))
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, &localError{op: "moving the archive into place at " + path, err: errors.Join(err, os.Remove(tmp.Name())), next: retry}
	}
	return saved, nil
}

func snapshotLoad(fs *flag.FlagSet) action {
	onServer := fs.Bool("on-server", false, "load FILE from under the server's <data dir>/snapshots instead (needs serve --persist)")
	return func(ctx context.Context, r *runner, args []string) error {
		if err := wantArgs(args, 1, "one argument, the archive path"); err != nil {
			return err
		}
		c, err := r.slowClient()
		if err != nil {
			return err
		}
		var info *admin.SnapshotInfo
		if *onServer {
			info, err = c.LoadSnapshotFrom(ctx, args[0])
		} else {
			info, err = loadArchive(ctx, c, args[0])
		}
		if err != nil {
			return err
		}
		return r.emit(info, func(w io.Writer) error { return writeSnapshotInfo(w, "loaded", args[0], info) })
	}
}

// loadArchive streams the archive at path to the server.
func loadArchive(ctx context.Context, c *admin.Client, path string) (*admin.SnapshotInfo, error) {
	f, err := openArchive(path, "load")
	if err != nil {
		return nil, err
	}
	// The wrapper hides Close, which net/http would call on a request
	// body; the file is closed here, once.
	info, err := c.LoadSnapshot(ctx, struct{ io.Reader }{f})
	return info, errors.Join(err, f.Close())
}

// openArchive opens a local archive for the snapshot subcommand verb.
func openArchive(path, verb string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, &localError{op: "opening the archive " + path, err: err, next: "check the path, then run 'understudy snapshot " + verb + "' again"}
	}
	return f, nil
}

func writeSnapshotInfo(w io.Writer, verb, path string, info *admin.SnapshotInfo) error {
	_, err := fmt.Fprintf(w, "%s %s: %d section(s) by %s, clock %s %s\n", verb, path, info.Sections, info.Writer, info.Clock.UTC().Format(time.RFC3339Nano), info.Mode)
	if err == nil {
		err = writePendingEffects(w, info.PendingEffects)
	}
	return err
}

// writePendingEffects warns that a saved archive lacks the effects of n
// committed writes; it prints nothing when n is 0.
func writePendingEffects(w io.Writer, n int) error {
	if n == 0 {
		return nil
	}
	_, err := fmt.Fprintf(w, "warning: %d effect(s) of committed writes, such as deliveries, had not run when the state was cut; the archive holds those writes without them\n", n)
	return err
}

// readArchive reads a local archive for inspect and diff, accepting any
// section version; snapshot.Read stops at maxArchiveBytes as it streams.
func readArchive(path, verb string) (*snapshot.Archive, error) {
	f, err := openArchive(path, verb)
	if err != nil {
		return nil, err
	}
	a, err := snapshot.Read(f, snapshot.ReadOptions{MaxBytes: maxArchiveBytes})
	if cerr := f.Close(); err == nil && cerr != nil {
		err = &localError{op: "closing the archive " + path, err: cerr, next: "run 'understudy snapshot " + verb + "' again"}
	}
	if err != nil {
		return nil, &localError{
			op: "reading the archive " + path, err: err,
			next: "check that " + path + " is an archive written by 'understudy snapshot save', then run 'understudy snapshot " + verb + "' again",
		}
	}
	return a, nil
}

// manifestView is an archive's manifest as inspect prints it.
type manifestView struct {
	Format       int           `json:"format"`
	Writer       string        `json:"writer"`
	ModelPin     string        `json:"modelPin"`
	Clock        time.Time     `json:"clock"`
	Mode         string        `json:"mode"`
	Seed         *uint64       `json:"seed,omitempty"`
	JournalNext  uint64        `json:"journalNext"`
	Blobs        string        `json:"blobs"`
	LateServices []string      `json:"lateServices,omitempty"`
	Faults       bool          `json:"faults"`
	Journal      bool          `json:"journal"`
	Sections     []sectionView `json:"sections"`
}

type sectionView struct {
	Path    string `json:"path"`
	Version int    `json:"version"`
	SHA256  string `json:"sha256"`
	Blobs   int    `json:"blobs"`
	Late    bool   `json:"late,omitempty"`
}

func snapshotInspect(_ context.Context, r *runner, args []string) error {
	if err := wantArgs(args, 1, "one argument, the archive path"); err != nil {
		return err
	}
	a, err := readArchive(args[0], "inspect")
	if err != nil {
		return err
	}
	m := a.Manifest
	v := manifestView{
		Format: m.Format, Writer: m.Writer, ModelPin: m.ModelPin, Clock: m.Clock.At.Wire(), Mode: m.Clock.ModeName(),
		Seed: m.Seed, JournalNext: uint64(m.JournalNext), Blobs: m.Blobs.String(),
		Faults: a.Faults != nil, Journal: a.Journal != nil,
	}
	for _, s := range m.LateServices {
		v.LateServices = append(v.LateServices, string(s))
	}
	for _, s := range m.Sections {
		v.Sections = append(v.Sections, sectionView{Path: s.Path, Version: s.Version, SHA256: s.SHA256, Blobs: len(s.Blobs), Late: s.Late})
	}
	return r.emit(v, func(w io.Writer) error { return writeManifest(w, v) })
}

func writeManifest(w io.Writer, v manifestView) error {
	var b strings.Builder
	seed := "none"
	if v.Seed != nil {
		seed = strconv.FormatUint(*v.Seed, 10)
	}
	fmt.Fprintf(&b, "writer        %s\nmodels        %s\nformat        archive format version %d\nclock         %s %s\nseed          %s\njournal next  %d\nblobs         %s\n",
		v.Writer, v.ModelPin, v.Format, v.Clock.UTC().Format(time.RFC3339Nano), v.Mode, seed, v.JournalNext, v.Blobs)
	if len(v.LateServices) > 0 {
		fmt.Fprintf(&b, "late data     %s\n", strings.Join(v.LateServices, ", "))
	}
	fmt.Fprintf(&b, "fault rules   %s\njournal       %s\nsections      %d\n", savedOrNot(v.Faults), savedOrNot(v.Journal), len(v.Sections))
	for _, s := range v.Sections {
		fmt.Fprintf(&b, "  %s  v%d  sha256:%s  %d blob(s)", s.Path, s.Version, s.SHA256[:min(12, len(s.SHA256))], s.Blobs)
		if s.Late {
			b.WriteString("  late")
		}
		b.WriteString("\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// savedOrNot says whether an archive holds an optional part.
func savedOrNot(saved bool) string {
	if saved {
		return "saved in the archive"
	}
	return "not saved"
}

// diffView is snapshot diff's JSON result.
type diffView struct {
	Identical   bool             `json:"identical"`
	Differences []differenceView `json:"differences"`
}

type differenceView struct {
	Entry   string `json:"entry"`
	Pointer string `json:"pointer,omitempty"`
	Change  string `json:"change"`
	Before  string `json:"before,omitempty"`
	After   string `json:"after,omitempty"`
}

func snapshotDiff(fs *flag.FlagSet) action {
	normalize := fs.Bool("normalize", false, "mask what differs between two runs of one workload: request ids, receipt handles, wall times, hosts, accounts, event ids")
	return func(_ context.Context, r *runner, args []string) error {
		if err := wantArgs(args, 2, "two arguments, the archives A and B"); err != nil {
			return err
		}
		a, err := readArchive(args[0], "diff")
		if err != nil {
			return err
		}
		b, err := readArchive(args[1], "diff")
		if err != nil {
			return err
		}
		diffs, err := snapshot.Diff(a, b, *normalize)
		if err != nil {
			return err
		}
		v := diffView{Identical: len(diffs) == 0, Differences: []differenceView{}}
		for _, d := range diffs {
			v.Differences = append(v.Differences, differenceView{Entry: d.Entry, Pointer: d.Pointer, Change: d.Change.String(), Before: d.Before, After: d.After})
		}
		return r.emit(v, func(w io.Writer) error {
			if len(diffs) == 0 {
				_, err := fmt.Fprintln(w, "the archives are identical")
				return err
			}
			var sb strings.Builder
			for _, d := range diffs {
				sb.WriteString(d.String() + "\n")
			}
			_, err := io.WriteString(w, sb.String())
			return err
		})
	}
}
