package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Problem is one way the vendored files differ from UPSTREAM.json or from the
// pin.
type Problem struct {
	Path   string
	Detail string
}

// VerifyError lists every problem -verify found, so one run reports them all.
type VerifyError struct {
	Dir      string
	Problems []Problem
}

func (e *VerifyError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s does not match its pin (%d problems):", e.Dir, len(e.Problems))
	for _, p := range e.Problems {
		fmt.Fprintf(&b, "\n  %s: %s", p.Path, p.Detail)
	}
	b.WriteString("\nThe vendored files must be byte-identical to upstream commit " + pinnedCommit +
		"; restore them with git checkout, or re-run 'go run ./internal/cmd/vendor -fetch' when bumping the pin on purpose.")
	return b.String()
}

// verify recomputes the hashes of every file UPSTREAM.json records under
// root, offline. It also fails when the manifest names another commit or
// another set of models than the pin, or when an unrecorded model file sits
// in the tree.
func verify(root fs.FS, dir string) error {
	m, err := readManifest(root)
	if err != nil {
		return fmt.Errorf("verify %s: %w", dir, err)
	}
	var problems []Problem
	add := func(p, format string, args ...any) {
		problems = append(problems, Problem{Path: p, Detail: fmt.Sprintf(format, args...)})
	}
	if m.Commit != pinnedCommit {
		add(manifestName, "records commit %s, but the pin is %s", m.Commit, pinnedCommit)
	}
	if m.Repository != upstreamRepo {
		add(manifestName, "records repository %s, but the pin is %s", m.Repository, upstreamRepo)
	}
	if got, want := modelsOf(m.Files), pinnedModels(); !slices.Equal(got, want) {
		add(manifestName, "records models %v, but the pin is %v", got, want)
	}
	if got := pathsOf(m.Legal); !slices.Equal(got, legalPaths()) {
		add(manifestName, "records legal files %v, but they must be %v", got, legalPaths())
	}
	for _, f := range slices.Concat(m.Legal, m.Files) {
		checkFile(root, f, add)
	}
	recorded := pathsOf(m.Files)
	err = fs.WalkDir(root, "models", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && !slices.Contains(recorded, p) {
			add(p, "is not recorded in %s; remove it or vendor it with -fetch", manifestName)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("verify %s: walk models: %w", dir, err)
	}
	if len(problems) > 0 {
		return &VerifyError{Dir: dir, Problems: problems}
	}
	return nil
}

func readManifest(root fs.FS) (Manifest, error) {
	raw, err := fs.ReadFile(root, manifestName)
	if err != nil {
		return Manifest{}, fmt.Errorf("read %s: %w; run 'go run ./internal/cmd/vendor -fetch' to create it", manifestName, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("parse %s: %w", manifestName, err)
	}
	return m, nil
}

func checkFile(root fs.FS, f File, add func(p, format string, args ...any)) {
	if !fs.ValidPath(f.Path) || path.Clean(f.Path) != f.Path {
		add(f.Path, "is not a clean relative path")
		return
	}
	content, err := fs.ReadFile(root, f.Path)
	if err != nil {
		add(f.Path, "cannot be read: %v", err)
		return
	}
	got := record(f.Dir, f.Path, content)
	if got.Bytes != f.Bytes {
		add(f.Path, "has %d bytes, %s records %d", got.Bytes, manifestName, f.Bytes)
	}
	if got.GitBlobSHA1 != f.GitBlobSHA1 {
		add(f.Path, "has git blob SHA-1 %s, %s records %s", got.GitBlobSHA1, manifestName, f.GitBlobSHA1)
	}
	if got.SHA256 != f.SHA256 {
		add(f.Path, "has SHA-256 %s, %s records %s", got.SHA256, manifestName, f.SHA256)
	}
}

func modelsOf(files []File) []model {
	out := make([]model, len(files))
	for i, f := range files {
		out[i] = model{Dir: f.Dir, Path: f.Path}
	}
	return out
}

func pathsOf(files []File) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

// writeFileAtomic writes content to a temp file beside name and renames it
// into place, so an interrupted run never leaves a half-written file.
func writeFileAtomic(name string, content []byte) error {
	dir, base := filepath.Dir(name), filepath.Base(name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+base+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// CreateTemp makes the file 0600; vendored files are ordinary repo files
	// that other users (a Docker build context) must read.
	_, werr := tmp.Write(content)
	merr := tmp.Chmod(0o644)
	cerr := tmp.Close()
	if err := errors.Join(werr, merr, cerr); err != nil {
		return errors.Join(err, os.Remove(tmpName))
	}
	if err := os.Rename(tmpName, name); err != nil {
		return errors.Join(err, os.Remove(tmpName))
	}
	return nil
}
