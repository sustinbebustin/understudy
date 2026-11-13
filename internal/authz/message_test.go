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
// ARN>]". Every recorded denial of a pair on a resource writes "on
// resource: <arn>", implicit and explicit alike (IAM-ENF-01 to IAM-ENF-15,
// recorded 2026-11-03), so the documented examples are compared in that
// form. S3's variant (4.2) and the recorded STS text (4.3) are compared
// byte for byte.

func TestDenialMessageIAMGrammar(t *testing.T) {
	john := userOf("123456789012", "John", "AIDAJOHNEXAMPLE00001")
	hr := userOf("123456789012", "HR", "AIDAHREXAMPLE0000001")
	session := sessionOf("111122223333", "app", "AROAAPPEXAMPLE000001", "s")
	const (
		vault      = "arn:aws:secretsmanager:us-east-1:123456789012:secret:*"
		deployment = "arn:aws:codedeploy:us-east-1:123456789012:deploymentgroup:*"
		role       = "arn:aws:iam::123456789012:role/R"
	)
	// onResource is " on resource: <arn>", written on every denial of a
	// resource (IAM-ENF-15).
	onResource := func(arn string, _ bool) string { return " on resource: " + arn }
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
// quotes, the policy ARN after ", with policy ARN:", and "Access Denied"
// for a principal of another account ("Cross-account requests outside of
// the same organization return a generic Access Denied message"). An
// implicit identity denial names the quoted resource too (IAM-ENF-12,
// recorded 2026-11-03), where the documented example leaves it out.
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
			`User: arn:aws:iam::123456789012:user/MaryMajor is not authorized to perform: s3:GetObject on resource: "arn:aws:s3:::amzn-s3-demo-bucket1/object-name" because no identity-based policy allows the s3:GetObject action`,
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

// Recorded denials of a role session (testdata/parity/<service>/IAM-ENF,
// recorded 2026-11-03), byte for byte: SQS writes the action in lower
// case (IAM-ENF-01, IAM-ENF-02), SNS with the prefix "SNS:" (IAM-ENF-03),
// and kms:Decrypt of a ciphertext names the key ARN in the documented
// grammar (IAM-ENF-05), not the aws-doc "resource associated with this
// ciphertext" text. Inline and session-policy denials carry no policy ARN,
// a managed policy's does (IAM-ENF-15).
func TestDenialMessagesMatchTheRecordings(t *testing.T) {
	none := sessionOf("123456789012", "us1103-enf-none", "AROAENFNONEEXAMPLE01", "enf")
	mix := sessionOf("123456789012", "us1103-enf-mix", "AROAENFMIXEXAMPLE001", "enf")
	const (
		queue = "arn:aws:sqs:us-east-2:123456789012:us1103-enf-q"
		topic = "arn:aws:sns:us-east-2:123456789012:us1103-enf-t2"
		key   = "arn:aws:kms:us-east-2:123456789012:key/45c1da4a-a2c9-4510-9930-7f77b26866b5"
		group = "arn:aws:logs:us-east-2:123456789012:log-group:/us1103/enf"
		rule  = "arn:aws:events:us-east-2:123456789012:rule/us1103-enf-r"
		// identityARN is the sender identity an SES send denial names
		// (SES-28).
		identityARN = "arn:aws:ses:us-east-2:123456789012:identity/us1103-me@example.com"
	)
	cases := []struct {
		fixture string
		d       authz.Denial
		g       authz.Grammar
		want    string
	}{
		{
			"sqs/IAM-ENF/01-raw.json",
			authz.Denial{Principal: none, Action: actionOf("sqs:GetQueueAttributes"), Resource: on(queue, "123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.IdentityBased}},
			authz.GrammarSQS,
			"User: arn:aws:sts::123456789012:assumed-role/us1103-enf-none/enf is not authorized to perform: sqs:getqueueattributes on resource: " + queue + " because no identity-based policy allows the sqs:getqueueattributes action",
		},
		{
			"sns/IAM-ENF/03-get.json",
			authz.Denial{Principal: none, Action: actionOf("sns:GetTopicAttributes"), Resource: on(topic, "123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.IdentityBased}},
			authz.GrammarSNS,
			"User: arn:aws:sts::123456789012:assumed-role/us1103-enf-none/enf is not authorized to perform: SNS:GetTopicAttributes on resource: " + topic + " because no identity-based policy allows the SNS:GetTopicAttributes action",
		},
		{
			"kms/IAM-ENF/05-decrypt.json",
			authz.Denial{Principal: none, Action: actionOf("kms:Decrypt"), Resource: on(key, "123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.IdentityBased}},
			authz.GrammarIAM,
			"User: arn:aws:sts::123456789012:assumed-role/us1103-enf-none/enf is not authorized to perform: kms:Decrypt on resource: " + key + " because no identity-based policy allows the kms:Decrypt action",
		},
		{
			"sqs/IAM-ENF/15-inline-sqs.json",
			authz.Denial{Principal: mix, Action: actionOf("sqs:GetQueueAttributes"), Resource: on("arn:aws:sqs:us-east-2:123456789012:us1103-enf-q", "123456789012"), Decision: authz.ExplicitDeny{PolicyType: authz.IdentityBased}},
			authz.GrammarSQS,
			"User: arn:aws:sts::123456789012:assumed-role/us1103-enf-mix/enf is not authorized to perform: sqs:getqueueattributes on resource: " + queue + " with an explicit deny in an identity-based policy",
		},
		{
			"sns/IAM-ENF/15-managed-sns.json",
			authz.Denial{Principal: mix, Action: actionOf("sns:GetTopicAttributes"), Resource: on("arn:aws:sns:us-east-2:123456789012:us1103-enf-t", "123456789012"), Decision: authz.ExplicitDeny{PolicyType: authz.IdentityBased, PolicyARN: "arn:aws:iam::123456789012:policy/us1103-enf-deny"}},
			authz.GrammarSNS,
			"User: arn:aws:sts::123456789012:assumed-role/us1103-enf-mix/enf is not authorized to perform: SNS:GetTopicAttributes on resource: arn:aws:sns:us-east-2:123456789012:us1103-enf-t with an explicit deny in an identity-based policy: arn:aws:iam::123456789012:policy/us1103-enf-deny",
		},
		{
			"logs/IAM-ENF/15-session-implicit.json",
			authz.Denial{Principal: mix, Action: actionOf("logs:DescribeLogStreams"), Resource: on(group, "123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.SessionPolicy}},
			authz.GrammarIAM,
			"User: arn:aws:sts::123456789012:assumed-role/us1103-enf-mix/enf is not authorized to perform: logs:DescribeLogStreams on resource: " + group + " because no session policy allows the logs:DescribeLogStreams action",
		},
		{
			"events/IAM-ENF/15-session-explicit.json",
			authz.Denial{Principal: mix, Action: actionOf("events:DescribeRule"), Resource: on(rule, "123456789012"), Decision: authz.ExplicitDeny{PolicyType: authz.SessionPolicy}},
			authz.GrammarIAM,
			"User: arn:aws:sts::123456789012:assumed-role/us1103-enf-mix/enf is not authorized to perform: events:DescribeRule on resource: " + rule + " with an explicit deny in a session policy",
		},
		{
			"logs/IAM-ENF/15-boundary-implicit.json",
			authz.Denial{Principal: mix, Action: actionOf("logs:DescribeLogStreams"), Resource: on(group, "123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.PermissionsBoundary}},
			authz.GrammarIAM,
			"User: arn:aws:sts::123456789012:assumed-role/us1103-enf-mix/enf is not authorized to perform: logs:DescribeLogStreams on resource: " + group + " because no permissions boundary allows the logs:DescribeLogStreams action",
		},
		{
			"s3/IAM-ENF/12-raw.json",
			authz.Denial{Principal: none, Action: actionOf("s3:GetObject"), Resource: on("arn:aws:s3:::us1103-enf-b/exists", "123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.IdentityBased}},
			authz.GrammarS3,
			`User: arn:aws:sts::123456789012:assumed-role/us1103-enf-none/enf is not authorized to perform: s3:GetObject on resource: "arn:aws:s3:::us1103-enf-b/exists" because no identity-based policy allows the s3:GetObject action`,
		},
		{
			"ses/SES-28/01-v1-send.json",
			authz.Denial{Principal: none, Action: actionOf("ses:SendEmail"), Resource: on(identityARN, "123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.IdentityBased}},
			authz.GrammarSES,
			"User 'arn:aws:sts::123456789012:assumed-role/us1103-enf-none/enf' is not authorized to perform 'ses:SendEmail' on resource '" + identityARN + "'",
		},
		{
			"ses/SES-28/04-v2-raw.json",
			authz.Denial{Principal: none, Action: actionOf("ses:SendRawEmail"), Resource: on(identityARN, "123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.IdentityBased}},
			authz.GrammarSES,
			"User 'arn:aws:sts::123456789012:assumed-role/us1103-enf-none/enf' is not authorized to perform 'ses:SendRawEmail' on resource '" + identityARN + "'",
		},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			if got := c.d.Message(c.g); got != c.want {
				t.Errorf("Message =\n%q\nwant the recorded\n%q", got, c.want)
			}
		})
	}
}

// An allowed pair has no message, in every grammar; the root renders as
// its account root ARN (identity.Render, iam-sts-kms.md 4.1).
func TestDenialMessageOfAnAllowIsEmptyAndRootIsItsARN(t *testing.T) {
	root := identity.Root{Account: account("111122223333")}
	for _, g := range []authz.Grammar{authz.GrammarIAM, authz.GrammarS3, authz.GrammarSTS, authz.GrammarSQS, authz.GrammarSNS, authz.GrammarSES} {
		if got := (authz.Denial{Principal: root, Action: actionOf("s3:GetObject"), Resource: anyIn("111122223333"), Decision: authz.Allow{}}).Message(g); got != "" {
			t.Errorf("Message(%d) of an Allow = %q, want empty", g, got)
		}
	}
	d := authz.Denial{Principal: root, Action: actionOf("sts:AssumeRole"), Resource: on("arn:aws:iam::111122223333:role/R", "111122223333"), Decision: authz.ImplicitDeny{PolicyType: authz.IdentityBased}}
	if got := d.Message(authz.GrammarSTS); !strings.HasPrefix(got, "User: arn:aws:iam::111122223333:root is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::111122223333:role/R") {
		t.Errorf("Message(GrammarSTS) for root = %q, want the recorded form naming the root ARN (iam-enforcement-surfaces.md 12.2)", got)
	}
}
