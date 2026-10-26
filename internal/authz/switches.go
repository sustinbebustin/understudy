package authz

import (
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
)

// Switches are the evaluation and context switches (iam-evaluation.md 12.3,
// iam-enforcement-surfaces.md 12.8), each at the one value understudy
// implements; the IAM service reports them.
//
// Only the switches of per-document evaluation (policydoc.Evaluate) are
// registered yet. The decision, context and enforcement switches are not,
// since what they govern is not implemented, and no entry lists its Ops
// until the IAM service reports these switches.
func Switches() []fidelity.Entry {
	return []fidelity.Entry{
		evaluationSwitch("iam.condition.bool-variables", "substituted", "6.6", "IAM-EVAL-01"),
		evaluationSwitch("iam.condition.multivalued-plain-operator", "any-value", "6.12", "IAM-EVAL-02"),
		evaluationSwitch("iam.condition.empty-value", "null", "6.11", "IAM-EVAL-05"),
		evaluationSwitch("iam.condition.ipv6-default-prefix", "128", "6.8", "IAM-EVAL-07"),
		evaluationSwitch("iam.variables.unresolved", "value-null", "7.3", "IAM-EVAL-16"),
		evaluationSwitch("iam.not-principal.match", "account-and-principal", "5.4", "IAM-EVAL-18"),
		evaluationSwitch("iam.wildcard-principal.service", "matches", "2.9", "IAM-EVAL-33"),
	}
}

// evaluationSwitch is a switch of iam-evaluation.md 12.3 whose default is
// inferred from the section it cites and settled by a recording queue item
// of iam-evaluation.md 13.
func evaluationSwitch(name, value, section, record string) fidelity.Entry {
	return fidelity.Switch[string]{
		Name:    name,
		Value:   value,
		Label:   evidence.Inferred,
		Ref:     "docs/research/iam-evaluation.md#" + section,
		Record:  record,
		Settles: fidelity.NoScenario{Reason: "unrecorded: " + record + " waits in the recording queue (iam-evaluation.md 13)"},
	}.Entry()
}
