package iam_test

import (
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/service/iam"
)

// A role on the default path "/" is read by role/<name>, and a deleted
// role no longer has a trust policy to read. An ARN in another partition
// or naming the "aws" account of AWS managed policies names no role.
func TestTheTrustPolicyFollowsTheRolesLifecycle(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		var provider authz.PolicyProvider = iam.New(k.Deps(iamapi.Service()), iam.Options{})
		h := &harness{K: k}
		created := must(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "runner", AssumeRolePolicyDocument: trustLambda}).Role
		role := parseARN(h, created.Arn)
		if role.Resource != "role/runner" {
			h.Fatalf("CreateRole ARN %s, want resource role/runner", created.Arn)
		}
		if doc, ok := provider.ResourcePolicy(role); !ok || !reflect.DeepEqual(doc, policydoc.ParseStored(trustLambda)) {
			h.Errorf("ResourcePolicy(%s) = %+v, %t; want the trust policy", role, doc, ok)
		}
		for _, miss := range []string{
			"arn:aws-cn:iam::000000000000:role/runner",
			"arn:aws:iam::aws:role/runner",
			"arn:aws:iam::000000000000:role/",
		} {
			if doc, ok := provider.ResourcePolicy(parseARN(h, miss)); ok || doc != nil {
				h.Errorf("ResourcePolicy(%s) = %+v, %t; want nil, false", miss, doc, ok)
			}
		}

		must(h, iamapi.OpDeleteRole, &iamapi.DeleteRoleInput{RoleName: "runner"})
		if doc, ok := provider.ResourcePolicy(role); ok || doc != nil {
			h.Errorf("ResourcePolicy(%s) after DeleteRole = %+v, %t; want nil, false", role, doc, ok)
		}
	}, kerneltest.Services(iamapi.Service()))
}
