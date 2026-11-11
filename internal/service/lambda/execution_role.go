package lambda

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// checkRoleTrust asks the Authorizer, under iam.enforce advise and enforce,
// whether lambda.amazonaws.com may assume role, the execution role a
// CreateFunction or UpdateFunctionConfiguration passes to the function ref
// names; under off there is no Authorizer and nothing is asked. AWS refuses
// a role whose trust policy does not let Lambda in, or that does not exist,
// with errRoleNotAssumable (iam-enforcement-surfaces.md 4.3, 13.28;
// recorded for CreateFunction only). The caller's iam:PassRole on the role
// is asked before, at step 8, as AWS answered it first (26-no-passrole).
// No role (an update that passes none) is not asked about, nor one that is
// not an IAM ARN, which validation answers. The aws:SourceArn and
// aws:SourceAccount keys, the function and its account, are inferred from
// the confused-deputy keys other service principals send. Call it holding
// no lock.
func checkRoleTrust(ctx context.Context, c *kernel.Call, ref functionRef, role *string) *awserr.Error {
	roleARN, ok := executionRole(role)
	if !ok {
		return nil
	}
	as := kernel.AsService{Name: lambdaService, Source: functionResource(ref.scope, ref.name), SourceAccount: ref.scope.Account}
	q := kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: registry.IAMAction{Prefix: "sts", Name: "AssumeRole"}, Resource: kernel.ResourceARN{ARN: roleARN}}}}
	if err := kernel.AuthorizeAs(ctx, c, as, q); err != nil {
		if _, denied := err.Origin.(awserr.Denial); denied {
			return errRoleNotAssumable()
		}
		return err
	}
	return nil
}
