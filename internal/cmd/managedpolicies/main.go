package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/policycatalogue"
	awsmanagedpolicies "github.com/sustinbebustin/understudy/third_party/aws-managed-policies"
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

// run parses the command line, runs its one mode and, when it succeeds,
// prints the summary of the catalogue in the directory.
func run(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("managedpolicies", flag.ContinueOnError)
	fs.SetOutput(stderr)
	doFetch := fs.Bool("fetch", false, "read the AWS Managed Policy Reference Guide from docs.aws.amazon.com and rewrite the catalogue (network)")
	doVerify := fs.Bool("verify", false, "check the catalogue, its hashes, LICENSE and NOTICE, offline")
	dir := fs.String("dir", "third_party/aws-managed-policies", "the catalogue directory")
	if err := fs.Parse(args); err != nil {
		// -h and -help print the usage, which is what was asked for.
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	var err error
	switch {
	case *doFetch == *doVerify || fs.NArg() > 0:
		return errUsage
	case *doFetch:
		err = docsFetcher().fetch(ctx, *dir)
	default:
		err = verify(os.DirFS(*dir), *dir)
	}
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(*dir, awsmanagedpolicies.PoliciesFile))
	if err != nil {
		return err
	}
	cat, err := policycatalogue.Parse(data)
	if err != nil {
		return err
	}
	fmt.Fprintln(stderr, "managedpolicies:", summary(cat))
	return nil
}

// summary is the review line each mode prints, which the maintainer
// compares with the counts of iam-evaluation.md 10.4: the total, one count
// per Type and the highest default version by its number.
func summary(cat *policycatalogue.Catalogue) string {
	counts := map[policycatalogue.Type]int{}
	highest := -1
	for p := range cat.All() {
		counts[p.Type]++
		digits, _, _ := strings.Cut(strings.TrimPrefix(p.VersionID, "v"), ".")
		if n, err := strconv.Atoi(digits); err == nil && n > highest {
			highest = n
		}
	}
	types := []policycatalogue.Type{policycatalogue.AWSManaged, policycatalogue.ServiceLinkedRole, policycatalogue.ServiceRole, policycatalogue.JobFunction}
	perType := make([]string, len(types))
	for i, t := range types {
		perType[i] = fmt.Sprintf("%d %s", counts[t], t)
	}
	top := "none"
	if highest >= 0 {
		top = "v" + strconv.Itoa(highest)
	}
	return fmt.Sprintf("%d policies (%s), highest default version %s", cat.Len(), strings.Join(perType, ", "), top)
}
