package authz_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/evidence"
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
		"iam.resource-tag.case-duplicates",
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
	// iam-evaluation.md 12.3 labels mfa-present aws-doc: S-KEYS documents
	// that a Bool false test "applies only to temporary credentials that
	// support using MFA".
	for _, e := range authz.Switches() {
		if e.Name == "iam.context.mfa-present" && e.Label != evidence.AWSDoc {
			t.Errorf("iam.context.mfa-present label = %s, want %s (iam-evaluation.md 12.3)", e.Label, evidence.AWSDoc)
		}
	}
}
