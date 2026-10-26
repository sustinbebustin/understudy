package authz

import (
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
)

// The research notes the switches cite, as Ref paths and in Settles reasons.
const (
	evaluationNote  = "iam-evaluation.md"
	enforcementNote = "iam-enforcement-surfaces.md"
)

// Switches are the evaluation and context switches (iam-evaluation.md 12.3,
// iam-enforcement-surfaces.md 12.8), each at the one value understudy
// implements; the IAM service reports them. The values are what
// policydoc.Evaluate, Decide, BuildContext and Denial.Message follow; the
// enforce pair (iam.enforce.order, iam.enforce.missing-resource) is what the
// kernel's step 8 follows. No entry lists its Ops until the IAM service
// reports these switches.
func Switches() []fidelity.Entry {
	inferred := evidence.Inferred
	return []fidelity.Entry{
		// policydoc.Evaluate
		researchSwitch("iam.condition.bool-variables", "substituted", inferred, evaluationNote, "6.6", "IAM-EVAL-01"),
		researchSwitch("iam.condition.multivalued-plain-operator", "any-value", inferred, evaluationNote, "6.12", "IAM-EVAL-02"),
		researchSwitch("iam.condition.empty-value", "null", inferred, evaluationNote, "6.11", "IAM-EVAL-05"),
		researchSwitch("iam.condition.ipv6-default-prefix", "128", inferred, evaluationNote, "6.8", "IAM-EVAL-07"),
		researchSwitch("iam.variables.unresolved", "value-null", inferred, evaluationNote, "7.3", "IAM-EVAL-16"),
		researchSwitch("iam.not-principal.match", "account-and-principal", inferred, evaluationNote, "5.4", "IAM-EVAL-18"),
		researchSwitch("iam.wildcard-principal.service", "matches", inferred, evaluationNote, "2.9", "IAM-EVAL-33"),
		// Decide
		researchSwitch("iam.wildcard-principal.role-session", "direct", inferred, evaluationNote, "2.3", "IAM-EVAL-19"),
		researchSwitch("kms.key-policy.role-boundary", "limited", inferred, evaluationNote, "4.3", "IAM-EVAL-27"),
		// Get and Put are inferred; Delete is aws-doc (iam-enforcement-surfaces.md 6.3).
		researchSwitch("s3.root.bucket-policy-lockout", "get-put-delete", inferred, enforcementNote, "6.3", "IAM-ENF-31"),
		// BuildContext
		researchSwitch("iam.context.mfa-present", "false-in-sessions", evidence.AWSDoc, evaluationNote, "8.1", "IAM-EVAL-26"),
		researchSwitch("iam.context.service-principal-identity-keys", "absent", inferred, evaluationNote, "2.9", "IAM-EVAL-33"),
		researchSwitch("iam.resource-tag.case-duplicates", "absent", inferred, evaluationNote, "6.13", "IAM-EVAL-31"),
		fidelity.Switch[string]{
			Name:  "iam.context.secure-transport",
			Value: "assume-tls",
			Label: inferred,
			Ref:   "docs/research/" + evaluationNote + "#8.2",
			Settles: fidelity.NoScenario{Reason: "none needed: AWS's own value is always true for its HTTPS endpoints, " +
				"so the choice is understudy's (iam-enforcement-surfaces.md 12.8)"},
		}.Entry(),
		// Denial.Message: the phrases and the one reason are aws-doc, where
		// "on resource:" goes is inferred (iam-enforcement-surfaces.md 4.1).
		researchSwitch("iam.denial.grammar", grammarValue, inferred, enforcementNote, "4.1", "IAM-ENF-15"),
		// The kernel's step 8.
		researchSwitch("iam.enforce.order", "validate-first", inferred, enforcementNote, "5.3", "IAM-ENF-13"),
		researchSwitch("iam.enforce.missing-resource", "deny-first", inferred, enforcementNote, "5.3", "IAM-ENF-14"),
	}
}

// grammarValue is switch iam.denial.grammar's value: the reason clauses of
// iam-enforcement-surfaces.md 4.1, one reason per denial, and "on resource:"
// written on explicit denials only.
const grammarValue = "iam-4.1, one reason, on resource on explicit denials"

// researchSwitch is a switch whose default is chosen in section of note and
// settled by the recording queue item record of that note's section 13.
func researchSwitch(name, value string, label evidence.Label, note, section, record string) fidelity.Entry {
	return fidelity.Switch[string]{
		Name:    name,
		Value:   value,
		Label:   label,
		Ref:     "docs/research/" + note + "#" + section,
		Record:  record,
		Settles: fidelity.NoScenario{Reason: "unrecorded: " + record + " waits in the recording queue (" + note + " 13)"},
	}.Entry()
}
