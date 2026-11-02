package authz

import (
	"slices"
	"strings"

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
// implements. The values are what policydoc.Evaluate, Decide, BuildContext
// and Denial.Message follow; the enforce pair (iam.enforce.order,
// iam.enforce.missing-resource) is what the kernel's step 8 follows. No
// entry lists its Ops: the service that reports it names them (SwitchesOf).
// iam.enforce.missing-resource is deny-first only for the operations asked
// at step 8; those that resolve a resource by name first report
// ResolveFirstSwitch instead.
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
				"so no recording can tell the values apart and the choice is understudy's (iam-evaluation.md 12.3, iam-enforcement-surfaces.md 12.8)"},
		}.Entry(),
		// Denial.Message: the phrases and the one reason are aws-doc, where
		// "on resource:" goes is inferred (iam-enforcement-surfaces.md 4.1).
		researchSwitch("iam.denial.grammar", grammarValue, inferred, enforcementNote, "4.1", "IAM-ENF-15"),
		// The kernel's step 8.
		researchSwitch("iam.enforce.order", "validate-first", inferred, enforcementNote, "5.3", "IAM-ENF-13"),
		researchSwitch("iam.enforce.missing-resource", "deny-first", inferred, enforcementNote, "5.3", "IAM-ENF-14"),
	}
}

// SwitchesOf are the Switches a service reports, those whose names begin
// with service and a dot, each governing ops: IAM reports the evaluation
// switches, KMS and S3 their own (iam-evaluation.md 12.3,
// iam-enforcement-surfaces.md 12.8).
func SwitchesOf(service string, ops []string) []fidelity.Entry {
	var out []fidelity.Entry
	for _, e := range Switches() {
		if strings.HasPrefix(e.Name, service+".") {
			e.Ops = slices.Clone(ops)
			out = append(out, e)
		}
	}
	return out
}

// DenialSwitch is a service's switch of iam-enforcement-surfaces.md 12.8
// for how it answers a denial: value chosen in section of the note, backed
// by label, settled by the recording queue item record, governing ops.
func DenialSwitch(name, value string, label evidence.Label, section, record string, ops []string) fidelity.Entry {
	return EnforcementSwitch(name, value, label, section, record, ops)
}

// EnforcementSwitch is any service switch of iam-enforcement-surfaces.md
// 12.8, such as how a delivery its target refuses ends or which keys a
// delivery sends: value chosen in section of the note, backed by label,
// settled by the recording queue item record, governing ops.
func EnforcementSwitch(name, value string, label evidence.Label, section, record string, ops []string) fidelity.Entry {
	e := researchSwitch(name, value, label, enforcementNote, section, record)
	e.Ops = slices.Clone(ops)
	return e
}

// ResolveFirstSwitch is service's switch <service>.enforce.missing-resource.by-name,
// at resolve-first, governing ops: the operations that find their resource
// by name (kernel.Bindings.ResolvedFirstOps, and IAM GetRole), whose ARN
// only the lookup learns, so a missing resource answers its not-found error
// before authorization. It deviates from deny-first, the default of
// iam.enforce.missing-resource, whose AWS behavior is unrecorded
// (iam-enforcement-surfaces.md 5.3, 12.8; amended 2026-10-27).
func ResolveFirstSwitch(service string, ops []string) fidelity.Entry {
	return EnforcementSwitch(service+".enforce.missing-resource.by-name", "resolve-first", evidence.Inferred, "5.3", "IAM-ENF-14", ops)
}

// DeliveryResolveFirstSwitch is service's switch
// <service>.enforce.missing-resource.delivery, at resolve-first, governing
// ops: the operations that receive deliveries
// (kernel.Bindings.DeliveryResolvedFirstOps), where a delivery to a target
// that does not exist answers the target's not-found error before the
// delivering service is authorized, chosen in section of the note and
// settled by record. AWS reports a delivery to a deleted queue as
// NonExistentQueue (sns.md 7.1), and EventBridge one to a target that no
// longer exists apart from a missing permission (8.4), which a target
// with no policy left could not show if the delivering service were
// authorized first; the order itself is inferred
// (iam-enforcement-surfaces.md 8.2, 8.4, 12.8).
func DeliveryResolveFirstSwitch(service, section, record string, ops []string) fidelity.Entry {
	return EnforcementSwitch(service+".enforce.missing-resource.delivery", "resolve-first", evidence.Inferred, section, record, ops)
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
