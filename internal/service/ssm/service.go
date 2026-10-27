package ssm

import (
	"context"
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

	b.PutParameter(s.putParameter, registry.Partial{Note: notePutParameter})
	b.GetParameter(s.getParameter, registry.Partial{Note: noteGetParameter})
	b.GetParameters(s.getParameters, registry.Partial{Note: noteGetParameters})
	b.GetParametersByPath(s.getParametersByPath, registry.Partial{Note: noteGetParametersByPath})
	b.DescribeParameters(s.describeParameters, registry.Partial{Note: noteDescribeParameters})
	b.GetParameterHistory(s.getParameterHistory, registry.Partial{Note: noteGetParameterHistory})
	b.DeleteParameter(s.deleteParameter, registry.Partial{Note: reserved + "; no Parameter Store Change events are sent"})
	b.DeleteParameters(s.deleteParameters, registry.Partial{Note: noteDeleteParameters})
	b.LabelParameterVersion(s.labelParameterVersion, registry.Partial{Note: noteLabelParameterVersion})
	b.UnlabelParameterVersion(s.unlabelParameterVersion, registry.Partial{Note: reserved})
	b.AddTagsToResource(s.addTagsToResource, registry.Partial{Note: reserved + "; " + parameterOnly})
	b.RemoveTagsFromResource(s.removeTagsFromResource, registry.Partial{Note: reserved + "; " + parameterOnly})
	b.ListTagsForResource(s.listTagsForResource, registry.Partial{Note: reserved + "; " + parameterOnly +
		"; unrecorded, understudy's choice: an untagged parameter answers TagList []"})
	return s
}

// The coverage notes (ssm-parameters.md 12.1).
const (
	reserved      = "a name under /aws/service/ or /aws/reference/secretsmanager/ makes the call answer 501"
	parameterOnly = "ResourceType Parameter only: the other resource types name nothing understudy stores, so they answer InvalidResourceId"

	notePutParameter = reserved + "; parameter policies answer 501; DataType aws:ec2:image answers 501, since understudy has no EC2 to validate an AMI; " +
		"an Advanced-tier SecureString, including an Intelligent-Tiering one that resolves to Advanced, answers 501; " +
		"AllowedPattern is compiled as Go RE2: a pattern RE2 cannot compile answers 501, except an unbalanced bracket or parenthesis, " +
		"a trailing backslash, a repetition without an argument or a reversed character range, which answer InvalidAllowedPatternException; " +
		"no Parameter Store Change events are sent; the default tier is always Standard (the service setting is unbound); " +
		"unrecorded, understudy's choice: an identical overwrite creates a version, Tags with Overwrite are refused for a new name too, " +
		"an Intelligent-Tiering request answers with the resolved tier, an Advanced parameter written without Tier stays Advanced, " +
		"an empty AllowedPattern on an overwrite clears the stored one, an empty Value is accepted, " +
		"a deleted name can be created again at once with a fresh history, StringList values are stored verbatim, " +
		"value size is counted in bytes and an oversize value answers ValidationException, " +
		"a hierarchical parameter cannot change type (HierarchyTypeMismatchException) while a root parameter can, " +
		"and the messages for name, depth, length, size, count, type-change, missing-Type, KeyId-on-String, AllowedPattern and {{...}} refusals are understudy's own"
	noteGetParameter = "no AWS public parameters: names under /aws/service/ answer 501; " +
		"Secrets Manager references (names under /aws/reference/secretsmanager/) answer 501; " +
		"the legacy Query protocol (?Action= without X-Amz-Target), which AWS also serves, answers 501 for this and every SSM operation; " +
		"unrecorded, understudy's choice: an ARN of another account or Region answers ParameterNotFound, and the ParameterVersionNotFound message is understudy's own"
	noteGetParameters = "no AWS public parameters: a name under /aws/service/ makes the call answer 501; " +
		"a Secrets Manager reference (a name under /aws/reference/secretsmanager/) makes the call answer 501; " +
		"unrecorded, understudy's choice: a name given twice is answered once, and a name that is not valid, or whose version or label does not exist, is listed in InvalidParameters"
	noteGetParametersByPath = "no AWS public parameters or Secrets Manager references: paths under /aws/service/ or /aws/reference/secretsmanager/ answer 501; " +
		"unrecorded, understudy's choice: 10 results per page without MaxResults, Path / lists root parameters, and a Label filter's results carry no Selector"
	noteDescribeParameters = "AWS public parameters and Secrets Manager references are never listed: " +
		"a Name or Path filter value under /aws/service/ or /aws/reference/secretsmanager/ answers 501; " +
		"Shared lists nothing, since no parameter can be shared (resource policies are unbound); " +
		"the deprecated Filters member answers 501 (use ParameterFilters); " +
		"unrecorded, understudy's choice: 50 results per page without MaxResults, a Path filter without Option is OneLevel, " +
		"the values of one filter match any of them, a tag filter without Values matches any value of its key, " +
		"BeginsWith and Contains are case-sensitive, and LastModifiedUser of a role session is the assumed-role session ARN"
	noteGetParameterHistory = reserved + "; unrecorded, understudy's choice: 50 versions per page without MaxResults, " +
		"version numbers keep counting after the oldest of 100 is dropped, and LastModifiedUser of a role session is the assumed-role session ARN"
	noteDeleteParameters = reserved + "; no Parameter Store Change events are sent; " +
		"unrecorded, understudy's choice: a name that is not valid is listed in InvalidParameters"
	noteLabelParameterVersion = reserved + "; no Parameter Store Change events are sent; " +
		"the ParameterVersionLabelLimitExceeded message is understudy's own (AWS's is unrecorded)"
)

// Switches lists the behaviors chosen without AWS-recorded evidence
// (ssm-parameters.md 12.2).
func (*Service) Switches() []fidelity.Entry {
	const ref = "docs/research/ssm-parameters.md#"
	return []fidelity.Entry{
		fidelity.Switch[string]{
			Name:    "ssm.name.root-slash-echo",
			Value:   "request-spelling",
			Label:   evidence.Inferred,
			Ref:     ref + "32-root-parameters-and-the-leading-slash",
			Record:  "SSM-07",
			Ops:     []string{"ssm.GetParameter", "ssm.GetParameters", "ssm.DescribeParameters", "ssm.GetParameterHistory"},
			Settles: fidelity.NoScenario{Reason: "unrecorded: SSM-07 not yet recorded"},
		}.Entry(),
		fidelity.Switch[string]{
			Name:    "ssm.error.parameter-not-found-message",
			Value:   "empty",
			Label:   evidence.Inferred,
			Ref:     ref + "22-errors",
			Record:  "SSM-02",
			Ops:     []string{"ssm.DeleteParameter", "ssm.GetParameterHistory", "ssm.LabelParameterVersion", "ssm.UnlabelParameterVersion"},
			Settles: fidelity.NoScenario{Reason: "unrecorded: SSM-02 not yet recorded"},
		}.Entry(),
		fidelity.Switch[string]{
			Name:    "ssm.put.omitted-fields",
			Value:   "preserved",
			Label:   evidence.Inferred,
			Ref:     ref + "71-putparameter",
			Record:  "SSM-10",
			Ops:     []string{"ssm.PutParameter"},
			Settles: fidelity.NoScenario{Reason: "unrecorded: SSM-10 not yet recorded"},
		}.Entry(),
		fidelity.Switch[string]{
			Name:    "ssm.kms.key-id-echo",
			Value:   "verbatim",
			Label:   evidence.Inferred,
			Ref:     ref + "54-keyid-in-metadata",
			Record:  "SSM-22",
			Ops:     []string{"ssm.PutParameter", "ssm.DescribeParameters", "ssm.GetParameterHistory"},
			Settles: fidelity.NoScenario{Reason: "unrecorded: SSM-22 not yet recorded"},
		}.Entry(),
		fidelity.Switch[string]{
			Name:    "ssm.list.order",
			Value:   "name-ascending",
			Label:   evidence.Inferred,
			Ref:     ref + "74-getparametersbypath",
			Record:  "SSM-19, SSM-20",
			Ops:     []string{"ssm.GetParametersByPath", "ssm.DescribeParameters"},
			Settles: fidelity.NoScenario{Reason: "unrecorded: SSM-19 and SSM-20 not yet recorded"},
		}.Entry(),
	}
}

// errNotImplementedYet is the answer of an operation whose handler has not
// landed yet.
func errNotImplementedYet(op string) *awserr.Error {
	return ssmapi.ErrNotImplemented("understudy does not implement ssm " + op +
		" yet. Run 'understudy coverage --service ssm' for what is supported. Nothing was changed.")
}

// errUnsupported answers a request that needs a feature of op understudy
// does not implement (spec "Scope"; ssm-parameters.md 12.1): 501 naming
// the feature and where to ask for it.
func errUnsupported(op, feature string) *awserr.Error {
	return ssmapi.ErrNotImplemented(fmt.Sprintf(
		"understudy does not implement %s in ssm %s; nothing was changed. "+
			"Run 'understudy coverage --service ssm' for what is supported; to ask for it, see %s?q=ssm",
		feature, op, issuesURL))
}

// notYet answers an operation whose handler has not landed yet, after the
// reserved-namespace check every operation runs first (checkReserved).
func notYet(op string, raws ...string) *awserr.Error {
	if err := checkReserved(op, raws...); err != nil {
		return err
	}
	return errNotImplementedYet(op)
}

// The handlers below are not implemented yet: each answers notYet.

func (*Service) getParameterHistory(_ context.Context, _ *kernel.Call, in *ssmapi.GetParameterHistoryInput) (*ssmapi.GetParameterHistoryOutput, *awserr.Error) {
	return nil, notYet("GetParameterHistory", in.Name)
}

func (*Service) deleteParameters(_ context.Context, _ *kernel.Call, in *ssmapi.DeleteParametersInput) (*ssmapi.DeleteParametersOutput, *awserr.Error) {
	return nil, notYet("DeleteParameters", in.Names...)
}

func (*Service) labelParameterVersion(_ context.Context, _ *kernel.Call, in *ssmapi.LabelParameterVersionInput) (*ssmapi.LabelParameterVersionOutput, *awserr.Error) {
	return nil, notYet("LabelParameterVersion", in.Name)
}

func (*Service) unlabelParameterVersion(_ context.Context, _ *kernel.Call, in *ssmapi.UnlabelParameterVersionInput) (*ssmapi.UnlabelParameterVersionOutput, *awserr.Error) {
	return nil, notYet("UnlabelParameterVersion", in.Name)
}

func (*Service) addTagsToResource(_ context.Context, _ *kernel.Call, in *ssmapi.AddTagsToResourceInput) (*ssmapi.AddTagsToResourceOutput, *awserr.Error) {
	return nil, notYet("AddTagsToResource", in.ResourceId)
}

func (*Service) removeTagsFromResource(_ context.Context, _ *kernel.Call, in *ssmapi.RemoveTagsFromResourceInput) (*ssmapi.RemoveTagsFromResourceOutput, *awserr.Error) {
	return nil, notYet("RemoveTagsFromResource", in.ResourceId)
}
