package codestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const mergedDir = "merged"

// Merged is the tree a function's layers fill /opt with: each layer's tree
// copied in the order the function names them, a later file replacing an
// earlier one at the same path (lambda-destinations-layers.md 8.1, 11.5).
type Merged struct {
	// Key is the SHA-256 over the layers' digests concatenated in order,
	// so the same layers in the same order share one tree.
	Key Digest
	// Dir is the absolute path of the merged tree, the source of the
	// read-only /opt mount.
	Dir string
}

// mergeKey is the Key of the merged tree of layers, in order.
func mergeKey(layers []Digest) Digest {
	h := sha256.New()
	for _, d := range layers {
		h.Write(d[:])
	}
	var key Digest
	h.Sum(key[:0])
	return key
}

// Merge builds the merged tree of layers, whose trees PutZip must hold, or
// finds the one already built, and adds a reference to it. Each layer's
// tree is copied over the earlier ones: a later file replaces an earlier
// one at the same path, keeping its own mode, which is how understudy reads
// "merge order" (switch lambda.layers.merge-conflict,
// lambda-destinations-layers.md 8.1, LDL-12). The tree is a copy, so it
// outlives the layers' own trees. A layer the store does not hold is a
// *NotFoundError, no layers a *MergeError, and a file where an earlier
// layer has a directory (or the reverse) a *MergeConflictError; nothing is
// published then.
func (s *Store) Merge(ctx context.Context, layers []Digest) (Merged, error) {
	if len(layers) == 0 {
		return Merged{}, &MergeError{Reason: "no layers"}
	}
	key := mergeKey(layers)
	if m, ok := s.retainMerged(key); ok {
		return m, nil
	}
	if err := s.holdAll(layers); err != nil {
		return Merged{}, err
	}
	staged, err := s.overlay(ctx, layers)
	// The layers' trees were held only while being copied.
	for _, d := range layers {
		err = errors.Join(err, s.Release(d))
	}
	if err != nil {
		if staged != "" {
			err = errors.Join(err, s.removeStaged(staged))
		}
		return Merged{}, err
	}
	return s.publishMerged(staged, key)
}

// ReleaseMerged drops one reference to the merged tree key names and
// removes the tree with the last one. A key the store holds no merged tree
// for is a *MergedNotFoundError.
func (s *Store) ReleaseMerged(key Digest) error {
	s.mu.Lock()
	refs, ok := s.merged[key]
	if !ok {
		s.mu.Unlock()
		return &MergedNotFoundError{Key: key}
	}
	if refs > 1 {
		s.merged[key] = refs - 1
		s.mu.Unlock()
		return nil
	}
	delete(s.merged, key)
	// Detached under the lock, as Release detaches a tree, so a concurrent
	// Merge of the same layers builds afresh.
	detached := path.Join(tmpDir, "released-merged-"+hex.EncodeToString(key[:])+"-"+randomName())
	err := s.root.Rename(path.Join(mergedDir, hex.EncodeToString(key[:])), detached)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("code store: detach merged layer tree %x: %w", key, err)
	}
	if err := s.root.RemoveAll(detached); err != nil {
		return fmt.Errorf("code store: remove released merged layer tree %x: %w", key, err)
	}
	return nil
}

func (s *Store) mergedPath(key Digest) string {
	return filepath.Join(s.dir, mergedDir, hex.EncodeToString(key[:]))
}

func (s *Store) retainMerged(key Digest) (Merged, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	refs, ok := s.merged[key]
	if !ok {
		return Merged{}, false
	}
	s.merged[key] = refs + 1
	return Merged{Key: key, Dir: s.mergedPath(key)}, true
}

// holdAll adds a reference to every layer's tree, so none is removed while
// Merge copies it, or to none when the store lacks one.
func (s *Store) holdAll(layers []Digest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range layers {
		if _, ok := s.trees[d]; !ok {
			return &NotFoundError{Sha256: d}
		}
	}
	for _, d := range layers {
		s.trees[d].refs++
	}
	return nil
}

// overlay copies the layers' trees, in order, into a new staging directory
// and returns it. On an error it returns the staging directory to remove,
// or "" when none was created.
func (s *Store) overlay(ctx context.Context, layers []Digest) (string, error) {
	staged := path.Join(tmpDir, "merged-"+randomName())
	if err := s.root.Mkdir(staged, dirMode); err != nil {
		return "", fmt.Errorf("code store: create staging directory: %w", err)
	}
	// /opt itself is 0755 whatever the process umask, as extracted trees are.
	if err := s.root.Chmod(staged, dirMode); err != nil {
		return staged, fmt.Errorf("code store: set the merged tree's mode: %w", err)
	}
	for i, d := range layers {
		if err := s.copyLayer(ctx, staged, i, d); err != nil {
			return staged, err
		}
	}
	return staged, nil
}

// copyLayer copies the tree of the layer at index i of Merge's argument
// over staged.
func (s *Store) copyLayer(ctx context.Context, staged string, i int, d Digest) error {
	src := path.Join(treesDir, hex.EncodeToString(d[:]))
	return fs.WalkDir(s.root.FS(), src, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("code store: walk the tree of layer %d (CodeSha256 %s): %w", i, d, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == src {
			return nil
		}
		rel := strings.TrimPrefix(p, src+"/")
		conflict, err := s.mergeEntry(p, path.Join(staged, rel), e)
		switch {
		case conflict:
			return &MergeConflictError{Path: rel, Layer: i, File: !e.IsDir()}
		case err != nil:
			return fmt.Errorf("code store: merge %s of layer %d (CodeSha256 %s): %w", rel, i, d, err)
		}
		return nil
	})
}

// mergeEntry copies the entry e at src to target with its mode: a
// directory is created unless an earlier layer created it, and a file
// replaces an earlier layer's. conflict reports a file where an earlier
// layer has a directory, or the reverse, and copies nothing.
func (s *Store) mergeEntry(src, target string, e fs.DirEntry) (conflict bool, err error) {
	info, err := e.Info()
	if err != nil {
		return false, err
	}
	existing, err := s.root.Lstat(target)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if exists && existing.IsDir() != e.IsDir() {
		return true, nil
	}
	switch {
	case e.IsDir() && !exists:
		err = s.root.Mkdir(target, dirMode)
	case !e.IsDir():
		err = s.copyFile(src, target)
	}
	if err != nil {
		return false, err
	}
	return false, s.root.Chmod(target, info.Mode().Perm())
}

func (s *Store) copyFile(src, dst string) error {
	in, err := s.root.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := s.root.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		return errors.Join(err, out.Close())
	}
	return out.Close()
}

// publishMerged moves a staged merged tree into place, or adds a reference
// to the tree a concurrent Merge of the same layers published first.
func (s *Store) publishMerged(staged string, key Digest) (Merged, error) {
	m := Merged{Key: key, Dir: s.mergedPath(key)}
	s.mu.Lock()
	if refs, ok := s.merged[key]; ok {
		s.merged[key] = refs + 1
		s.mu.Unlock()
		return m, s.removeStaged(staged)
	}
	if err := s.root.Rename(staged, path.Join(mergedDir, hex.EncodeToString(key[:]))); err != nil {
		s.mu.Unlock()
		return Merged{}, errors.Join(fmt.Errorf("code store: publish merged layer tree %x: %w", key, err), s.removeStaged(staged))
	}
	s.merged[key] = 1
	s.mu.Unlock()
	return m, nil
}

// MergeError reports layers Merge cannot merge for a reason other than a
// conflict between them, such as an empty list.
type MergeError struct {
	Reason string
}

func (e *MergeError) Error() string {
	return "codestore: cannot merge the layers into /opt: " + e.Reason
}

// MergeConflictError reports a layer with a file where an earlier layer
// has a directory, or the reverse; nothing is published.
type MergeConflictError struct {
	// Path is slash-separated and relative to /opt.
	Path string
	// Layer is the index, in Merge's argument, of the layer that conflicts.
	Layer int
	// File is true when that layer has a file at Path and the earlier one
	// a directory.
	File bool
}

func (e *MergeConflictError) Error() string {
	has, earlier := "a directory", "a file"
	if e.File {
		has, earlier = earlier, has
	}
	return fmt.Sprintf("codestore: layer %d has %s at %s where an earlier layer has %s; the layers cannot be merged into /opt", e.Layer, has, e.Path, earlier)
}

// MergedNotFoundError reports a key the store holds no merged tree for: it
// was never merged, or its last reference was released.
type MergedNotFoundError struct {
	Key Digest
}

func (e *MergedNotFoundError) Error() string {
	return fmt.Sprintf("code store holds no merged layer tree %x; it was never merged or its last reference was released", e.Key)
}
