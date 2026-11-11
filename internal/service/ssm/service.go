package ssm

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Service is AWS Systems Manager Parameter Store: one state cell per
// account and region. SecureString values are encrypted through KMS, which
// must be bound in the same kernel.
type Service struct {
	k     *kernel.Kernel
	cells *store.Partitioned[aws.Scope, State]
}

// New binds the thirteen Parameter Store operations (ssm-parameters.md
// 12.1). Every other SSM operation, the service settings and the resource
// policies included, stays unbound and answers 501.
func New(d kernel.ServiceDeps, _ Options) *Service {
	s := &Service{
		k:     d.Kernel,
		cells: store.NewPartitioned(d.Kernel.Gate(), stateCodec(), newState),
	}
	b := ssmapi.NewBinder(d.Binder)

	b.PutParameter(s.putParameter, registry.Partial{Note: notePutParameter},
		byName(func(in *ssmapi.PutParameterInput) string { return in.Name }), putActions())
	b.GetParameter(s.getParameter, registry.Partial{Note: noteGetParameter},
		byName(func(in *ssmapi.GetParameterInput) string { return in.Name }))
	b.GetParameters(s.getParameters, registry.Partial{Note: noteGetParameters},
		byNames(func(in *ssmapi.GetParametersInput) []string { return in.Names }))
	b.GetParametersByPath(s.getParametersByPath, registry.Partial{Note: noteGetParametersByPath}, byPath())
	b.DescribeParameters(s.describeParameters, registry.Partial{Note: noteDescribeParameters})
	b.GetParameterHistory(s.getParameterHistory, registry.Partial{Note: noteGetParameterHistory},
		byName(func(in *ssmapi.GetParameterHistoryInput) string { return in.Name }))
	b.DeleteParameter(s.deleteParameter, registry.Partial{Note: noteDeleteParameters},
		byName(func(in *ssmapi.DeleteParameterInput) string { return in.Name }))
	b.DeleteParameters(s.deleteParameters, registry.Partial{Note: noteDeleteParameters},
		byNames(func(in *ssmapi.DeleteParametersInput) []string { return in.Names }))
	b.LabelParameterVersion(s.labelParameterVersion, registry.Partial{Note: noteLabelParameterVersion},
		byName(func(in *ssmapi.LabelParameterVersionInput) string { return in.Name }))
	b.UnlabelParameterVersion(s.unlabelParameterVersion, registry.Partial{Note: reserved + "; " + reservedText},
		byName(func(in *ssmapi.UnlabelParameterVersionInput) string { return in.Name }))
	b.AddTagsToResource(s.addTagsToResource, registry.Partial{Note: reserved + "; " + parameterOnly + "; " + reservedText},
		byTagTarget(func(in *ssmapi.AddTagsToResourceInput) (ssmapi.ResourceTypeForTagging, string) {
			return in.ResourceType, in.ResourceId
		}))
	b.RemoveTagsFromResource(s.removeTagsFromResource, registry.Partial{Note: reserved + "; " + parameterOnly + "; " + reservedText},
		byTagTarget(func(in *ssmapi.RemoveTagsFromResourceInput) (ssmapi.ResourceTypeForTagging, string) {
			return in.ResourceType, in.ResourceId
		}))
	b.ListTagsForResource(s.listTagsForResource, registry.Partial{Note: reserved + "; " + parameterOnly +
		"; TagList lists tags in the order they were first set, where AWS's order is its own; " + reservedText},
		byTagTarget(func(in *ssmapi.ListTagsForResourceInput) (ssmapi.ResourceTypeForTagging, string) {
			return in.ResourceType, in.ResourceId
		}))
	return s
}

// The coverage notes (ssm-parameters.md 12.1).
const (
	reserved      = "a name under /aws/service/ or /aws/reference/secretsmanager/ makes the call answer 501"
	parameterOnly = "ResourceType Parameter only: the other resource types name nothing understudy stores, so they answer InvalidResourceId"

	// reservedText is the reserved-name answer outside PutParameter
	// (ssm-parameters.md 3.1; SSM-04 records PutParameter only).
	reservedText = "unrecorded, understudy's choice: another name whose first level is aws answers PutParameter's recorded " +
		"AccessDeniedException text (No access to reserved parameter name: <name>.), where AWS's text for this operation may differ"

	// The default key is called as ssm.amazonaws.com (ssm-parameters.md
	// 5.6, inferred), so step 8 never sees the caller's kms: actions on it.
	defaultKeyEncrypt = "under iam.enforce, a SecureString on the default key alias/aws/ssm is encrypted as SSM itself, " +
		"so an explicit identity-policy deny of kms:Encrypt on that key is not applied"
	defaultKeyDecrypt = "under iam.enforce, a SecureString on the default key alias/aws/ssm is decrypted as SSM itself, " +
		"so an explicit identity-policy deny of kms:Decrypt on that key is not applied"

	// kmsMessage is how a KMS failure's message differs from AWS's
	// (ssm-parameters.md 5.5; SSM-22).
	kmsMessage = "a KMS failure carries KMS's message without the KMS client's \"(Service: AWSKMS; ...; Request ID: ...; Proxy: null)\" suffix AWS appends"

	notePutParameter = reserved + "; parameter policies answer 501; DataType aws:ec2:image answers 501, since understudy has no EC2 to validate an AMI; " +
		"an Advanced-tier SecureString, including an Intelligent-Tiering one that resolves to Advanced, answers 501; " +
		"AllowedPattern is compiled as Go RE2, where AWS's dialect is Java's: a pattern RE2 cannot compile, such as a lookahead or a possessive quantifier, " +
		"answers 501, except an unbalanced bracket or parenthesis, a trailing backslash, a repetition without an argument or a reversed character range, " +
		"which answer InvalidAllowedPatternException; " +
		"no Parameter Store Change events are sent; the default tier is always Standard (the service setting is unbound); " +
		"a {{...}} value's refusal leaves out the value AWS's message quotes; " + kmsMessage + "; " +
		"unrecorded, understudy's choice: the count refusal's message is understudy's own; " +
		defaultKeyEncrypt
	noteGetParameter = "no AWS public parameters: names under /aws/service/ answer 501, where AWS answers AccessDeniedException for a name it does not publish; " +
		"Secrets Manager references (names under /aws/reference/secretsmanager/) answer 501; " +
		"the legacy Query protocol (?Action= without X-Amz-Target), which AWS also serves, answers 501 for this and every SSM operation; " +
		kmsMessage + "; " +
		defaultKeyDecrypt + "; " + reservedText
	noteGetParameters = "no AWS public parameters: a name under /aws/service/ makes the call answer 501; " +
		"a Secrets Manager reference (a name under /aws/reference/secretsmanager/) makes the call answer 501; " +
		"unrecorded, understudy's choice: a parameter asked by name and by ARN is answered once per spelling; " +
		kmsMessage + "; " + defaultKeyDecrypt + "; " + reservedText
	noteGetParametersByPath = "no AWS public parameters or Secrets Manager references: paths under /aws/service/ or /aws/reference/secretsmanager/ answer 501; " +
		"results come in name order across pages, where AWS's pages are ordered within but not across (switch ssm.list.order); " +
		kmsMessage + "; " + defaultKeyDecrypt
	noteDescribeParameters = "AWS public parameters and Secrets Manager references are never listed: " +
		"a Name or Path filter value under /aws/service/ or /aws/reference/secretsmanager/ answers 501; " +
		"Shared lists nothing, since no parameter can be shared (resource policies are unbound); " +
		"the deprecated Filters member alone answers 501 (use ParameterFilters); " +
		"results come in name order across pages, where AWS's pages are ordered within but not across (switch ssm.list.order); " +
		"unrecorded, understudy's choice: the values of one filter match any of them"
	noteGetParameterHistory   = reserved + "; " + kmsMessage + "; " + defaultKeyDecrypt + "; " + reservedText
	noteDeleteParameters      = reserved + "; no Parameter Store Change events are sent; " + reservedText
	noteLabelParameterVersion = reserved + "; no Parameter Store Change events are sent; " +
		"InvalidLabels lists the refused labels in request order, where AWS's order is its own; " + reservedText
)

// Switches lists the behaviors chosen before AWS's were recorded
// (ssm-parameters.md 12.2). Four are recorded as understudy behaves, each
// settled by a parity step of its recording. ssm.list.order is the one AWS
// contradicts: its pages
// are ordered by name within but not across pages (SSM-19, SSM-20), an
// order understudy cannot reproduce, so understudy keeps the stable name
// order Terraform's data sources need (10.2).
func (*Service) Switches() []fidelity.Entry {
	const ref = "docs/research/ssm-parameters.md#"
	settledBy := func(item, step string) fidelity.SettledBy {
		return fidelity.SettledBy{Step: fidelity.StepRef{Scenario: "ssm/" + item, Step: step}}
	}
	return []fidelity.Entry{
		fidelity.Switch[string]{
			Name:    "ssm.name.root-slash-echo",
			Value:   "request-spelling",
			Label:   evidence.AWSRecorded,
			Ref:     ref + "32-root-parameters-and-the-leading-slash",
			Record:  "SSM-07",
			Ops:     []string{"ssm.GetParameter", "ssm.GetParameters", "ssm.DescribeParameters", "ssm.GetParameterHistory"},
			Settles: settledBy("SSM-07", "02-get-slash"),
		}.Entry(),
		fidelity.Switch[string]{
			Name:    "ssm.error.parameter-not-found-message",
			Value:   "empty-except-label",
			Label:   evidence.AWSRecorded,
			Ref:     ref + "22-errors",
			Record:  "SSM-02",
			Ops:     []string{"ssm.DeleteParameter", "ssm.GetParameterHistory", "ssm.LabelParameterVersion", "ssm.UnlabelParameterVersion"},
			Settles: settledBy("SSM-02", "03-label"),
		}.Entry(),
		fidelity.Switch[string]{
			Name:    "ssm.put.omitted-fields",
			Value:   "preserved",
			Label:   evidence.AWSRecorded,
			Ref:     ref + "71-putparameter",
			Record:  "SSM-10",
			Ops:     []string{"ssm.PutParameter"},
			Settles: settledBy("SSM-10", "02-describe"),
		}.Entry(),
		fidelity.Switch[string]{
			Name:    "ssm.kms.key-id-echo",
			Value:   "verbatim",
			Label:   evidence.AWSRecorded,
			Ref:     ref + "54-keyid-in-metadata",
			Record:  "SSM-22",
			Ops:     []string{"ssm.PutParameter", "ssm.DescribeParameters", "ssm.GetParameterHistory"},
			Settles: settledBy("SSM-22", "01-describe"),
		}.Entry(),
		fidelity.Switch[string]{
			Name:    "ssm.list.order",
			Value:   "name-ascending",
			Label:   evidence.Inferred,
			Ref:     ref + "74-getparametersbypath",
			Record:  "SSM-19, SSM-20",
			Ops:     []string{"ssm.GetParametersByPath", "ssm.DescribeParameters"},
			Settles: settledBy("SSM-20", "01-default-page"),
		}.Entry(),
	}
}

// errUnsupported answers a request that needs a feature of op understudy
// does not implement (ssm-parameters.md 12.1): 501 naming
// the feature and where to ask for it.
func errUnsupported(op, feature string) *awserr.Error {
	return ssmapi.ErrNotImplemented(fmt.Sprintf(
		"understudy does not implement %s in ssm %s; nothing was changed. "+
			"Run 'understudy coverage --service ssm' for what is supported; to ask for it, see %s?q=ssm",
		feature, op, issuesURL))
}
