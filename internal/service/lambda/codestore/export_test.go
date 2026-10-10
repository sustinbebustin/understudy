package codestore

import (
	"fmt"
	"maps"
	"slices"
)

// Trees lists the digests the store holds trees for, and fails when the
// tree directory disagrees with the index or the temporary directory still
// holds anything.
func (s *Store) Trees() ([]Digest, error) {
	s.mu.Lock()
	held := slices.Collect(maps.Keys(s.trees))
	s.mu.Unlock()
	trees, err := s.list(treesDir)
	if err != nil {
		return nil, err
	}
	if len(trees) != len(held) {
		return nil, fmt.Errorf("index holds %d trees, %s holds %q", len(held), treesDir, trees)
	}
	left, err := s.list(tmpDir)
	if err != nil {
		return nil, err
	}
	if len(left) != 0 {
		return nil, fmt.Errorf("%s still holds %q", tmpDir, left)
	}
	return held, nil
}

func (s *Store) list(dir string) ([]string, error) {
	f, err := s.root.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(-1)
}
