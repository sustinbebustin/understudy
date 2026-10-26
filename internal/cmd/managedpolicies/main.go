package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/sustinbebustin/understudy/internal/policycatalogue"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Args[1:], os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "managedpolicies:", err)
		os.Exit(1)
	}
}

// errUsage reports a command line that names no mode or both.
var errUsage = errors.New("give exactly one of -fetch or -verify; see -h")

// notImplementedError is the answer of every step not built yet.
type notImplementedError struct{ op string }

func (e *notImplementedError) Error() string { return e.op + " is not implemented yet" }

// run parses the command line and runs its one mode.
func run(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("managedpolicies", flag.ContinueOnError)
	fs.SetOutput(stderr)
	doFetch := fs.Bool("fetch", false, "read the AWS Managed Policy Reference Guide from docs.aws.amazon.com and rewrite the catalogue (network)")
	doVerify := fs.Bool("verify", false, "check the catalogue, its hashes, LICENSE and NOTICE, offline")
	dir := fs.String("dir", "third_party/aws-managed-policies", "the catalogue directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case *doFetch == *doVerify || fs.NArg() > 0:
		return errUsage
	case *doFetch:
		return docsFetcher().fetch(ctx, *dir)
	default:
		return verify(os.DirFS(*dir), *dir)
	}
}

// summary is the review line each mode prints, which the maintainer
// compares with iam-evaluation.md 10.4. Not implemented yet: it is empty.
func summary(*policycatalogue.Catalogue) string { return "" }
