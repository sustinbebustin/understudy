package authz_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// Decide beyond the worked examples: the root user (iam-evaluation.md 2.6),
// key policies that let IAM policies grant (4.3, 4.4), the policy type an
// implicit deny names, the role trust policy's explicit deny, and the
// unsupplied keys Decide gathers for the journal
// (iam-enforcement-surfaces.md 12.2).

const allowAll = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`

func decide(t *testing.T, p identity.Principal, action string, r authz.Resource, ctx *policydoc.Context, s policySet) authz.Outcome {
	t.Helper()
	if ctx == nil {
		ctx = contextKeys()
	}
	return authz.Decide(authz.Request{Principal: p, Action: actionOf(action), Resource: r, Context: ctx}, s.policies())
}

// "By default, all requests are implicitly denied with the exception of
// the AWS account root user, which has full access" (iam-evaluation.md 2.6,
// S-ENF): root needs no policy in its own account. An explicit deny still
// binds it (2.6), and SQS has no documented API bypass for a queue policy
// that denies everyone, so none is invented (12.5 item 2, IAM-EVAL-23).
func TestDecideRootInItsOwnAccount(t *testing.T) {
	root := identity.Root{Account: account("111122223333")}
	queueARN := "arn:aws:sqs:us-east-1:111122223333:locked"
	denyAll := authz.Generic{Document: docOf(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"sqs:*","Resource":"*"}]}`)}

	outcome{decision: authz.Allow{}}.check(t, "root, no policy", decide(t, root, "sqs:SendMessage", on(queueARN, "111122223333"), nil, policySet{}).Decision)
	outcome{decision: authz.Allow{}}.check(t, "root, a request on no resource", decide(t, root, "sqs:ListQueues", anyIn("111122223333"), nil, policySet{}).Decision)
	explicit(authz.ResourceBased, "").check(t, "root, a queue policy denying everyone", decide(t, root, "sqs:SetQueueAttributes", on(queueARN, "111122223333"), nil, policySet{resource: denyAll}).Decision)
	implicit().check(t, "root of another account, no resource policy", decide(t, identity.Root{Account: account("444455556666")}, "sqs:SendMessage", on(queueARN, "111122223333"), nil, policySet{}).Decision)
}

// The bucket owner's root may call GetBucketPolicy, PutBucketPolicy and
// DeleteBucketPolicy "even if their bucket policy explicitly denies the
// root principal's access" (iam-evaluation.md 2.6, S3-PBP); for Delete the
// S3 troubleshooting page says so too (iam-enforcement-surfaces.md 6.3).
// Get and Put are inferred there, so the exempt actions are read from
// switch s3.root.bucket-policy-lockout, whose value names them. Any other
// action, and another account's root, stays denied.
func TestDecideRootKeepsBucketPolicyManagement(t *testing.T) {
	value := evalSwitch(t, "s3.root.bucket-policy-lockout")
	exempt := map[string]bool{}
	for verb := range strings.SplitSeq(value, "-") {
		switch verb {
		case "get", "put", "delete":
			exempt[strings.ToUpper(verb[:1])+verb[1:]+"BucketPolicy"] = true
		default:
			t.Fatalf("s3.root.bucket-policy-lockout = %q, a value this test does not know (iam-enforcement-surfaces.md 12.8)", value)
		}
	}
	if !exempt["DeleteBucketPolicy"] {
		t.Errorf("s3.root.bucket-policy-lockout = %q leaves out DeleteBucketPolicy, which AWS documents (iam-enforcement-surfaces.md 6.3)", value)
	}
	const bucket = "arn:aws:s3:::locked"
	denyAll := policySet{resource: authz.Generic{Document: docOf(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:*","Resource":["`+bucket+`","`+bucket+`/*"]}]}`)}}
	owner := identity.Root{Account: account("111122223333")}
	for _, name := range []string{"GetBucketPolicy", "PutBucketPolicy", "DeleteBucketPolicy"} {
		want := explicit(authz.ResourceBased, "")
		if exempt[name] {
			want = allow()
		}
		want.check(t, "owner's root "+name, decide(t, owner, "s3:"+name, on(bucket, "111122223333"), nil, denyAll).Decision)
	}
	explicit(authz.ResourceBased, "").check(t, "owner's root s3:GetObject", decide(t, owner, "s3:GetObject", on(bucket+"/k", "111122223333"), nil, denyAll).Decision)
	if exempt["DeleteBucketPolicy"] {
		other := identity.Root{Account: account("444455556666")}
		notAllowed := decide(t, other, "s3:DeleteBucketPolicy", on(bucket, "111122223333"), nil, denyAll).Decision
		if _, ok := notAllowed.(authz.Allow); ok {
			t.Error("another account's root deletes a bucket policy that denies it, want the deny (S3-PBP: the bucket owner's account)")
		}
	}
}

// A key policy statement naming "*" lets any principal use the key
// "whenever they have corresponding permissions in their own account"
// (iam-evaluation.md 4.4, K-OVR #key-policy-elements): in one account too,
// it delegates to IAM policies and grants nothing alone.
func TestDecideKeyPolicyWildcardDelegatesToIAMPolicies(t *testing.T) {
	const key = "arn:aws:kms:us-west-2:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	star := authz.KeyPolicy{Document: docOf(t, `[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"kms:Decrypt","Resource":"*"}]`)}
	alice := userOf("111122223333", "Alice", "AIDAALICEEXAMPLE0001")
	implicit().check(t, "same account, no IAM policy", decide(t, alice, "kms:Decrypt", on(key, "111122223333"), nil, policySet{resource: star}).Decision)
	iam := []authz.Policy{inlinePolicy(t, "decrypt", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kms:Decrypt","Resource":"`+key+`"}]}`)}
	allow().check(t, "same account, an IAM policy", decide(t, alice, "kms:Decrypt", on(key, "111122223333"), nil, policySet{resource: star, identity: iam}).Decision)
}

// Whether a permissions boundary limits a key policy grant to a role ARN
// is UNVERIFIED for KMS (iam-evaluation.md 4.3, IAM-EVAL-27): switch
// kms.key-policy.role-boundary decides.
func TestDecideKeyPolicyRoleGrantFollowsItsSwitch(t *testing.T) {
	var want outcome
	switch v := evalSwitch(t, "kms.key-policy.role-boundary"); v {
	case "limited":
		want = implicit()
	case "not-limited":
		want = allow()
	default:
		t.Fatalf("kms.key-policy.role-boundary = %q, a value this test does not know (iam-evaluation.md 12.3)", v)
	}
	const key = "arn:aws:kms:us-west-2:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	kp := authz.KeyPolicy{Document: docOf(t, `[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:role/EncryptionApp"},"Action":"kms:Decrypt","Resource":"*"}]`)}
	narrow := managedPolicy(t, "arn:aws:iam::111122223333:policy/narrow", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListAllMyBuckets","Resource":"*"}]}`)
	app := sessionOf("111122223333", "EncryptionApp", "AROAENCRYPTIONAPP001", "app")
	want.check(t, "key policy naming the role, session with a narrow boundary", decide(t, app, "kms:Decrypt", on(key, "111122223333"), nil, policySet{resource: kp, boundary: &narrow}).Decision)
}

// An implicit deny names the policy type whose step of iam-evaluation.md
// 2.2 failed, as AWS's own examples do (2.11, iam-enforcement-surfaces.md
// 4.1): a trust policy that does not allow ("because no role trust policy
// allows the sts:AssumeRole action"), no identity policy ("because no
// identity-based policy allows"), a boundary that does not allow ("because
// no permissions boundary allows"), a session policy that does not allow
// ("because no session policy allows"), and a resource policy that alone
// could allow a service principal ("because no resource-based policy
// allows").
//
// Which one reason a denial names when several apply is inferred
// (iam-evaluation.md 12.4), the choice of switch iam.denial.grammar.
func TestDecideImplicitDenyNamesTheStepThatFailed(t *testing.T) {
	if v := evalSwitch(t, "iam.denial.grammar"); !strings.Contains(v, "one reason") {
		t.Fatalf("iam.denial.grammar = %q, a value this test does not know (iam-enforcement-surfaces.md 12.8)", v)
	}
	const q = "arn:aws:sqs:us-east-1:111122223333:q"
	u := userOf("111122223333", "U", "AIDAUEXAMPLE00000001")
	s := sessionOf("111122223333", "app", "AROAAPPEXAMPLE000001", "s")
	allowSend := []authz.Policy{inlinePolicy(t, "send", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}`)}
	narrow := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListAllMyBuckets","Resource":"*"}]}`
	boundary := managedPolicy(t, "arn:aws:iam::111122223333:policy/narrow", narrow)
	lambdaTrust := authz.Trust{Document: docOf(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)}
	cases := []struct {
		name string
		got  authz.Outcome
		want authz.PolicyType
	}{
		{"trust", decide(t, u, "sts:AssumeRole", on("arn:aws:iam::111122223333:role/R", "111122223333"), nil, policySet{resource: lambdaTrust, identity: allowSend}), authz.RoleTrust},
		{"identity", decide(t, s, "sqs:SendMessage", on(q, "111122223333"), nil, policySet{}), authz.IdentityBased},
		{"boundary", decide(t, u, "sqs:SendMessage", on(q, "111122223333"), nil, policySet{identity: allowSend, boundary: &boundary}), authz.PermissionsBoundary},
		{"session", decide(t, s, "sqs:SendMessage", on(q, "111122223333"), nil, policySet{identity: allowSend, session: []authz.Policy{inlinePolicy(t, "session", narrow)}}), authz.SessionPolicy},
		{"service", decide(t, identity.ServicePrincipal{Name: "sns.amazonaws.com"}, "sqs:SendMessage", on(q, "111122223333"), nil, policySet{}), authz.ResourceBased},
	}
	for _, c := range cases {
		if got, ok := c.got.Decision.(authz.ImplicitDeny); !ok || got.PolicyType != c.want {
			t.Errorf("%s: Decide = %#v, want ImplicitDeny{%s}", c.name, c.got.Decision, c.want)
		}
	}
}

// A Deny in a role's trust policy is "an explicit deny in the role trust
// policy" (iam-evaluation.md 2.11, iam-enforcement-surfaces.md 4.1).
func TestDecideTrustPolicyDenyIsRoleTrust(t *testing.T) {
	trust := authz.Trust{Document: docOf(t, `{"Version":"2012-10-17","Statement":[`+
		`{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"},"Action":"sts:AssumeRole"},`+
		`{"Effect":"Deny","Principal":{"AWS":"arn:aws:iam::111122223333:user/U"},"Action":"sts:AssumeRole"}]}`)}
	u := userOf("111122223333", "U", "AIDAUEXAMPLE00000001")
	got := decide(t, u, "sts:AssumeRole", on("arn:aws:iam::111122223333:role/R", "111122223333"), nil,
		policySet{resource: trust, identity: []authz.Policy{inlinePolicy(t, "all", allowAll)}})
	explicit(authz.RoleTrust, "").check(t, "trust policy deny", got.Decision)
}

// The policy types as the denial grammar writes them (iam-evaluation.md
// 2.11, recorded spellings; iam-enforcement-surfaces.md 4.1).
func TestPolicyTypeStrings(t *testing.T) {
	want := map[authz.PolicyType]string{
		authz.IdentityBased:       "identity-based policy",
		authz.ResourceBased:       "resource-based policy",
		authz.PermissionsBoundary: "permissions boundary",
		authz.SessionPolicy:       "session policy",
		authz.RoleTrust:           "role trust policy",
	}
	for pt, text := range want {
		if got := pt.String(); got != text {
			t.Errorf("PolicyType(%d).String() = %q, want %q", pt, got, text)
		}
	}
}

// Every key a policy tested that understudy cannot supply reaches the
// journal with the policy that tested it: the type, the managed policy's
// ARN or the inline policy's name, "" for a resource policy, and the
// statement (iam-enforcement-surfaces.md 12.2; the spec's settled row 2).
// The key decides as absent.
func TestDecideGathersUnsuppliedKeys(t *testing.T) {
	const q = "arn:aws:sqs:us-east-1:111122223333:q"
	managed := managedPolicy(t, "arn:aws:iam::111122223333:policy/from-office",
		`{"Version":"2012-10-17","Statement":[{"Sid":"Office","Effect":"Allow","Action":"sqs:*","Resource":"*","Condition":{"IpAddress":{"aws:SourceIp":"203.0.113.0/24"}}}]}`)
	inline := inlinePolicy(t, "vpc-only",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"sqs:*","Resource":"*","Condition":{"StringEquals":{"aws:SourceIp":"198.51.100.1"}}}]}`)
	rp := authz.Generic{Document: docOf(t, `{"Version":"2012-10-17","Statement":[{"Sid":"Net","Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"*","Condition":{"IpAddress":{"aws:SourceIp":"203.0.113.0/24"}}}]}`)}
	ctx := policydoc.NewContext("sqs")
	ctx.Unsupplied("aws:SourceIp")
	u := userOf("111122223333", "U", "AIDAUEXAMPLE00000001")
	got := decide(t, u, "sqs:SendMessage", on(q, "111122223333"), ctx, policySet{identity: []authz.Policy{managed, inline}, resource: rp})

	implicit().check(t, "every grant rests on the unsupplied key", got.Decision)
	want := []authz.Unsupplied{
		{Key: "aws:SourceIp", PolicyType: authz.IdentityBased, Policy: managed.ARN, Statement: policydoc.StatementRef{Index: 0, Sid: "Office"}},
		{Key: "aws:SourceIp", PolicyType: authz.IdentityBased, Policy: "vpc-only", Statement: policydoc.StatementRef{Index: 0}},
		{Key: "aws:SourceIp", PolicyType: authz.ResourceBased, Policy: "", Statement: policydoc.StatementRef{Index: 0, Sid: "Net"}},
	}
	if !sameUnsuppliedSet(got.Unsupplied, want) {
		t.Errorf("Unsupplied = %+v, want %+v", got.Unsupplied, want)
	}
}

// sameUnsuppliedSet compares as multisets: the order is not part of what
// Decide promises.
func sameUnsuppliedSet(got, want []authz.Unsupplied) bool {
	if len(got) != len(want) {
		return false
	}
	used := make([]bool, len(want))
	for _, g := range got {
		found := false
		for i, w := range want {
			if !used[i] && reflect.DeepEqual(g, w) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
