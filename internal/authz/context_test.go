package authz_test

import (
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// The request context of iam-evaluation.md 8.1 for one pair: the principal
// keys of 7.2 (S-VARS #principaltable, aws-doc), the keys understudy
// computes from the call, the delivery's source keys
// (iam-enforcement-surfaces.md 8.1) and the request keys a binding
// supplies. A key AWS would send that understudy cannot compute is
// Unsupplied (12.2); one AWS would not send is Absent.

var contextNow = clock.FromWire(time.Date(2026, 10, 26, 12, 30, 45, 0, time.UTC))

type presence struct {
	p      policydoc.Presence
	values []string // checked when p is Present and values is non-nil
}

var (
	absent     = presence{p: policydoc.Absent}
	unsupplied = presence{p: policydoc.Unsupplied}
)

func present(values ...string) presence { return presence{p: policydoc.Present, values: values} }

func checkKeys(t *testing.T, label string, c *policydoc.Context, want map[string]presence) {
	t.Helper()
	for key, w := range want {
		got, p := c.Lookup(key)
		if p != w.p || (w.p == policydoc.Present && w.values != nil && !slices.Equal(got.Values, w.values)) {
			t.Errorf("%s: %s = %v (presence %d), want %v (presence %d)", label, key, got.Values, p, w.values, w.p)
		}
	}
}

func contextInput(p identity.Principal, action string, r authz.Resource) authz.ContextInput {
	return authz.ContextInput{Principal: p, Action: actionOf(action), Resource: r, Now: contextNow, Region: "us-east-1", Via: authz.Direct{}}
}

// The principal keys per principal kind (iam-evaluation.md 7.2, 8.1, 2.10;
// S-KEYS #condition-keys-principalarn: for a role session "the request
// context returns the ARN of the role").
func TestBuildContextPrincipalKeys(t *testing.T) {
	const bucket = "arn:aws:s3:::b"
	r := on(bucket, "111122223333")
	user := userOf("111122223333", "ana", "AIDAANAEXAMPLE000001")
	session := sessionOf("111122223333", "app", "AROAAPPEXAMPLE000001", "s1")

	checkKeys(t, "root", authz.BuildContext(contextInput(identity.Root{Account: account("111122223333")}, "s3:GetObject", r)), map[string]presence{
		"aws:PrincipalArn":          present("arn:aws:iam::111122223333:root"),
		"aws:PrincipalAccount":      present("111122223333"),
		"aws:userid":                present("111122223333"),
		"aws:PrincipalType":         present("Account"),
		"aws:username":              absent,
		"aws:PrincipalIsAWSService": present("false"),
		"aws:MultiFactorAuthAge":    absent,
	})
	checkKeys(t, "user", authz.BuildContext(contextInput(user, "s3:GetObject", r)), map[string]presence{
		"aws:PrincipalArn":          present("arn:aws:iam::111122223333:user/ana"),
		"aws:PrincipalAccount":      present("111122223333"),
		"aws:userid":                present("AIDAANAEXAMPLE000001"),
		"aws:PrincipalType":         present("User"),
		"aws:username":              present("ana"),
		"aws:PrincipalIsAWSService": present("false"),
		// Temporary credentials only (8.1): a user's long-term keys carry neither.
		"aws:MultiFactorAuthPresent": absent,
		"aws:TokenIssueTime":         absent,
	})
	checkKeys(t, "session", authz.BuildContext(contextInput(session, "s3:GetObject", r)), map[string]presence{
		"aws:PrincipalArn":          present("arn:aws:iam::111122223333:role/app"),
		"aws:PrincipalAccount":      present("111122223333"),
		"aws:userid":                present("AROAAPPEXAMPLE000001:s1"),
		"aws:PrincipalType":         present("AssumedRole"),
		"aws:username":              absent,
		"aws:PrincipalIsAWSService": present("false"),
	})
	checkKeys(t, "anonymous", authz.BuildContext(contextInput(identity.Anonymous{}, "s3:GetObject", r)), map[string]presence{
		"aws:PrincipalArn":          absent,
		"aws:PrincipalAccount":      present("anonymous"),
		"aws:userid":                present("anonymous"),
		"aws:PrincipalType":         present("Anonymous"),
		"aws:username":              absent,
		"aws:PrincipalIsAWSService": absent,
	})
}

// A service principal: aws:PrincipalIsAWSService is true and
// aws:PrincipalServiceName names it (iam-evaluation.md 2.9, S-KEYS
// #condition-keys-principalisawsservice, -principalservicename). Whether
// it carries aws:PrincipalType, aws:userid and aws:PrincipalAccount is
// UNVERIFIED (IAM-EVAL-33): switch
// iam.context.service-principal-identity-keys.
func TestBuildContextServicePrincipalKeys(t *testing.T) {
	var identityKey presence
	switch v := evalSwitch(t, "iam.context.service-principal-identity-keys"); v {
	case "absent":
		identityKey = absent
	case "present":
		identityKey = presence{p: policydoc.Present}
	default:
		t.Fatalf("iam.context.service-principal-identity-keys = %q, a value this test does not know (iam-evaluation.md 12.3)", v)
	}
	in := contextInput(identity.ServicePrincipal{Name: "sns.amazonaws.com"}, "sqs:SendMessage", on("arn:aws:sqs:us-east-1:111122223333:q", "111122223333"))
	in.Via = authz.Delivery{Source: arnOf("arn:aws:sns:us-east-1:111122223333:t"), SourceAccount: account("111122223333")}
	checkKeys(t, "service", authz.BuildContext(in), map[string]presence{
		"aws:PrincipalIsAWSService": present("true"),
		"aws:PrincipalServiceName":  present("sns.amazonaws.com"),
		"aws:PrincipalType":         identityKey,
		"aws:userid":                identityKey,
		"aws:PrincipalAccount":      identityKey,
		"aws:username":              absent,
	})
}

// A role session's own keys: aws:TokenIssueTime is when STS minted it
// (iam-evaluation.md 8.1), aws:SourceIdentity the source identity it was
// given, and aws:MultiFactorAuthPresent per switch iam.context.mfa-present
// (IAM-EVAL-26): MFA is not checked, so it is never true (12.6).
func TestBuildContextSessionKeys(t *testing.T) {
	var mfa presence
	switch v := evalSwitch(t, "iam.context.mfa-present"); v {
	case "false-in-sessions":
		mfa = present("false")
	case "absent":
		mfa = absent
	default:
		t.Fatalf("iam.context.mfa-present = %q, a value this test does not know (iam-evaluation.md 12.3)", v)
	}
	issued := contextNow.Add(-5 * time.Minute)
	session := sessionOf("111122223333", "app", "AROAAPPEXAMPLE000001", "s1")
	session.Attributes = &identity.SessionAttributes{SourceIdentity: "ana@example.com", Issued: issued}
	c := authz.BuildContext(contextInput(session, "s3:GetObject", on("arn:aws:s3:::b", "111122223333")))
	checkKeys(t, "session", c, map[string]presence{
		"aws:MultiFactorAuthPresent": mfa,
		"aws:SourceIdentity":         present("ana@example.com"),
		"aws:TokenIssueTime":         {p: policydoc.Present},
	})
	if v, p := c.Lookup("aws:TokenIssueTime"); p == policydoc.Present {
		if len(v.Values) != 1 {
			t.Fatalf("aws:TokenIssueTime = %v, want one value", v.Values)
		}
		at, err := time.Parse(time.RFC3339, v.Values[0])
		if err != nil || !at.Equal(issued.Wire()) {
			t.Errorf("aws:TokenIssueTime = %q (%v), want the issue instant %s in ISO 8601 (iam-evaluation.md 8.1)", v.Values[0], err, issued.Wire().Format(time.RFC3339))
		}
	}

	// A session without attributes (a Lambda execution environment's)
	// still has temporary credentials, so AWS sends an issue time that
	// understudy does not know.
	bare := sessionOf("111122223333", "app", "AROAAPPEXAMPLE000001", "env")
	checkKeys(t, "session without attributes", authz.BuildContext(contextInput(bare, "s3:GetObject", on("arn:aws:s3:::b", "111122223333"))), map[string]presence{
		"aws:TokenIssueTime": unsupplied,
		"aws:SourceIdentity": absent,
	})
}

// The keys understudy computes from the call (iam-evaluation.md 8.1): the
// emulator clock (invariant 5), the region, the headers when sent, the
// resource owner, whether the call came through a service for the caller
// (S-KEYS #condition-keys-viaawsservice), and TLS per switch
// iam.context.secure-transport (8.2).
func TestBuildContextRequestKeys(t *testing.T) {
	var secure string
	switch v := evalSwitch(t, "iam.context.secure-transport"); v {
	case "assume-tls":
		secure = "true"
	default:
		t.Fatalf("iam.context.secure-transport = %q, a value this test does not know (iam-evaluation.md 12.3)", v)
	}
	user := userOf("111122223333", "ana", "AIDAANAEXAMPLE000001")
	in := contextInput(user, "s3:GetObject", on("arn:aws:s3:::b/k", "444455556666"))
	in.Region = "eu-west-1"
	in.UserAgent = "aws-cli/2.37.13"
	in.Referer = "https://example.com/page"
	c := authz.BuildContext(in)
	checkKeys(t, "direct call", c, map[string]presence{
		"aws:EpochTime":        present("1793017845"),
		"aws:RequestedRegion":  present("eu-west-1"),
		"aws:UserAgent":        present("aws-cli/2.37.13"),
		"aws:referer":          present("https://example.com/page"),
		"aws:ResourceAccount":  present("444455556666"),
		"aws:SecureTransport":  present(secure),
		"aws:ViaAWSService":    present("false"),
		"aws:SourceArn":        absent,
		"aws:SourceAccount":    absent,
		"aws:PrincipalOrgID":   absent,
		"aws:SourceVpc":        absent,
		"aws:SourceIp":         unsupplied,
		"aws:ResourceTag/team": unsupplied,
	})
	if v, p := c.Lookup("aws:CurrentTime"); p != policydoc.Present || len(v.Values) != 1 {
		t.Errorf("aws:CurrentTime = %v (presence %d), want the emulator clock", v.Values, p)
	} else if at, err := time.Parse(time.RFC3339, v.Values[0]); err != nil || !at.Equal(contextNow.Wire()) {
		t.Errorf("aws:CurrentTime = %q, want %s in ISO 8601 UTC", v.Values[0], contextNow.Wire().Format(time.RFC3339))
	}

	in.UserAgent, in.Referer = "", ""
	in.Via = authz.ForwardAccess{}
	in.Resource = anyIn("111122223333")
	checkKeys(t, "forward access, no headers, no resource", authz.BuildContext(in), map[string]presence{
		"aws:UserAgent":       absent,
		"aws:referer":         absent,
		"aws:ViaAWSService":   present("true"),
		"aws:ResourceAccount": present("111122223333"),
	})
}

// A delivery carries aws:SourceArn always and aws:SourceAccount when the
// delivering service sends it: EventBridge sends none to Lambda
// (iam-enforcement-surfaces.md 8.1, aws-doc).
func TestBuildContextDeliveryKeys(t *testing.T) {
	q := on("arn:aws:sqs:us-east-1:111122223333:q", "111122223333")
	s3 := contextInput(identity.ServicePrincipal{Name: "s3.amazonaws.com"}, "sqs:SendMessage", q)
	s3.Via = authz.Delivery{Source: arnOf("arn:aws:s3:::uploads"), SourceAccount: account("111122223333")}
	checkKeys(t, "S3 notification", authz.BuildContext(s3), map[string]presence{
		"aws:SourceArn":     present("arn:aws:s3:::uploads"),
		"aws:SourceAccount": present("111122223333"),
	})
	events := contextInput(identity.ServicePrincipal{Name: "events.amazonaws.com"}, "lambda:InvokeFunction", on("arn:aws:lambda:us-east-1:111122223333:function:f", "111122223333"))
	events.Via = authz.Delivery{Source: arnOf("arn:aws:events:us-east-1:111122223333:rule/r")}
	checkKeys(t, "EventBridge to Lambda", authz.BuildContext(events), map[string]presence{
		"aws:SourceArn":     present("arn:aws:events:us-east-1:111122223333:rule/r"),
		"aws:SourceAccount": absent,
	})
}

// Principal tags become aws:PrincipalTag/<key> (iam-evaluation.md 8.1);
// two tags whose keys differ only in case match one policy key with two
// values, which AWS's choice between is UNVERIFIED (6.13, IAM-EVAL-31):
// switch iam.resource-tag.case-duplicates.
func TestBuildContextPrincipalTags(t *testing.T) {
	var duplicate presence
	switch v := evalSwitch(t, "iam.resource-tag.case-duplicates"); v {
	case "absent":
		duplicate = absent
	case "first":
		duplicate = present("a")
	default:
		t.Fatalf("iam.resource-tag.case-duplicates = %q, a value this test does not know (iam-evaluation.md 12.3)", v)
	}
	in := contextInput(userOf("111122223333", "ana", "AIDAANAEXAMPLE000001"), "s3:GetObject", on("arn:aws:s3:::b", "111122223333"))
	in.PrincipalTags = []authz.Tag{{Key: "dept", Value: "123"}, {Key: "team", Value: "a"}, {Key: "Team", Value: "b"}}
	checkKeys(t, "tags", authz.BuildContext(in), map[string]presence{
		"aws:PrincipalTag/dept": present("123"),
		"aws:PrincipalTag/DEPT": present("123"),
		"aws:PrincipalTag/team": duplicate,
		"aws:PrincipalTag/role": absent,
	})
}

// Request keys a binding supplies: a key with values is present
// (multivalued per Multivalued, or with several values), a key with none is
// one the request does not carry. Without an aws:TagKeys entry the tag
// families are Unsupplied, since AWS sends them with every tagging request
// (8.1); with one, they are as supplied. A key of the action's own service
// that nothing set is Unsupplied, as AWS sends its service keys per action
// (8.2).
func TestBuildContextRequestKeysFromTheBinding(t *testing.T) {
	r := on("arn:aws:iam::111122223333:role/R", "111122223333")
	user := userOf("111122223333", "ana", "AIDAANAEXAMPLE000001")

	in := contextInput(user, "sts:AssumeRole", r)
	in.Keys = map[string][]string{
		"sts:ExternalId":        {"ext-1"},
		"aws:TagKeys":           {"team"},
		"aws:RequestTag/team":   {"blue"},
		"sts:TransitiveTagKeys": {"team"},
		"sts:SourceIdentity":    {},
		"sts:RoleSessionName":   {"s1"},
	}
	c := authz.BuildContext(in)
	checkKeys(t, "AssumeRole with a tag", c, map[string]presence{
		"sts:ExternalId":        present("ext-1"),
		"aws:TagKeys":           present("team"),
		"aws:RequestTag/team":   present("blue"),
		"aws:RequestTag/other":  absent,
		"sts:TransitiveTagKeys": present("team"),
		"sts:SourceIdentity":    absent,
		"sts:DurationSeconds":   unsupplied,
	})
	for _, key := range []string{"aws:TagKeys", "sts:TransitiveTagKeys"} {
		if v, _ := c.Lookup(key); !v.Multivalued {
			t.Errorf("%s is single-valued, want multivalued (iam-evaluation.md 6.12)", key)
		}
	}
	if v, _ := c.Lookup("sts:ExternalId"); v.Multivalued {
		t.Error("sts:ExternalId is multivalued, want single-valued")
	}

	in.Keys = map[string][]string{"sts:RoleSessionName": {"s1"}}
	checkKeys(t, "no tag keys supplied", authz.BuildContext(in), map[string]presence{
		"aws:TagKeys":         unsupplied,
		"aws:RequestTag/team": unsupplied,
	})
}

// kms:CallerAccount is the caller's account for a KMS action
// (iam-enforcement-surfaces.md 9.2, aws-doc).
func TestBuildContextKMSCallerAccount(t *testing.T) {
	in := contextInput(userOf("111122223333", "ana", "AIDAANAEXAMPLE000001"), "kms:Decrypt", on("arn:aws:kms:us-east-1:444455556666:key/1234abcd-12ab-34cd-56ef-1234567890ab", "444455556666"))
	checkKeys(t, "kms", authz.BuildContext(in), map[string]presence{"kms:CallerAccount": present("111122223333")})
}

// Multivalued keys (iam-evaluation.md 6.12, S-SETS, S-KEYS); the tag
// families are single-valued. Key names are not case-sensitive (6.2).
func TestMultivalued(t *testing.T) {
	for _, key := range []string{
		"aws:TagKeys", "aws:CalledVia", "aws:PrincipalServiceNamesList", "aws:PrincipalOrgPaths",
		"aws:ResourceOrgPaths", "aws:SourceOrgPaths", "aws:VpceOrgPaths", "aws:tagkeys",
	} {
		if !authz.Multivalued(key) {
			t.Errorf("Multivalued(%q) = false, want true", key)
		}
	}
	for _, key := range []string{"aws:RequestTag/team", "aws:ResourceTag/team", "aws:PrincipalTag/team", "aws:SourceArn", "aws:username"} {
		if authz.Multivalued(key) {
			t.Errorf("Multivalued(%q) = true, want false", key)
		}
	}
}
