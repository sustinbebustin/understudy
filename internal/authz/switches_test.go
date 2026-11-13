package authz_test

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
)

// The evaluation and context switches of iam-evaluation.md 12.3 and
// iam-enforcement-surfaces.md 12.8 that the evaluator implements are
// registered, each once, with a label, a reference and a value (CLAUDE.md
// invariant 3: a deviation from AWS is a fidelity.Switch). The values are
// what the tests of each behavior read; they are not pinned here.
func TestSwitchesRegisterEveryEvaluationChoice(t *testing.T) {
	names := []string{
		"iam.context.secure-transport",
		"iam.context.mfa-present",
		"iam.context.service-principal-identity-keys",
		"iam.condition.bool-variables",
		"iam.condition.multivalued-plain-operator",
		"iam.condition.empty-value",
		"iam.condition.ipv6-default-prefix",
		"iam.variables.unresolved",
		"iam.not-principal.match",
		"iam.wildcard-principal.role-session",
		"iam.wildcard-principal.service",
		"iam.principal-tag.case-duplicates",
		"iam.policy.principal-existence",
		"kms.key-policy.role-boundary",
		"s3.root.bucket-policy-lockout",
		"iam.denial.grammar",
		"iam.enforce.order",
		"iam.enforce.missing-resource",
	}
	seen := map[string]int{}
	for _, e := range authz.Switches() {
		seen[e.Name]++
		if e.Value == "" || e.Ref == "" {
			t.Errorf("switch %s has value %q and ref %q, want both", e.Name, e.Value, e.Ref)
		}
		if _, err := evidence.ParseLabel(string(e.Label)); err != nil {
			t.Errorf("switch %s label %q: %v", e.Name, e.Label, err)
		}
	}
	for _, name := range names {
		if seen[name] != 1 {
			t.Errorf("authz.Switches() registers %s %d times, want once", name, seen[name])
		}
	}
	// mfa-present was aws-doc (S-KEYS: a Bool false test "applies only to
	// temporary credentials that support using MFA") and is now recorded:
	// both Null false and Bool false matched in a session without MFA
	// (iam-evaluation.md 12.3, IAM-EVAL-26).
	for _, e := range authz.Switches() {
		if e.Name == "iam.context.mfa-present" && e.Label != evidence.AWSRecorded {
			t.Errorf("iam.context.mfa-present label = %s, want %s (iam-evaluation.md 12.3)", e.Label, evidence.AWSRecorded)
		}
		// IAM-EVAL-31 recorded a lower-case aws:ResourceTag lookup only,
		// and understudy supplies no resource tag; principal tags follow
		// it by inference (iam-evaluation.md 6.13).
		if e.Name == "iam.principal-tag.case-duplicates" && e.Label != evidence.Inferred {
			t.Errorf("iam.principal-tag.case-duplicates label = %s, want %s (iam-evaluation.md 6.13)", e.Label, evidence.Inferred)
		}
		// AWS refuses a principal that does not exist (IAM-EVAL-24);
		// understudy deliberately does not check (iam-evaluation.md 3.4).
		if e.Name == "iam.policy.principal-existence" && (e.Value != "unchecked" || e.Label != evidence.Inferred) {
			t.Errorf("iam.policy.principal-existence = %s (%s), want unchecked (%s)", e.Value, e.Label, evidence.Inferred)
		}
		// A recorded deviation (ADR 0003, amendment 2026-11-13): AWS
		// authorizes first in SQS, CloudWatch and Logs, understudy
		// validates first everywhere (iam-enforcement-surfaces.md 5.3,
		// IAM-ENF-13).
		if e.Name == "iam.enforce.order" {
			reason := ""
			if s, ok := e.Settles.(fidelity.NoScenario); ok {
				reason = s.Reason
			}
			if e.Value != "validate-first" || e.Label != evidence.Inferred || !strings.HasPrefix(reason, "AWS contradicts it: IAM-ENF-13 recorded ") {
				t.Errorf("iam.enforce.order = %s (%s), settles %q; want validate-first (%s), a reason opening \"AWS contradicts it: IAM-ENF-13 recorded \"",
					e.Value, e.Label, reason, evidence.Inferred)
			}
		}
		if e.Name == "iam.resource-tag.case-duplicates" {
			t.Errorf("iam.resource-tag.case-duplicates is registered, but aws:ResourceTag/ is never supplied (iam-evaluation.md 8.2)")
		}
	}
}
