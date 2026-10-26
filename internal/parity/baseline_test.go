package parity

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"
)

// kmsAliasBaseline is one scenario's ratchet entry as baseline.json writes
// it: a step that matches and is routed, a reconstructed step with no
// operation, and a settled overlay claim.
const kmsAliasBaseline = `{
  "version": 1,
  "scenarios": [
    {
      "scenario": "kms/KMS-alias",
      "steps": [
        {"step": "01-duplicate", "fixture": "kms/KMS-alias/01-duplicate.json", "operation": "kms.CreateAlias", "outcome": {"kind": "match"}},
        {"step": "02-duplicate-raw", "fixture": "kms/KMS-alias/02-duplicate-raw.json", "outcome": {"kind": "reconstructed"}}
      ],
      "claims": [
        {"target": {"kind": "overlay", "shape": "com.amazonaws.kms#AlreadyExistsException"}, "step": "01-duplicate", "field": "status", "result": {"kind": "settled"}}
      ]
    }
  ]
}
`

func kmsAliasBaselineValue() Baseline {
	return Baseline{Scenarios: []BaselineScenario{{
		Scenario: "kms/KMS-alias",
		Steps: []BaselineStep{
			{Step: "01-duplicate", Fixture: kmsDuplicate, Operation: "kms.CreateAlias", Outcome: Match{}},
			{Step: "02-duplicate-raw", Fixture: kmsDuplicateRaw, Outcome: Reconstructed{}},
		},
		Claims: []BaselineClaim{
			{Target: OverlayTarget{Shape: "com.amazonaws.kms#AlreadyExistsException"}, Step: "01-duplicate", Field: "status", Result: Settled{}},
		},
	}}}
}

func TestParseBaselineReadsEveryKind(t *testing.T) {
	t.Parallel()
	got, err := ParseBaseline([]byte(kmsAliasBaseline))
	if err != nil {
		t.Fatalf("ParseBaseline: %v", err)
	}
	if diff := cmp.Diff(kmsAliasBaselineValue(), got, equateEmpty); diff != "" {
		t.Errorf("ParseBaseline (-want +got):\n%s", diff)
	}

	every := `{"version": 1, "scenarios": [{"scenario": "sts/R15", "steps": [
  {"step": "a", "fixture": "sts/R15/a.json", "outcome": {"kind": "differs", "reason": "status, error/code"}},
  {"step": "b", "fixture": "sts/R15/b.json", "outcome": {"kind": "skipped", "reason": "docker"}},
  {"step": "c", "fixture": "sts/R15/c.json", "outcome": {"kind": "skipped", "reason": "rest-routing"}},
  {"step": "d", "fixture": "sts/R15/d.json", "outcome": {"kind": "skipped", "reason": "no-exchange"}},
  {"step": "e", "fixture": "sts/R15/e.json", "outcome": {"kind": "pending"}}
], "claims": [
  {"target": {"kind": "switch", "name": "sts.x"}, "step": "a", "field": "status", "result": {"kind": "contradicted"}},
  {"target": {"kind": "switch", "name": "sts.y"}, "step": "a", "field": "body", "result": {"kind": "insensitive", "by": "waiver"}},
  {"target": {"kind": "overlay", "shape": "com.amazonaws.sts#X", "covers": ["dev.understudy#a"]}, "step": "b", "field": "status", "result": {"kind": "insensitive", "by": "normalizer"}},
  {"target": {"kind": "switch", "name": "sts.z"}, "step": "e", "field": "status", "result": {"kind": "open"}}
]}]}`
	got, err = ParseBaseline([]byte(every))
	if err != nil {
		t.Fatalf("ParseBaseline(every kind): %v", err)
	}
	want := Baseline{Scenarios: []BaselineScenario{{
		Scenario: "sts/R15",
		Steps: []BaselineStep{
			{Step: "a", Fixture: "sts/R15/a.json", Outcome: Differs{Reason: "status, error/code"}},
			{Step: "b", Fixture: "sts/R15/b.json", Outcome: Skipped{Reason: SkipDocker}},
			{Step: "c", Fixture: "sts/R15/c.json", Outcome: Skipped{Reason: SkipRESTRouting}},
			{Step: "d", Fixture: "sts/R15/d.json", Outcome: Skipped{Reason: SkipNoExchange}},
			{Step: "e", Fixture: "sts/R15/e.json", Outcome: Pending{}},
		},
		Claims: []BaselineClaim{
			{Target: SwitchTarget{Name: "sts.x"}, Step: "a", Field: "status", Result: Contradicted{}},
			{Target: SwitchTarget{Name: "sts.y"}, Step: "a", Field: "body", Result: Insensitive{By: HiddenByWaiver}},
			{Target: OverlayTarget{Shape: "com.amazonaws.sts#X", Covers: []string{"dev.understudy#a"}}, Step: "b", Field: "status", Result: Insensitive{By: HiddenByNormalizer}},
			{Target: SwitchTarget{Name: "sts.z"}, Step: "e", Field: "status", Result: Open{}},
		},
	}}}
	if diff := cmp.Diff(want, got, equateEmpty); diff != "" {
		t.Errorf("ParseBaseline(every kind) (-want +got):\n%s", diff)
	}
}

func TestParseBaselineIsStrict(t *testing.T) {
	t.Parallel()
	step := func(outcome string) string {
		return `{"version": 1, "scenarios": [{"scenario": "kms/KMS-alias", "steps": [{"step": "s", "fixture": "kms/KMS-alias/s.json", "outcome": ` + outcome + `}]}]}`
	}
	claim := func(target, result string) string {
		return `{"version": 1, "scenarios": [{"scenario": "kms/KMS-alias", "steps": [{"step": "s", "fixture": "kms/KMS-alias/s.json", "outcome": {"kind": "match"}}], "claims": [{"target": ` + target + `, "step": "s", "field": "status", "result": ` + result + `}]}]}`
	}
	sw := `{"kind": "switch", "name": "a.b"}`
	cases := map[string]string{
		"not json":                  `{`,
		"version 2":                 `{"version": 2, "scenarios": []}`,
		"unknown member":            `{"version": 1, "scenarios": [], "extra": 1}`,
		"unknown outcome kind":      step(`{"kind": "passed"}`),
		"differs without a reason":  step(`{"kind": "differs"}`),
		"skipped without a reason":  step(`{"kind": "skipped"}`),
		"skipped for another cause": step(`{"kind": "skipped", "reason": "gpu"}`),
		"match with a reason":       step(`{"kind": "match", "reason": "x"}`),
		"unknown target kind":       claim(`{"kind": "rule", "name": "a"}`, `{"kind": "settled"}`),
		"unknown result kind":       claim(sw, `{"kind": "passed"}`),
		"insensitive without by":    claim(sw, `{"kind": "insensitive"}`),
		"insensitive by another":    claim(sw, `{"kind": "insensitive", "by": "luck"}`),
		"step without a fixture":    `{"version": 1, "scenarios": [{"scenario": "kms/KMS-alias", "steps": [{"step": "s", "outcome": {"kind": "match"}}]}]}`,
	}
	for name, doc := range cases {
		_, err := ParseBaseline([]byte(doc))
		var be *BaselineError
		if !errors.As(err, &be) || be.Reason == "" {
			t.Errorf("%s: ParseBaseline error = %v; want a *BaselineError with a reason", name, err)
		}
	}
	// The documents the cases break are valid unbroken.
	for _, doc := range []string{step(`{"kind": "match"}`), claim(sw, `{"kind": "settled"}`), `{"version": 1, "scenarios": []}`} {
		if _, err := ParseBaseline([]byte(doc)); err != nil {
			t.Errorf("ParseBaseline(%s): %v; want nil", doc, err)
		}
	}
}

// The canonical form: version then scenarios, two-space indentation, a
// trailing newline; an empty operation and empty claims left out.
func TestBaselineEncodeIsCanonical(t *testing.T) {
	t.Parallel()
	enc, err := kmsAliasBaselineValue().Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.HasPrefix(enc, []byte("{\n  \"version\": 1,\n  \"scenarios\": [\n")) || !bytes.HasSuffix(enc, []byte("}\n")) {
		t.Errorf("Encode =\n%s\nwant version then scenarios, two-space indentation, a trailing newline", enc)
	}
	if got := bytes.Count(enc, []byte(`"operation"`)); got != 1 {
		t.Errorf("Encode holds %d operation members; want 1, the reconstructed step's left out:\n%s", got, enc)
	}
	back, err := ParseBaseline(enc)
	if err != nil {
		t.Fatalf("ParseBaseline(Encode): %v\n%s", err, enc)
	}
	if diff := cmp.Diff(kmsAliasBaselineValue(), back, equateEmpty); diff != "" {
		t.Errorf("ParseBaseline(Encode) (-want +got):\n%s", diff)
	}

	noClaims := Baseline{Scenarios: []BaselineScenario{{Scenario: "kms/KMS-alias", Steps: kmsAliasBaselineValue().Scenarios[0].Steps}}}
	enc, err = noClaims.Encode()
	if err != nil {
		t.Fatalf("Encode(no claims): %v", err)
	}
	if bytes.Contains(enc, []byte(`"claims"`)) {
		t.Errorf("Encode(no claims) =\n%s\nwant no claims member", enc)
	}
}

// BaselineOf keeps outcomes and results, drops the differences, and sorts
// scenarios by id while steps and claims keep scenario order.
func TestBaselineOf(t *testing.T) {
	t.Parallel()
	results := []Result{
		{
			Scenario: "sts/R15",
			Steps: []StepResult{
				{
					Step: "x-assume-11-policy-arns", Fixture: "sts/R15/x-assume-11-policy-arns.json", Operation: "sts.AssumeRole", Outcome: Differs{Reason: "error/message"},
					Differences: []Difference{{Path: "error/message", Kind: Changed, Expected: "a", Actual: "b"}},
				},
				{Step: "06-assume-ok", Fixture: stsAssumeOK, Operation: "sts.AssumeRole", Outcome: Match{}},
			},
		},
		{
			Scenario: "kms/KMS-alias",
			Steps: []StepResult{
				{Step: "01-duplicate", Fixture: kmsDuplicate, Operation: "kms.CreateAlias", Outcome: Match{}},
				{Step: "02-duplicate-raw", Fixture: kmsDuplicateRaw, Outcome: Reconstructed{}},
			},
			Claims: []ClaimResult{{
				Claim: Claim{Target: OverlayTarget{Shape: "com.amazonaws.kms#AlreadyExistsException"}, Step: "01-duplicate", Field: "status"},
				State: Settled{},
			}},
		},
	}
	want := Baseline{Scenarios: []BaselineScenario{
		kmsAliasBaselineValue().Scenarios[0],
		{Scenario: "sts/R15", Steps: []BaselineStep{
			{Step: "x-assume-11-policy-arns", Fixture: "sts/R15/x-assume-11-policy-arns.json", Operation: "sts.AssumeRole", Outcome: Differs{Reason: "error/message"}},
			{Step: "06-assume-ok", Fixture: stsAssumeOK, Operation: "sts.AssumeRole", Outcome: Match{}},
		}},
	}}
	if diff := cmp.Diff(want, BaselineOf(results), equateEmpty); diff != "" {
		t.Errorf("BaselineOf (-want +got):\n%s", diff)
	}
}

// withStep returns a copy of the KMS-alias baseline with step i's outcome
// replaced.
func withStep(i int, o Outcome) Baseline {
	b := kmsAliasBaselineValue()
	b.Scenarios[0].Steps[i].Outcome = o
	return b
}

func withClaim(r ClaimState) Baseline {
	b := kmsAliasBaselineValue()
	b.Scenarios[0].Claims[0].Result = r
	return b
}

const (
	dupKey   = "kms/KMS-alias#01-duplicate"
	rawKey   = "kms/KMS-alias#02-duplicate-raw"
	claimKey = "kms/KMS-alias#01-duplicate overlay:com.amazonaws.kms#AlreadyExistsException status"
)

// The ratchet fails on any change from the baseline, and says which way it
// went: a regression from match or settled, or an improvement that was not
// recorded.
func TestRatchet(t *testing.T) {
	t.Parallel()
	base := kmsAliasBaselineValue()
	if got := Ratchet(base, base); got != nil {
		t.Errorf("Ratchet(b, b) = %v; want nil", got)
	}
	cases := []struct {
		name      string
		want, got Baseline
		drift     Drift
		text      string
	}{
		{
			"a step regresses", base, withStep(0, Differs{Reason: "status"}),
			Drift{Key: dupKey, Was: `{"kind":"match"}`, Now: `{"kind":"differs","reason":"status"}`},
			dupKey + `: baseline {"kind":"match"}, replay {"kind":"differs","reason":"status"} (regression)`,
		},
		{
			"a step improves unrecorded", withStep(0, Differs{Reason: "status"}), base,
			Drift{Key: dupKey, Was: `{"kind":"differs","reason":"status"}`, Now: `{"kind":"match"}`},
			dupKey + `: baseline {"kind":"differs","reason":"status"}, replay {"kind":"match"} (improvement not recorded)`,
		},
		{
			// A recording landed for a queued step and replays green.
			"a pending step matches", withStep(1, Pending{}), withStep(1, Match{}),
			Drift{Key: rawKey, Was: `{"kind":"pending"}`, Now: `{"kind":"match"}`},
			rawKey + `: baseline {"kind":"pending"}, replay {"kind":"match"} (improvement not recorded)`,
		},
		{
			// A recording landed for a queued step and disagrees: a finding.
			"a pending step differs", withStep(1, Pending{}), withStep(1, Differs{Reason: "body/message"}),
			Drift{Key: rawKey, Was: `{"kind":"pending"}`, Now: `{"kind":"differs","reason":"body/message"}`},
			rawKey + `: baseline {"kind":"pending"}, replay {"kind":"differs","reason":"body/message"}`,
		},
		{
			"a skip reason changes", withStep(1, Skipped{Reason: SkipDocker}), withStep(1, Skipped{Reason: SkipRESTRouting}),
			Drift{Key: rawKey, Was: `{"kind":"skipped","reason":"docker"}`, Now: `{"kind":"skipped","reason":"rest-routing"}`},
			rawKey + `: baseline {"kind":"skipped","reason":"docker"}, replay {"kind":"skipped","reason":"rest-routing"}`,
		},
		{
			"a claim regresses", base, withClaim(Insensitive{By: HiddenByWaiver}),
			Drift{Key: claimKey, Was: `{"kind":"settled"}`, Now: `{"kind":"insensitive","by":"waiver"}`},
			claimKey + `: baseline {"kind":"settled"}, replay {"kind":"insensitive","by":"waiver"} (regression)`,
		},
		{
			"a claim improves unrecorded", withClaim(Contradicted{}), base,
			Drift{Key: claimKey, Was: `{"kind":"contradicted"}`, Now: `{"kind":"settled"}`},
			claimKey + `: baseline {"kind":"contradicted"}, replay {"kind":"settled"} (improvement not recorded)`,
		},
	}
	for _, c := range cases {
		got := Ratchet(c.want, c.got)
		if diff := cmp.Diff([]Drift{c.drift}, got); diff != "" {
			t.Errorf("%s: Ratchet (-want +got):\n%s", c.name, diff)
			continue
		}
		if s := got[0].String(); s != c.text {
			t.Errorf("%s: Drift.String() = %q; want %q", c.name, s, c.text)
		}
	}
}

func TestRatchetKeys(t *testing.T) {
	t.Parallel()
	base := kmsAliasBaselineValue()

	// The routed operation is part of a step's key.
	rerouted := kmsAliasBaselineValue()
	rerouted.Scenarios[0].Steps[0].Operation = "kms.UpdateAlias"
	if got := Ratchet(base, rerouted); len(got) == 0 || got[0].Key != dupKey {
		t.Errorf("Ratchet(rerouted) = %v; want a drift at %s", got, dupKey)
	}

	// A switch claim's key names the switch.
	sw := kmsAliasBaselineValue()
	sw.Scenarios[0].Claims[0].Target = SwitchTarget{Name: "kms.alias.status"}
	swSettled := kmsAliasBaselineValue()
	swSettled.Scenarios[0].Claims[0].Target = SwitchTarget{Name: "kms.alias.status"}
	swSettled.Scenarios[0].Claims[0].Result = Open{}
	if got := Ratchet(sw, swSettled); len(got) != 1 || got[0].Key != dupKey+" switch:kms.alias.status status" {
		t.Errorf("Ratchet(switch claim) = %v; want one drift at %s switch:kms.alias.status status", got, dupKey)
	}

	// A scenario on one side only drifts, absent on the other.
	empty := Baseline{}
	if got := Ratchet(base, empty); len(got) == 0 || !slices.ContainsFunc(got, func(d Drift) bool { return d.Now == "absent" && strings.HasPrefix(d.Key, "kms/KMS-alias") }) {
		t.Errorf("Ratchet(base, empty) = %v; want kms/KMS-alias drifting to absent", got)
	}
	if got := Ratchet(empty, base); len(got) == 0 || !slices.ContainsFunc(got, func(d Drift) bool { return d.Was == "absent" && strings.HasPrefix(d.Key, "kms/KMS-alias") }) {
		t.Errorf("Ratchet(empty, base) = %v; want kms/KMS-alias drifting from absent", got)
	}
}

func outcomeGen() *rapid.Generator[Outcome] {
	return rapid.Custom(func(t *rapid.T) Outcome {
		switch rapid.IntRange(0, 4).Draw(t, "outcome") {
		case 0:
			return Match{}
		case 1:
			return Differs{Reason: strings.Join(rapid.SliceOfN(rapid.SampledFrom([]string{"status", "error/code", "body/a", "bind body/KeyId", "setup 0 kms.CreateKey: 400"}), 1, 3).Draw(t, "reason"), ", ")}
		case 2:
			return Reconstructed{}
		case 3:
			return Skipped{Reason: rapid.SampledFrom([]SkipReason{SkipDocker, SkipRESTRouting, SkipNoExchange}).Draw(t, "skip")}
		default:
			return Pending{}
		}
	})
}

func claimStateGen() *rapid.Generator[ClaimState] {
	return rapid.Custom(func(t *rapid.T) ClaimState {
		switch rapid.IntRange(0, 3).Draw(t, "state") {
		case 0:
			return Settled{}
		case 1:
			return Insensitive{By: rapid.SampledFrom([]HiddenBy{HiddenByWaiver, HiddenByNormalizer}).Draw(t, "by")}
		case 2:
			return Contradicted{}
		default:
			return Open{}
		}
	})
}

func baselineGen() *rapid.Generator[Baseline] {
	return rapid.Custom(func(t *rapid.T) Baseline {
		ids := rapid.SliceOfNDistinct(rapid.StringMatching(`[a-z0-9]{2,6}/[A-Za-z0-9-]{1,8}`), 0, 4, rapid.ID[string]).Draw(t, "scenarios")
		slices.Sort(ids)
		var b Baseline
		for _, id := range ids {
			sc := BaselineScenario{Scenario: id}
			for i := range rapid.IntRange(1, 4).Draw(t, "steps") {
				name := rapid.StringMatching(`[A-Za-z0-9._-]{1,8}`).Draw(t, "step") + "-" + string(rune('a'+i))
				sc.Steps = append(sc.Steps, BaselineStep{
					Step:      name,
					Fixture:   id + "/" + name + ".json",
					Operation: rapid.SampledFrom([]string{"", "kms.CreateAlias", "sts.AssumeRole"}).Draw(t, "operation"),
					Outcome:   outcomeGen().Draw(t, "outcome"),
				})
			}
			// The index keeps each claim's key distinct.
			for j := range rapid.IntRange(0, 2).Draw(t, "claims") {
				var target Target = SwitchTarget{Name: rapid.StringMatching(`[a-z]{2,6}\.[a-z-]{1,8}`).Draw(t, "switch") + strconv.Itoa(j)}
				if rapid.Bool().Draw(t, "overlay") {
					target = OverlayTarget{
						Shape:  "com.amazonaws.kms#" + rapid.StringMatching(`[A-Z][a-z]{1,8}`).Draw(t, "shape") + strconv.Itoa(j),
						Covers: rapid.SliceOfN(rapid.StringMatching(`dev\.understudy#[a-z]{1,8}`), 0, 2).Draw(t, "covers"),
					}
				}
				sc.Claims = append(sc.Claims, BaselineClaim{
					Target: target,
					Step:   rapid.SampledFrom(sc.Steps).Draw(t, "claim step").Step,
					Field:  rapid.SampledFrom(leafPaths).Draw(t, "field"),
					Result: claimStateGen().Draw(t, "result"),
				})
			}
			b.Scenarios = append(b.Scenarios, sc)
		}
		return b
	})
}

// Any baseline reads back as written and encodes to the same bytes again,
// and the ratchet finds no drift between a baseline and itself.
func TestBaselineRoundTrip(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		b := baselineGen().Draw(t, "baseline")
		enc, err := b.Encode()
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		back, err := ParseBaseline(enc)
		if err != nil {
			t.Fatalf("ParseBaseline(Encode(b)): %v\n%s", err, enc)
		}
		if diff := cmp.Diff(b, back, equateEmpty); diff != "" {
			t.Fatalf("ParseBaseline(Encode(b)) (-b +parsed):\n%s", diff)
		}
		again, err := back.Encode()
		if err != nil || !bytes.Equal(enc, again) {
			t.Fatalf("Encode is not stable (%v):\n%s\nthen\n%s", err, enc, again)
		}
		if got := Ratchet(b, back); got != nil {
			t.Fatalf("Ratchet(b, parsed) = %v; want nil", got)
		}
	})
}

// The checked-in ratchet holds the minimum slice: the KMS-alias, sqs/R01,
// sts/R15 and events/R14 scenarios. The KMS-alias raw step's request body
// was not recorded (testdata/parity/kms/KMS-alias/02-duplicate-raw.json),
// so it is reconstructed and settles nothing.
func TestBaselineHoldsTheMinimumSlice(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("baseline.json")
	if err != nil {
		t.Fatalf("reading internal/parity/baseline.json: %v; run just parity-baseline", err)
	}
	b, err := ParseBaseline(data)
	if err != nil {
		t.Fatalf("ParseBaseline(baseline.json): %v", err)
	}
	have := map[string]BaselineScenario{}
	for _, sc := range b.Scenarios {
		have[sc.Scenario] = sc
	}
	for _, id := range []string{"events/R14", "kms/KMS-alias", "sqs/R01", "sts/R15"} {
		if _, ok := have[id]; !ok {
			t.Errorf("baseline.json has no %s scenario", id)
		}
	}
	for _, s := range have["kms/KMS-alias"].Steps {
		if s.Step != "02-duplicate-raw" {
			continue
		}
		if diff := cmp.Diff(Outcome(Reconstructed{}), s.Outcome); diff != "" || s.Operation != "" {
			t.Errorf("kms/KMS-alias#02-duplicate-raw: operation %q, outcome (-want +got):\n%s", s.Operation, diff)
		}
	}
	for _, c := range have["kms/KMS-alias"].Claims {
		if c.Step != "02-duplicate-raw" {
			continue
		}
		if diff := cmp.Diff(ClaimState(Open{}), c.Result); diff != "" {
			t.Errorf("claim on kms/KMS-alias#02-duplicate-raw (-want +got):\n%s", diff)
		}
	}
	if enc, err := b.Encode(); err != nil || !bytes.Equal(enc, data) {
		t.Errorf("baseline.json is not in canonical form (%v); run just parity-baseline", err)
	}
}
