package authz_test

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/identity"
)

// Denial messages. The phrases are AWS's documented grammar
// (iam-enforcement-surfaces.md 4.1, aws-doc): "User: <arn> is not
// authorized to perform: <action>", then "because no <type> allows the
// <action> action" or "with an explicit deny in <a|an|the> <type>[: <policy
// ARN>]". Where "on resource: <arn>" goes is inconsistent across AWS's
// examples (4.1, IAM-ENF-15), so it is read from switch iam.denial.grammar.
// S3's variant (4.2, aws-doc), the KMS ciphertext text (4.3, aws-doc) and
// the recorded STS text (4.3, aws-recorded) are compared byte for byte.

// onResourceOnExplicitOnly reads iam.denial.grammar: whether "on resource:"
// is written on explicit denials only.
func onResourceOnExplicitOnly(t *testing.T) bool {
	t.Helper()
	v := evalSwitch(t, "iam.denial.grammar")
	if strings.Contains(v, "on resource on explicit denials") {
		return true
	}
	t.Fatalf("iam.denial.grammar = %q, a value this test does not know (iam-enforcement-surfaces.md 12.8)", v)
	return false
}

func TestDenialMessageIAMGrammar(t *testing.T) {
	explicitOnly := onResourceOnExplicitOnly(t)
	john := userOf("123456789012", "John", "AIDAJOHNEXAMPLE00001")
	hr := userOf("123456789012", "HR", "AIDAHREXAMPLE0000001")
	session := sessionOf("111122223333", "app", "AROAAPPEXAMPLE000001", "s")
	const (
		vault      = "arn:aws:secretsmanager:us-east-1:123456789012:secret:*"
		deployment = "arn:aws:codedeploy:us-east-1:123456789012:deploymentgroup:*"
		role       = "arn:aws:iam::123456789012:role/R"
	)
	// onResource is " on resource: <arn>" where the switch writes it.
	onResource := func(arn string, isExplicit bool) string {
		if isExplicit || !explicitOnly {
			return " on resource: " + arn
		}
		return ""
	}
	cases := []struct {
		name string
		d    authz.Denial
		want string
	}{
		{
			// iam-enforcement-surfaces.md 4.1, "resource-based, implicit".
			"resource-based implicit",
			authz.Denial{Principal: john, Action: actionOf("secretsmanager:GetSecretValue"), Resource: on(vault, "123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.ResourceBased}},
			"User: arn:aws:iam::123456789012:user/John is not authorized to perform: secretsmanager:GetSecretValue" + onResource(vault, false) +
				" because no resource-based policy allows the secretsmanager:GetSecretValue action",
		},
		{
			// 4.1, "role trust, implicit".
			"role trust implicit",
			authz.Denial{Principal: john, Action: actionOf("sts:AssumeRole"), Resource: on(role, "123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.RoleTrust}},
			"User: arn:aws:iam::123456789012:user/John is not authorized to perform: sts:AssumeRole" + onResource(role, false) +
				" because no role trust policy allows the sts:AssumeRole action",
		},
		{
			// 4.1, "identity-based, implicit", on no resource.
			"identity-based implicit on no resource",
			authz.Denial{Principal: hr, Action: actionOf("codecommit:ListRepositories"), Resource: anyIn("123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.IdentityBased}},
			"User: arn:aws:iam::123456789012:user/HR is not authorized to perform: codecommit:ListRepositories because no identity-based policy allows the codecommit:ListRepositories action",
		},
		{
			// 4.1, "identity-based, explicit with ARN".
			"identity-based explicit with a policy ARN",
			authz.Denial{Principal: hr, Action: actionOf("codedeploy:ListDeployments"), Resource: on(deployment, "123456789012"), Decision: authz.ExplicitDeny{PolicyType: authz.IdentityBased, PolicyARN: "arn:aws:iam::123456789012:policy/HRAccessPolicy"}},
			"User: arn:aws:iam::123456789012:user/HR is not authorized to perform: codedeploy:ListDeployments" + onResource(deployment, true) +
				" with an explicit deny in an identity-based policy: arn:aws:iam::123456789012:policy/HRAccessPolicy",
		},
		{
			// 4.1, "resource-based, explicit".
			"resource-based explicit",
			authz.Denial{Principal: john, Action: actionOf("secretsmanager:GetSecretValue"), Resource: on(vault, "123456789012"), Decision: authz.ExplicitDeny{PolicyType: authz.ResourceBased}},
			"User: arn:aws:iam::123456789012:user/John is not authorized to perform: secretsmanager:GetSecretValue" + onResource(vault, true) +
				" with an explicit deny in a resource-based policy",
		},
		{
			// 4.1, "permissions boundary, implicit" and "explicit".
			"permissions boundary implicit",
			authz.Denial{Principal: hr, Action: actionOf("codedeploy:ListDeployments"), Resource: on(deployment, "123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.PermissionsBoundary}},
			"User: arn:aws:iam::123456789012:user/HR is not authorized to perform: codedeploy:ListDeployments" + onResource(deployment, false) +
				" because no permissions boundary allows the codedeploy:ListDeployments action",
		},
		{
			"permissions boundary explicit",
			authz.Denial{Principal: hr, Action: actionOf("codedeploy:ListDeployments"), Resource: on(deployment, "123456789012"), Decision: authz.ExplicitDeny{PolicyType: authz.PermissionsBoundary, PolicyARN: "arn:aws:iam::123456789012:policy/DeveloperPermissionBoundary"}},
			"User: arn:aws:iam::123456789012:user/HR is not authorized to perform: codedeploy:ListDeployments" + onResource(deployment, true) +
				" with an explicit deny in a permissions boundary: arn:aws:iam::123456789012:policy/DeveloperPermissionBoundary",
		},
		{
			// 4.1, "session policy"; the caller is named by its session ARN (4.4).
			"session policy implicit",
			authz.Denial{Principal: session, Action: actionOf("codecommit:ListRepositories"), Resource: anyIn("111122223333"), Decision: authz.ImplicitDeny{PolicyType: authz.SessionPolicy}},
			"User: arn:aws:sts::111122223333:assumed-role/app/s is not authorized to perform: codecommit:ListRepositories because no session policy allows the codecommit:ListRepositories action",
		},
		{
			"session policy explicit",
			authz.Denial{Principal: session, Action: actionOf("codecommit:ListRepositories"), Resource: anyIn("111122223333"), Decision: authz.ExplicitDeny{PolicyType: authz.SessionPolicy, PolicyARN: "arn:aws:iam::111122223333:policy/session"}},
			"User: arn:aws:sts::111122223333:assumed-role/app/s is not authorized to perform: codecommit:ListRepositories with an explicit deny in a session policy: arn:aws:iam::111122223333:policy/session",
		},
		{
			// 4.1, "role trust, explicit" (note "the", not "a").
			"role trust explicit",
			authz.Denial{Principal: john, Action: actionOf("sts:AssumeRole"), Resource: on(role, "123456789012"), Decision: authz.ExplicitDeny{PolicyType: authz.RoleTrust}},
			"User: arn:aws:iam::123456789012:user/John is not authorized to perform: sts:AssumeRole" + onResource(role, true) +
				" with an explicit deny in the role trust policy",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.d.Message(authz.GrammarIAM); got != c.want {
				t.Errorf("Message(GrammarIAM) =\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

// S3's variant (iam-enforcement-surfaces.md 4.2, aws-doc): the resource in
// quotes, the policy ARN after ", with policy ARN:", the resource left out
// of an implicit identity denial, and "Access Denied" for a principal of
// another account ("Cross-account requests outside of the same
// organization return a generic Access Denied message").
func TestDenialMessageS3Grammar(t *testing.T) {
	mary := userOf("123456789012", "MaryMajor", "AIDAMARYMAJOREXAMPL1")
	const object = "arn:aws:s3:::amzn-s3-demo-bucket1/object-name"
	cases := []struct {
		name string
		d    authz.Denial
		want string
	}{
		{
			"explicit with a policy ARN",
			authz.Denial{Principal: mary, Action: actionOf("s3:GetObject"), Resource: on(object, "123456789012"), Decision: authz.ExplicitDeny{PolicyType: authz.IdentityBased, PolicyARN: "arn:aws:iam::123456789012:policy/ExampleIdentityBasedPolicy"}},
			`User: arn:aws:iam::123456789012:user/MaryMajor is not authorized to perform: s3:GetObject on resource: "arn:aws:s3:::amzn-s3-demo-bucket1/object-name" with an explicit deny in an identity-based policy, with policy ARN: arn:aws:iam::123456789012:policy/ExampleIdentityBasedPolicy`,
		},
		{
			"implicit identity",
			authz.Denial{Principal: mary, Action: actionOf("s3:GetObject"), Resource: on(object, "123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.IdentityBased}},
			"User: arn:aws:iam::123456789012:user/MaryMajor is not authorized to perform: s3:GetObject because no identity-based policy allows the s3:GetObject action",
		},
		{
			"another account",
			authz.Denial{Principal: userOf("444455556666", "MaryMajor", "AIDAMARYMAJOREXAMPL2"), Action: actionOf("s3:GetObject"), Resource: on(object, "123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.ResourceBased}},
			"Access Denied",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.d.Message(authz.GrammarS3); got != c.want {
				t.Errorf("Message(GrammarS3) =\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

// The one AWS recording of a policy denial: AssumeRole against a trust
// policy that does not name the caller, a role session, answers with no
// reason clause (iam-enforcement-surfaces.md 4.3, aws-recorded,
// testdata/parity/sts/R15/06-assume-denied.json).
func TestDenialMessageSTSGrammarMatchesTheRecording(t *testing.T) {
	caller := sessionOf("123456789012", "AccountFullAccessRole", "AROAACCOUNTFULLACCES", "00000000-0000-0000-0000-000000000003")
	d := authz.Denial{
		Principal: caller, Action: actionOf("sts:AssumeRole"),
		Resource: on("arn:aws:iam::123456789012:role/usr-rec-20261014-deny", "123456789012"),
		Decision: authz.ImplicitDeny{PolicyType: authz.RoleTrust},
	}
	const want = "User: arn:aws:sts::123456789012:assumed-role/AccountFullAccessRole/00000000-0000-0000-0000-000000000003 is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::123456789012:role/usr-rec-20261014-deny"
	if got := d.Message(authz.GrammarSTS); got != want {
		t.Errorf("Message(GrammarSTS) =\n%q\nwant\n%q", got, want)
	}
}

// kms:Decrypt names "the resource associated with this ciphertext", with
// no colon after "User" and no key ARN (iam-enforcement-surfaces.md 4.3,
// aws-doc).
func TestDenialMessageKMSCiphertextGrammar(t *testing.T) {
	d := authz.Denial{
		Principal: userOf("111122223333", "Alice", "AIDAALICEEXAMPLE0001"), Action: actionOf("kms:Decrypt"),
		Resource: on("arn:aws:kms:us-west-2:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab", "111122223333"),
		Decision: authz.ImplicitDeny{PolicyType: authz.IdentityBased},
	}
	const want = "User arn:aws:iam::111122223333:user/Alice is not authorized to perform: kms:Decrypt on the resource associated with this ciphertext because no identity-based policy allows the kms:Decrypt action"
	if got := d.Message(authz.GrammarKMSCiphertext); got != want {
		t.Errorf("Message(GrammarKMSCiphertext) =\n%q\nwant\n%q", got, want)
	}
}

// An allowed pair has no message, in every grammar; the root renders as
// its account root ARN (identity.Render, iam-sts-kms.md 4.1).
func TestDenialMessageOfAnAllowIsEmptyAndRootIsItsARN(t *testing.T) {
	root := identity.Root{Account: account("111122223333")}
	for _, g := range []authz.Grammar{authz.GrammarIAM, authz.GrammarS3, authz.GrammarSTS, authz.GrammarKMSCiphertext} {
		if got := (authz.Denial{Principal: root, Action: actionOf("s3:GetObject"), Resource: anyIn("111122223333"), Decision: authz.Allow{}}).Message(g); got != "" {
			t.Errorf("Message(%d) of an Allow = %q, want empty", g, got)
		}
	}
	d := authz.Denial{Principal: root, Action: actionOf("sts:AssumeRole"), Resource: on("arn:aws:iam::111122223333:role/R", "111122223333"), Decision: authz.ImplicitDeny{PolicyType: authz.IdentityBased}}
	if got := d.Message(authz.GrammarSTS); !strings.HasPrefix(got, "User: arn:aws:iam::111122223333:root is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::111122223333:role/R") {
		t.Errorf("Message(GrammarSTS) for root = %q, want the recorded form naming the root ARN (iam-enforcement-surfaces.md 12.2)", got)
	}
}
