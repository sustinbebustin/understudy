package blob

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"
)

// The snapshot cut pins blobs under Gate.Exclusive and handlers retain them
// under cell locks, so neither may wait on a directory sync. Sync completing
// while the store's mutex is held shows it never takes that mutex, so Pin and
// Retain, which take only the mutex, never wait for it.
func TestSyncRunsWithoutTheStoreMutex(t *testing.T) {
	t.Parallel()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	s, err := NewDisk(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Put(context.Background(), bytes.NewReader([]byte("body")), 4); err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- s.Sync() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Sync waited for the store mutex")
	}
}
