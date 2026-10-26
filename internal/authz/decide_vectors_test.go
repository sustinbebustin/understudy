package authz_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The worked examples of iam-evaluation.md 9 that need several policies:
// single account (A), explicit and implicit deny (B), cross account (C, D),
// resource-policy principal forms (E), permissions boundaries (F), session
// policies (G), trust policies (H), KMS key policies (I), N5, the request
// context example (O), NotPrincipal and wildcard principals (S13 to S26),
// service principals (T) and the third-party-recorded simulator outcomes
// (U). Each row cites its vector id; the section of 9 that lists it names
// the AWS page anchor. Where a row's single denying policy settles the type
// a denial names (iam-evaluation.md 2.11), the row checks it too.
//
// Decide reads the request context it is given and builds none, so each
// row sets the keys its vector states (aws:PrincipalArn for a statement
// that tests it, the delivery's source keys for a service principal).
//
// I10 (cross-account KMS management operations) is not a decision vector:
// the KMS service answers it under kms.cross-account.management-error
// (iam-evaluation.md 4.4).

func account(s string) aws.AccountID {
	a, err := aws.ParseAccountID(s)
	if err != nil {
		panic(err)
	}
	return a
}

func arnOf(s string) aws.ARN {
	a, err := aws.ParseARN(s)
	if err != nil {
		panic(err)
	}
	return a
}

func actionOf(s string) registry.IAMAction {
	prefix, name, ok := strings.Cut(s, ":")
	if !ok {
		panic("action without a prefix: " + s)
	}
	return registry.IAMAction{Prefix: prefix, Name: name}
}

func userOf(acct, name, id string) identity.User {
	return identity.User{Account: account(acct), Path: "/", Name: name, ID: id}
}

func sessionOf(acct, role, roleID, session string) identity.AssumedRole {
	return identity.AssumedRole{
		Account: account(acct), Role: identity.RoleRef{Name: role, Path: "/", ID: roleID},
		Session: session, Source: identity.FromAssumeRole{},
	}
}

func on(arn, owner string) authz.Resource {
	return authz.OnResource{ARN: arnOf(arn), Owner: account(owner)}
}
func anyIn(acct string) authz.Resource { return authz.AnyResource{Account: account(acct)} }

// docOf parses a policy document; a fragment of statements (a JSON object
// or list that is not a document) is wrapped in a 2012-10-17 document, as
// iam-evaluation.md 9.5 and 9.9 read their fragments.
func docOf(t fataler, text string) policydoc.Document {
	t.Helper()
	if !strings.Contains(text, `"Statement"`) {
		if !strings.HasPrefix(text, "[") {
			text = "[" + text + "]"
		}
		text = `{"Version":"2012-10-17","Statement":` + text + `}`
	}
	return *parse(t, text)
}

// policySet names the policies of a row; the zero value is a principal
// with no policy at all on a resource with no resource policy.
type policySet struct {
	identity []authz.Policy
	boundary *authz.Policy
	session  []authz.Policy
	resource authz.ResourcePolicy
}

func (s policySet) policies() authz.Policies {
	p := authz.Policies{Identity: s.identity, Boundary: authz.NoBoundary{}, Session: s.session, Resource: s.resource}
	if s.boundary != nil {
		p.Boundary = authz.BoundedBy{Policy: *s.boundary}
	}
	if p.Resource == nil {
		p.Resource = authz.NoResourcePolicy{}
	}
	return p
}

func inlinePolicy(t fataler, name, text string) authz.Policy {
	return authz.Policy{Name: name, Document: docOf(t, text)}
}

func managedPolicy(t fataler, arn, text string) authz.Policy {
	_, name, _ := strings.Cut(arn, ":policy/")
	return authz.Policy{ARN: arn, Name: name[strings.LastIndex(name, "/")+1:], Document: docOf(t, text)}
}

// contextKeys is a request context holding the given key, value pairs,
// every other key absent.
func contextKeys(pairs ...string) *policydoc.Context {
	c := policydoc.NewContext("")
	for i := 0; i+1 < len(pairs); i += 2 {
		c.Set(pairs[i], pairs[i+1])
	}
	return c
}

// outcome is what a row expects: a decision, or only "no explicit deny"
// where the vector says "not denied".
type outcome struct {
	decision  authz.Decision
	notDenied bool
}

func allow() outcome    { return outcome{decision: authz.Allow{}} }
func implicit() outcome { return outcome{decision: authz.ImplicitDeny{}} }

func explicit(t authz.PolicyType, arn string) outcome {
	return outcome{decision: authz.ExplicitDeny{PolicyType: t, PolicyARN: arn}}
}
func notDenied() outcome { return outcome{notDenied: true} }

// decisionName is a decision's variant name.
func decisionName(d authz.Decision) string {
	if d == nil {
		return "nil"
	}
	return reflect.TypeOf(d).Name()
}

// check compares got with want: the variant always; an explicit deny's
// policy type and ARN when the row names them.
func (want outcome) check(t *testing.T, label string, got authz.Decision) {
	t.Helper()
	if want.notDenied {
		if _, denied := got.(authz.ExplicitDeny); denied || got == nil {
			t.Errorf("%s: Decide = %#v, want no explicit deny", label, got)
		}
		return
	}
	if decisionName(got) != decisionName(want.decision) {
		t.Errorf("%s: Decide = %#v, want %s", label, got, decisionName(want.decision))
		return
	}
	w, wantsExplicit := want.decision.(authz.ExplicitDeny)
	g, isExplicit := got.(authz.ExplicitDeny)
	if wantsExplicit && isExplicit {
		if (w.PolicyType != 0 && g.PolicyType != w.PolicyType) || (w.PolicyARN != "" && g.PolicyARN != w.PolicyARN) {
			t.Errorf("%s: Decide = %#v, want %#v", label, got, w)
		}
	}
}

type decideVector struct {
	id        string
	principal identity.Principal
	action    string
	resource  authz.Resource
	ctx       *policydoc.Context
	set       policySet
	want      outcome
}

func runDecideVectors(t *testing.T, section string, vectors []decideVector) {
	t.Helper()
	for i := range vectors {
		v := &vectors[i]
		t.Run(v.id, func(t *testing.T) {
			ctx := v.ctx
			if ctx == nil {
				ctx = contextKeys()
			}
			got := authz.Decide(authz.Request{Principal: v.principal, Action: actionOf(v.action), Resource: v.resource, Context: ctx}, v.set.policies())
			v.want.check(t, "iam-evaluation.md "+section+" vector "+v.id, got.Decision)
		})
	}
}

// evalSwitch is the value of the evaluation switch name, which governs a
// behavior the research leaves UNVERIFIED (iam-evaluation.md 12.3).
func evalSwitch(t fataler, name string) string {
	t.Helper()
	for _, e := range authz.Switches() {
		if e.Name == name {
			return e.Value
		}
	}
	t.Fatalf("authz.Switches() registers no %s (iam-evaluation.md 12.3, iam-enforcement-surfaces.md 12.8)", name)
	return ""
}

// A: single account, iam-evaluation.md 9.2 (S-ONE).
func TestDecideSingleAccountVectors(t *testing.T) {
	const (
		ad = `{"Version":"2012-10-17","Statement":[{"Sid":"AllowS3ListRead","Effect":"Allow","Action":["s3:GetBucketLocation","s3:GetAccountPublicAccessBlock","s3:ListAccessPoints","s3:ListAllMyBuckets"],"Resource":"arn:aws:s3:::*"},` +
			`{"Sid":"AllowS3Self","Effect":"Allow","Action":"s3:*","Resource":["arn:aws:s3:::amzn-s3-demo-bucket-carlossalazar/*","arn:aws:s3:::amzn-s3-demo-bucket-carlossalazar"]},` +
			`{"Sid":"DenyS3Logs","Effect":"Deny","Action":"s3:*","Resource":"arn:aws:s3:::*log*"}]}`
		rp = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:user/carlossalazar"},"Action":"s3:*",` +
			`"Resource":["arn:aws:s3:::amzn-s3-demo-bucket-carlossalazar/*","arn:aws:s3:::amzn-s3-demo-bucket-carlossalazar"]}]}`
		bucket = "arn:aws:s3:::amzn-s3-demo-bucket-carlossalazar"
	)
	carlos := userOf("123456789012", "carlossalazar", "AIDACARLOSSALAZAR001")
	id := []authz.Policy{inlinePolicy(t, "carlos", ad)}
	bucketPolicy := authz.Generic{Document: docOf(t, rp)}
	runDecideVectors(t, "9.2", []decideVector{
		{"A1", carlos, "s3:PutObject", on(bucket+"-logs/report.txt", "123456789012"), nil, policySet{identity: id}, explicit(authz.IdentityBased, "")},
		{"A2", carlos, "s3:PutObject", on(bucket+"/report.txt", "123456789012"), nil, policySet{identity: id, resource: bucketPolicy}, allow()},
		{"A3", carlos, "s3:PutObject", on(bucket+"/report.txt", "123456789012"), nil, policySet{identity: id}, allow()},
		{"A4", carlos, "s3:PutObject", on(bucket+"/report.txt", "123456789012"), nil, policySet{resource: bucketPolicy}, allow()},
		{"A5", carlos, "s3:PutObject", on(bucket+"/catalog.txt", "123456789012"), nil, policySet{identity: id, resource: bucketPolicy}, explicit(authz.IdentityBased, "")},
	})
}

// B: explicit and implicit deny, iam-evaluation.md 9.3 (S-DENY).
func TestDecideExplicitAndImplicitDenyVectors(t *testing.T) {
	const (
		p     = `{"Version":"2012-10-17","Statement":[{"Sid":"AllowGetList","Effect":"Allow","Action":["iam:Get*","iam:List*"],"Resource":"*"},{"Sid":"DenyReports","Effect":"Deny","Action":"iam:*Report","Resource":"*"}]}`
		extra = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:GenerateCredentialReport","Resource":"*"}]}`
	)
	u := userOf("123456789012", "reader", "AIDAREADEREXAMPLE001")
	base := policySet{identity: []authz.Policy{inlinePolicy(t, "p", p)}}
	both := policySet{identity: []authz.Policy{inlinePolicy(t, "p", p), inlinePolicy(t, "extra", extra)}}
	star := anyIn("123456789012")
	runDecideVectors(t, "9.3", []decideVector{
		{"B1", u, "iam:GetUser", star, nil, base, allow()},
		{"B2", u, "iam:CreatePolicy", star, nil, base, implicit()},
		{"B3", u, "iam:GetOrganizationsAccessReport", star, nil, base, explicit(authz.IdentityBased, "")},
		{"B4", u, "iam:GenerateCredentialReport", star, nil, both, explicit(authz.IdentityBased, "")},
	})
}

// C, D: cross account, iam-evaluation.md 9.4 (S-XACCT, S-XRES).
func TestDecideCrossAccountVectors(t *testing.T) {
	const (
		cid = `{"Version":"2012-10-17","Statement":[{"Sid":"AllowS3ListRead","Effect":"Allow","Action":"s3:ListAllMyBuckets","Resource":"*"},` +
			`{"Sid":"AllowS3ProductionObjectActions","Effect":"Allow","Action":"s3:*Object*","Resource":"arn:aws:s3:::amzn-s3-demo-bucket-production/*"},` +
			`{"Sid":"DenyS3Logs","Effect":"Deny","Action":"s3:*","Resource":["arn:aws:s3:::*log*","arn:aws:s3:::*log*/*"]}]}`
		crp = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject*","s3:PutObject*","s3:ReplicateObject","s3:RestoreObject"],` +
			`"Principal":{"AWS":"arn:aws:iam::111111111111:role/Demo"},"Resource":"arn:aws:s3:::amzn-s3-demo-bucket-production/*"}]}`
		drp   = `{"Version":"2012-10-17","Statement":[{"Sid":"PrincipalAccess","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"},"Action":"s3:*","Resource":"arn:aws:s3:::BucketA/*"}]}`
		user2 = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:Get*","s3:List*"],"Resource":"arn:aws:s3:::BucketA/*"}]}`
		full  = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`
		prod  = "arn:aws:s3:::amzn-s3-demo-bucket-production"
	)
	demo := sessionOf("111111111111", "Demo", "AROADEMOEXAMPLE00001", "carlos")
	cIdentity := []authz.Policy{inlinePolicy(t, "demo", cid)}
	cBucket := authz.Generic{Document: docOf(t, crp)}
	dBucket := authz.Generic{Document: docOf(t, drp)}
	u1 := userOf("111122223333", "User1", "AIDAUSER1EXAMPLE0001")
	u2 := userOf("111122223333", "User2", "AIDAUSER2EXAMPLE0001")
	runDecideVectors(t, "9.4", []decideVector{
		{"C1", demo, "s3:PutObject", on(prod+"-logs/report.txt", "222222222222"), nil, policySet{identity: cIdentity}, explicit(authz.IdentityBased, "")},
		{"C2", demo, "s3:PutObject", on(prod+"/report.txt", "222222222222"), nil, policySet{identity: cIdentity, resource: cBucket}, allow()},
		{"C3", demo, "s3:DeleteObject", on(prod+"/report.txt", "222222222222"), nil, policySet{identity: cIdentity, resource: cBucket}, implicit()},
		{"C4", demo, "s3:PutBucketPolicy", on(prod, "222222222222"), nil, policySet{identity: cIdentity, resource: cBucket}, implicit()},
		{"D1", identity.Root{Account: account("111122223333")}, "s3:PutObject", on("arn:aws:s3:::BucketA/k", "444455556666"), nil, policySet{resource: dBucket}, allow()},
		{"D2", u2, "s3:GetObject", on("arn:aws:s3:::BucketA/k", "444455556666"), nil, policySet{identity: []authz.Policy{inlinePolicy(t, "user2", user2)}, resource: dBucket}, allow()},
		{"D3", u2, "s3:PutObject", on("arn:aws:s3:::BucketA/k", "444455556666"), nil, policySet{identity: []authz.Policy{inlinePolicy(t, "user2", user2)}, resource: dBucket}, implicit()},
		{"D4", u1, "s3:GetObject", on("arn:aws:s3:::BucketA/k", "444455556666"), nil, policySet{resource: dBucket}, implicit()},
		{"D5", u2, "s3:DeleteObject", on("arn:aws:s3:::BucketA/k", "444455556666"), nil, policySet{identity: []authz.Policy{inlinePolicy(t, "full", full)}, resource: dBucket}, allow()},
	})
}

// E: what a same-account resource-policy grant is worth by the principal
// form it names, iam-evaluation.md 9.5 and 2.3 (S-ENF, S-POLICIES
// #policies_bound and #policies_session, S-PRINCIPAL).
func TestDecideResourcePolicyPrincipalFormVectors(t *testing.T) {
	const (
		q        = "arn:aws:sqs:us-east-1:111122223333:q"
		stmt     = `,"Action":"sqs:SendMessage","Resource":"` + q + `"}`
		session  = "arn:aws:sts::111122223333:assumed-role/examplerole/examplerolesessionname"
		roleARN  = "arn:aws:iam::111122223333:role/examplerole"
		userARN  = "arn:aws:iam::111122223333:user/exampleuser"
		rpSess   = `{"Effect":"Allow","Principal":{"AWS":"` + session + `"}` + stmt
		rpRole   = `{"Effect":"Allow","Principal":{"AWS":"` + roleARN + `"}` + stmt
		rpUser   = `{"Effect":"Allow","Principal":{"AWS":"` + userARN + `"}` + stmt
		rpAcct   = `{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"}` + stmt
		rpStar   = `{"Effect":"Allow","Principal":"*"` + stmt
		rpStarPA = `{"Effect":"Allow","Principal":"*","Condition":{"ArnEquals":{"aws:PrincipalArn":"` + roleARN + `"}}` + stmt
		idAllow  = `{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"` + q + `"}`
		idDeny   = `{"Effect":"Deny","Action":"sqs:SendMessage","Resource":"*"}`
		narrow   = `{"Effect":"Allow","Action":"s3:ListAllMyBuckets","Resource":"*"}`
		bndDeny  = `[{"Effect":"Allow","Action":"*","Resource":"*"},{"Effect":"Deny","Action":"sqs:SendMessage","Resource":"*"}]`
	)
	sess := sessionOf("111122223333", "examplerole", "AROAEXAMPLEROLEID0001", "examplerolesessionname")
	usr := userOf("111122223333", "exampleuser", "AIDAEXAMPLEUSERID0001")
	queueRes := on(q, "111122223333")
	rp := func(text string) authz.ResourcePolicy { return authz.Generic{Document: docOf(t, text)} }
	bndNarrow := managedPolicy(t, "arn:aws:iam::111122223333:policy/narrow", narrow)
	bndDenyP := managedPolicy(t, "arn:aws:iam::111122223333:policy/deny-send", bndDeny)
	spNarrow := []authz.Policy{inlinePolicy(t, "session", narrow)}
	allowID := []authz.Policy{inlinePolicy(t, "allow", idAllow)}
	principalARN := contextKeys("aws:PrincipalArn", roleARN)

	var e16 outcome
	switch v := evalSwitch(t, "iam.wildcard-principal.role-session"); v {
	case "direct":
		e16 = allow()
	case "limited":
		e16 = implicit()
	default:
		t.Fatalf("iam.wildcard-principal.role-session = %q, a value this test does not know (iam-evaluation.md 12.3)", v)
	}

	runDecideVectors(t, "9.5", []decideVector{
		{"E1", sess, "sqs:SendMessage", queueRes, nil, policySet{resource: rp(rpSess), boundary: &bndNarrow, session: spNarrow}, allow()},
		{"E2", sess, "sqs:SendMessage", queueRes, nil, policySet{resource: rp(rpRole)}, allow()},
		{"E3", sess, "sqs:SendMessage", queueRes, nil, policySet{resource: rp(rpRole), boundary: &bndNarrow}, implicit()},
		{"E4", sess, "sqs:SendMessage", queueRes, nil, policySet{resource: rp(rpRole), session: spNarrow}, implicit()},
		{"E5", usr, "sqs:SendMessage", queueRes, nil, policySet{resource: rp(rpUser), boundary: &bndNarrow}, allow()},
		{"E6", usr, "sqs:SendMessage", queueRes, nil, policySet{resource: rp(rpAcct)}, implicit()},
		{"E7", usr, "sqs:SendMessage", queueRes, nil, policySet{resource: rp(rpAcct), identity: allowID}, allow()},
		{"E8", sess, "sqs:SendMessage", queueRes, nil, policySet{resource: rp(rpAcct), identity: allowID, boundary: &bndNarrow}, implicit()},
		{"E9", sess, "sqs:SendMessage", queueRes, principalARN, policySet{resource: rp(rpStarPA), boundary: &bndNarrow, session: spNarrow}, allow()},
		{"E10", sess, "sqs:SendMessage", queueRes, principalARN, policySet{resource: rp(rpStarPA), identity: []authz.Policy{inlinePolicy(t, "deny", idDeny)}}, explicit(authz.IdentityBased, "")},
		{"E11", sess, "sqs:SendMessage", queueRes, nil, policySet{resource: rp(rpSess), boundary: &bndDenyP}, explicit(authz.PermissionsBoundary, bndDenyP.ARN)},
		{"E12", sess, "sqs:SendMessage", queueRes, nil, policySet{identity: allowID}, allow()},
		{"E13", sess, "sqs:SendMessage", queueRes, nil, policySet{}, implicit()},
		{"E14", usr, "sqs:SendMessage", queueRes, nil, policySet{resource: rp(rpUser), boundary: &bndDenyP}, explicit(authz.PermissionsBoundary, bndDenyP.ARN)},
		{"E15", usr, "sqs:SendMessage", queueRes, nil, policySet{resource: rp(rpStar)}, allow()},
		{"E16", sess, "sqs:SendMessage", queueRes, nil, policySet{resource: rp(rpStar), boundary: &bndNarrow}, e16},
	})
}

// F: permissions boundaries, iam-evaluation.md 9.6 (S-BOUND; F.iamFull and
// F.s3ro are AWS managed policies, M-FULL).
func TestDecidePermissionsBoundaryVectors(t *testing.T) {
	const (
		shirleyB = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*","cloudwatch:*","ec2:*"],"Resource":"*"}]}`
		shirleyI = `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"iam:CreateUser","Resource":"*"}}`
		x        = `{"Version":"2012-10-17","Statement":[{"Sid":"ServiceBoundaries","Effect":"Allow","Action":["s3:*","cloudwatch:*","ec2:*","dynamodb:*"],"Resource":"*"},` +
			`{"Sid":"AllowIAMConsoleForCredentials","Effect":"Allow","Action":["iam:ListUsers","iam:GetAccountPasswordPolicy"],"Resource":"*"},` +
			`{"Sid":"AllowManageOwnPasswordAndAccessKeys","Effect":"Allow","Action":["iam:*AccessKey*","iam:ChangePassword","iam:GetUser","iam:*ServiceSpecificCredential*","iam:*SigningCertificate*"],"Resource":["arn:aws:iam::*:user/${aws:username}"]},` +
			`{"Sid":"DenyS3Logs","Effect":"Deny","Action":"s3:*","Resource":["arn:aws:s3:::logs","arn:aws:s3:::logs/*"]},` +
			`{"Sid":"DenyEC2Production","Effect":"Deny","Action":"ec2:*","Resource":"arn:aws:ec2:*:*:instance/i-1234567890abcdef0"}]}`
		zb = `{"Version":"2012-10-17","Statement":[{"Sid":"CreateOrChangeOnlyWithBoundary","Effect":"Allow","Action":["iam:AttachUserPolicy","iam:CreateUser","iam:DeleteUserPolicy","iam:DetachUserPolicy","iam:PutUserPermissionsBoundary","iam:PutUserPolicy"],"Resource":"*","Condition":{"StringEquals":{"iam:PermissionsBoundary":"arn:aws:iam::123456789012:policy/XCompanyBoundaries"}}},` +
			`{"Sid":"CloudWatchAndOtherIAMTasks","Effect":"Allow","Action":["cloudwatch:*","iam:CreateAccessKey","iam:CreateGroup","iam:CreateLoginProfile","iam:CreatePolicy","iam:DeleteGroup","iam:DeletePolicy","iam:DeletePolicyVersion","iam:DeleteUser","iam:GetAccountPasswordPolicy","iam:GetGroup","iam:GetLoginProfile","iam:GetPolicy","iam:GetPolicyVersion","iam:GetRolePolicy","iam:GetUser","iam:GetUserPolicy","iam:ListAccessKeys","iam:ListAttachedRolePolicies","iam:ListAttachedUserPolicies","iam:ListEntitiesForPolicy","iam:ListGroups","iam:ListGroupsForUser","iam:ListMFADevices","iam:ListPolicies","iam:ListPolicyVersions","iam:ListRolePolicies","iam:ListSSHPublicKeys","iam:ListServiceSpecificCredentials","iam:ListSigningCertificates","iam:ListUserPolicies","iam:ListUsers","iam:SetDefaultPolicyVersion","iam:SimulateCustomPolicy","iam:SimulatePrincipalPolicy","iam:UpdateGroup","iam:UpdateLoginProfile","iam:UpdateUser"],"NotResource":"arn:aws:iam::123456789012:user/Maria"},` +
			`{"Sid":"NoBoundaryPolicyEdit","Effect":"Deny","Action":["iam:CreatePolicyVersion","iam:DeletePolicy","iam:DeletePolicyVersion","iam:SetDefaultPolicyVersion"],"Resource":["arn:aws:iam::123456789012:policy/XCompanyBoundaries","arn:aws:iam::123456789012:policy/DelegatedUserBoundary"]},` +
			`{"Sid":"NoBoundaryUserDelete","Effect":"Deny","Action":"iam:DeleteUserPermissionsBoundary","Resource":"*"}]}`
		zi = `{"Version":"2012-10-17","Statement":[{"Sid":"IAM","Effect":"Allow","Action":"iam:*","Resource":"*"},` +
			`{"Sid":"CloudWatchLimited","Effect":"Allow","Action":["cloudwatch:GetDashboard","cloudwatch:GetMetricData","cloudwatch:ListDashboards","cloudwatch:GetMetricStatistics","cloudwatch:ListMetrics"],"Resource":"*"},` +
			`{"Sid":"S3BucketContents","Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::ZhangBucket"}]}`
		iamFull = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["iam:*","organizations:DescribeAccount","organizations:DescribeOrganization","organizations:DescribeOrganizationalUnit","organizations:DescribePolicy","organizations:ListChildren","organizations:ListParents","organizations:ListPoliciesForTarget","organizations:ListRoots","organizations:ListPolicies","organizations:ListTargetsForPolicy"],"Resource":"*"}]}`
		s3ro    = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:Get*","s3:List*","s3:Describe*","s3-object-lambda:Get*","s3-object-lambda:List*"],"Resource":"*"}]}`
		acct    = "123456789012"
		iamUser = "arn:aws:iam::123456789012:user/"
		xARN    = "arn:aws:iam::123456789012:policy/XCompanyBoundaries"
		zbARN   = "arn:aws:iam::123456789012:policy/DelegatedUserBoundary"
	)
	shirley := userOf(acct, "Shirley", "AIDASHIRLEYEXAMPLE01")
	zhang := userOf(acct, "Zhang", "AIDAZHANGEXAMPLE0001")
	nikhil := userOf(acct, "Nikhil", "AIDANIKHILEXAMPLE001")
	shirleyBoundary := managedPolicy(t, "arn:aws:iam::123456789012:policy/ShirleyBoundary", shirleyB)
	shirleySet := policySet{identity: []authz.Policy{inlinePolicy(t, "create-user", shirleyI)}, boundary: &shirleyBoundary}
	zhangBoundary := managedPolicy(t, zbARN, zb)
	zhangSet := policySet{identity: []authz.Policy{managedPolicy(t, "arn:aws:iam::123456789012:policy/DelegatedUserPermissions", zi)}, boundary: &zhangBoundary}
	xBoundary := managedPolicy(t, xARN, x)
	nikhilID := []authz.Policy{managedPolicy(t, "arn:aws:iam::aws:policy/IAMFullAccess", iamFull), managedPolicy(t, "arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess", s3ro)}
	nikhilSet := policySet{identity: nikhilID, boundary: &xBoundary}
	withRP := func(text string) policySet {
		s := nikhilSet
		s.resource = authz.Generic{Document: docOf(t, text)}
		return s
	}
	logsPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:user/Nikhil"},"Action":"s3:PutObject","Resource":"arn:aws:s3:::logs/*"}]}`
	smARN := "arn:aws:secretsmanager:us-east-1:123456789012:secret:nikhil-AbCdEf"
	secretPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:user/Nikhil"},"Action":"secretsmanager:GetSecretValue","Resource":"*"}]}`
	username := contextKeys("aws:username", "Nikhil")
	star := anyIn(acct)

	runDecideVectors(t, "9.6", []decideVector{
		{"F1", shirley, "iam:CreateUser", star, nil, shirleySet, implicit()},
		{"F2", shirley, "s3:ListAllMyBuckets", star, nil, shirleySet, implicit()},
		{"F3", zhang, "iam:CreateUser", on(iamUser+"Nikhil", acct), nil, zhangSet, implicit()},
		{"F4", zhang, "iam:CreateUser", on(iamUser+"Nikhil", acct), contextKeys("iam:PermissionsBoundary", xARN), zhangSet, allow()},
		{"F5", zhang, "s3:ListBucket", on("arn:aws:s3:::ZhangBucket", acct), nil, zhangSet, implicit()},
		{"F6", zhang, "cloudwatch:GetDashboard", star, nil, zhangSet, allow()},
		{"F7", zhang, "cloudwatch:PutDashboard", star, nil, zhangSet, implicit()},
		{"F8", zhang, "iam:DeleteUserPermissionsBoundary", on(iamUser+"Nikhil", acct), nil, zhangSet, explicit(authz.PermissionsBoundary, zbARN)},
		{"F9", zhang, "iam:SetDefaultPolicyVersion", on(xARN, acct), nil, zhangSet, explicit(authz.PermissionsBoundary, zbARN)},
		{"F10", zhang, "iam:CreateAccessKey", on(iamUser+"Maria", acct), nil, zhangSet, implicit()},
		{"F11", zhang, "iam:CreateAccessKey", on(iamUser+"Nikhil", acct), nil, zhangSet, allow()},
		{"F12", nikhil, "iam:ChangePassword", on(iamUser+"Nikhil", acct), username, nikhilSet, allow()},
		{"F13", nikhil, "iam:CreateUser", on(iamUser+"x", acct), username, nikhilSet, implicit()},
		{"F14", nikhil, "iam:PutUserPolicy", on(iamUser+"Nikhil", acct), username, nikhilSet, implicit()},
		{"F15", nikhil, "s3:GetObject", on("arn:aws:s3:::any-bucket/k", acct), username, nikhilSet, allow()},
		{"F16", nikhil, "s3:PutObject", on("arn:aws:s3:::logs/k", acct), username, withRP(logsPolicy), explicit(authz.PermissionsBoundary, xARN)},
		{"F17", nikhil, "secretsmanager:GetSecretValue", on(smARN, acct), username, withRP(secretPolicy), allow()},
		{"F18", nikhil, "iam:ChangePassword", on(iamUser+"Other", acct), username, nikhilSet, implicit()},
	})
}

// G: session policies, iam-evaluation.md 9.7 (S-ARPERM).
func TestDecideSessionPolicyVectors(t *testing.T) {
	const (
		role = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::productionapp"},` +
			`{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject"],"Resource":"arn:aws:s3:::productionapp/*"}]}`
		session = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::productionapp"},` +
			`{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject"],"Resource":"arn:aws:s3:::productionapp/*"}]}`
		bucket = `{"Version":"2012-10-17","Statement":{"Principal":{"AWS":"*"},"Effect":"Deny","Action":"s3:DeleteObject","Resource":"arn:aws:s3:::productionapp/*"}}`
		acct   = "111122223333"
	)
	s := sessionOf(acct, "ProductionApp", "AROAPRODUCTIONAPP001", "app")
	object := on("arn:aws:s3:::productionapp/k", acct)
	roleID := []authz.Policy{inlinePolicy(t, "role", role)}
	sp := []authz.Policy{inlinePolicy(t, "session", session)}
	runDecideVectors(t, "9.7", []decideVector{
		{"G1", s, "s3:DeleteObject", object, nil, policySet{identity: roleID}, allow()},
		{"G2", s, "s3:DeleteObject", object, nil, policySet{identity: roleID, session: sp}, implicit()},
		{"G3", s, "s3:PutObject", object, nil, policySet{identity: roleID, session: sp}, allow()},
		{"G4", s, "s3:DeleteObject", object, nil, policySet{identity: roleID, resource: authz.Generic{Document: docOf(t, bucket)}}, explicit(authz.ResourceBased, "")},
	})
}

// H: trust policies and AssumeRole, iam-evaluation.md 9.8 (STS-AR,
// S-STSCMP, S-TAGS, K-EVAL). H8: root cannot call AssumeRole (S-STSCMP),
// and no statement denies it, so the refusal is an implicit deny.
func TestDecideTrustPolicyVectors(t *testing.T) {
	const (
		roleARN = "arn:aws:iam::111122223333:role/R"
		trust   = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"%s"},"Action":%s%s}]}`
		assume  = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Resource":"` + roleARN + `"}]}`
	)
	trustOf := func(principal, action, condition string) authz.ResourcePolicy {
		text := strings.Replace(strings.Replace(strings.Replace(trust, "%s", principal, 1), "%s", action, 1), "%s", condition, 1)
		return authz.Trust{Document: docOf(t, text)}
	}
	tUser := trustOf("arn:aws:iam::111122223333:user/U", `"sts:AssumeRole"`, "")
	tAcct := trustOf("arn:aws:iam::111122223333:root", `"sts:AssumeRole"`, "")
	tExt := trustOf("arn:aws:iam::444455556666:root", `"sts:AssumeRole"`, "")
	tExtUser := trustOf("arn:aws:iam::444455556666:user/X", `"sts:AssumeRole"`, "")
	tLambda := authz.Trust{Document: docOf(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)}
	tMFA := trustOf("arn:aws:iam::111122223333:root", `"sts:AssumeRole"`, `,"Condition":{"Bool":{"aws:MultiFactorAuthPresent":true}}`)
	tTags := trustOf("arn:aws:iam::111122223333:user/U", `["sts:AssumeRole","sts:TagSession"]`, "")
	tBob := authz.Trust{Document: docOf(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:user/bob"},"Action":"sts:AssumeRole"}]}`)}

	u := userOf("111122223333", "U", "AIDAUEXAMPLE00000001")
	x := userOf("444455556666", "X", "AIDAXEXAMPLE00000001")
	bob := userOf("111122223333", "bob", "AIDABOBEXAMPLE000001")
	role := on(roleARN, "111122223333")
	assumeOnU := []authz.Policy{inlinePolicy(t, "assume", assume)}
	runDecideVectors(t, "9.8", []decideVector{
		{"H1", u, "sts:AssumeRole", role, nil, policySet{resource: tUser}, allow()},
		{"H2", u, "sts:AssumeRole", role, nil, policySet{resource: tAcct, identity: assumeOnU}, allow()},
		{"H3", u, "sts:AssumeRole", role, nil, policySet{resource: tAcct}, implicit()},
		{"H4", x, "sts:AssumeRole", role, nil, policySet{resource: tExt, identity: assumeOnU}, allow()},
		{"H5", x, "sts:AssumeRole", role, nil, policySet{resource: tExt}, implicit()},
		{"H6", x, "sts:AssumeRole", role, nil, policySet{resource: tExtUser}, implicit()},
		{"H7", u, "sts:AssumeRole", role, nil, policySet{resource: tLambda, identity: assumeOnU}, implicit()},
		{"H8", identity.Root{Account: account("111122223333")}, "sts:AssumeRole", role, nil, policySet{resource: tAcct}, implicit()},
		{"H9", u, "sts:AssumeRole", role, nil, policySet{resource: tMFA, identity: assumeOnU}, implicit()},
		{"H10", u, "sts:TagSession", role, nil, policySet{resource: tUser}, implicit()},
		{"H11", u, "sts:TagSession", role, nil, policySet{resource: tTags}, allow()},
		{"H12", bob, "sts:AssumeRole", on("arn:aws:iam::111122223333:role/Engineering", "111122223333"), nil, policySet{resource: tBob}, allow()},
	})
}

// I: KMS key policies, iam-evaluation.md 9.9 (K-EVAL, K-DET, K-POL, K-OVR,
// K-XACCT).
func TestDecideKeyPolicyVectors(t *testing.T) {
	const (
		key     = "arn:aws:kms:us-west-2:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"
		xkey    = "arn:aws:kms:us-west-2:444455556666:key/1234abcd-12ab-34cd-56ef-1234567890ab"
		kDef    = `{"Version":"2012-10-17","Id":"key-test-1","Statement":[{"Sid":"DelegateToIAMPolicies","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"},"Action":"kms:*","Resource":"*"}]}`
		kNoAcct = `[{"Sid":"Allow access for Key Administrators","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:role/KMSKeyAdmins"},"Action":["kms:Describe*","kms:Put*","kms:Create*","kms:Update*","kms:Enable*","kms:Revoke*","kms:List*","kms:Disable*","kms:Get*","kms:Delete*","kms:ScheduleKeyDeletion","kms:CancelKeyDeletion"],"Resource":"*"},` +
			`{"Sid":"Allow use of the key","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:role/EncryptionApp"},"Action":["kms:DescribeKey","kms:GenerateDataKey*","kms:Encrypt","kms:ReEncrypt*","kms:Decrypt"],"Resource":"*"},` +
			`{"Sid":"Allow attachment of persistent resources","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:role/EncryptionApp"},"Action":["kms:ListGrants","kms:CreateGrant","kms:RevokeGrant"],"Resource":"*","Condition":{"Bool":{"kms:GrantIsForAWSResource":true}}}]`
		kAcct2 = `{"Id":"key-policy-acct-2","Version":"2012-10-17","Statement":[{"Sid":"PermissionUseIAMpolicies","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::444455556666:root"},"Action":"kms:*","Resource":"*"},` +
			`{"Sid":"AllowAccount1UseThisKMSKeys","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"},"Action":["kms:Encrypt","kms:Decrypt","kms:ReEncryptFrom","kms:ReEncryptTo","kms:GenerateDataKey","kms:GenerateDataKeyWithoutPlaintext","kms:DescribeKey"],"Resource":"*"}]}`
		kEngIAM = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["kms:Encrypt","kms:Decrypt","kms:ReEncryptFrom","kms:ReEncryptTo","kms:GenerateDataKey","kms:GenerateDataKeyWithoutPlaintext","kms:DescribeKey"],"Resource":["` + xkey + `"]}]}`
		kStar   = `[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"kms:Decrypt","Resource":"*"}]`
		aliceKP = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:user/Alice"},"Action":"kms:Encrypt","Resource":"*"}]}`
	)
	kp := func(text string) authz.ResourcePolicy { return authz.KeyPolicy{Document: docOf(t, text)} }
	iamOn := func(effect, action, resource string) []authz.Policy {
		return []authz.Policy{inlinePolicy(t, "kms", `{"Version":"2012-10-17","Statement":[{"Effect":"`+effect+`","Action":"`+action+`","Resource":"`+resource+`"}]}`)}
	}
	alice := userOf("111122223333", "Alice", "AIDAALICEEXAMPLE0001")
	app := sessionOf("111122223333", "EncryptionApp", "AROAENCRYPTIONAPP001", "app")
	eng := sessionOf("111122223333", "Engineering", "AROAENGINEERING00001", "eng")
	root := identity.Root{Account: account("111122223333")}
	ext := userOf("444455556666", "Bob", "AIDAEXTBOBEXAMPLE001")
	k := on(key, "111122223333")
	runDecideVectors(t, "9.9", []decideVector{
		{"I1", alice, "kms:Encrypt", k, nil, policySet{resource: kp(kDef)}, implicit()},
		{"I2", alice, "kms:Encrypt", k, nil, policySet{resource: kp(kDef), identity: iamOn("Allow", "kms:Encrypt", key)}, allow()},
		{"I3", alice, "kms:Encrypt", k, nil, policySet{resource: kp(kNoAcct), identity: iamOn("Allow", "kms:Encrypt", key)}, implicit()},
		{"I4", alice, "kms:Encrypt", k, nil, policySet{resource: kp(aliceKP), identity: iamOn("Deny", "kms:Encrypt", key)}, explicit(authz.IdentityBased, "")},
		{"I5", app, "kms:Decrypt", k, nil, policySet{resource: kp(kNoAcct)}, allow()},
		{"I6", root, "kms:Decrypt", k, nil, policySet{resource: kp(kNoAcct)}, implicit()},
		{"I7", root, "kms:Decrypt", k, nil, policySet{resource: kp(kDef)}, allow()},
		{"I8", eng, "kms:Decrypt", on(xkey, "444455556666"), nil, policySet{resource: kp(kAcct2), identity: []authz.Policy{inlinePolicy(t, "eng", kEngIAM)}}, allow()},
		{"I9", eng, "kms:Decrypt", on(xkey, "444455556666"), nil, policySet{resource: kp(kAcct2)}, implicit()},
		{"I11", ext, "kms:Decrypt", k, nil, policySet{resource: kp(kStar), identity: iamOn("Allow", "kms:Decrypt", key)}, allow()},
		{"I12", ext, "kms:Decrypt", k, nil, policySet{resource: kp(kStar)}, implicit()},
		{"I13", app, "kms:CreateGrant", k, contextKeys("kms:GrantIsForAWSResource", "true"), policySet{resource: kp(kNoAcct)}, allow()},
		{"I14", app, "kms:CreateGrant", k, contextKeys("kms:GrantIsForAWSResource", "false"), policySet{resource: kp(kNoAcct)}, implicit()},
	})
}

// N5 and O: the request context, iam-evaluation.md 9.13 (S-SVEX, S-CTX
// #reqcontext_example).
func TestDecideRequestContextVectors(t *testing.T) {
	const (
		ec2 = `[{"Effect":"Allow","Action":"ec2:CreateVolume","Resource":"*"},` +
			`{"Effect":"Allow","Action":"ec2:CreateTags","Resource":"arn:aws:ec2:us-east-1:123456789012:volume/*","Condition":{"StringLike":{"aws:RequestTag/project":"*"}}},` +
			`{"Effect":"Allow","Action":"ec2:CreateTags","Resource":"arn:aws:ec2:us-east-1:123456789012:*/*","Condition":{"StringNotEquals":{"aws:ResourceTag/environment":"production"}}}]`
		o = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:CreateBucket","Resource":"arn:aws:s3:::amzn-s3-demo-bucket1","Condition":{"StringEquals":{"aws:PrincipalTag/dept":"123"}}}]}`
	)
	builder := userOf("123456789012", "builder", "AIDABUILDEREXAMPLE01")
	creator := userOf("111122223333", "creator", "AIDACREATOREXAMPLE01")
	bucket := on("arn:aws:s3:::amzn-s3-demo-bucket1", "111122223333")
	oSet := policySet{identity: []authz.Policy{inlinePolicy(t, "o", o)}}
	runDecideVectors(t, "9.13", []decideVector{
		{"N5", builder, "ec2:CreateTags", on("arn:aws:ec2:us-east-1:123456789012:volume/vol-1", "123456789012"), contextKeys("aws:RequestTag/project", "Feature3"), policySet{identity: []authz.Policy{inlinePolicy(t, "ec2", ec2)}}, allow()},
		{"O1", creator, "s3:CreateBucket", bucket, contextKeys("aws:PrincipalTag/dept", "123"), oSet, allow()},
		{"O2", creator, "s3:DeleteBucket", bucket, contextKeys("aws:PrincipalTag/dept", "123"), oSet, implicit()},
		{"O3", creator, "s3:CreateBucket", bucket, contextKeys("aws:PrincipalTag/dept", "321"), oSet, implicit()},
		{"O4", creator, "s3:CreateBucket", bucket, nil, oSet, implicit()},
	})
}

// S13 to S26: NotPrincipal and wildcard principals in resource policies,
// iam-evaluation.md 9.17 (S-NOTPRIN, S-BOUND, S-PRINCIPAL, S-OPS, S-KEYS).
// Whether "*" names a service principal is UNVERIFIED (IAM-EVAL-33), so
// the rows whose denial needs it follow iam.wildcard-principal.service.
func TestDecideNotPrincipalAndWildcardVectors(t *testing.T) {
	const (
		q      = "arn:aws:sqs:us-east-1:111122223333:q"
		object = "arn:aws:s3:::b/k"
		s13    = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","NotPrincipal":{"AWS":["arn:aws:iam::111122223333:user/exampleuser","arn:aws:iam::111122223333:root"]},"Action":"sqs:*","Resource":"*"}]}`
		s14    = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:*","Resource":"*","Condition":{"ArnNotEquals":{"aws:PrincipalArn":"arn:aws:iam::444455556666:user/user-name"}}}]}`
		s17    = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:*","Resource":"*","Condition":{"StringNotEqualsIfExists":{"aws:PrincipalServiceName":"codebuild.amazonaws.com"}}}]}`
		s20    = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:PutObject","Resource":"*","Condition":{"StringNotEqualsIfExists":{"aws:SourceVpc":"vpc-111bbb22"},"BoolIfExists":{"aws:PrincipalIsAWSService":"false"}}}]}`
		s23    = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:*","Resource":"*","Condition":{"StringNotEqualsIfExists":{"aws:SourceAccount":"111122223333"},"Null":{"aws:SourceAccount":"false"},"Bool":{"aws:PrincipalIsAWSService":"true"}}}]}`
		allAll = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`
	)
	var serviceDenied outcome
	switch v := evalSwitch(t, "iam.wildcard-principal.service"); v {
	case "matches":
		serviceDenied = explicit(authz.ResourceBased, "")
	case "excludes":
		serviceDenied = notDenied()
	default:
		t.Fatalf("iam.wildcard-principal.service = %q, a value this test does not know (iam-evaluation.md 12.3)", v)
	}
	rp := func(text string) policySet {
		return policySet{resource: authz.Generic{Document: docOf(t, text)}, identity: []authz.Policy{inlinePolicy(t, "all", allAll)}}
	}
	bounded := rp(s13)
	boundary := managedPolicy(t, "arn:aws:iam::111122223333:policy/all", allAll)
	bounded.boundary = &boundary
	exUser := userOf("111122223333", "exampleuser", "AIDAEXAMPLEUSERID0001")
	named := userOf("444455556666", "user-name", "AIDAUSERNAMEEXAMPLE1")
	other := userOf("444455556666", "other", "AIDAOTHEREXAMPLE0001")
	bucketObj := on(object, "444455556666")
	svc := func(name string) identity.ServicePrincipal { return identity.ServicePrincipal{Name: name} }
	runDecideVectors(t, "9.17", []decideVector{
		{"S13", exUser, "sqs:SendMessage", on(q, "111122223333"), nil, bounded, explicit(authz.ResourceBased, "")},
		{"S14", named, "s3:PutObject", bucketObj, contextKeys("aws:PrincipalArn", "arn:aws:iam::444455556666:user/user-name"), rp(s14), notDenied()},
		{"S15", other, "s3:PutObject", bucketObj, contextKeys("aws:PrincipalArn", "arn:aws:iam::444455556666:user/other"), rp(s14), explicit(authz.ResourceBased, "")},
		{"S16", identity.Anonymous{}, "s3:PutObject", bucketObj, nil, rp(s14), explicit(authz.ResourceBased, "")},
		{"S17", svc("codebuild.amazonaws.com"), "s3:PutObject", bucketObj, contextKeys("aws:PrincipalServiceName", "codebuild.amazonaws.com"), rp(s17), notDenied()},
		{"S18", svc("sns.amazonaws.com"), "s3:PutObject", bucketObj, contextKeys("aws:PrincipalServiceName", "sns.amazonaws.com"), rp(s17), serviceDenied},
		{"S19", other, "s3:PutObject", bucketObj, nil, rp(s17), explicit(authz.ResourceBased, "")},
		{"S20", other, "s3:PutObject", bucketObj, contextKeys("aws:PrincipalIsAWSService", "false"), rp(s20), explicit(authz.ResourceBased, "")},
		{"S21", other, "s3:PutObject", bucketObj, contextKeys("aws:PrincipalIsAWSService", "false", "aws:SourceVpc", "vpc-111bbb22"), rp(s20), notDenied()},
		{"S22", svc("cloudtrail.amazonaws.com"), "s3:PutObject", bucketObj, contextKeys("aws:PrincipalIsAWSService", "true"), rp(s20), notDenied()},
		{"S23", svc("s3.amazonaws.com"), "s3:PutObject", bucketObj, contextKeys("aws:PrincipalIsAWSService", "true", "aws:SourceAccount", "111122223333"), rp(s23), notDenied()},
		{"S24", svc("s3.amazonaws.com"), "s3:PutObject", bucketObj, contextKeys("aws:PrincipalIsAWSService", "true", "aws:SourceAccount", "444455556666"), rp(s23), serviceDenied},
		{"S25", other, "s3:PutObject", bucketObj, contextKeys("aws:PrincipalIsAWSService", "false"), rp(s23), notDenied()},
		{"S26", svc("s3.amazonaws.com"), "s3:PutObject", bucketObj, contextKeys("aws:PrincipalIsAWSService", "true"), rp(s23), notDenied()},
	})
}

// T: service principals and confused-deputy conditions, iam-evaluation.md
// 9.18 (S-DEPUTY).
func TestDecideServicePrincipalVectors(t *testing.T) {
	const (
		ct = `[{"Sid":"CloudTrailAclCheck","Effect":"Allow","Principal":{"Service":"cloudtrail.amazonaws.com"},"Action":"s3:GetBucketAcl","Resource":"arn:aws:s3:::amzn-s3-demo-bucket1","Condition":{"StringEquals":{"aws:SourceAccount":"111122223333"}}}]`
		as = `[{"Effect":"Allow","Principal":{"Service":["appstream.amazonaws.com"]},"Action":"s3:GetObject","Resource":"arn:aws:s3:::amzn-s3-demo-bucket2/examplefile.psh","Condition":{"ArnEquals":{"aws:SourceArn":"arn:aws:appstream:us-east-1:111122223333:fleet/ExampleFleetName"}}}]`
	)
	trail := identity.ServicePrincipal{Name: "cloudtrail.amazonaws.com"}
	appstream := identity.ServicePrincipal{Name: "appstream.amazonaws.com"}
	bucket1 := on("arn:aws:s3:::amzn-s3-demo-bucket1", "111122223333")
	file := on("arn:aws:s3:::amzn-s3-demo-bucket2/examplefile.psh", "111122223333")
	ctSet := policySet{resource: authz.Generic{Document: docOf(t, ct)}}
	asSet := policySet{resource: authz.Generic{Document: docOf(t, as)}}
	runDecideVectors(t, "9.18", []decideVector{
		{"T1", trail, "s3:GetBucketAcl", bucket1, contextKeys("aws:SourceAccount", "111122223333"), ctSet, allow()},
		{"T2", trail, "s3:GetBucketAcl", bucket1, contextKeys("aws:SourceAccount", "444455556666"), ctSet, implicit()},
		{"T3", appstream, "s3:GetObject", file, contextKeys("aws:SourceArn", "arn:aws:appstream:us-east-1:111122223333:fleet/ExampleFleetName"), asSet, allow()},
		{"T4", appstream, "s3:GetObject", file, contextKeys("aws:SourceArn", "arn:aws:appstream:us-east-1:111122223333:fleet/OtherFleet"), asSet, implicit()},
		{"T5", userOf("111122223333", "u", "AIDAUEXAMPLE00000002"), "s3:GetBucketAcl", bucket1, nil, ctSet, implicit()},
	})
}

// U: the third-party-recorded simulator outcomes, iam-evaluation.md 9.19
// (localstack@8b9a79f test_simulate_principle_policy, validated
// 2025-04-21).
func TestDecideRecordedSimulatorVectors(t *testing.T) {
	const policy = `{"Version":"2012-10-17","Statement":{"Sid":"","Effect":"Allow","Action":"s3:PutObject","Resource":"*"}}`
	u := userOf("000000000000", "simulated", "AIDASIMULATEDEXAMPL1")
	set := policySet{identity: []authz.Policy{inlinePolicy(t, "simulated", policy)}}
	bucket := on("arn:aws:s3:::simulated-bucket", "000000000000")
	runDecideVectors(t, "9.19", []decideVector{
		{"U1", u, "s3:PutObject", bucket, nil, set, allow()},
		{"U2", u, "s3:GetObjectVersion", bucket, nil, set, implicit()},
	})
}
