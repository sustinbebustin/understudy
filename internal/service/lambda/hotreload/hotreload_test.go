package hotreload_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/service/lambda/hotreload"
)

// watch runs a watcher over dir with the default timing in the bubble and
// returns the count of changes it reported and a stop function.
func watch(t *testing.T, dir string) (changes *atomic.Int32, stop func()) {
	t.Helper()
	w, err := hotreload.New(dir, hotreload.Options{})
	if err != nil {
		t.Fatal(err)
	}
	changes = new(atomic.Int32)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx, func() { changes.Add(1) })
	}()
	return changes, func() {
		cancel()
		<-done
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEditReportsOneChangeAfterTheQuietPeriod(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "handler.py"), "v1")
		changes, stop := watch(t, dir)
		defer stop()

		time.Sleep(time.Second)
		if n := changes.Load(); n != 0 {
			t.Fatalf("changes before any edit = %d, want 0", n)
		}
		write(t, filepath.Join(dir, "handler.py"), "version 2")
		// The next poll sees the edit; one more quiet poll confirms it.
		time.Sleep(hotreload.DefaultInterval + hotreload.DefaultQuiet + hotreload.DefaultInterval)
		synctest.Wait()
		if n := changes.Load(); n != 1 {
			t.Fatalf("changes after one edit = %d, want 1", n)
		}
		time.Sleep(5 * time.Second)
		if n := changes.Load(); n != 1 {
			t.Errorf("changes while nothing changed = %d, want still 1", n)
		}
	})
}

func TestBurstOfEditsReportsOnceAfterTheLast(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "a"), "a")
		changes, stop := watch(t, dir)
		defer stop()

		// A build writing files over 1.5 s, one write per poll, half way
		// between polls: every poll sees a change, so the quiet period never
		// passes until the writes stop.
		time.Sleep(hotreload.DefaultInterval / 2)
		for i := range 5 {
			write(t, filepath.Join(dir, "a"), string(make([]byte, i+2)))
			time.Sleep(hotreload.DefaultInterval)
			synctest.Wait()
			if n := changes.Load(); n != 0 {
				t.Fatalf("change reported during the burst after write %d", i)
			}
		}
		time.Sleep(hotreload.DefaultQuiet + hotreload.DefaultInterval)
		synctest.Wait()
		if n := changes.Load(); n != 1 {
			t.Fatalf("changes after the burst = %d, want 1", n)
		}
	})
}

func TestAddedRemovedAndNestedFilesAreChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "pkg"), 0o700); err != nil {
			t.Fatal(err)
		}
		changes, stop := watch(t, dir)
		defer stop()
		settle := func() {
			time.Sleep(2 * time.Second)
			synctest.Wait()
		}

		write(t, filepath.Join(dir, "pkg", "mod.py"), "x")
		settle()
		if err := os.Remove(filepath.Join(dir, "pkg", "mod.py")); err != nil {
			t.Fatal(err)
		}
		settle()
		write(t, filepath.Join(dir, "bootstrap"), "#!/bin/sh")
		settle()
		if n := changes.Load(); n != 3 {
			t.Errorf("changes = %d, want 3 (add nested, remove nested, add top-level)", n)
		}
	})
}

func TestNewRefusesWhatIsNotAnAbsoluteDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	write(t, file, "x")
	for name, path := range map[string]string{
		"relative": "build",
		"missing":  filepath.Join(dir, "missing"),
		"file":     file,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := hotreload.New(path, hotreload.Options{})
			var de *hotreload.DirError
			if !errors.As(err, &de) || de.Dir != path {
				t.Fatalf("New(%q) = %v, want a *DirError naming it", path, err)
			}
		})
	}
}

func TestNewRefusesAQuietPeriodShorterThanThePollInterval(t *testing.T) {
	if _, err := hotreload.New(t.TempDir(), hotreload.Options{Interval: time.Second, Quiet: time.Millisecond}); err == nil {
		t.Fatal("New accepted a quiet period no poll can measure")
	}
}
