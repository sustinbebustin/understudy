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

// errUsage reports a command line that names no mode or more than one.
var errUsage = errors.New("give exactly one of -fetch, -service-reference or -verify; see -h")

// dirs are the three vendor directories, relative to the working directory.
type dirs struct {
	models, corpus, reference string
}

func run(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("vendor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	doFetch := fs.Bool("fetch", false, "download the pinned models from GitHub and rewrite UPSTREAM.json (network)")
	doReference := fs.Bool("service-reference", false, "download the AWS Service Reference for the twelve services and rewrite the derived actions.json (network)")
	doVerify := fs.Bool("verify", false, "recompute every recorded hash and check the corpus pin and the actions table, offline")
	var d dirs
	fs.StringVar(&d.models, "dir", "third_party/api-models-aws", "the models vendor directory")
	fs.StringVar(&d.corpus, "corpus-dir", "third_party/smithy-protocol-tests", "the protocol compliance corpus directory")
	fs.StringVar(&d.reference, "reference-dir", "third_party/aws-service-reference", "the derived Service Reference table directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	modes := 0
	for _, m := range []bool{*doFetch, *doReference, *doVerify} {
		if m {
			modes++
		}
	}
	switch {
	case modes != 1 || fs.NArg() > 0:
		return errUsage
	case *doFetch:
		if err := githubFetcher().fetch(ctx, d.models); err != nil {
			return err
		}
		return verify(os.DirFS(d.models), d.models)
	case *doReference:
		if err := serviceReferenceFetcher().fetchActions(ctx, d.reference); err != nil {
			return err
		}
		return verifyActions(os.DirFS(d.reference), os.DirFS(d.models), d.reference)
	default:
		return verifyAll(d)
	}
}

// verifyAll checks the three vendored inputs and reports every failure.
func verifyAll(d dirs) error {
	return errors.Join(
		verify(os.DirFS(d.models), d.models),
		verifyCorpus(os.DirFS(d.corpus), d.corpus),
		verifyActions(os.DirFS(d.reference), os.DirFS(d.models), d.reference),
	)
}
