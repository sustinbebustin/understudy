// archive/zip refuses insecure entry names itself: a second layer under the
// Lambda code store's own zip-slip checks (internal/service/lambda/codestore).
//
//go:debug zipinsecurepath=0

// Command understudy is a local AWS emulator for development machines and CI.
//
// The command tree lives in internal/cli; main hands it the process's
// streams, environment and files, and cancels it on SIGINT and SIGTERM; a
// second signal during the graceful shutdown kills the process.
// The version is stamped at link time:
//
//	go build -ldflags "-X main.version=v0.1.0" ./cmd/understudy
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/sustinbebustin/understudy/internal/cli"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// The first signal starts a graceful shutdown; restoring the default
	// handling then lets a second one end a slow or hung shutdown at once.
	context.AfterFunc(ctx, stop)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run executes one command and returns its exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return cli.Run(ctx, args, cli.Options{
		Stdout:   stdout,
		Stderr:   stderr,
		Env:      os.Environ(),
		ReadFile: os.ReadFile,
		Version:  version,
	})
}
