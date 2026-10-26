package parity

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// A step routed elsewhere drifts with both operations named, and is
// neither a regression nor an improvement while its outcome holds.
func TestRatchetNamesARoutingChange(t *testing.T) {
	t.Parallel()
	rerouted := kmsAliasBaselineValue()
	rerouted.Scenarios[0].Steps[0].Operation = "kms.UpdateAlias"
	got := Ratchet(kmsAliasBaselineValue(), rerouted)
	want := []Drift{{Key: dupKey, Was: `{"kind":"match"} operation kms.CreateAlias`, Now: `{"kind":"match"} operation kms.UpdateAlias`}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("Ratchet (-want +got):\n%s", diff)
	}
	if s := got[0].String(); strings.Contains(s, "(regression)") || strings.Contains(s, "(improvement") {
		t.Errorf("Drift.String() = %q; want no direction for a routing change", s)
	}
	unrouted := kmsAliasBaselineValue()
	unrouted.Scenarios[0].Steps[0].Operation = ""
	if got := Ratchet(kmsAliasBaselineValue(), unrouted); len(got) != 1 || got[0].Now != `{"kind":"match"} operation none` {
		t.Errorf("Ratchet(unrouted) = %v; want the replay side to read operation none", got)
	}
}

// An overlay claim whose covers change names other evidence items, and a
// step whose fixture moves replays another recording: both drift, though
// every outcome and result holds, so baseline.json cannot keep stale ones.
func TestRatchetSeesCoversAndFixtureChanges(t *testing.T) {
	t.Parallel()
	covered := func(covers ...string) Baseline {
		b := kmsAliasBaselineValue()
		b.Scenarios[0].Claims[0].Target = OverlayTarget{Shape: "com.amazonaws.kms#AlreadyExistsException", Covers: covers}
		return b
	}
	got := Ratchet(covered("dev.understudy#statusOverride"), covered("dev.understudy#errorNamespace"))
	want := []Drift{
		{Key: dupKey + " overlay:com.amazonaws.kms#AlreadyExistsException[dev.understudy#statusOverride] status", Was: `{"kind":"settled"}`, Now: "absent"},
		{Key: dupKey + " overlay:com.amazonaws.kms#AlreadyExistsException[dev.understudy#errorNamespace] status", Was: "absent", Now: `{"kind":"settled"}`},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Ratchet(covers changed) (-want +got):\n%s", diff)
	}
	if got := Ratchet(covered("b", "a"), covered("a", "b")); got != nil {
		t.Errorf("Ratchet(covers reordered) = %v; want nil, since covers are a set", got)
	}

	moved := kmsAliasBaselineValue()
	moved.Scenarios[0].Steps[0].Fixture = "sts/KMS-alias/01-duplicate.json"
	got = Ratchet(kmsAliasBaselineValue(), moved)
	want = []Drift{{Key: dupKey, Was: `{"kind":"match"} fixture kms/KMS-alias/01-duplicate.json`, Now: `{"kind":"match"} fixture sts/KMS-alias/01-duplicate.json`}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Ratchet(fixture moved) (-want +got):\n%s", diff)
	}
}

// Two claims with one target, step and field keep distinct keys, so a drift
// in the second is not hidden by the first.
func TestRatchetKeepsRepeatedClaimKeysApart(t *testing.T) {
	t.Parallel()
	twice := func(second ClaimState) Baseline {
		b := kmsAliasBaselineValue()
		c := b.Scenarios[0].Claims[0]
		c.Result = second
		b.Scenarios[0].Claims = append(b.Scenarios[0].Claims, c)
		return b
	}
	got := Ratchet(twice(Settled{}), twice(Open{}))
	if len(got) != 1 || got[0].Key != claimKey+" (2)" {
		t.Errorf("Ratchet = %v; want one drift at %s (2)", got, claimKey)
	}
}

func TestParseBaselineRefusesInconsistentEntries(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"claim on an unlisted step": `{"version": 1, "scenarios": [{"scenario": "kms/KMS-alias", "steps": [{"step": "s", "fixture": "kms/KMS-alias/s.json", "outcome": {"kind": "match"}}],
  "claims": [{"target": {"kind": "switch", "name": "a.b"}, "step": "t", "field": "status", "result": {"kind": "settled"}}]}]}`,
		"claim on a group field": `{"version": 1, "scenarios": [{"scenario": "kms/KMS-alias", "steps": [{"step": "s", "fixture": "kms/KMS-alias/s.json", "outcome": {"kind": "match"}}],
  "claims": [{"target": {"kind": "switch", "name": "a.b"}, "step": "s", "field": "header", "result": {"kind": "settled"}}]}]}`,
		"scenario listed twice": `{"version": 1, "scenarios": [
  {"scenario": "kms/KMS-alias", "steps": [{"step": "s", "fixture": "kms/KMS-alias/s.json", "outcome": {"kind": "match"}}]},
  {"scenario": "kms/KMS-alias", "steps": [{"step": "s", "fixture": "kms/KMS-alias/s.json", "outcome": {"kind": "match"}}]}]}`,
		"step listed twice": `{"version": 1, "scenarios": [{"scenario": "kms/KMS-alias", "steps": [
  {"step": "s", "fixture": "kms/KMS-alias/s.json", "outcome": {"kind": "match"}}, {"step": "s", "fixture": "kms/KMS-alias/s.json", "outcome": {"kind": "match"}}]}]}`,
		"scenario without steps": `{"version": 1, "scenarios": [{"scenario": "kms/KMS-alias", "steps": []}]}`,
		"overlay with a name":    `{"version": 1, "scenarios": [{"scenario": "kms/KMS-alias", "steps": [{"step": "s", "fixture": "kms/KMS-alias/s.json", "outcome": {"kind": "match"}}], "claims": [{"target": {"kind": "overlay", "shape": "com.amazonaws.kms#X", "name": "x"}, "step": "s", "field": "status", "result": {"kind": "open"}}]}]}`,
		"settled with a by":      `{"version": 1, "scenarios": [{"scenario": "kms/KMS-alias", "steps": [{"step": "s", "fixture": "kms/KMS-alias/s.json", "outcome": {"kind": "match"}}], "claims": [{"target": {"kind": "switch", "name": "a.b"}, "step": "s", "field": "status", "result": {"kind": "settled", "by": "waiver"}}]}]}`,
	}
	for name, doc := range cases {
		_, err := ParseBaseline([]byte(doc))
		if be, ok := errors.AsType[*BaselineError](err); !ok || be.Reason == "" {
			t.Errorf("%s: ParseBaseline error = %v; want a *BaselineError with a reason", name, err)
		}
	}
}

// Encode refuses what the parser would, rather than write a file the
// ratchet cannot read back.
func TestBaselineEncodeRefusesAStepWithoutAnOutcome(t *testing.T) {
	t.Parallel()
	b := kmsAliasBaselineValue()
	b.Scenarios[0].Steps[0].Outcome = nil
	if _, err := b.Encode(); err == nil {
		t.Error("Encode(a step without an outcome) = nil error; want it refused")
	}
}
