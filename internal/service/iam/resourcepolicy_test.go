package iam_test

import (
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/service/iam"
)

// trustEC2 is a second trust policy, for UpdateAssumeRolePolicy.
const trustEC2 = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

// A role's trust policy is its resource policy ("The trust policy is the
// role's resource policy", iam-evaluation.md 3.1; iam-enforcement-surfaces.md
// 7.9), read by the role's ARN, path included, as IAM stores it now. Any
// other ARN names no trust policy.
func TestTheTrustPolicyIsTheRolesResourcePolicy(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		var provider authz.PolicyProvider = iam.New(k.Deps(iamapi.Service()), iam.Options{})
		h := &harness{K: k}
		created := must(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "worker", Path: new("/team/"), AssumeRolePolicyDocument: trustLambda}).Role
		role := parseARN(h, created.Arn)

		read := func(want string) {
			h.Helper()
			doc, ok := provider.ResourcePolicy(role)
			if !ok || doc == nil || !reflect.DeepEqual(doc, policydoc.ParseStored(want)) {
				h.Errorf("ResourcePolicy(%s) = %+v, %t; want the trust policy %s", role, doc, ok, want)
			}
		}
		read(trustLambda)
		must(h, iamapi.OpUpdateAssumeRolePolicy, &iamapi.UpdateAssumeRolePolicyInput{RoleName: "worker", PolicyDocument: trustEC2})
		read(trustEC2)

		for _, miss := range []string{
			"arn:aws:iam::000000000000:role/worker",       // the path differs
			"arn:aws:iam::000000000000:role/other/worker", // the path differs
			"arn:aws:iam::000000000000:role/team/absent",  // no such role
			"arn:aws:iam::111111111111:role/team/worker",  // another account has none
			"arn:aws:iam::000000000000:user/team/worker",  // not a role
			"arn:aws:iam::000000000000:policy/team/worker",
		} {
			a, err := aws.ParseARN(miss)
			if err != nil {
				h.Fatal(err)
			}
			if doc, ok := provider.ResourcePolicy(a); ok || doc != nil {
				h.Errorf("ResourcePolicy(%s) = %+v, %t; want nil, false", miss, doc, ok)
			}
		}
	}, kerneltest.Services(iamapi.Service()))
}
