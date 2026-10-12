package understudytest

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/sustinbebustin/understudy/internal/server"
)

// When Serve cannot listen, release still closes what New opened: the
// temporary directory for extracted function code is gone afterwards.
func TestReleaseAfterAFailedServeClosesWhatNewOpened(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	busy, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	cfg := serverConfig(Options{Docker: DockerDisabled{}})
	cfg.Listen = netip.MustParseAddrPort(busy.Addr().String())
	cfg.LogOutput = io.Discard
	_, release, err := launch(t.Context(), cfg)
	if bind, ok := errors.AsType[*server.BindError](err); !ok || bind.Addr != cfg.Listen {
		t.Fatalf("launch on the busy address %s = %v, want a *server.BindError naming it", cfg.Listen, err)
	}
	if release == nil {
		t.Fatal("launch returned no release after New succeeded")
	}
	if dirs := codeDirs(t, tmp); len(dirs) != 1 {
		t.Fatalf("code directories before release = %v, want the one New made", dirs)
	}
	if err := release(); err != nil {
		t.Errorf("release = %v, want nil: Serve's error was already returned", err)
	}
	if dirs := codeDirs(t, tmp); len(dirs) != 0 {
		t.Errorf("code directories after release = %v, want none", dirs)
	}
}

func codeDirs(t *testing.T, dir string) []string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(dir, "understudy-code-*"))
	if err != nil {
		t.Fatal(err)
	}
	return dirs
}
