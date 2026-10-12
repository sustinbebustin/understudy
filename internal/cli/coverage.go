package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/coverage"
	"github.com/sustinbebustin/understudy/internal/server"
)

// The coverage formats.
const (
	formatText     = "text"
	formatMarkdown = "markdown"
	formatJSON     = "json"
)

// coverageFlags are the flags of coverage.
type coverageFlags struct {
	services stringList
	format   string
	switches bool
	overlays string
}

func coverageCmd() *command {
	return leaf("coverage", "Report protocol compliance, operation coverage and evidence, from the running server or offline",
		"coverage [--service S] [--format text|markdown|json] [--switches] [--overlays DIR]",
		[]string{
			"understudy coverage",
			"understudy coverage --service sqs --switches",
			"understudy coverage --format markdown --overlays codegen/overlays > docs/coverage.md",
		},
		func(fs *flag.FlagSet) action {
			var f coverageFlags
			fs.Var(&f.services, "service", "report only this service's operations, such as sqs; repeat for more")
			fs.StringVar(&f.format, "format", "", "text, markdown or json (default json with --output json, else text)")
			fs.BoolVar(&f.switches, "switches", false, "list the fidelity switches in the text report")
			fs.StringVar(&f.overlays, "overlays", "", "directory of overlay files whose evidence to add, such as codegen/overlays")
			return func(ctx context.Context, r *runner, args []string) error {
				if err := wantArgs(args, 0, "no arguments"); err != nil {
					return err
				}
				return r.coverage(ctx, f)
			}
		})
}

func (r *runner) coverage(ctx context.Context, f coverageFlags) error {
	format := f.format
	switch {
	case format == "" && r.output == outputJSON:
		format = formatJSON
	case format == "":
		format = formatText
	case format != formatText && format != formatMarkdown && format != formatJSON:
		return &usageError{msg: fmt.Sprintf("--format %q is not text, markdown or json", format)}
	}
	report, source, err := r.coverageReport(ctx)
	if err != nil {
		return err
	}
	if f.overlays != "" {
		entries, err := r.readOverlays(f.overlays)
		if err != nil {
			return err
		}
		report.Overlays = coverage.OverlaysRead{Entries: entries}
	}
	if err := filterServices(&report, f.services); err != nil {
		return err
	}
	var out []byte
	switch format {
	case formatJSON:
		if out, err = report.JSON(); err != nil {
			return err
		}
		out = append(out, '\n')
	case formatMarkdown:
		out = report.Markdown()
	default:
		var b strings.Builder
		if err := writeCoverageText(&b, report, f.switches, source); err != nil {
			return err
		}
		out = []byte(b.String())
	}
	if _, err := r.o.Stdout.Write(out); err != nil {
		return fmt.Errorf("writing the report: %w", err)
	}
	return nil
}

// coverageReport asks the server at the endpoint for its report, or, when
// none answers, builds the emulator in process and asks it.
func (r *runner) coverageReport(ctx context.Context) (coverage.Report, string, error) {
	c, err := r.client()
	if err != nil {
		return coverage.Report{}, "", err
	}
	data, err := c.Coverage(ctx, formatJSON)
	source := "the server at " + r.endpointURL()
	if unreachable, ok := errors.AsType[*admin.UnreachableError](err); ok && unreachable != nil {
		data, err = offlineCoverage(ctx, r.o.Version)
		source = "this binary (no server answered at " + r.endpointURL() + ")"
	}
	if err != nil {
		return coverage.Report{}, "", err
	}
	report, err := coverage.ParseJSON(data)
	return report, source, err
}

// offlineCoverage wires the emulator in process, without listening, and
// asks its admin API for the report, so the offline report is the one a
// server would send.
func offlineCoverage(ctx context.Context, version string) ([]byte, error) {
	srv, err := server.New(server.Config{LogOutput: io.Discard, Version: version}) //nolint:contextcheck // server.New takes no context: wiring does no I/O a caller could cancel
	if err != nil {
		return nil, fmt.Errorf("building the emulator in process for an offline report: %w", err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, admin.Prefix+"/coverage?format=json", http.NoBody))
	if err := srv.Close(); err != nil { //nolint:contextcheck // Close takes no context: it releases files and memory
		return nil, fmt.Errorf("releasing the in-process emulator: %w", err)
	}
	if rec.Code != http.StatusOK {
		return nil, fmt.Errorf("the in-process admin API answered GET %s/coverage with %d: %s", admin.Prefix, rec.Code, rec.Body)
	}
	return rec.Body.Bytes(), nil
}

// filterServices keeps the services named; an unknown name is refused
// with the known ones.
func filterServices(report *coverage.Report, names []string) error {
	if len(names) == 0 {
		return nil
	}
	known := make([]string, 0, len(report.Services))
	for _, s := range report.Services {
		known = append(known, string(s.Service))
	}
	for _, n := range names {
		if !slices.Contains(known, n) {
			return &usageError{msg: fmt.Sprintf("--service %q is not a service; the services are %s", n, strings.Join(known, ", "))}
		}
	}
	report.Services = slices.DeleteFunc(report.Services, func(s coverage.Service) bool { return !slices.Contains(names, string(s.Service)) })
	return nil
}

// readOverlays reads the evidence of every overlay file in dir.
func (r *runner) readOverlays(dir string) ([]coverage.OverlayEntry, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, &usageError{msg: fmt.Sprintf("--overlays %q: %v", dir, err)}
	}
	if len(files) == 0 {
		return nil, &usageError{msg: fmt.Sprintf("--overlays %q holds no overlay files; point it at codegen/overlays in the repository", dir)}
	}
	var out []coverage.OverlayEntry
	for _, f := range files {
		data, err := r.o.ReadFile(f)
		if err != nil {
			return nil, &localError{op: "reading overlay " + f, err: err, next: "check that --overlays names a readable directory such as codegen/overlays, then run 'understudy coverage' again"}
		}
		entries, err := coverage.ParseOverlay(filepath.Base(f), data)
		if err != nil {
			return nil, &localError{op: "parsing overlay " + f, err: err, next: "fix or remove " + f + " under --overlays, then run 'understudy coverage' again"}
		}
		out = append(out, entries...)
	}
	return out, nil
}

// writeCoverageText writes the three measures apart, never added together.
func writeCoverageText(w io.Writer, report coverage.Report, switches bool, source string) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Protocol compliance\tcases\tpassed\tskipped\tfailed")
	for _, s := range report.Protocols {
		fmt.Fprintf(tw, "  %s %s\t%d\t%d\t%d\t%d\n", s.Protocol, s.Kind, s.Cases, s.Passed, len(s.Skipped), s.Failed)
	}
	fmt.Fprintln(tw, "\nOperations\tmodel\tfull\tstored-only\tpartial\tunbound")
	for _, s := range report.Services {
		n := map[string]int{}
		for _, op := range s.Operations {
			n[coverage.StatusName(op.Status)]++
		}
		fmt.Fprintf(tw, "  %s\t%d\t%d\t%d\t%d\t%d\n", s.Service, len(s.Operations), n["full"], n["stored-only"], n["partial"], n["unbound"])
	}
	fmt.Fprintf(tw, "\nFidelity switches: %d\n", len(report.Switches))
	if switches {
		for _, s := range report.Switches {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", s.Name, s.Value, s.Label, s.Ref)
		}
	}
	if read, ok := report.Overlays.(coverage.OverlaysRead); ok {
		fmt.Fprintf(tw, "Overlay evidence items: %d\n", len(read.Entries))
	}
	fmt.Fprintf(tw, "\nSource: %s\n", source)
	return tw.Flush()
}
