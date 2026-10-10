package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Args[1:], os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "vendor:", err)
		os.Exit(1)
	}
}

// errUsage reports a command line that names neither or both modes.
var errUsage = errors.New("give exactly one of -fetch or -verify; see -h")

func run(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("vendor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	doFetch := fs.Bool("fetch", false, "download the pinned models from GitHub and rewrite UPSTREAM.json (network)")
	doVerify := fs.Bool("verify", false, "recompute every recorded hash offline")
	dir := fs.String("dir", "third_party/api-models-aws", "the vendor directory, relative to the working directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case *doFetch == *doVerify:
		return errUsage
	case *doFetch:
		if err := githubFetcher().fetch(ctx, *dir); err != nil {
			return err
		}
		return verify(os.DirFS(*dir), *dir)
	default:
		return verify(os.DirFS(*dir), *dir)
	}
}
