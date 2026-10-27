package kms_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/journal"
)

// checkNotRun lists the resources of the check-not-run advisories
// journaled so far.
func checkNotRun(h *harness) []string {
	var out []string
	events := h.Journal.Query(journal.Filter{Kind: journal.KindAdvisory}).Events
	for i := range events {
		if a, ok := events[i].Detail.(journal.Advisory); ok && a.Kind == journal.AdvisoryCheckNotRun {
			out = append(out, a.Resource.String())
		}
	}
	return out
}

// AWS refuses a key policy that would not let its caller put a policy
// later, unless BypassPolicyLockoutSafetyCheck is true (iam-evaluation.md
// 4.5). understudy does not run that check, so each key policy AWS would
// check, one sent to CreateKey or PutKeyPolicy without the bypass, journals
// a check-not-run advisory naming the key; the default policy, and a bypassed
// one, journal none.
func TestUncheckedKeyPoliciesJournalCheckNotRun(t *testing.T) {
	run(t, func(h *harness) {
		t := h.T
		plain := h.key()
		must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{Policy: new(rootPolicy), BypassPolicyLockoutSafetyCheck: true})
		if got := checkNotRun(h); len(got) != 0 {
			t.Fatalf("advisories after a default and a bypassed key = %v, want none", got)
		}
		sent := must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{Policy: new(rootPolicy)}).KeyMetadata.KeyId
		must(h, kmsapi.OpPutKeyPolicy, &kmsapi.PutKeyPolicyInput{KeyId: plain, Policy: rootPolicy})
		must(h, kmsapi.OpPutKeyPolicy, &kmsapi.PutKeyPolicyInput{KeyId: plain, Policy: rootPolicy, BypassPolicyLockoutSafetyCheck: true})
		want := []string{keyARN(sent), keyARN(plain)}
		got := checkNotRun(h)
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("check-not-run advisories name %v, want %v", got, want)
		}
	})
}
