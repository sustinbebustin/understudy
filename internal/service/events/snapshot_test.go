package events_test

import (
	"errors"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// State version 2 holds buses and rules by bus. Before the first release a
// version 1 section, written by an older binary, is refused naming both
// versions rather than migrated (ADR 0009).
func TestSnapshotRefusesAVersion1Section(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("orders")
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		for i := range sections {
			sections[i].Version = 1
		}
		err = h.svc.Import(sections)
		vm, ok := errors.AsType[*store.VersionMismatchError](err)
		if !ok {
			t.Fatalf("Import(a version 1 section) = %v, want a *store.VersionMismatchError", err)
		}
		if vm.Section != 1 || vm.Running != 2 || h.svc.SchemaVersion() != 2 {
			t.Errorf("mismatch %+v, schema version %d; want section 1 refused by running 2", vm, h.svc.SchemaVersion())
		}
	})
}

// A snapshot round trip keeps every bus with its settings, tags and
// policy, the default bus's tags, and the rules and targets of a custom
// bus (eventbridge-buses.md 10.2.6: buses, policies and the default bus's
// state belong in the snapshot, ARCHITECTURE.md 13).
func TestSnapshotRoundTripKeepsBuses(t *testing.T) {
	run(t, func(h *harness) {
		must(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{
			Name: "orders", Description: new("d"), KmsKeyIdentifier: new("alias/orders"),
			DeadLetterConfig: &eventbridgeapi.DeadLetterConfig{Arn: new("arn:aws:sqs:us-east-1:000000000000:bus-dlq")},
			LogConfig:        &eventbridgeapi.LogConfig{IncludeDetail: new(eventbridgeapi.IncludeDetailFull), Level: new(eventbridgeapi.LevelError)},
			Tags:             []eventbridgeapi.Tag{{Key: "team", Value: "a"}},
		})
		h.permit(new("orders"), "s1", "111111111111")
		h.putRuleOn("orders", "r", `{"source": ["shop"]}`)
		h.targetOn("orders", "r", eventbridgeapi.Target{Id: "q", Arn: queueARN, Input: new(`{"k": 1}`)})
		must(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{ResourceARN: busPrefix + "default", Tags: []eventbridgeapi.Tag{{Key: "k", Value: "v"}}})

		type view struct {
			orders, def   *eventbridgeapi.DescribeEventBusOutput
			ordersTags    []eventbridgeapi.Tag
			defTags       []eventbridgeapi.Tag
			rule          *eventbridgeapi.DescribeRuleOutput
			targets       []eventbridgeapi.Target
			defaultsRules int
		}
		look := func() view {
			t.Helper()
			return view{
				orders:        h.describeBus(new("orders")),
				def:           h.describeBus(nil),
				ordersTags:    must(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: busPrefix + "orders"}).Tags,
				defTags:       must(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: busPrefix + "default"}).Tags,
				rule:          must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r", EventBusName: new("orders")}),
				targets:       must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "r", EventBusName: new("orders")}).Targets,
				defaultsRules: len(must(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{}).Rules),
			}
		}
		before := look()
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		if _, err := call(h, eventbridgeapi.OpDescribeEventBus, &eventbridgeapi.DescribeEventBusInput{Name: new("orders")}); err == nil {
			t.Fatal("orders survived the reset")
		}
		if err := h.svc.Import(sections); err != nil {
			t.Fatalf("Import = %v", err)
		}
		h.svc.Rearm(h.Clock.Now())
		if after := look(); !reflect.DeepEqual(after, before) {
			t.Errorf("after the round trip:\n%+v\nwant\n%+v", after, before)
		}
	})
}

// edit is a change to every exported section: each pattern must match.
type edit struct {
	pattern     *regexp.Regexp
	replacement string
}

func applyEdits(h *harness, sections []store.Section, edits []edit) []store.Section {
	h.Helper()
	out := slices.Clone(sections)
	for i := range out {
		data := out[i].Data
		for _, e := range edits {
			if !e.pattern.Match(data) {
				h.Fatalf("section %s has no match for %s: %s", out[i].Path, e.pattern, data)
			}
			data = e.pattern.ReplaceAll(data, []byte(e.replacement))
		}
		out[i].Data = data
	}
	return out
}

// A snapshot holding a bus CreateEventBus, TagResource or PutPermission
// could not have stored fails to load, names the bus, and leaves the state
// as it was. Each case edits the snapshot of the bus orders, with policy
// statements s1 and s2.
func TestImportRefusesBusesNoCallCouldStore(t *testing.T) {
	const tail = "; the snapshot was not loaded. Fix or remove the bus in the snapshot, or recreate it with CreateEventBus"
	for _, tt := range []struct {
		name  string
		edits []edit
		bus   string
	}{
		{"bus named other than its key", []edit{{regexp.MustCompile(`"name":"orders"`), `"name":"other"`}}, "orders"},
		{"bus name CreateEventBus refuses", []edit{
			{regexp.MustCompile(`"orders":\{`), `"a/b":{`},
			{regexp.MustCompile(`"name":"orders"`), `"name":"a/b"`},
		}, "a/b"},
		{"more tags than a bus may have", []edit{{regexp.MustCompile(`"name":"orders"`), `"name":"orders","tags":` + manyTags(51)}}, "orders"},
		{"statement that is not an object", []edit{{regexp.MustCompile(`"json":"(?:[^"\\]|\\.)*"`), `"json":"[]"`}}, "orders"},
		{"two statements with one Sid", []edit{{regexp.MustCompile(`"sid":"s2"`), `"sid":"s1"`}}, "orders"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			run(t, func(h *harness) {
				h.createBus("orders")
				h.permit(new("orders"), "s1", "111111111111")
				h.permit(new("orders"), "s2", "*")
				sections, err := h.svc.Export()
				if err != nil {
					h.Fatal(err)
				}
				err = h.svc.Import(applyEdits(h, sections, tt.edits))
				// Import wraps a section's error with the section's name, so
				// the events text follows that prefix.
				want := `events: the stored event bus "` + tt.bus + `" `
				if err == nil || !strings.Contains(err.Error(), want) || !strings.HasSuffix(err.Error(), tail) {
					h.Fatalf("Import = %v, want an error containing %q and ending %q", err, want, tail)
				}
				if got := slices.Sorted(maps.Keys(bySid(h.policyOf(new("orders"))))); !slices.Equal(got, []string{"s1", "s2"}) {
					h.Errorf("orders' statements after the refused import = %v, want s1 and s2 unchanged", got)
				}
			})
		})
	}
}

// A rule stored under a bus the snapshot does not hold could not have been
// put: the snapshot fails to load, naming the rule and the bus.
func TestImportRefusesRulesOfAnUnknownBus(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("orders")
		h.putRuleOn("orders", "r", `{"source": ["s"]}`)
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		err = h.svc.Import(applyEdits(h, sections, []edit{{regexp.MustCompile(`"orders":\{"r":`), `"ghost":{"r":`}}))
		if want := `events: the stored rule "r" on the event bus "ghost" `; err == nil || !strings.Contains(err.Error(), want) {
			h.Fatalf("Import = %v, want an error containing %q", err, want)
		}
		must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r", EventBusName: new("orders")})
	})
}

// The resources view lists each stored bus with its rule count, then each
// rule under its ARN, rule/<bus>/<name> on a custom bus
// (eventbridge-buses.md 5.2); the default bus appears once a write stores
// it.
func TestResourcesListBusesThenRules(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("orders")
		h.putRuleOn("orders", "r", `{"source": ["s"]}`)
		h.patternRule("d", `{"source": ["s"]}`)
		resources := h.svc.Resources(h.Scope)
		arns := make([]string, 0, len(resources))
		summaries := map[string]string{}
		for _, r := range resources {
			arns = append(arns, r.ARN.String())
			summaries[r.ARN.String()] = r.Summary
		}
		if want := []string{busPrefix + "orders", ruleARN + "d", ruleARNOn("orders", "r")}; !slices.Equal(arns, want) {
			t.Errorf("Resources = %v, want %v", arns, want)
		}
		if s := summaries[busPrefix+"orders"]; s != "1 rules" {
			t.Errorf("orders summary = %q, want \"1 rules\"", s)
		}
		must(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{ResourceARN: busPrefix + "default", Tags: []eventbridgeapi.Tag{{Key: "k", Value: "v"}}})
		var buses []string
		for _, r := range h.svc.Resources(h.Scope) {
			if strings.Contains(r.ARN.Resource, "event-bus/") {
				buses = append(buses, r.ARN.String())
			}
		}
		if want := []string{busPrefix + "default", busPrefix + "orders"}; !slices.Equal(buses, want) {
			t.Errorf("buses in Resources after tagging the default bus = %v, want %v", buses, want)
		}
	})
}
