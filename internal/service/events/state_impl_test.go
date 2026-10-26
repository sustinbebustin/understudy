package events_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/events"
	"github.com/sustinbebustin/understudy/internal/store"
)

// storedV2 is a version 2 section of the caller's scope: the default bus,
// materialized with a tag, and the custom bus orders with every setting, a
// policy of three statements (two without a Sid) and a tag; the rule d on
// the default bus and r on orders, each with one target (eventbridge-buses.md
// 10.2).
const storedV2 = `{"buses":{` +
	`"default":{"createdAt":"2000-01-01T00:00:00Z","modifiedAt":"2000-01-01T00:00:00Z","name":"default","tags":{"k":"v"}},` +
	`"orders":{"createdAt":"2000-01-01T00:00:01Z","deadLetter":{"arn":"arn:aws:sqs:us-east-1:000000000000:bus-dlq"},"description":"d",` +
	`"kmsKeyIdentifier":"alias/orders","log":{"includeDetail":"FULL","level":"ERROR"},"modifiedAt":"2000-01-01T00:00:02Z","name":"orders",` +
	`"policy":{"statements":[{"json":"{\"Sid\":\"s1\"}","sid":"s1"},{"json":"{}"},{"json":"{}"}]},"tags":{"team":"a"}}},` +
	`"firstUse":"2000-01-01T00:00:00Z",` +
	`"rules":{` +
	`"default":{"d":{"name":"d","state":"ENABLED","targets":[{"arn":"arn:aws:sqs:us-east-1:000000000000:q","id":"q","input":{"kind":"matched-event"},"seq":1}],"trigger":{"kind":"pattern","pattern":"{\"source\":[\"s\"]}"}}},` +
	`"orders":{"r":{"name":"r","state":"ENABLED","targets":[{"arn":"arn:aws:sqs:us-east-1:000000000000:q","id":"q","input":{"kind":"matched-event"},"seq":2}],"trigger":{"kind":"pattern","pattern":"{\"source\":[\"s\"]}"}}}},` +
	`"targetSeq":2}`

// exportOne exports the service's state, which must be one section.
func exportOne(h *harness) store.Section {
	h.Helper()
	sections, err := h.svc.Export()
	if err != nil {
		h.Fatal(err)
	}
	if len(sections) != 1 {
		h.Fatalf("Export = %d sections, want 1", len(sections))
	}
	return sections[0]
}

// replaced is data with each old text replaced by its new one; every old
// text must occur.
func replaced(h *harness, data string, pairs ...string) string {
	h.Helper()
	for i := 0; i+1 < len(pairs); i += 2 {
		if !strings.Contains(data, pairs[i]) {
			h.Fatalf("no %q in %s", pairs[i], data)
		}
		data = strings.ReplaceAll(data, pairs[i], pairs[i+1])
	}
	return data
}

// importData imports data as the caller's scope's section, at the running
// schema version, in place of sec's data.
func importData(h *harness, sec store.Section, data string) error {
	sec.Data = []byte(data)
	return h.svc.Import([]store.Section{sec})
}

// A version 2 section with buses and rules keyed by bus loads, lists each
// stored bus and then each rule under its two-form ARN (eventbridge-buses.md
// 5.2), and exports as it was loaded (storedV2 is in the codec's canonical
// member order).
func TestImportLoadsBusesAndRulesByBus(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("seed", `{"source": ["s"]}`)
		sec := exportOne(h)
		if err := importData(h, sec, storedV2); err != nil {
			t.Fatalf("Import = %v", err)
		}
		resources := h.svc.Resources(h.Scope)
		arns := make([]string, 0, len(resources))
		summaries := make([]string, 0, len(resources))
		for _, r := range resources {
			arns = append(arns, r.ARN.String())
			summaries = append(summaries, r.Summary)
		}
		wantARNs := []string{busPrefix + "default", busPrefix + "orders", ruleARN + "d", ruleARN + "orders/r"}
		if !slices.Equal(arns, wantARNs) {
			t.Errorf("Resources = %v, want %v", arns, wantARNs)
		}
		if want := []string{"1 rules", "1 rules", "ENABLED, 1 targets", "ENABLED, 1 targets"}; !slices.Equal(summaries, want) {
			t.Errorf("summaries = %v, want %v", summaries, want)
		}
		must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "d"})
		if after := exportOne(h); !sameJSON(t, string(after.Data), storedV2) {
			t.Errorf("export after the import = %s\nwant %s", after.Data, storedV2)
		}
	})
}

// Rules are stored under their bus's name, the default bus's as default.
func TestSnapshotKeysRulesByBus(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("r", `{"source": ["s"]}`)
		var st struct {
			Buses map[string]json.RawMessage            `json:"buses"`
			Rules map[string]map[string]json.RawMessage `json:"rules"`
		}
		if err := json.Unmarshal(exportOne(h).Data, &st); err != nil {
			t.Fatal(err)
		}
		if _, ok := st.Rules["default"]["r"]; !ok || len(st.Rules) != 1 {
			t.Errorf("rules = %v, want r under default only", st.Rules)
		}
		if st.Buses != nil {
			t.Errorf("buses = %v, want none: a rule does not materialize the default bus", st.Buses)
		}
	})
}

// Before the first release a version 1 section, written by an older
// binary, is refused naming both versions rather than migrated (ADR 0009),
// also when it holds only default-bus rules.
func TestSnapshotRefusesAVersion1RulesSection(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("r", `{"source": ["s"]}`)
		sec := exportOne(h)
		sec.Version = 1
		err := h.svc.Import([]store.Section{sec})
		vm, ok := errors.AsType[*store.VersionMismatchError](err)
		if !ok {
			t.Fatalf("Import(a version 1 section) = %v, want a *store.VersionMismatchError", err)
		}
		if vm.Section != 1 || vm.Running != 2 {
			t.Errorf("mismatch %+v, want section 1 refused by running 2", vm)
		}
		must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r"})
	})
}

// A snapshot holding a bus no call could have stored fails to load naming
// the bus and leaves the state as it was. Statements without a Sid may
// repeat; a Sid may not.
func TestImportRefusesStoredBuses(t *testing.T) {
	const tail = "; the snapshot was not loaded. Fix or remove the bus in the snapshot, or recreate it with CreateEventBus"
	for _, tt := range []struct {
		name   string
		pairs  []string
		bus    string
		reason string
	}{
		{"key with a slash", []string{`"orders":{"createdAt"`, `"a/b":{"createdAt"`, `"name":"orders"`, `"name":"a/b"`, `"orders":{"r"`, `"a/b":{"r"`}, "a/b", "not an event bus name"},
		{"key over 256 characters", []string{`"orders":{"createdAt"`, `"` + strings.Repeat("o", 257) + `":{"createdAt"`, `"name":"orders"`, `"name":"` + strings.Repeat("o", 257) + `"`}, strings.Repeat("o", 257), "not an event bus name"},
		{"name other than its key", []string{`"name":"orders"`, `"name":"other"`}, "orders", `is named "other"`},
		{"51 tags", []string{`"tags":{"team":"a"}`, `"tags":` + manyTags(51)}, "orders", "has 51 tags; a bus has at most 50"},
		{"statement that is a list", []string{`{"json":"{\"Sid\":\"s1\"}","sid":"s1"}`, `{"json":"[]","sid":"s1"}`}, "orders", `Sid "s1") that is not a JSON object`},
		{"statement that is not JSON", []string{`{"json":"{}"},{"json":"{}"}`, `{"json":"{"},{"json":"{}"}`}, "orders", "number 2"},
		{"two statements with one Sid", []string{`{"json":"{}"},{"json":"{}"}`, `{"json":"{}","sid":"s1"},{"json":"{}"}`}, "orders", `two policy statements with the Sid "s1"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			run(t, func(h *harness) {
				h.patternRule("keep", `{"source": ["s"]}`)
				sec := exportOne(h)
				err := importData(h, sec, replaced(h, storedV2, tt.pairs...))
				be, ok := errors.AsType[*events.StoredBusError](err)
				if !ok || be.Bus != tt.bus || !strings.Contains(be.Reason, tt.reason) {
					h.Fatalf("Import = %v, want a *StoredBusError on %q saying %q", err, tt.bus, tt.reason)
				}
				if want := `events: the stored event bus "` + tt.bus + `" ` + be.Reason + tail; be.Error() != want || !strings.HasSuffix(err.Error(), ": "+want) {
					h.Errorf("error = %q, want it to end with the cause %q", err.Error(), want)
				}
				must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "keep"})
			})
		})
	}
}

// A snapshot holding a rule no call could have stored on its bus fails to
// load naming the rule and its bus: a rule of a bus the snapshot does not
// hold, a schedule on a custom bus (eventbridge-buses.md 5.3), or a target
// sequence number another bus's rule has, since TargetSeq counts across
// the scope's buses.
func TestImportRefusesStoredRulesByBus(t *testing.T) {
	const tail = "; the snapshot was not loaded. Fix or remove the rule in the snapshot, or recreate it with PutRule"
	for _, tt := range []struct {
		name      string
		pairs     []string
		rule, bus string
		reason    string
	}{
		{"rule of a bus not held", []string{`"orders":{"r"`, `"ghost":{"r"`}, "r", "ghost", "is stored under a bus the snapshot does not hold"},
		{"schedule on a custom bus", []string{
			`"orders":{"r":{"name":"r","state":"ENABLED","targets":[{"arn":"arn:aws:sqs:us-east-1:000000000000:q","id":"q","input":{"kind":"matched-event"},"seq":2}],"trigger":{"kind":"pattern","pattern":"{\"source\":[\"s\"]}"}}}`,
			`"orders":{"r":{"name":"r","state":"ENABLED","targets":[{"arn":"arn:aws:sqs:us-east-1:000000000000:q","id":"q","input":{"kind":"matched-event"},"seq":2}],"trigger":{"kind":"schedule","schedule":"rate(5 minutes)"}}}`,
		}, "r", "orders", "only a rule on the default event bus"},
		{"sequence number of another bus's rule", []string{`"seq":2`, `"seq":1`}, "r", "orders", `a target of the rule "d" on the event bus "default" also has`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			run(t, func(h *harness) {
				h.patternRule("keep", `{"source": ["s"]}`)
				sec := exportOne(h)
				err := importData(h, sec, replaced(h, storedV2, tt.pairs...))
				re, ok := errors.AsType[*events.StoredRuleError](err)
				if !ok || re.Rule != tt.rule || re.Bus != tt.bus || !strings.Contains(re.Reason, tt.reason) {
					h.Fatalf("Import = %v, want a *StoredRuleError on %q of %q saying %q", err, tt.rule, tt.bus, tt.reason)
				}
				if want := `events: the stored rule "` + tt.rule + `" on the event bus "` + tt.bus + `" ` + re.Reason + tail; re.Error() != want || !strings.HasSuffix(err.Error(), ": "+want) {
					h.Errorf("error = %q, want it to end with the cause %q", err.Error(), want)
				}
				must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "keep"})
			})
		})
	}
}

// A bus entry in rules with no rules loads as no rules, and an empty
// default bus entry does not keep the scope in the snapshot.
func TestImportDropsEmptyRuleMaps(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("seed", `{"source": ["s"]}`)
		sec := exportOne(h)
		if err := importData(h, sec, `{"rules":{"default":{},"ghost":null}}`); err != nil {
			t.Fatalf("Import = %v", err)
		}
		if sections, err := h.svc.Export(); err != nil || len(sections) != 0 {
			t.Errorf("Export = %d sections, %v; want none", len(sections), err)
		}
	})
}

// firstUse is the admission instant of the first call that changes the
// scope's state (events.default-bus.creation-time, eventbridge-buses.md
// 10.5): a call that changes nothing does not set it, a later change does
// not move it. A scope left with only firstUse is still saved, so an
// unmaterialized default bus keeps its creation time across a snapshot.
func TestFirstUseIsTheFirstChange(t *testing.T) {
	run(t, func(h *harness) {
		firstUse := func() time.Time {
			h.Helper()
			var st struct {
				FirstUse time.Time `json:"firstUse"`
			}
			if err := json.Unmarshal(exportOne(h).Data, &st); err != nil {
				h.Fatal(err)
			}
			return st.FirstUse
		}
		must(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: "missing"})
		must(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{})
		h.advance(time.Minute)
		first := h.Clock.Now().Wire()
		h.patternRule("r", `{"source": ["s"]}`)
		h.advance(time.Minute)
		h.target("r", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		must(h, eventbridgeapi.OpDisableRule, &eventbridgeapi.DisableRuleInput{Name: "r"})
		must(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{ResourceARN: ruleARN + "r", Tags: []eventbridgeapi.Tag{{Key: "k", Value: "v"}}})
		if got := firstUse(); !got.Equal(first) {
			t.Errorf("firstUse = %v, want the first PutRule's admission %v", got, first)
		}
		must(h, eventbridgeapi.OpRemoveTargets, &eventbridgeapi.RemoveTargetsInput{Rule: "r", Ids: []string{"q"}})
		must(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: "r"})
		if got := firstUse(); !got.Equal(first) {
			t.Errorf("firstUse of a scope left without rules = %v, want %v", got, first)
		}
		sections := []store.Section{exportOne(h)}
		h.svc.Reset(kernel.ResetScope{})
		h.advance(time.Minute)
		if err := h.svc.Import(sections); err != nil {
			t.Fatalf("Import = %v", err)
		}
		if d := h.describeBus(nil); d.CreationTime == nil || !d.CreationTime.Equal(first) {
			t.Errorf("default bus after the round trip = %+v, want CreationTime %v", d, first)
		}
	})
}
