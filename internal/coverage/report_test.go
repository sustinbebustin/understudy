package coverage_test

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/coverage"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/protocoltest"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/jsonrpc10"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

func empty[In, Out any](context.Context, *kernel.Call, *In) (*Out, *awserr.Error) {
	return new(Out), nil
}

// built returns the report of the synthetic JsonRpc10 service with three
// operations bound, one per coverage, and the rest unbound.
func built(t *testing.T) coverage.Report {
	t.Helper()
	var r coverage.Report
	kerneltest.Run(t, func(k *kerneltest.K) {
		b := jsonrpc10.NewBinder(k.Kernel.Binder(jsonrpc10.Service()))
		b.EmptyInputAndEmptyOutput(empty, registry.Full{})
		b.NoInputAndNoOutput(empty, registry.StoredOnly{Note: "kept, never acted on"})
		b.SimpleScalarProperties(empty, registry.Partial{Note: "no NaN"})
		r = coverage.Build(coverage.Input{
			Registry: k.Kernel.Registry(),
			Bindings: k.Kernel.Bindings(),
			Baseline: protocoltest.Baseline{Suites: []protocoltest.Suite{
				{Protocol: ref.AWSJSON10, Kind: protocoltest.Request, Cases: 4, Passing: []string{"A", "B"}},
			}},
			Skips: protocoltest.Skips{
				{Key: protocoltest.Key{Protocol: ref.AWSJSON10, Kind: protocoltest.Request, ID: "C"}, Category: protocoltest.ClientOnly{}, Reason: "host prefix"},
				{Key: protocoltest.Key{Protocol: ref.AWSJSON10, Kind: protocoltest.Request, ID: "D"}, Category: protocoltest.Gap{}, Reason: "nested error member (internal/awserr)"},
			},
			Ungenerated: []protocoltest.Ungenerated{
				{Key: protocoltest.Key{Protocol: ref.AWSJSON10, Kind: protocoltest.Request, ID: "G"}, Reason: "service not generated"},
			},
			Switches: []fidelity.Entry{
				{Name: "z.inferred", Value: "on", Label: evidence.Inferred, Ref: "research.md#1"},
				{Name: "a.doc", Value: "3", Label: evidence.AWSDoc, Ref: "research.md#2", Record: "R1"},
			},
			Overlays: coverage.OverlaysRead{Entries: []coverage.OverlayEntry{
				{File: "sqs.json", Shape: "com.amazonaws.sqs#X", Label: evidence.Inferred, Source: "sqs.md#4"},
				{File: "sqs.json", Shape: "com.amazonaws.sqs#Y", Label: evidence.AWSRecorded, Source: "R2", Covers: []string{"smithy.api#httpError"}},
			}},
		})
	}, kerneltest.Services(jsonrpc10.Service()))
	return r
}

// Operation coverage is read from the kernel's bindings: what each bind
// site stated, unbound for the rest, ordered by status then name.
func TestBuildReadsOperationStatusFromTheKernel(t *testing.T) {
	r := built(t)
	if len(r.Services) != 1 {
		t.Fatalf("services = %+v", r.Services)
	}
	ops := r.Services[0].Operations
	want := []coverage.Operation{
		{Name: "EmptyInputAndEmptyOutput", Status: coverage.Full{}},
		{Name: "NoInputAndNoOutput", Status: coverage.StoredOnly{Note: "kept, never acted on"}},
		{Name: "SimpleScalarProperties", Status: coverage.Partial{Note: "no NaN"}},
	}
	if !reflect.DeepEqual(ops[:3], want) {
		t.Errorf("first operations = %+v, want %+v", ops[:3], want)
	}
	for _, op := range ops[3:] {
		if op.Status != (coverage.Unbound{}) {
			t.Errorf("%s = %#v, want unbound", op.Name, op.Status)
		}
	}
	if n := len(ops); n != len(jsonrpc10.Service().Operations) {
		t.Errorf("%d operations, want every one of the model's %d", n, len(jsonrpc10.Service().Operations))
	}
}

// Protocol compliance counts passed, skipped and failed cases apart, each
// skip with its category and reason, and lists the cases left ungenerated
// beside the counts. A gap is understudy's own failure: it counts as
// failed, never as skipped, and is listed with its reason.
func TestBuildCountsComplianceFromTheBaseline(t *testing.T) {
	r := built(t)
	want := []coverage.Suite{{
		Protocol: ref.AWSJSON10, Kind: protocoltest.Request, Cases: 4, Passed: 2, Failed: 1,
		Skipped:       []coverage.SkippedCase{{ID: "C", Category: "client-only", Reason: "host prefix"}},
		KnownFailures: []coverage.KnownFailure{{ID: "D", Reason: "nested error member (internal/awserr)"}},
	}}
	if !reflect.DeepEqual(r.Protocols, want) {
		t.Errorf("Protocols = %+v, want %+v", r.Protocols, want)
	}
	ungenerated := []coverage.UngeneratedCase{{Protocol: ref.AWSJSON10, Kind: protocoltest.Request, ID: "G", Reason: "service not generated"}}
	if !reflect.DeepEqual(r.Ungenerated, ungenerated) {
		t.Errorf("Ungenerated = %+v, want %+v", r.Ungenerated, ungenerated)
	}
}

// The Markdown has the three measures in their own sections and no line
// that adds them together; it lists every operation by status, every
// switch and every overlay item with its label.
func TestMarkdownStatesTheThreeMeasuresApart(t *testing.T) {
	md := string(built(t).Markdown())
	for _, want := range []string{
		"## Protocol compliance",
		"| awsjson1_0 | request | 4 | 2 | 1 | 1 |",
		"| awsjson1_0 | request | `C` | client-only | host prefix |",
		"### Known failures",
		"| awsjson1_0 | request | `D` | nested error member (internal/awserr) |",
		"1 server-runnable case is not generated",
		"| awsjson1_0 | request | `G` | service not generated |",
		"## Operations",
		"| EmptyInputAndEmptyOutput | full |  |",
		"| NoInputAndNoOutput | stored-only | kept, never acted on |",
		"| SimpleScalarProperties | partial | no NaN |",
		"## Evidence",
		"2 switches: 1 aws-doc, 1 inferred.",
		"| `a.doc` | `3` | aws-doc | research.md#2 | R1 |",
		"2 evidence items: 1 aws-recorded, 1 inferred.",
		"| aws-recorded | sqs.json | `com.amazonaws.sqs#Y` | smithy.api#httpError | R2 |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown lacks %q:\n%s", want, md)
		}
	}
	if i, j := strings.Index(md, "`a.doc`"), strings.Index(md, "`z.inferred`"); i < 0 || j < i {
		t.Errorf("switches are not ordered strongest evidence first")
	}
	if strings.Contains(strings.ToLower(md), "total") {
		t.Errorf("Markdown sums the measures:\n%s", md)
	}
}

// A report built without the overlay files says where they are listed.
func TestMarkdownWithoutOverlays(t *testing.T) {
	md := string(coverage.Build(coverage.Input{}).Markdown())
	if !strings.Contains(md, "does not carry the overlay files") {
		t.Errorf("Markdown = %s", md)
	}
}

// A pipe or line break in a cell cannot break the table.
func TestMarkdownEscapesCells(t *testing.T) {
	md := string(coverage.Build(coverage.Input{Switches: []fidelity.Entry{
		{Name: "s", Value: "a|b", Label: evidence.Inferred, Ref: "line\nbreak"},
	}}).Markdown())
	if !strings.Contains(md, "| `s` | `a\\|b` | inferred | line break |  |") {
		t.Errorf("Markdown = %s", md)
	}
}

// roundTrip checks that JSON reads back as the report it wrote: the same
// JSON and the same Markdown. Empty lists read back empty rather than nil,
// which neither form tells apart.
func roundTrip(t interface {
	Helper()
	Fatalf(format string, args ...any)
}, r coverage.Report,
) {
	t.Helper()
	data, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON = %v", err)
	}
	got, err := coverage.ParseJSON(data)
	if err != nil {
		t.Fatalf("ParseJSON(%s) = %v", data, err)
	}
	again, err := got.JSON()
	if err != nil || !bytes.Equal(again, data) || !bytes.Equal(got.Markdown(), r.Markdown()) {
		t.Fatalf("round trip of %s gave %s, %v", data, again, err)
	}
}

// JSON reads back as the report it wrote.
func TestJSONRoundTrip(t *testing.T) {
	roundTrip(t, built(t))
}

// Random reports survive JSON, whatever their statuses and overlays.
func TestJSONRoundTripProperty(t *testing.T) {
	status := rapid.Custom(func(t *rapid.T) coverage.Status {
		switch rapid.IntRange(0, 3).Draw(t, "status") {
		case 0:
			return coverage.Full{}
		case 1:
			return coverage.StoredOnly{Note: rapid.String().Draw(t, "note")}
		case 2:
			return coverage.Partial{Note: rapid.String().Draw(t, "note")}
		default:
			return coverage.Unbound{}
		}
	})
	rapid.Check(t, func(t *rapid.T) {
		r := coverage.Report{Overlays: coverage.OverlaysNotCarried{}}
		for _, name := range rapid.SliceOf(rapid.StringMatching(`[A-Z][a-z]{1,8}`)).Draw(t, "ops") {
			if len(r.Services) == 0 {
				r.Services = []coverage.Service{{Service: "svc"}}
			}
			r.Services[0].Operations = append(r.Services[0].Operations, coverage.Operation{Name: name, Status: status.Draw(t, "status")})
		}
		if rapid.Bool().Draw(t, "overlays") {
			r.Overlays = coverage.OverlaysRead{Entries: []coverage.OverlayEntry{{
				File: "a.json", Shape: rapid.String().Draw(t, "shape"), Label: rapid.SampledFrom(evidence.Labels()).Draw(t, "label"),
				Source: rapid.String().Draw(t, "source"),
			}}}
		}
		roundTrip(t, r)
	})
}
