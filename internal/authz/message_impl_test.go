package authz_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/identity"
)

// Denial messages beyond the blind tests: forms the grammars imply for cases
// AWS's examples do not show, and the one-line rule (iam-evaluation.md 2.11).

func TestDenialMessageEdgeForms(t *testing.T) {
	root := identity.Root{Account: account("123456789012")}
	mary := userOf("123456789012", "MaryMajor", "AIDAMARYMAJOREXAMPL1")
	object := on("arn:aws:s3:::amzn-s3-demo-bucket1/object-name", "123456789012")
	cases := []struct {
		name string
		d    authz.Denial
		g    authz.Grammar
		want string
	}{
		{
			// An inline policy's denial carries no ARN, so no ": " follows
			// the type (iam-enforcement-surfaces.md 12.3), and a pair on no
			// resource has no "on resource:".
			"IAM explicit inline on no resource",
			authz.Denial{Principal: mary, Action: actionOf("s3:ListAllMyBuckets"), Resource: anyIn("123456789012"), Decision: authz.ExplicitDeny{PolicyType: authz.IdentityBased}},
			authz.GrammarIAM,
			"User: arn:aws:iam::123456789012:user/MaryMajor is not authorized to perform: s3:ListAllMyBuckets with an explicit deny in an identity-based policy",
		},
		{
			// The bucket owner's root is in the owner's account (4.2).
			"S3 root of the owner, resource policy deny",
			authz.Denial{Principal: root, Action: actionOf("s3:GetObject"), Resource: object, Decision: authz.ExplicitDeny{PolicyType: authz.ResourceBased}},
			authz.GrammarS3,
			`User: arn:aws:iam::123456789012:root is not authorized to perform: s3:GetObject on resource: "arn:aws:s3:::amzn-s3-demo-bucket1/object-name" with an explicit deny in a resource-based policy`,
		},
		{
			"S3 anonymous",
			authz.Denial{Principal: identity.Anonymous{}, Action: actionOf("s3:GetObject"), Resource: object, Decision: authz.ImplicitDeny{PolicyType: authz.ResourceBased}},
			authz.GrammarS3,
			"Access Denied",
		},
		{
			"S3 service principal",
			authz.Denial{Principal: identity.ServicePrincipal{Name: "logging.s3.amazonaws.com"}, Action: actionOf("s3:PutObject"), Resource: object, Decision: authz.ImplicitDeny{PolicyType: authz.ResourceBased}},
			authz.GrammarS3,
			"Access Denied",
		},
		{
			// STS names the role; with no resource there is none to name.
			"STS on no resource",
			authz.Denial{Principal: mary, Action: actionOf("sts:TagSession"), Resource: anyIn("123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.RoleTrust}},
			authz.GrammarSTS,
			"User: arn:aws:iam::123456789012:user/MaryMajor is not authorized to perform: sts:TagSession",
		},
		{
			// SQS lower-cases the action and SNS writes its prefix "SNS"
			// on a pair on no resource too (IAM-ENF-01, IAM-ENF-03).
			"SQS on no resource",
			authz.Denial{Principal: mary, Action: actionOf("sqs:ListQueues"), Resource: anyIn("123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.IdentityBased}},
			authz.GrammarSQS,
			"User: arn:aws:iam::123456789012:user/MaryMajor is not authorized to perform: sqs:listqueues because no identity-based policy allows the sqs:listqueues action",
		},
		{
			"SNS on no resource",
			authz.Denial{Principal: mary, Action: actionOf("sns:ListTopics"), Resource: anyIn("123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.IdentityBased}},
			authz.GrammarSNS,
			"User: arn:aws:iam::123456789012:user/MaryMajor is not authorized to perform: SNS:ListTopics because no identity-based policy allows the SNS:ListTopics action",
		},
		{
			"an unknown grammar renders as IAM's",
			authz.Denial{Principal: mary, Action: actionOf("sqs:SendMessage"), Resource: anyIn("123456789012"), Decision: authz.ImplicitDeny{PolicyType: authz.IdentityBased}},
			authz.Grammar(200),
			"User: arn:aws:iam::123456789012:user/MaryMajor is not authorized to perform: sqs:SendMessage because no identity-based policy allows the sqs:SendMessage action",
		},
		{
			"no decision",
			authz.Denial{Principal: mary, Action: actionOf("sqs:SendMessage"), Resource: anyIn("123456789012")},
			authz.GrammarIAM,
			"",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.d.Message(c.g); got != c.want {
				t.Errorf("Message =\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

// Every denial message is one line and begins as its grammar does
// (iam-evaluation.md 2.11, iam-enforcement-surfaces.md 4.1 to 4.3).
func TestPropertyDenialMessageIsOneLine(t *testing.T) {
	principals := []identity.Principal{
		identity.Root{Account: account("111122223333")},
		userOf("111122223333", "U", "AIDAUEXAMPLE00000001"),
		sessionOf("444455556666", "app", "AROAAPPEXAMPLE000001", "s"),
		identity.ServicePrincipal{Name: "sns.amazonaws.com"},
		identity.Anonymous{},
	}
	types := []authz.PolicyType{authz.IdentityBased, authz.ResourceBased, authz.PermissionsBoundary, authz.SessionPolicy, authz.RoleTrust}
	rapid.Check(t, func(rt *rapid.T) {
		pt := rapid.SampledFrom(types).Draw(rt, "type")
		decision := rapid.SampledFrom([]authz.Decision{
			authz.ImplicitDeny{PolicyType: pt},
			authz.ExplicitDeny{PolicyType: pt},
			authz.ExplicitDeny{PolicyType: pt, PolicyARN: "arn:aws:iam::111122223333:policy/p"},
		}).Draw(rt, "decision")
		d := authz.Denial{
			Principal: rapid.SampledFrom(principals).Draw(rt, "principal"),
			Action:    actionOf(rapid.SampledFrom([]string{"sqs:SendMessage", "s3:GetObject", "sts:AssumeRole"}).Draw(rt, "action")),
			Resource:  rapid.SampledFrom([]authz.Resource{on(implQueue, "111122223333"), anyIn("111122223333")}).Draw(rt, "resource"),
			Decision:  decision,
		}
		g := rapid.SampledFrom([]authz.Grammar{authz.GrammarIAM, authz.GrammarS3, authz.GrammarSTS, authz.GrammarSQS, authz.GrammarSNS}).Draw(rt, "grammar")
		msg := d.Message(g)
		if strings.ContainsAny(msg, "\r\n") {
			rt.Fatalf("Message(%d) = %q spans lines", g, msg)
		}
		if msg != "Access Denied" && !strings.HasPrefix(msg, "User") {
			rt.Fatalf("Message(%d) = %q, want the grammar's \"User\" opening or S3's generic text", g, msg)
		}
		if msg == "Access Denied" && g != authz.GrammarS3 {
			rt.Fatalf("Message(%d) = %q: only S3 answers the generic text", g, msg)
		}
	})
}
