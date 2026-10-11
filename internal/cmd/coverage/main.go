package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"

	"github.com/sustinbebustin/understudy/internal/coverage"
	"github.com/sustinbebustin/understudy/internal/server"
)

// Exit codes: 0 when the report is written, 1 when it could not be built,
// 2 for a usage error.
const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

// run prints the report args ask for to stdout and returns the exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("coverage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "markdown", "report format: markdown or json")
	overlays := fs.String("overlays", filepath.Join("codegen", "overlays"), "directory of the overlay files whose evidence the report lists")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *format != "markdown" && *format != "json" || fs.NArg() > 0 {
		fmt.Fprintf(stderr, "coverage: -format is markdown or json and no arguments follow; got -format %s %q\n", *format, fs.Args())
		return exitUsage
	}
	report, err := build(ctx, *overlays)
	if err != nil {
		fmt.Fprintln(stderr, "coverage:", err)
		return exitFailed
	}
	out := report.Markdown()
	if *format == "json" {
		if out, err = report.JSON(); err != nil {
			fmt.Fprintln(stderr, "coverage:", err)
			return exitFailed
		}
	}
	if _, err := stdout.Write(out); err != nil {
		fmt.Fprintln(stderr, "coverage: writing the report:", err)
		return exitFailed
	}
	return exitOK
}

// build asks an in-process emulator for its report and adds the overlay
// evidence read from dir.
func build(ctx context.Context, dir string) (coverage.Report, error) {
	entries, err := readOverlays(dir)
	if err != nil {
		return coverage.Report{}, err
	}
	srv, err := server.New(server.Config{LogOutput: io.Discard, Version: "coverage"})
	if err != nil {
		return coverage.Report{}, err
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/_understudy/v1/coverage?format=json", http.NoBody))
	if err := srv.Close(); err != nil {
		return coverage.Report{}, fmt.Errorf("releasing the in-process emulator: %w", err)
	}
	if rec.Code != http.StatusOK {
		return coverage.Report{}, fmt.Errorf("the admin API answered GET /_understudy/v1/coverage with %d: %s", rec.Code, rec.Body)
	}
	report, err := coverage.ParseJSON(rec.Body.Bytes())
	if err != nil {
		return coverage.Report{}, err
	}
	report.Overlays = coverage.OverlaysRead{Entries: entries}
	return report, nil
}

// readOverlays reads the evidence of every overlay file in dir.
func readOverlays(dir string) ([]coverage.OverlayEntry, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no overlay files in %s; run from the module root or pass -overlays", dir)
	}
	var out []coverage.OverlayEntry
	var errs []error
	for _, f := range slices.Sorted(slices.Values(files)) {
		data, err := os.ReadFile(f)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		entries, err := coverage.ParseOverlay(filepath.Base(f), data)
		errs = append(errs, err)
		out = append(out, entries...)
	}
	return out, errors.Join(errs...)
}
