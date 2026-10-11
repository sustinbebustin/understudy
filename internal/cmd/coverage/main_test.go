package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/coverage"
)

var overlayDir = filepath.Join("..", "..", "..", "codegen", "overlays")

// The Markdown report lists the three measures apart, SQS's operations by
// status, and every overlay entry by label.
func TestMarkdownReport(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(t.Context(), []string{"-overlays", overlayDir}, &out, &errOut); code != exitOK {
		t.Fatalf("run = %d: %s", code, errOut.String())
	}
	md := out.String()
	for _, want := range []string{
		"## Protocol compliance", "| awsjson1_0 | request |",
		"## Operations", "### sqs", "| SendMessage |",
		"### Fidelity switches", "### Overlay entries", "| sqs.json |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	if strings.Contains(md, "does not carry the overlay files") {
		t.Error("the report says it has no overlays")
	}
}

// The JSON report reads back, overlays included.
func TestJSONReport(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(t.Context(), []string{"-format", "json", "-overlays", overlayDir}, &out, &errOut); code != exitOK {
		t.Fatalf("run = %d: %s", code, errOut.String())
	}
	r, err := coverage.ParseJSON(out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if o, ok := r.Overlays.(coverage.OverlaysRead); !ok || len(o.Entries) == 0 {
		t.Errorf("overlays = %#v, want the entries read", r.Overlays)
	}
	if len(r.Services) != 12 {
		t.Errorf("%d services, want the twelve wired", len(r.Services))
	}
}

// A bad flag or an overlay directory with nothing in it fails with a
// message saying what to do.
func TestUsageErrors(t *testing.T) {
	for _, tt := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"-format", "html"}, exitUsage, "markdown or json"},
		{[]string{"extra"}, exitUsage, "no arguments"},
		{[]string{"-overlays", t.TempDir()}, exitFailed, "run from the module root or pass -overlays"},
	} {
		var out, errOut bytes.Buffer
		if code := run(t.Context(), tt.args, &out, &errOut); code != tt.code || !strings.Contains(errOut.String(), tt.want) {
			t.Errorf("run(%q) = %d, %q; want %d and %q", tt.args, code, errOut.String(), tt.code, tt.want)
		}
	}
}
