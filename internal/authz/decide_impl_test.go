package authz_test

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// Decide beyond the blind tests: the order an explicit deny's policy type is
// chosen in, the policy type of every implicit deny a trust policy or a
// service principal meets, what a key policy alone grants in its account,
// and the nil inputs Decide reads as their empty variants.

const (
	implQueue = "arn:aws:sqs:us-east-1:111122223333:q"
	implRole  = "arn:aws:iam::111122223333:role/R"
	denyAll   = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"*","Resource":"*"}]}`
)

// wantDecision compares the variant and, for both deny variants, the policy
// type, which outcome.check leaves out for an implicit deny.
func wantDecision(t *testing.T, label string, got, want authz.Decision) {
	t.Helper()
	if got != want {
		t.Errorf("%s: Decide = %#v, want %#v", label, got, want)
	}
}

// Several policy types deny: understudy names the first in the order
// resource, identity, boundary, session (iam-evaluation.md 12.4; AWS names
// one, iam-enforcement-surfaces.md 4.1). Resource before identity is
// recorded (IAM-EVAL-21, 2026-11-03); the rest is inferred.
func TestDecideExplicitDenyNamesTheFirstTypeInOrder(t *testing.T) {
	s := sessionOf("111122223333", "app", "AROAAPPEXAMPLE000001", "s")
	q := on(implQueue, "111122223333")
	denyRP := authz.Generic{Document: docOf(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"*","Resource":"*"}]}`)}
	idDeny := managedPolicy(t, "arn:aws:iam::111122223333:policy/id-deny", denyAll)
	bDeny := managedPolicy(t, "arn:aws:iam::111122223333:policy/b-deny", denyAll)
	sDeny := managedPolicy(t, "arn:aws:iam::111122223333:policy/s-deny", denyAll)
	cases := []struct {
		name string
		set  policySet
		want authz.Decision
	}{
		{"all four", policySet{identity: []authz.Policy{idDeny}, boundary: &bDeny, session: []authz.Policy{sDeny}, resource: denyRP}, authz.ExplicitDeny{PolicyType: authz.ResourceBased}},
		{"identity, boundary, session", policySet{identity: []authz.Policy{idDeny}, boundary: &bDeny, session: []authz.Policy{sDeny}}, authz.ExplicitDeny{PolicyType: authz.IdentityBased, PolicyARN: idDeny.ARN}},
		{"boundary, session", policySet{boundary: &bDeny, session: []authz.Policy{sDeny}}, authz.ExplicitDeny{PolicyType: authz.PermissionsBoundary, PolicyARN: bDeny.ARN}},
		{"session", policySet{session: []authz.Policy{sDeny}}, authz.ExplicitDeny{PolicyType: authz.SessionPolicy, PolicyARN: sDeny.ARN}},
		{"inline session policy", policySet{session: []authz.Policy{inlinePolicy(t, "inline", denyAll)}}, authz.ExplicitDeny{PolicyType: authz.SessionPolicy}},
		{"resource alone", policySet{resource: denyRP}, authz.ExplicitDeny{PolicyType: authz.ResourceBased}},
	}
	for _, c := range cases {
		wantDecision(t, c.name, decide(t, s, "sqs:SendMessage", q, nil, c.set).Decision, c.want)
	}
}

// The second identity policy that denies names its own ARN, not the first
// policy's (iam-enforcement-surfaces.md 12.3: the policy ARN is the denying
// managed policy's).
func TestDecideExplicitDenyNamesTheDenyingPolicy(t *testing.T) {
	u := userOf("111122223333", "U", "AIDAUEXAMPLE00000001")
	allow := managedPolicy(t, "arn:aws:iam::111122223333:policy/allow", allowAll)
	deny := managedPolicy(t, "arn:aws:iam::111122223333:policy/deny", denyAll)
	got := decide(t, u, "sqs:SendMessage", on(implQueue, "111122223333"), nil, policySet{identity: []authz.Policy{allow, deny}}).Decision
	wantDecision(t, "allow then deny", got, authz.ExplicitDeny{PolicyType: authz.IdentityBased, PolicyARN: deny.ARN})
}

// A trust policy must allow explicitly (iam-evaluation.md 3.1), so a trust
// that names no one the caller is denies as "no role trust policy allows",
// from the role's account and from another (2.4, 3.3), and a service role's
// trust admits its service principal (3.6).
func TestDecideTrustPolicyImplicitDenyTypes(t *testing.T) {
	lambdaTrust := authz.Trust{Document: docOf(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)}
	assume := []authz.Policy{inlinePolicy(t, "assume", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Resource":"*"}]}`)}
	role := on(implRole, "111122223333")
	other := userOf("444455556666", "X", "AIDAXEXAMPLE00000001")
	wantDecision(t, "another account, trust names a service", decide(t, other, "sts:AssumeRole", role, nil, policySet{resource: lambdaTrust, identity: assume}).Decision, authz.ImplicitDeny{PolicyType: authz.RoleTrust})
	wantDecision(t, "lambda assumes its execution role", decide(t, identity.ServicePrincipal{Name: "lambda.amazonaws.com"}, "sts:AssumeRole", role, nil, policySet{resource: lambdaTrust}).Decision, authz.Allow{})
	wantDecision(t, "another service", decide(t, identity.ServicePrincipal{Name: "events.amazonaws.com"}, "sts:AssumeRole", role, nil, policySet{resource: lambdaTrust}).Decision, authz.ImplicitDeny{PolicyType: authz.RoleTrust})
	// Root cannot call AssumeRole even when the trust names its account
	// (iam-evaluation.md 3.5, S-STSCMP).
	acctTrust := authz.Trust{Document: docOf(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"111122223333"},"Action":"sts:AssumeRole"}]}`)}
	wantDecision(t, "root", decide(t, identity.Root{Account: account("111122223333")}, "sts:AssumeRole", role, nil, policySet{resource: acctTrust}).Decision, authz.ImplicitDeny{PolicyType: authz.IdentityBased})
}

// "You can use the key policy alone to control access if the key and the
// IAM principal are in the same AWS account" (iam-evaluation.md 4.1, K-CTL),
// and root reaches a key through a statement naming "*" as it does through
// the account (4.3).
func TestDecideKeyPolicyAloneInItsAccount(t *testing.T) {
	const key = "arn:aws:kms:us-west-2:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	k := on(key, "111122223333")
	alice := userOf("111122223333", "Alice", "AIDAALICEEXAMPLE0001")
	names := authz.KeyPolicy{Document: docOf(t, `[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:user/Alice"},"Action":"kms:Encrypt","Resource":"*"}]`)}
	wantDecision(t, "user named, no IAM policy", decide(t, alice, "kms:Encrypt", k, nil, policySet{resource: names}).Decision, authz.Allow{})
	wantDecision(t, "user named, another action", decide(t, alice, "kms:Decrypt", k, nil, policySet{resource: names}).Decision, authz.ImplicitDeny{PolicyType: authz.ResourceBased})
	star := authz.KeyPolicy{Document: docOf(t, `[{"Effect":"Allow","Principal":"*","Action":"kms:Decrypt","Resource":"*"}]`)}
	wantDecision(t, "root, key policy naming *", decide(t, identity.Root{Account: account("111122223333")}, "kms:Decrypt", k, nil, policySet{resource: star}).Decision, authz.Allow{})
	// A service principal reaches a key only through a statement naming it.
	wantDecision(t, "service, key policy naming *", decide(t, identity.ServicePrincipal{Name: "s3.amazonaws.com"}, "kms:Decrypt", k, nil, policySet{resource: star}).Decision, authz.Allow{})
	wantDecision(t, "service, key policy naming a user", decide(t, identity.ServicePrincipal{Name: "s3.amazonaws.com"}, "kms:Encrypt", k, nil, policySet{resource: names}).Decision, authz.ImplicitDeny{PolicyType: authz.ResourceBased})
}

// Only a resource policy naming "*" grants an anonymous caller
// (iam-evaluation.md 2.10); a nil principal is anonymous.
func TestDecideAnonymousCaller(t *testing.T) {
	q := on(implQueue, "111122223333")
	public := authz.Generic{Document: docOf(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"*"}]}`)}
	toAccount := authz.Generic{Document: docOf(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"111122223333"},"Action":"sqs:SendMessage","Resource":"*"}]}`)}
	for _, p := range []identity.Principal{identity.Anonymous{}, nil} {
		wantDecision(t, "public queue", decide(t, p, "sqs:SendMessage", q, nil, policySet{resource: public}).Decision, authz.Allow{})
		wantDecision(t, "queue naming the account", decide(t, p, "sqs:SendMessage", q, nil, policySet{resource: toAccount}).Decision, authz.ImplicitDeny{PolicyType: authz.ResourceBased})
	}
}

// A nil Boundary is NoBoundary and a nil resource policy NoResourcePolicy.
func TestDecideReadsNilPoliciesAsNone(t *testing.T) {
	u := userOf("111122223333", "U", "AIDAUEXAMPLE00000001")
	r := authz.Request{Principal: u, Action: actionOf("sqs:SendMessage"), Resource: on(implQueue, "111122223333"), Context: contextKeys()}
	got := authz.Decide(r, authz.Policies{Identity: []authz.Policy{inlinePolicy(t, "all", allowAll)}})
	wantDecision(t, "nil boundary and resource policy", got.Decision, authz.Allow{})
	got = authz.Decide(r, authz.Policies{})
	wantDecision(t, "no policy at all", got.Decision, authz.ImplicitDeny{PolicyType: authz.IdentityBased})
}

// Unsupplied keys carry the type of the policy that tested them: a
// boundary's ARN, a session policy's name, and a trust policy as the role
// trust policy (iam-enforcement-surfaces.md 12.2).
func TestDecideGathersUnsuppliedKeysOfEveryPolicyType(t *testing.T) {
	cond := `"Condition":{"StringEquals":{"aws:SourceIp":"203.0.113.1"}}`
	boundary := managedPolicy(t, "arn:aws:iam::111122223333:policy/b", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*",`+cond+`}]}`)
	session := inlinePolicy(t, "session", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*",`+cond+`}]}`)
	trust := authz.Trust{Document: docOf(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"111122223333"},"Action":"sts:AssumeRole",`+cond+`}]}`)}
	ctx := policydoc.NewContext("sts")
	ctx.Unsupplied("aws:SourceIp")
	s := sessionOf("111122223333", "app", "AROAAPPEXAMPLE000001", "s")
	got := decide(t, s, "sts:AssumeRole", on(implRole, "111122223333"), ctx, policySet{boundary: &boundary, session: []authz.Policy{session}, resource: trust})
	want := []authz.Unsupplied{
		{Key: "aws:SourceIp", PolicyType: authz.PermissionsBoundary, Policy: boundary.ARN},
		{Key: "aws:SourceIp", PolicyType: authz.SessionPolicy, Policy: "session"},
		{Key: "aws:SourceIp", PolicyType: authz.RoleTrust, Policy: ""},
	}
	if !sameUnsuppliedSet(got.Unsupplied, want) {
		t.Errorf("Unsupplied = %+v, want %+v", got.Unsupplied, want)
	}
	wantDecision(t, "trust resting on the unsupplied key", got.Decision, authz.ImplicitDeny{PolicyType: authz.RoleTrust})
}

// The bucket owner's root keeps bucket-policy management whatever the case
// of the action (iam-evaluation.md 5.2: actions are case-insensitive), and
// only on an S3 resource.
func TestDecideRootBucketPolicyExemptionMatchesActionsCaseInsensitively(t *testing.T) {
	const bucket = "arn:aws:s3:::locked"
	denyAll := policySet{resource: authz.Generic{Document: docOf(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"*","Resource":"*"}]}`)}}
	root := identity.Root{Account: account("111122223333")}
	wantDecision(t, "S3:deletebucketpolicy", decide(t, root, "S3:deletebucketpolicy", on(bucket, "111122223333"), nil, denyAll).Decision, authz.Allow{})
	wantDecision(t, "a queue", decide(t, root, "s3:DeleteBucketPolicy", on(implQueue, "111122223333"), nil, denyAll).Decision, authz.ExplicitDeny{PolicyType: authz.ResourceBased})
}

// Properties over drawn principals, resources and policy sets: Decide always
// decides, an unconditional Deny in an identity policy of an IAM principal
// decides as that identity policy unless a resource policy denies too
// (iam-evaluation.md 2.2 step 1, 12.4; IAM-EVAL-21), and
// the root in its own account with no Deny anywhere is allowed except on a
// role or a key (2.6).
func TestPropertyDecide(t *testing.T) {
	principals := []identity.Principal{
		identity.Root{Account: account("111122223333")},
		userOf("111122223333", "U", "AIDAUEXAMPLE00000001"),
		userOf("444455556666", "X", "AIDAXEXAMPLE00000001"),
		sessionOf("111122223333", "app", "AROAAPPEXAMPLE000001", "s"),
		identity.ServicePrincipal{Name: "sns.amazonaws.com"},
		identity.Anonymous{},
	}
	resources := []authz.Resource{on(implQueue, "111122223333"), on(implRole, "111122223333"), anyIn("111122223333"), on("arn:aws:s3:::b/k", "444455556666")}
	statements := []string{
		`{"Effect":"Allow","Action":"*","Resource":"*"}`,
		`{"Effect":"Allow","Action":"sqs:*","Resource":"*"}`,
		`{"Effect":"Deny","Action":"s3:*","Resource":"*"}`,
		`{"Effect":"Allow","Action":"*","Resource":"*","Condition":{"StringEquals":{"aws:username":"U"}}}`,
	}
	resourceStatements := []string{
		`{"Effect":"Allow","Principal":"*","Action":"*","Resource":"*"}`,
		`{"Effect":"Allow","Principal":{"AWS":"111122223333"},"Action":"*","Resource":"*"}`,
		`{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},"Action":"sqs:SendMessage","Resource":"*"}`,
		`{"Effect":"Deny","Principal":{"AWS":"arn:aws:iam::111122223333:user/U"},"Action":"sqs:*","Resource":"*"}`,
	}
	actions := []string{"sqs:SendMessage", "sts:AssumeRole", "s3:GetObject", "kms:Decrypt"}
	rapid.Check(t, func(rt *rapid.T) {
		policies := func(label string, pool []string) []authz.Policy {
			n := rapid.IntRange(0, 2).Draw(rt, label+" count")
			out := make([]authz.Policy, n)
			for i := range out {
				out[i] = inlinePolicy(rt, label, `{"Version":"2012-10-17","Statement":[`+rapid.SampledFrom(pool).Draw(rt, label)+`]}`)
			}
			return out
		}
		p := rapid.SampledFrom(principals).Draw(rt, "principal")
		res := rapid.SampledFrom(resources).Draw(rt, "resource")
		set := policySet{identity: policies("identity", statements), session: policies("session", statements)}
		if rp := policies("resource policy", resourceStatements); len(rp) > 0 {
			doc := rp[0].Document
			set.resource = rapid.SampledFrom([]authz.ResourcePolicy{authz.Generic{Document: doc}, authz.Trust{Document: doc}, authz.KeyPolicy{Document: doc}}).Draw(rt, "resource kind")
		}
		req := authz.Request{Principal: p, Action: actionOf(rapid.SampledFrom(actions).Draw(rt, "action")), Resource: res, Context: contextKeys()}
		got := authz.Decide(req, set.policies())
		if got.Decision == nil {
			rt.Fatalf("Decide(%+v, %+v) = nil", req, set)
		}

		withDeny := set
		withDeny.identity = append([]authz.Policy{inlinePolicy(rt, "deny", denyAll)}, set.identity...)
		switch p.(type) {
		case identity.User, identity.AssumedRole:
			// A resource policy's own Deny still names the denial
			// (IAM-EVAL-21, recorded 2026-11-03).
			want := authz.ExplicitDeny{PolicyType: authz.IdentityBased}
			if d, ok := got.Decision.(authz.ExplicitDeny); ok && d.PolicyType != authz.IdentityBased && d.PolicyType != authz.SessionPolicy && d.PolicyType != authz.PermissionsBoundary {
				want = d
			}
			if d := authz.Decide(req, withDeny.policies()).Decision; d != want {
				rt.Fatalf("with a Deny of everything first in the identity policies: %#v, want %#v", d, want)
			}
		case identity.Root, identity.ServicePrincipal, identity.Anonymous:
		}

		root, isRoot := p.(identity.Root)
		_, onRole := set.resource.(authz.Trust)
		_, onKey := set.resource.(authz.KeyPolicy)
		if _, denied := got.Decision.(authz.ExplicitDeny); isRoot && !denied && !onRole && !onKey && ownerAccount(res) == root.Account.String() {
			if _, ok := got.Decision.(authz.Allow); !ok {
				rt.Fatalf("root in its own account, no Deny: %#v, want Allow", got.Decision)
			}
		}
	})
}

func ownerAccount(r authz.Resource) string {
	switch r := r.(type) {
	case authz.OnResource:
		return r.Owner.String()
	case authz.AnyResource:
		return r.Account.String()
	}
	return ""
}
