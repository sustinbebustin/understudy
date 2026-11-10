// archive/zip refuses insecure entry names itself: a second layer under the
// Lambda code store's own zip-slip checks (internal/service/lambda/codestore).
//
//go:debug zipinsecurepath=0

// Command understudy is a local AWS emulator for development machines and CI.
//
// The command tree lives in internal/cli; main hands it the process's
// streams, environment and files, and cancels it on SIGINT and SIGTERM; a
// second signal during the graceful shutdown kills the process.
//
// A release stamps the version at link time, without the v
// (.goreleaser.yaml):
//
//	go build -ldflags "-X main.version=0.1.2" ./cmd/understudy
//
// Without the stamp, `go install ...@v0.1.2` and a build in a checkout still
// name a version: the module version the go command records in the binary.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/sustinbebustin/understudy/internal/cli"
)

var version = "dev"

// resolveVersion is the stamped version, else the main module's version from
// the build info, else "dev". A build in a checkout records one from git (a
// tag, or a pseudo-version, "+dirty" with local changes); a test binary, or a
// build without VCS information, records "(devel)".
func resolveVersion(stamped string, info *debug.BuildInfo, ok bool) string {
	if stamped != "dev" || !ok || info == nil {
		return stamped
	}
	if v := info.Main.Version; strings.HasPrefix(v, "v") {
		return strings.TrimPrefix(v, "v")
	}
	return stamped
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	// The first signal starts a graceful shutdown; a second one must end a
	// slow or hung shutdown at once. Default handling is restored before
	// the cancel, so no part of the shutdown can begin while a second
	// signal would still be relayed to sigs and dropped.
	go func() {
		<-sigs
		signal.Stop(sigs)
		cancel()
	}()
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	os.Exit(code)
}

// run executes one command and returns its exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	info, ok := debug.ReadBuildInfo()
	return cli.Run(ctx, args, cli.Options{
		Stdout:   stdout,
		Stderr:   stderr,
		Env:      os.Environ(),
		ReadFile: os.ReadFile,
		Version:  resolveVersion(version, info, ok),
	})
}
