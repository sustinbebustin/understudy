package codegen

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// isGenerated reports whether dir/name is a file the generator owns: its
// name ends in _gen.go or _gen_test.go and its first line is the header
// file.bytes writes. The name alone is not enough, as a hand-written
// fixtures_gen_test.go must survive a run.
func isGenerated(dir, name string) (bool, error) {
	if !strings.HasSuffix(name, "_gen.go") && !strings.HasSuffix(name, "_gen_test.go") {
		return false, nil
	}
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return false, err
	}
	line, rerr := bufio.NewReader(f).ReadString('\n')
	if errors.Is(rerr, io.EOF) {
		rerr = nil // a one-line file: line holds all of it
	}
	if err := errors.Join(rerr, f.Close()); err != nil {
		return false, err
	}
	line = strings.TrimSuffix(line, "\n")
	return strings.HasPrefix(line, headerPrefix) && strings.HasSuffix(line, headerSuffix), nil
}

// Write replaces the generated files of dir with files: each through a temp
// file and a rename, so a failed run never leaves a half-written file, and
// generated files the run no longer produces are removed. Hand-written
// files in dir, including ones named like generated files but without the
// generator's header, are left alone.
func Write(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return failf(StageWrite, dir, "%w", err)
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := writeAtomic(filepath.Join(dir, name), files[name]); err != nil {
			return failf(StageWrite, filepath.Join(dir, name), "%w", err)
		}
	}
	stale, err := staleFiles(dir, files)
	if err != nil {
		return failf(StageWrite, dir, "%w", err)
	}
	for _, name := range stale {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return failf(StageWrite, filepath.Join(dir, name), "remove a stale generated file: %w", err)
		}
	}
	return nil
}

// DriftError lists the generated files that differ from what the inputs
// produce now.
type DriftError struct {
	Dir     string
	Changed []string // differ or are missing
	Stale   []string // generated files the inputs no longer produce
}

func (e *DriftError) Error() string {
	return fmt.Sprintf("%s is out of date with its inputs: changed or missing %v, stale %v; run 'just generate' and commit the result", e.Dir, e.Changed, e.Stale)
}

// Check compares dir with files without writing anything.
func Check(dir string, files map[string][]byte) error {
	d := &DriftError{Dir: dir}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		have, err := os.ReadFile(filepath.Join(dir, name))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			d.Changed = append(d.Changed, name)
		case err != nil:
			return failf(StageWrite, filepath.Join(dir, name), "%w", err)
		case !bytes.Equal(have, files[name]):
			d.Changed = append(d.Changed, name)
		}
	}
	stale, err := staleFiles(dir, files)
	if err != nil {
		return failf(StageWrite, dir, "%w", err)
	}
	d.Stale = stale
	if len(d.Changed)+len(d.Stale) > 0 {
		return d
	}
	return nil
}

func staleFiles(dir string, files map[string][]byte) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if _, keep := files[e.Name()]; keep || e.IsDir() {
			continue
		}
		gen, err := isGenerated(dir, e.Name())
		if err != nil {
			return nil, err
		}
		if gen {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func writeAtomic(name string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+".tmp*")
	if err != nil {
		return err
	}
	// CreateTemp makes the file 0600; generated sources are ordinary repo
	// files that other users (a Docker build context) must read.
	_, werr := tmp.Write(content)
	merr := tmp.Chmod(0o644)
	cerr := tmp.Close()
	if err := errors.Join(werr, merr, cerr); err != nil {
		return errors.Join(err, os.Remove(tmp.Name()))
	}
	if err := os.Rename(tmp.Name(), name); err != nil {
		return errors.Join(err, os.Remove(tmp.Name()))
	}
	return nil
}
