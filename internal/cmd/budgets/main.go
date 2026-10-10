package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"github.com/sustinbebustin/understudy/internal/journal"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	ok, err := run(ctx, os.Stdout)
	stop()
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, "budgets:", err)
		os.Exit(1)
	case !ok:
		fmt.Fprintln(os.Stderr, "budgets: a measure is over its ceiling or could not be taken (ARCHITECTURE.md 5.7); "+
			"prune serde by shape role or tighten the closure (ARCHITECTURE.md 5.4) rather than raise the ceiling")
		os.Exit(1)
	}
}

// errNotLinux reports a host without /proc.
var errNotLinux = errors.New("the idle RSS budget reads /proc/<pid>/status, so budgets runs on Linux only")

// idle is how long the emulator idles before its RSS is read.
const idle = time.Second

// run measures every budget from the module root above the working
// directory, prints the table to w and reports whether all are kept. The
// error is for a run that could not start at all.
func run(ctx context.Context, w io.Writer) (bool, error) {
	if runtime.GOOS != "linux" {
		return false, errNotLinux
	}
	root, err := moduleRoot()
	if err != nil {
		return false, err
	}
	dir, err := os.MkdirTemp("", "understudy-budgets-")
	if err != nil {
		return false, err
	}
	rows := measureAll(ctx, root, dir)
	ok, err := writeTable(w, rows)
	return ok, errors.Join(err, os.RemoveAll(dir))
}

// measureAll takes every measure in turn; one that fails does not stop the
// others.
func measureAll(ctx context.Context, root, dir string) []Row {
	size, bin, err := measureBinary(ctx, root, dir)
	rows := []Row{{budgetBinary, size}}
	if err != nil {
		rows = append(rows, Row{budgetStartup, Failed{err}}, Row{budgetIdleRSS, Failed{err}})
	} else {
		rows = append(rows, serveRows(ctx, bin, dir)...)
	}
	return append(rows,
		Row{budgetColdBuild, coldBuild(ctx, root, dir)},
		Row{budgetLint, lintWallTime(ctx, root)},
		Row{budgetRingEvents, Measured{Count(journal.DefaultCapacity)}},
		Row{budgetRingBytes, Measured{Bytes(journal.DefaultMaxBytes)}},
	)
}

// serveRows starts the emulator, times its readiness, reads its idle RSS
// and stops it.
func serveRows(ctx context.Context, bin, dir string) []Row {
	s, startup, err := startServer(ctx, bin, dir)
	if err != nil {
		return []Row{{budgetStartup, Failed{err}}, {budgetIdleRSS, Failed{err}}}
	}
	var rss Result
	if b, err := idleRSS(ctx, s.cmd.Process.Pid, idle); err != nil {
		rss = Failed{err}
	} else {
		rss = Measured{b}
	}
	if err := s.stop(); err != nil {
		rss = Failed{err}
	}
	return []Row{{budgetStartup, Measured{Duration(startup)}}, {budgetIdleRSS, rss}}
}

// moduleRoot walks up from the working directory to the directory holding
// go.mod.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the working directory; run budgets inside the understudy module")
		}
		dir = parent
	}
}
