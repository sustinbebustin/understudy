package iam_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/policycatalogue"
	"github.com/sustinbebustin/understudy/internal/service/iam"
)

// A role's boundary, customer managed here, is its sessions' boundary
// (iam-evaluation.md 2.8), and a session without attributes, such as an
// execution-environment session, has the role's policies and tags and no
// session policy.
func TestIdentityOfASessionWithoutAttributesKeepsTheRolesBoundaryAndTags(t *testing.T) {
	identityHarness(t, func(h *harness, svc *iam.Service) {
		bound := must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: "bound", PolicyDocument: readDoc}).Policy
		role := must(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{
			RoleName: "fn", AssumeRolePolicyDocument: trustLambda, PermissionsBoundary: bound.Arn,
			Tags: []iamapi.Tag{{Key: "team", Value: "red"}},
		}).Role
		must(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "fn", PolicyArn: basicExecution})

		session := identity.AssumedRole{
			Account: aws.DefaultAccount(), Role: identity.RoleRef{Name: "fn", ID: role.RoleId},
			Session: "env-1", Source: identity.FromAssumeRole{},
		}
		got, err := svc.Identity(session)
		if err != nil {
			h.Fatalf("Identity(%s) = %v", identity.Render(session), err)
		}
		if len(got.Policies) != 1 || got.Policies[0].ARN != basicExecution {
			h.Errorf("Policies = %+v, want the attached %s", got.Policies, basicExecution)
		}
		wantBound := authz.BoundedBy{Policy: authz.Policy{ARN: *bound.Arn, Name: "bound", Document: docOf(h, readDoc)}}
		if !reflect.DeepEqual(got.Boundary, authz.Boundary(wantBound)) {
			h.Errorf("Boundary = %+v, want %+v", got.Boundary, wantBound)
		}
		if len(got.Session) != 0 {
			h.Errorf("Session = %+v, want none", got.Session)
		}
		if want := []authz.Tag{{Key: "team", Value: "red"}}; !reflect.DeepEqual(got.Tags, want) {
			h.Errorf("Tags = %+v, want the role's %+v", got.Tags, want)
		}
	})
}

// A session policy ARN that names no policy of the role's account is an
// *UnreadablePolicyError naming it, never read as no session policy: STS
// does not check PolicyArns, and AWS requires them to exist in the role's
// account (iam-evaluation.md 2.7).
func TestIdentityRefusesASessionPolicyARNThatNamesNoPolicy(t *testing.T) {
	identityHarness(t, func(h *harness, svc *iam.Service) {
		role := must(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "app", AssumeRolePolicyDocument: trustLambda}).Role
		const gone = "arn:aws:iam::000000000000:policy/gone"
		session := identity.AssumedRole{
			Account: aws.DefaultAccount(), Role: identity.RoleRef{Name: "app", Path: "/", ID: role.RoleId},
			Session: "s1", Source: identity.FromAssumeRole{},
			Attributes: &identity.SessionAttributes{PolicyARNs: []string{gone}},
		}
		_, err := svc.Identity(session)
		u, ok := errors.AsType[*authz.UnreadablePolicyError](err)
		if !ok || u.Policy != gone || u.Principal != identity.Render(session) || !strings.Contains(err.Error(), "names no customer managed policy") {
			h.Errorf("Identity with a dangling session policy ARN = %v, want an *UnreadablePolicyError naming %s", err, gone)
		}
	})
}

// A user's path is part of its ARN: the user named ana at /team/ is not
// the principal arn:aws:iam::<acct>:user/ana.
func TestIdentityOfAUserAtAnotherPathIsNoSuchPrincipal(t *testing.T) {
	identityHarness(t, func(h *harness, svc *iam.Service) {
		user := must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "ana", Path: new("/team/")}).User
		p := identity.User{Account: aws.DefaultAccount(), Path: "/", Name: "ana", ID: user.UserId}
		if _, err := svc.Identity(p); !isNoSuchPrincipal(err) {
			h.Errorf("Identity(%s) = %v, want *authz.NoSuchPrincipalError", identity.Render(p), err)
		}
	})
}

// An AWS managed policy is read from the catalogue when evaluated; a
// catalogue that fails to load makes the user's policies unreadable, naming
// the policy, never absent.
func TestIdentityRefusesWhenTheCatalogueDoesNotLoad(t *testing.T) {
	loads := true
	loader := func() (*policycatalogue.Catalogue, error) {
		if !loads {
			return nil, errors.New("synthetic decompression failure")
		}
		return catalogueOf(t, testPolicies()), nil
	}
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := iam.New(k.Deps(iamapi.Service()), iam.Options{Catalogue: loader})
		h := &harness{K: k}
		user := must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "ana"}).User
		must(h, iamapi.OpAttachUserPolicy, &iamapi.AttachUserPolicyInput{UserName: "ana", PolicyArn: basicExecution})
		loads = false

		p := identity.User{Account: aws.DefaultAccount(), Path: "/", Name: "ana", ID: user.UserId}
		_, err := svc.Identity(p)
		u, ok := errors.AsType[*authz.UnreadablePolicyError](err)
		if !ok || u.Policy != basicExecution || !strings.Contains(err.Error(), "synthetic decompression failure") {
			h.Errorf("Identity with an unloadable catalogue = %v, want an *UnreadablePolicyError naming %s and the load error", err, basicExecution)
		}
	}, kerneltest.Services(iamapi.Service()))
}
