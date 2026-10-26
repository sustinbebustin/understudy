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
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/service/iam"
)

// IAM is the evaluator's identity-policy source (iam-enforcement-surfaces.md
// 12.2): a user's or a session's role's inline policies, attached customer
// managed policies at their default version and attached AWS managed
// policies from the catalogue (iam-evaluation.md 2.1), the permissions
// boundary (2.8), a session's session policies (2.7) and the principal
// tags, a role's overridden by its session's case-insensitively (S-TAGS,
// 2.7).

// identityHarness runs fn against IAM serving the test catalogue, with the
// service itself for its provider methods.
func identityHarness(t *testing.T, fn func(h *harness, svc *iam.Service)) {
	t.Helper()
	opts := iam.Options{Catalogue: policycatalogue.Static(catalogueOf(t, testPolicies()))}
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := iam.New(k.Deps(iamapi.Service()), opts)
		fn(&harness{K: k}, svc)
	}, kerneltest.Services(iamapi.Service()))
}

func docOf(h *harness, text string) policydoc.Document {
	h.Helper()
	d, err := policydoc.Parse([]byte(text))
	if err != nil {
		h.Fatalf("parse %s: %v", text, err)
	}
	return d
}

const (
	sendDoc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}`
	readDoc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:ReceiveMessage","Resource":"*"}]}`
)

func TestIdentityOfAUser(t *testing.T) {
	identityHarness(t, func(h *harness, svc *iam.Service) {
		var source authz.IdentitySource = svc
		user := must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "ana", Path: new("/team/")}).User
		must(h, iamapi.OpPutUserPolicy, &iamapi.PutUserPolicyInput{UserName: "ana", PolicyName: "logs", PolicyDocument: allowLogs})
		custom := must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: "custom", PolicyDocument: sendDoc}).Policy
		must(h, iamapi.OpCreatePolicyVersion, &iamapi.CreatePolicyVersionInput{PolicyArn: *custom.Arn, PolicyDocument: readDoc, SetAsDefault: true})
		must(h, iamapi.OpAttachUserPolicy, &iamapi.AttachUserPolicyInput{UserName: "ana", PolicyArn: *custom.Arn})
		must(h, iamapi.OpAttachUserPolicy, &iamapi.AttachUserPolicyInput{UserName: "ana", PolicyArn: basicExecution})
		must(h, iamapi.OpPutUserPermissionsBoundary, &iamapi.PutUserPermissionsBoundaryInput{UserName: "ana", PermissionsBoundary: sqsFullAccess})
		must(h, iamapi.OpTagUser, &iamapi.TagUserInput{UserName: "ana", Tags: []iamapi.Tag{{Key: "team", Value: "blue"}}})

		p := identity.User{Account: aws.DefaultAccount(), Path: "/team/", Name: "ana", ID: user.UserId}
		got, err := source.Identity(p)
		if err != nil {
			h.Fatalf("Identity(%s) = %v", identity.Render(p), err)
		}
		want := authz.IdentitySet{
			Policies: []authz.Policy{
				{Name: "logs", Document: docOf(h, allowLogs)},
				{ARN: *custom.Arn, Name: "custom", Document: docOf(h, readDoc)},
				{ARN: basicExecution, Name: "AWSLambdaBasicExecutionRole", Document: docOf(h, guideDocument("logs:PutLogEvents"))},
			},
			Boundary: authz.BoundedBy{Policy: authz.Policy{ARN: sqsFullAccess, Name: "AmazonSQSFullAccess", Document: docOf(h, guideDocument("sqs:SendMessage"))}},
			Tags:     []authz.Tag{{Key: "team", Value: "blue"}},
		}
		if !reflect.DeepEqual(got.Policies, want.Policies) {
			h.Errorf("Policies =\n%+v\nwant inline then attached in attachment order, the default version:\n%+v", got.Policies, want.Policies)
		}
		if !reflect.DeepEqual(got.Boundary, want.Boundary) {
			h.Errorf("Boundary = %+v, want %+v", got.Boundary, want.Boundary)
		}
		if len(got.Session) != 0 {
			h.Errorf("Session = %+v, want none for a user", got.Session)
		}
		if !reflect.DeepEqual(got.Tags, want.Tags) {
			h.Errorf("Tags = %+v, want %+v", got.Tags, want.Tags)
		}
	})
}

func TestIdentityOfARoleSession(t *testing.T) {
	const sessionPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"arn:aws:sqs:us-east-1:000000000000:orders"}]}`
	identityHarness(t, func(h *harness, svc *iam.Service) {
		var source authz.IdentitySource = svc
		role := must(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{
			RoleName: "app", Path: new("/svc/"), AssumeRolePolicyDocument: trustLambda,
			Tags: []iamapi.Tag{{Key: "team", Value: "red"}, {Key: "env", Value: "dev"}},
		}).Role
		must(h, iamapi.OpPutRolePolicy, &iamapi.PutRolePolicyInput{RoleName: "app", PolicyName: "send", PolicyDocument: sendDoc})
		custom := must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: "custom", PolicyDocument: readDoc}).Policy

		session := identity.AssumedRole{
			Account: aws.DefaultAccount(), Role: identity.RoleRef{Name: "app", Path: "/svc/", ID: role.RoleId},
			Session: "s1", Source: identity.FromAssumeRole{},
			Attributes: &identity.SessionAttributes{
				Policy: sessionPolicy, PolicyARNs: []string{*custom.Arn, sqsFullAccess},
				Tags: []identity.SessionTag{{Key: "TEAM", Value: "green"}},
			},
		}
		got, err := source.Identity(session)
		if err != nil {
			h.Fatalf("Identity(%s) = %v", identity.Render(session), err)
		}
		if want := []authz.Policy{{Name: "send", Document: docOf(h, sendDoc)}}; !reflect.DeepEqual(got.Policies, want) {
			h.Errorf("Policies = %+v, want the role's inline policy %+v", got.Policies, want)
		}
		if len(got.Session) != 3 || !reflect.DeepEqual(got.Session[0].Document, docOf(h, sessionPolicy)) || got.Session[0].ARN != "" ||
			got.Session[1].ARN != *custom.Arn || !reflect.DeepEqual(got.Session[1].Document, docOf(h, readDoc)) ||
			got.Session[2].ARN != sqsFullAccess || !reflect.DeepEqual(got.Session[2].Document, docOf(h, guideDocument("sqs:SendMessage"))) {
			h.Errorf("Session = %+v, want the inline session policy, then %s and %s", got.Session, *custom.Arn, sqsFullAccess)
		}
		if _, none := got.Boundary.(authz.NoBoundary); !none {
			h.Errorf("Boundary = %+v, want NoBoundary", got.Boundary)
		}
		team, env := "", ""
		for _, tag := range got.Tags {
			switch strings.ToLower(tag.Key) {
			case "team":
				team += tag.Value
			case "env":
				env += tag.Value
			}
		}
		if team != "green" || env != "dev" {
			h.Errorf("Tags = %+v, want the session's TEAM=green over the role's team=red, and env=dev", got.Tags)
		}

		// The session's role is the role at that path; another path names
		// no role (a role session's ARN drops the path, the role's keeps it).
		elsewhere := session
		elsewhere.Role.Path = "/"
		if _, err := source.Identity(elsewhere); !isNoSuchPrincipal(err) {
			h.Errorf("Identity of a session of role/app at path / = %v, want *authz.NoSuchPrincipalError", err)
		}
	})
}

func isNoSuchPrincipal(err error) bool {
	_, ok := errors.AsType[*authz.NoSuchPrincipalError](err)
	return ok
}

// A root, a service principal and an anonymous caller have no identity
// policy (iam-evaluation.md 2.6, 2.9, 2.10); a user IAM does not hold is a
// *NoSuchPrincipalError that says what is missing.
func TestIdentityOfOtherPrincipals(t *testing.T) {
	identityHarness(t, func(h *harness, svc *iam.Service) {
		for _, p := range []identity.Principal{identity.Root{Account: aws.DefaultAccount()}, identity.ServicePrincipal{Name: "sns.amazonaws.com"}, identity.Anonymous{}} {
			got, err := svc.Identity(p)
			if err != nil || len(got.Policies) != 0 || len(got.Session) != 0 || len(got.Tags) != 0 {
				h.Errorf("Identity(%s) = %+v, %v; want an empty set", identity.Render(p), got, err)
			}
		}
		ghost := identity.User{Account: aws.DefaultAccount(), Path: "/", Name: "ghost", ID: "AIDAGHOSTEXAMPLE0001"}
		_, err := svc.Identity(ghost)
		if !isNoSuchPrincipal(err) || err.Error() != "authz: arn:aws:iam::000000000000:user/ghost does not exist, so it has no identity-based policy" {
			h.Errorf("Identity of a missing user = %v, want the *NoSuchPrincipalError text of contract 3.3", err)
		}
	})
}

// PrincipalOf reads the principal an IAM ARN names, for POST /iam/evaluate:
// a root, a user with its id, or a role as a session named "understudy"
// with no attributes; a user or role that does not exist names none.
func TestPrincipalOf(t *testing.T) {
	identityHarness(t, func(h *harness, svc *iam.Service) {
		user := must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "ana", Path: new("/team/")}).User
		role := must(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "app", Path: new("/svc/"), AssumeRolePolicyDocument: trustLambda}).Role
		parse := func(s string) aws.ARN { return parseARN(h, s) }

		if p, ok := svc.PrincipalOf(parse("arn:aws:iam::000000000000:root")); !ok || p != (identity.Root{Account: aws.DefaultAccount()}) {
			h.Errorf("PrincipalOf(root) = %#v, %t", p, ok)
		}
		wantUser := identity.User{Account: aws.DefaultAccount(), Path: "/team/", Name: "ana", ID: user.UserId}
		if p, ok := svc.PrincipalOf(parse(user.Arn)); !ok || p != wantUser {
			h.Errorf("PrincipalOf(%s) = %#v, %t; want %#v", user.Arn, p, ok, wantUser)
		}
		p, ok := svc.PrincipalOf(parse(role.Arn))
		s, isSession := p.(identity.AssumedRole)
		if !ok || !isSession || s.Account != aws.DefaultAccount() || s.Role != (identity.RoleRef{Name: "app", Path: "/svc/", ID: role.RoleId}) ||
			s.Session != "understudy" || s.Attributes != nil {
			h.Errorf("PrincipalOf(%s) = %#v, %t; want a session named understudy of the role", role.Arn, p, ok)
		}
		for _, miss := range []string{"arn:aws:iam::000000000000:user/ghost", "arn:aws:iam::000000000000:role/app", "arn:aws:iam::000000000000:role/svc/ghost"} {
			if p, ok := svc.PrincipalOf(parse(miss)); ok {
				h.Errorf("PrincipalOf(%s) = %#v, want none", miss, p)
			}
		}
	})
}

// The trust policy is also read through StoredPolicyReader, with the
// role's account as its owner; any other ARN names no stored policy, as
// ResourcePolicy answers (resourcepolicy_test.go).
func TestTheTrustPolicyIsTheRolesStoredPolicy(t *testing.T) {
	identityHarness(t, func(h *harness, svc *iam.Service) {
		var reader authz.StoredPolicyReader = svc
		role := must(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "worker", Path: new("/team/"), AssumeRolePolicyDocument: trustLambda}).Role
		got, ok := reader.StoredPolicy(parseARN(h, role.Arn))
		want := authz.StoredPolicy{Owner: aws.DefaultAccount(), Policy: authz.Parsed{Document: docOf(h, trustLambda)}}
		if !ok || !reflect.DeepEqual(got, want) {
			h.Errorf("StoredPolicy(%s) = %+v, %t; want %+v", role.Arn, got, ok, want)
		}
		for _, miss := range []string{"arn:aws:iam::000000000000:role/worker", "arn:aws:iam::000000000000:role/team/absent", "arn:aws:iam::000000000000:user/team/worker"} {
			if got, ok := reader.StoredPolicy(parseARN(h, miss)); ok {
				h.Errorf("StoredPolicy(%s) = %+v, want none", miss, got)
			}
		}
	})
}
