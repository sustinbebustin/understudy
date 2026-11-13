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
// iam.enforce.missing-resource is deny-first, as IAM-ENF-14 recorded for
// most services; the services AWS answers not-found first report their own
// resolve-first switch (RecordedResolveFirstSwitch), and IAM and Secrets
// Manager their by-name switch (RecordedDenyFirstSwitch).
func Switches() []fidelity.Entry {
	inferred := evidence.Inferred
	return []fidelity.Entry{
		// policydoc.Evaluate
		recordedSwitch("iam.condition.bool-variables", "literal", evaluationNote, "6.6", "IAM-EVAL-01", SimulatorUnreplayed),
		recordedSwitch("iam.condition.multivalued-plain-operator", "no-match", evaluationNote, "6.12", "IAM-EVAL-02", SimulatorUnreplayed),
		recordedSwitch("iam.condition.empty-value", "value", evaluationNote, "6.11", "IAM-EVAL-05", SimulatorUnreplayed),
		recordedSwitch("iam.condition.ipv6-default-prefix", "128", evaluationNote, "6.8", "IAM-EVAL-07", SimulatorUnreplayed),
		recordedSwitch("iam.variables.unresolved", "value-null", evaluationNote, "7.3", "IAM-EVAL-16", SimulatorUnreplayed),
		recordedSwitch("iam.not-principal.match", "every-level", evaluationNote, "5.4", "IAM-EVAL-18", SessionUnreplayed),
		recordedSwitch("iam.wildcard-principal.service", "matches", evaluationNote, "2.9", "IAM-EVAL-33", DeliveryUnreplayed),
		// Storing a trust policy. Checking would refuse every policy naming
		// a role never created in understudy, which the default setup
		// relies on (iam-evaluation.md 3.4).
		fidelity.Switch[string]{
			Name: "iam.policy.principal-existence", Value: "unchecked", Label: inferred,
			Ref: "docs/research/" + evaluationNote + "#3.4", Record: "IAM-EVAL-24",
			Settles: fidelity.NoScenario{Reason: "AWS contradicts it: IAM-EVAL-24 recorded " + recordedOn + " that IAM refuses a trust policy naming a role " +
				"that does not exist (MalformedPolicyDocument) and SQS a queue policy naming a missing user (InvalidAttributeValue); understudy stores " +
				"such a policy, so that setups naming roles never created in understudy keep working (" + evaluationNote + " 3.4)"},
		}.Entry(),
		// Decide
		recordedSwitch("iam.wildcard-principal.role-session", "direct", evaluationNote, "2.3", "IAM-EVAL-19", SessionUnreplayed),
		recordedSwitch("kms.key-policy.role-boundary", "limited", evaluationNote, "4.3", "IAM-EVAL-27", SessionUnreplayed),
		// Get and Put are inferred; Delete is aws-doc (iam-enforcement-surfaces.md 6.3).
		fidelity.Switch[string]{
			Name: "s3.root.bucket-policy-lockout", Value: "get-put-delete", Label: inferred,
			Ref: "docs/research/" + enforcementNote + "#6.3", Record: "IAM-ENF-31",
			Settles: fidelity.NoScenario{Reason: "unrecorded: IAM-ENF-31 needs the account's root user, which the " + recordedOn +
				" session, a free-plan project role, could not provide (" + enforcementNote + " 13.33)"},
		}.Entry(),
		// BuildContext
		recordedSwitch("iam.context.mfa-present", "false-in-sessions", evaluationNote, "8.1", "IAM-EVAL-26", SessionUnreplayed),
		// IAM-EVAL-33 recorded all three keys present for sns.amazonaws.com
		// but not their values, so understudy cannot supply them honestly
		// (iam-evaluation.md 2.9).
		fidelity.Switch[string]{
			Name: "iam.context.service-principal-identity-keys", Value: "absent", Label: inferred,
			Ref: "docs/research/" + evaluationNote + "#2.9", Record: "IAM-EVAL-33",
			Settles: fidelity.NoScenario{Reason: "IAM-EVAL-33 recorded " + recordedOn + " that a delivery by sns.amazonaws.com carries aws:PrincipalType, " +
				"aws:PrincipalAccount and aws:userid, but not their values; the value stays absent until a recording shows them (" + evaluationNote + " 2.9)"},
		}.Entry(),
		// IAM-EVAL-31 recorded only that a lower-case aws:ResourceTag key
		// found the lower-case tag; understudy supplies no resource tag
		// (8.2), so the recorded resource-tag switch is not registered and
		// principal tags follow it by inference (iam-evaluation.md 6.13).
		fidelity.Switch[string]{
			Name: "iam.principal-tag.case-duplicates", Value: "exact-case", Label: inferred,
			Ref: "docs/research/" + evaluationNote + "#6.13", Record: "IAM-EVAL-31",
			Settles: fidelity.NoScenario{Reason: "IAM-EVAL-31 recorded " + recordedOn + " that aws:ResourceTag/env found the queue tag env and not Env; " +
				"principal tags, and spellings matching neither tag, follow it by inference until a recording of aws:PrincipalTag shows them (" + evaluationNote + " 6.13)"},
		}.Entry(),
		fidelity.Switch[string]{
			Name:  "iam.context.secure-transport",
			Value: "assume-tls",
			Label: inferred,
			Ref:   "docs/research/" + evaluationNote + "#8.2",
			Settles: fidelity.NoScenario{Reason: "none needed: AWS's own value is always true for its HTTPS endpoints, " +
				"so no recording can tell the values apart and the choice is understudy's (iam-evaluation.md 12.3, iam-enforcement-surfaces.md 12.8)"},
		}.Entry(),
		// Denial.Message: the phrases and the one reason are aws-doc, and
		// every recorded denial writes "on resource:" (iam-enforcement-surfaces.md 4.1).
		recordedSwitch("iam.denial.grammar", grammarValue, enforcementNote, "4.1", "IAM-ENF-15", SessionUnreplayed),
		// Denial.ResourceText (kernel.WithDenialResource): the text is
		// recorded, what AWS evaluates against it is not (12.3).
		fidelity.Switch[string]{
			Name: "iam.denial.list-resource", Value: listResourceValue, Label: inferred,
			Ref: "docs/research/" + enforcementNote + "#12.3", Record: "IAM-ENF-41",
			Settles: fidelity.NoScenario{Reason: "IAM-ENF-35, IAM-ENF-37 and IAM-ENF-39 to IAM-ENF-41 recorded " + recordedOn + " the wildcard resource a list operation's denial names " +
				"(IAM-ENF-36 and IAM-ENF-38: none for Kinesis ListStreams and SES v1 ListIdentities); inferred: whether AWS matches a policy's Resource against that wildcard " +
				"rather than \"*\" is unrecorded, so evaluation and the access record keep \"*\" (" + enforcementNote + " 12.3)"},
		}.Entry(),
		// The kernel's step 8.
		// IAM-ENF-13 recorded the order; the label stays inferred while
		// how a recorded deviation is labelled is open.
		fidelity.Switch[string]{
			Name: "iam.enforce.order", Value: "validate-first", Label: inferred,
			Ref: "docs/research/" + enforcementNote + "#5.3", Record: "IAM-ENF-13",
			Settles: fidelity.NoScenario{Reason: "IAM-ENF-13 recorded " + recordedOn + " (testdata/parity): AWS validates before authorizing in every service it covers " +
				"except SQS, CloudWatch and Logs, which authorize first; understudy validates first in all of them (" + enforcementNote + " 5.3, 12.8)"},
		}.Entry(),
		recordedSwitch("iam.enforce.missing-resource", "deny-first", enforcementNote, "5.3", "IAM-ENF-14", missingResourceOrder+"; "+SessionUnreplayed),
	}
}

// missingResourceOrder is the order IAM-ENF-14 recorded per service for a
// caller with no permissions naming a resource that does not exist
// (iam-enforcement-surfaces.md 5.3), which switch
// iam.enforce.missing-resource states with its exceptions.
const missingResourceOrder = "deny-first in DynamoDB, EventBridge, IAM GetRole, Lambda, Logs, Secrets Manager, SNS and STS; " +
	"resolve-first, each under its own switch, for SQS queues (sqs.enforce.missing-resource), S3 buckets and keys (s3.enforce.missing-resource) " +
	"and KMS key ids (kms.enforce.missing-resource.by-name)"

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

// RecordedDenyFirstSwitch is the switch name at deny-first, governing ops,
// where IAM-ENF-14 recorded the denial of a caller with no permissions
// before the resource's not-found error, naming the resource as the
// request gave it (IAM GetRole, Secrets Manager): recorded says which
// calls the recording made, and inferred which of ops, and which choices,
// follow them by inference (iam-enforcement-surfaces.md 5.3, 12.8).
func RecordedDenyFirstSwitch(name, recorded, inferred string, ops []string) fidelity.Entry {
	return RecordedSwitch(name, "deny-first", "5.3", "IAM-ENF-14",
		"recorded: "+recorded+"; inferred: "+inferred+"; "+SessionUnreplayed, ops)
}

// AccountIDPrincipalSwitch is service's switch
// <service>.resource-policy.account-id-principal, at root-arn, governing
// ops: the operations that store a resource policy, which store a bare
// account id named as an AWS principal as the account's root ARN
// (policydoc.RootAccountPrincipals), as record recorded for the service
// (iam-evaluation.md 5.4, 12.3).
func AccountIDPrincipalSwitch(service, record string, ops []string) fidelity.Entry {
	e := recordedSwitch(service+".resource-policy.account-id-principal", "root-arn", evaluationNote, "5.4", record,
		"the capture is the read-back only; the put that stored the policy ran unrecorded, so the replay has no policy to read")
	e.Ops = slices.Clone(ops)
	return e
}

// RecordedResolveFirstSwitch is the switch name at resolve-first, governing
// ops, where IAM-ENF-14 recorded the not-found answer before authorization:
// recorded says which calls the recording made, and inferred which of ops
// follow them by inference (iam-enforcement-surfaces.md 5.3, 12.8).
func RecordedResolveFirstSwitch(name, recorded, inferred string, ops []string) fidelity.Entry {
	return RecordedSwitch(name, "resolve-first", "5.3", "IAM-ENF-14",
		"recorded: "+recorded+"; inferred: "+inferred+"; "+SessionUnreplayed, ops)
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
// written on every denial of a pair on a resource, as every recorded
// denial writes it (IAM-ENF-15).
const grammarValue = "iam-4.1, one reason, on resource whenever the pair has a resource"

// listResourceValue is switch iam.denial.list-resource's value: a denied
// list operation names its service's wildcard resource, such as
// arn:aws:states:<region>:<account>:stateMachine:*, while its pair is
// evaluated on "*" (iam-enforcement-surfaces.md 12.3).
const listResourceValue = "names the service wildcard, evaluated on *"

// recordedOn is the date of the session that recorded the IAM-ENF and
// IAM-EVAL items (testdata/parity/<service>/IAM-ENF, iam/IAM-EVAL-*).
const recordedOn = "2026-11-03"

// Why no parity scenario replays a recorded switch (testdata/README.md,
// Replay scenarios).
const (
	// SessionUnreplayed: the replay signs only as the account's root.
	SessionUnreplayed = "the recorded caller is a role session, and the replay signs only as the account's root"
	// DeliveryUnreplayed: the outcome is a delivery between services.
	DeliveryUnreplayed = "the recorded outcome is a delivery between services, which the replay cannot send"
	// SimulatorUnreplayed: understudy does not serve the simulator.
	SimulatorUnreplayed = "the recording is IAM's policy simulator, SimulateCustomPolicy, which understudy leaves unbound"
)

// RecordedSwitch is a service switch of iam-enforcement-surfaces.md 12.8
// whose value record settled against AWS: value chosen in section of the
// note, aws-recorded, governing ops; why says why no parity scenario
// replays it.
func RecordedSwitch(name, value, section, record, why string, ops []string) fidelity.Entry {
	e := recordedSwitch(name, value, enforcementNote, section, record, why)
	e.Ops = slices.Clone(ops)
	return e
}

// recordedSwitch is a switch whose value record, in note's section 13,
// recorded against AWS; why says why no parity scenario replays it.
func recordedSwitch(name, value, note, section, record, why string) fidelity.Entry {
	return fidelity.Switch[string]{
		Name:    name,
		Value:   value,
		Label:   evidence.AWSRecorded,
		Ref:     "docs/research/" + note + "#" + section,
		Record:  record,
		Settles: fidelity.NoScenario{Reason: record + " recorded " + recordedOn + " (testdata/parity); " + why},
	}.Entry()
}

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
