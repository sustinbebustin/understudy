package enforce

import (
	"errors"
	"strings"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// identity reads p's identity-based side. A user or role IAM does not hold
// has no identity-based policy, so it is implicitly denied rather than
// unevaluable (iam-evaluation.md 2.2 step 4).
func (s *Sources) identity(p identity.Principal) (authz.IdentitySet, error) {
	ids, err := s.Identities.Identity(p)
	if _, missing := errors.AsType[*authz.NoSuchPrincipalError](err); missing {
		return authz.IdentitySet{Boundary: authz.NoBoundary{}}, nil
	}
	return ids, err
}

// request is one pair to decide and what decides it.
type request struct {
	principal identity.Principal
	ids       authz.IdentitySet
	pair      kernel.Pair
	// account is where a pair on no resource is asked, and the owner of a
	// resource whose ARN names no account and that no service holds: the
	// call's account, or the evaluated principal's.
	account aws.AccountID
	// context is the request context's input; evaluate fills in the
	// principal, its tags, the action and the resource.
	context authz.ContextInput
}

// evaluation is one decided pair.
type evaluation struct {
	outcome authz.Outcome
	denial  authz.Denial
}

// evaluate decides one pair (iam-evaluation.md 2.2): the resource with
// its owner and policy, the request context, then authz.Decide.
func (s *Sources) evaluate(r request) (evaluation, error) {
	resource, policy, err := s.resourceOf(r.pair, r.account)
	if err != nil {
		return evaluation{}, err
	}
	in := r.context
	in.Principal, in.PrincipalTags, in.Action, in.Resource = r.principal, r.ids.Tags, r.pair.Action, resource
	boundary := r.ids.Boundary
	if boundary == nil {
		boundary = authz.NoBoundary{}
	}
	outcome := authz.Decide(
		authz.Request{Principal: r.principal, Action: r.pair.Action, Resource: resource, Context: authz.BuildContext(in)},
		authz.Policies{Identity: r.ids.Policies, Boundary: boundary, Session: r.ids.Session, Resource: policy},
	)
	return evaluation{
		outcome: outcome,
		denial: authz.Denial{
			Principal: r.principal, Action: r.pair.Action, Resource: resource, Decision: outcome.Decision,
			ResourceText: r.pair.DenialResource,
		},
	}, nil
}

// resourceOf is a pair's resource with the account that owns it, and the
// resource-based policy AWS reads for it (iam-enforcement-surfaces.md 12.2). The owner is
// the holding service's, else the ARN's account (an S3 ARN names none),
// else account. An sts action on a role reads the role's trust policy
// (iam-evaluation.md 3.1); any other action on an IAM resource reads none,
// since a role has no resource policy but its trust policy; a KMS key reads
// its key policy, which must allow (4.1). A trust or key policy no service
// holds is empty, so it allows nothing. A stored policy that does not parse
// is an *UnreadablePolicyError, never no policy
// (iam-enforcement-surfaces.md 12.10).
func (s *Sources) resourceOf(pair kernel.Pair, account aws.AccountID) (authz.Resource, authz.ResourcePolicy, error) {
	on, ok := pair.Resource.(kernel.ResourceARN)
	if !ok {
		return authz.AnyResource{Account: account}, authz.NoResourcePolicy{}, nil
	}
	arn := on.ARN
	stored, found := s.Resources.StoredPolicy(arn)
	owner := stored.Owner
	if !found || owner.IsZero() {
		owner = account
		if a, err := aws.ParseAccountID(arn.Account); err == nil {
			owner = a
		}
	}
	resource := authz.OnResource{ARN: arn, Owner: owner}
	var doc policydoc.Document
	held := false
	switch p := stored.Policy.(type) {
	case authz.Parsed:
		doc, held = p.Document, true
	case authz.Unparsed:
		return nil, nil, &UnreadablePolicyError{Resource: arn, Err: p.Err}
	case authz.NoPolicy, nil:
	}
	switch {
	case pair.Action.Prefix == "sts" && arn.Service == "iam" && strings.HasPrefix(arn.Resource, "role/"):
		return resource, authz.Trust{Document: doc}, nil
	case arn.Service == "iam":
		return resource, authz.NoResourcePolicy{}, nil
	case arn.Service == "kms" && strings.HasPrefix(arn.Resource, "key/"):
		return resource, authz.KeyPolicy{Document: doc}, nil
	case held:
		return resource, authz.Generic{Document: doc}, nil
	}
	return resource, authz.NoResourcePolicy{}, nil
}

// via maps how c reaches its service onto the request context's
// (iam-enforcement-surfaces.md 9): a forward-access call with the endpoint of the service that
// forwards it (kms:ViaService, iam-enforcement-surfaces.md 9.2), an
// execution role's, a delivery with its source keys, else a direct call.
func via(c *kernel.Call) authz.Via {
	switch m := c.Mode().(type) {
	case kernel.AsCaller:
		forward := authz.ForwardAccess{}
		if svc, ok := c.ForwardedBy(); ok {
			forward.ViaService = svc.SigningName + "." + c.Scope().Region.String() + ".amazonaws.com"
		}
		return forward
	case kernel.AsExecutionRole:
		return authz.ExecutionRole{}
	case kernel.AsService:
		return authz.Delivery{Source: m.Source, SourceAccount: m.SourceAccount}
	case kernel.AsPlatform, nil:
	}
	return authz.Direct{}
}

// serviceOf is the service of c's operation; "" for a call that names no
// modeled operation, which the kernel never asks about.
func serviceOf(c *kernel.Call) ref.ServiceID {
	if c.Op() == nil {
		return ""
	}
	return c.Op().Service.ID
}

// callRegion is aws:RequestedRegion: the call's region, and us-east-1 for
// IAM ("IAM calls are always made to the us-east-1 Region",
// iam-evaluation.md 8.1) and STS, whose state is per account as IAM's.
func callRegion(c *kernel.Call) string {
	switch serviceOf(c) {
	case "iam", "sts":
		return aws.DefaultRegion().String()
	}
	return c.Scope().Region.String()
}

// arnRegion is the region Evaluate supplies: the resource ARN's, else
// us-east-1.
func arnRegion(pair kernel.Pair) string {
	if on, ok := pair.Resource.(kernel.ResourceARN); ok && on.ARN.Region != "" {
		return on.ARN.Region
	}
	return aws.DefaultRegion().String()
}

// grammar is the form a denial of action, asked by a call to service, is
// written in (iam-enforcement-surfaces.md 4.1 to 4.3): STS's recorded text
// for a call to STS; else by the service that owns the action, S3's
// variant, SQS's and SNS's spelling of the action, or the documented
// grammar, which kms:Decrypt follows too (IAM-ENF-05). The action's own
// service writes it: S3 relays KMS's text for a refused kms:Decrypt
// (IAM-ENF-28), and EventBridge SQS's for a refused delivery (IAM-ENF-22).
// SQS answers the same text over JSON and Query (IAM-ENF-01, IAM-ENF-02).
func grammar(service ref.ServiceID, action registry.IAMAction) authz.Grammar {
	if service == "sts" {
		return authz.GrammarSTS
	}
	switch action.Prefix {
	case "s3":
		return authz.GrammarS3
	case "sqs":
		return authz.GrammarSQS
	case "sns":
		return authz.GrammarSNS
	}
	return authz.GrammarIAM
}
