// archive/zip refuses insecure entry names itself: a second layer under the
// Lambda code store's own zip-slip checks (internal/service/lambda/codestore).
//
//go:debug zipinsecurepath=0

// Command understudy is a local AWS emulator for development machines and CI.
//
// The command tree is built here and nowhere else; behavior lives in internal
// packages. The version is stamped at link time:
//
//	go build -ldflags "-X main.version=v0.1.0" ./cmd/understudy
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sustinbebustin/understudy/internal/server"
)

var version = "dev"

// Exit codes, as ARCHITECTURE.md 17 assigns them.
const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `understudy is a local AWS emulator.

Usage:
  understudy serve [flags]   run the emulator until interrupted
  understudy version         print the version

Run 'understudy serve --help' for the serve flags.
`

// run executes one command and returns its exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "version":
		if _, err := fmt.Fprintf(stdout, "understudy %s\n", version); err != nil {
			fmt.Fprintf(stderr, "understudy: cannot write the version to stdout: %v\n", err)
			return exitFailed
		}
		return exitOK
	case "serve":
		return serve(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "understudy: unknown command %q; the commands are serve and version\n", args[0])
		return exitUsage
	}
}

const serveExamples = `
Examples:
  understudy serve
  understudy serve --listen 127.0.0.1:0 --ready-file /tmp/understudy.json
  understudy serve --clock frozen@2026-01-01T00:00:00Z --seed 42 --log-format json
`

// serve runs the emulator until SIGINT or SIGTERM.
func serve(args []string, stdout, stderr io.Writer) int {
	cfg, err := parseServe(args, stdout)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	case err != nil:
		fmt.Fprintf(stderr, "understudy serve: %v\nRun 'understudy serve --help' for the flags.\n", err)
		return exitUsage
	}
	cfg.Version = version
	srv, err := server.New(cfg)
	if refused, ok := errors.AsType[*server.ListenError](err); ok {
		fmt.Fprintf(stderr, "understudy serve: %v\n", refused)
		return exitUsage
	}
	if err != nil {
		fmt.Fprintf(stderr, "understudy serve: %v\n", err)
		return exitFailed
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Serve(ctx); err != nil {
		// A Serve that could not listen leaves the Server built; Close
		// releases what New opened, and is a no-op after a shutdown.
		fmt.Fprintf(stderr, "understudy serve: %v\n", errors.Join(err, srv.Close()))
		return exitFailed
	}
	return exitOK
}

// parseServe reads the serve flags into a server configuration.
func parseServe(args []string, stdout io.Writer) (server.Config, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stdout)
	listen := fs.String("listen", "127.0.0.1:4566", "address to listen on; port 0 picks a free port")
	clockFlag := fs.String("clock", "running", "how the emulator clock starts: running, frozen, or frozen@<RFC 3339 time>")
	seed := fs.String("seed", "", "seed for reproducible ids (an unsigned integer); empty draws from crypto/rand")
	ready := fs.String("ready-file", "", "file to write {\"endpoint\", \"pid\"} to once listening")
	logFormat := fs.String("log-format", "text", "log format: text or json")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn or error")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: understudy serve [flags]\n\nFlags:\n")
		fs.PrintDefaults()
		fmt.Fprint(fs.Output(), serveExamples)
	}
	if err := fs.Parse(args); err != nil {
		return server.Config{}, err
	}
	if fs.NArg() > 0 {
		return server.Config{}, fmt.Errorf("unexpected argument %q; serve takes flags only", fs.Arg(0))
	}
	var cfg server.Config
	var err error
	if cfg.Listen, err = netip.ParseAddrPort(*listen); err != nil {
		return server.Config{}, fmt.Errorf("--listen %q is not an address and port such as 127.0.0.1:4566", *listen)
	}
	if cfg.ClockStart, err = parseClock(*clockFlag); err != nil {
		return server.Config{}, err
	}
	if *seed != "" {
		n, err := strconv.ParseUint(*seed, 10, 64)
		if err != nil {
			return server.Config{}, fmt.Errorf("--seed %q is not an unsigned integer", *seed)
		}
		cfg.Seed = &n
	}
	switch *logFormat {
	case "text":
		cfg.LogFormat = server.LogText
	case "json":
		cfg.LogFormat = server.LogJSON
	default:
		return server.Config{}, fmt.Errorf("--log-format %q is not text or json", *logFormat)
	}
	levels := map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}
	level, ok := levels[*logLevel]
	if !ok {
		return server.Config{}, fmt.Errorf("--log-level %q is not debug, info, warn or error", *logLevel)
	}
	cfg.LogLevel = level
	cfg.ReadyFile = *ready
	return cfg, nil
}

// parseClock reads --clock: running, frozen (at wall time now) or
// frozen@<RFC 3339 time>.
func parseClock(v string) (server.ClockStart, error) {
	switch {
	case v == "running":
		return server.ClockRunning{}, nil
	case v == "frozen":
		return server.ClockFrozen{}, nil
	case strings.HasPrefix(v, "frozen@"):
		at, err := time.Parse(time.RFC3339, strings.TrimPrefix(v, "frozen@"))
		if err != nil {
			return nil, fmt.Errorf("--clock %q: the time after frozen@ is not RFC 3339, such as frozen@2026-01-01T00:00:00Z", v)
		}
		return server.ClockFrozen{At: at}, nil
	}
	return nil, fmt.Errorf("--clock %q is not running, frozen or frozen@<RFC 3339 time>", v)
}
