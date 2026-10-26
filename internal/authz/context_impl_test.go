package authz_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// BuildContext beyond the blind tests: what it does with an identifier, a
// clock reading or a region it is not given (Unsupplied, never invented,
// iam-evaluation.md 12.6), kms:ViaService, an execution-role call, principal
// tags whose case duplicates agree, and request keys that replace computed
// ones.

// A user or role without its unique id, a zero clock and an empty region:
// AWS would send each key, so each is Unsupplied.
func TestBuildContextMarksUnknownValuesUnsupplied(t *testing.T) {
	user := identity.User{Account: account("111122223333"), Name: "ana"}
	in := contextInput(user, "s3:GetObject", on("arn:aws:s3:::b", "111122223333"))
	in.Now, in.Region = clock.Instant{}, ""
	checkKeys(t, "user without an id", authz.BuildContext(in), map[string]presence{
		"aws:userid":          unsupplied,
		"aws:username":        present("ana"),
		"aws:PrincipalArn":    present("arn:aws:iam::111122223333:user/ana"),
		"aws:CurrentTime":     unsupplied,
		"aws:EpochTime":       unsupplied,
		"aws:RequestedRegion": unsupplied,
	})
	session := sessionOf("111122223333", "app", "", "s1")
	session.Role.Path = "/team/"
	checkKeys(t, "role without an id", authz.BuildContext(contextInput(session, "s3:GetObject", on("arn:aws:s3:::b", "111122223333"))), map[string]presence{
		"aws:userid":       unsupplied,
		"aws:PrincipalArn": present("arn:aws:iam::111122223333:role/team/app"),
	})
	// Attributes with no issue time leave that one key unknown; the source
	// identity is still the session's.
	session.Attributes = &identity.SessionAttributes{SourceIdentity: "ana"}
	checkKeys(t, "attributes without an issue time", authz.BuildContext(contextInput(session, "s3:GetObject", anyIn("111122223333"))), map[string]presence{
		"aws:TokenIssueTime": unsupplied,
		"aws:SourceIdentity": present("ana"),
	})
}

// kms:ViaService is sent only for a call a service makes for the caller,
// with a value understudy cannot name (iam-enforcement-surfaces.md 9.2); a
// direct call does not carry it. Other kms keys nothing set stay Unsupplied
// (iam-evaluation.md 8.2).
func TestBuildContextKMSViaService(t *testing.T) {
	key := on("arn:aws:kms:us-east-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab", "111122223333")
	in := contextInput(userOf("111122223333", "ana", "AIDAANAEXAMPLE000001"), "kms:Decrypt", key)
	checkKeys(t, "direct", authz.BuildContext(in), map[string]presence{
		"kms:ViaService":        absent,
		"kms:EncryptionContext": unsupplied,
	})
	in.Via = authz.ForwardAccess{}
	checkKeys(t, "forward access", authz.BuildContext(in), map[string]presence{
		"kms:ViaService":    unsupplied,
		"kms:CallerAccount": present("111122223333"),
		"aws:CalledVia":     unsupplied,
		"aws:CalledViaLast": unsupplied,
	})
	// A service principal belongs to no account (identity.AccountOf).
	svc := contextInput(identity.ServicePrincipal{Name: "s3.amazonaws.com"}, "kms:Decrypt", key)
	checkKeys(t, "service principal", authz.BuildContext(svc), map[string]presence{"kms:CallerAccount": unsupplied})
}

// A function's execution role calls directly, not through a service for a
// caller (S-KEYS #condition-keys-viaawsservice).
func TestBuildContextExecutionRole(t *testing.T) {
	in := contextInput(sessionOf("111122223333", "fn-role", "AROAFNROLEEXAMPLE001", "fn"), "sqs:SendMessage", on(implQueue, "111122223333"))
	in.Via = authz.ExecutionRole{}
	checkKeys(t, "execution role", authz.BuildContext(in), map[string]presence{
		"aws:ViaAWSService": present("false"),
		"aws:CalledVia":     absent,
		"aws:SourceArn":     absent,
	})
}

// Two tags whose keys differ only in case but carry one value are one tag.
func TestBuildContextPrincipalTagCaseDuplicatesThatAgree(t *testing.T) {
	in := contextInput(userOf("111122223333", "ana", "AIDAANAEXAMPLE000001"), "s3:GetObject", anyIn("111122223333"))
	in.PrincipalTags = []authz.Tag{{Key: "team", Value: "a"}, {Key: "Team", Value: "a"}}
	checkKeys(t, "agreeing duplicates", authz.BuildContext(in), map[string]presence{"aws:PrincipalTag/TEAM": present("a")})
}

// A request key a binding supplies replaces what BuildContext marked, such as
// a resource tag the handler knows, and a binding that supplies aws:TagKeys
// with no values says the request tags nothing.
func TestBuildContextRequestKeysComeLast(t *testing.T) {
	in := contextInput(userOf("111122223333", "ana", "AIDAANAEXAMPLE000001"), "s3:PutObject", on("arn:aws:s3:::b/k", "111122223333"))
	in.Keys = map[string][]string{"aws:ResourceTag/team": {"blue"}, "aws:TagKeys": {}, "s3:x-amz-acl": {"private"}}
	c := authz.BuildContext(in)
	checkKeys(t, "binding keys", c, map[string]presence{
		"aws:ResourceTag/team":  present("blue"),
		"aws:ResourceTag/other": unsupplied,
		"aws:TagKeys":           absent,
		"aws:RequestTag/team":   absent,
		"s3:x-amz-acl":          present("private"),
		"s3:prefix":             unsupplied,
	})
	if v, p := c.Lookup("s3:x-amz-acl"); p != policydoc.Present || v.Multivalued {
		t.Errorf("s3:x-amz-acl = %+v (presence %d), want single-valued", v, p)
	}
	// Several values make a key multivalued whatever Multivalued says.
	in.Keys = map[string][]string{"s3:x-amz-grant-read": {"a", "b"}}
	if v, _ := authz.BuildContext(in).Lookup("s3:x-amz-grant-read"); !v.Multivalued || len(v.Values) != 2 {
		t.Errorf("s3:x-amz-grant-read = %+v, want both values, multivalued", v)
	}
}

// A pair on no resource in no known account has no aws:ResourceAccount to
// send: Unsupplied.
func TestBuildContextResourceAccountUnknown(t *testing.T) {
	in := contextInput(userOf("111122223333", "ana", "AIDAANAEXAMPLE000001"), "sqs:ListQueues", authz.AnyResource{})
	checkKeys(t, "no account", authz.BuildContext(in), map[string]presence{"aws:ResourceAccount": unsupplied})
}
