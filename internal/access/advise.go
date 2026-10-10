package access

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Advice says whether AWS would allow a delivery under the target's stored
// resource policy.
//
//sumtype:decl
type Advice interface{ isAdvice() }

// Allowed is a delivery the stored policy allows, or allows assuming the
// conditions understudy cannot evaluate hold. A Deny statement denies only
// when understudy can evaluate every one of its conditions.
type Allowed struct{}

// WouldDeny is a delivery AWS would refuse; Reason says why and how to fix
// the policy.
type WouldDeny struct{ Reason string }

func (Allowed) isAdvice()   {}
func (WouldDeny) isAdvice() {}

// Advise checks whether stored, the resource policy of target, lets
// servicePrincipal call action on target for a delivery from source, the way
// AWS evaluates SNS-to-SQS, S3 notification and EventBridge-target
// deliveries: aws:SourceArn is source and aws:SourceAccount its account. A
// nil stored is a target without a resource policy, which AWS treats as a
// denial. Advise runs before each delivery attempt, which understudy makes
// whatever the advice; the advice becomes a journal Advisory
// (ARCHITECTURE.md 16).
func Advise(stored *policydoc.Document, servicePrincipal ServicePrincipal, action registry.IAMAction, source, target aws.ARN) Advice {
	if stored == nil {
		return implicitDeny(target.String()+" has no resource policy, which does not allow", servicePrincipal, action, source, target)
	}
	context := map[string]string{"aws:SourceArn": source.String()}
	if source.Account != "" {
		context["aws:SourceAccount"] = source.Account
	}
	decision := policydoc.Allows(*stored, policydoc.Query{
		Principal: policydoc.ServiceCaller{Name: servicePrincipal.Name},
		Action:    action.String(),
		Resource:  target.String(),
		Context:   context,
	})
	switch d := decision.(type) {
	case policydoc.Allowed:
	case policydoc.ExplicitDeny:
		return WouldDeny{Reason: fmt.Sprintf(
			"%s in the resource policy of %s explicitly denies %s calling %s for a delivery from %s, so AWS would deny it; "+
				"understudy does not enforce resource policies, so it attempts the delivery anyway. "+
				"An Allow statement cannot override an explicit Deny: narrow that statement so it does not match this delivery, or remove it.",
			describeStatement(d.Statement), target, servicePrincipal, action, source,
		)}
	case policydoc.ImplicitDeny:
		return implicitDeny("the resource policy of "+target.String()+" does not allow", servicePrincipal, action, source, target)
	}
	return Allowed{}
}

// implicitDeny is the advice for a delivery no statement allows; what says
// whose policy does not allow it.
func implicitDeny(what string, servicePrincipal ServicePrincipal, action registry.IAMAction, source, target aws.ARN) WouldDeny {
	return WouldDeny{Reason: fmt.Sprintf(
		"%s %s to call %s for a delivery from %s, so AWS would deny it; "+
			"understudy does not enforce resource policies, so it attempts the delivery anyway. %s",
		what, servicePrincipal, action, source, remedy(servicePrincipal, action, source, target),
	)}
}

// remedy says how to grant the delivery. A function policy is not written
// directly: Lambda's AddPermission writes the statement (lambda-api.md
// 7.3). Other targets take the statement in their policy attribute.
func remedy(servicePrincipal ServicePrincipal, action registry.IAMAction, source, target aws.ARN) string {
	if target.Service == "lambda" {
		return fmt.Sprintf(
			"Grant it with Lambda's AddPermission: aws lambda add-permission --function-name %s --statement-id <unique id> --action %s --principal %s --source-arn %s, "+
				"or an aws_lambda_permission resource in Terraform with the same action, principal and source_arn.",
			target, action, servicePrincipal.Name, source,
		)
	}
	return fmt.Sprintf(
		"Add an Allow statement with Principal {\"Service\": %q}, Action %q, Resource %q and the condition ArnEquals aws:SourceArn %q.",
		servicePrincipal.Name, action, target, source,
	)
}

// describeStatement names a statement the way a reader finds it in the
// policy: by Sid, or by position when it has none.
func describeStatement(r policydoc.StatementRef) string {
	if r.Sid != "" {
		return fmt.Sprintf("The statement with Sid %q", r.Sid)
	}
	return fmt.Sprintf("Statement[%d] (it has no Sid)", r.Index)
}
