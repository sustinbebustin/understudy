package policydoc_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// Per-document evaluation beyond the worked examples: which principal form
// a resource-policy statement matched (iam-evaluation.md 2.3, which the
// multi-policy decision needs), NotPrincipal, the request context, the
// keys understudy cannot supply (iam-enforcement-surfaces.md 12.2), and
// the choices the research leaves UNVERIFIED, each read from its switch.

const (
	acct        = "111122223333"
	sessionARN  = "arn:aws:sts::111122223333:assumed-role/examplerole/examplerolesessionname"
	exRoleARN   = "arn:aws:iam::111122223333:role/examplerole"
	exRoleID    = "AROAEXAMPLEROLEID0001"
	exUserARN   = "arn:aws:iam::111122223333:user/exampleuser"
	exUserID    = "AIDAEXAMPLEUSERID0001"
	acctRootARN = "arn:aws:iam::111122223333:root"
	sqsQueue    = "arn:aws:sqs:us-east-1:111122223333:q"
)

var (
	exSession = policydoc.SessionRequester{Account: acct, ARN: sessionARN, RoleARN: exRoleARN, RoleID: exRoleID}
	exUser    = policydoc.UserRequester{Account: acct, ARN: exUserARN, ID: exUserID}
	exRoot    = policydoc.RootRequester{Account: acct}
	snsSvc    = policydoc.ServiceRequester{Name: "sns.amazonaws.com"}
)

// queuePolicy is a 2012-10-17 resource policy of one Allow (or Deny)
// statement on sqs:SendMessage with the given principal element JSON and
// an optional ,"Condition":{...} suffix.
func queuePolicy(effect, principal, condition string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"` + effect + `",` + principal +
		`,"Action":"sqs:SendMessage","Resource":"` + sqsQueue + `"` + condition + `}]}`
}

// matchesOf evaluates doc as a resource policy for requester and returns
// the Matches of the one applying statement of effect, nil when none
// applies.
func matchesOf(t failer, doc string, requester policydoc.Requester, ctx *policydoc.Context, hasBoundary bool) (allows, denies []policydoc.Match) {
	t.Helper()
	r := policydoc.Evaluate(parseDoc(t, doc), policydoc.ResourcePolicy, policydoc.Request{
		Requester: requester, Action: "sqs:SendMessage", Resource: sqsQueue, Context: ctx, HasBoundary: hasBoundary,
	})
	if len(r.Allows) > 1 || len(r.Denies) > 1 {
		t.Fatalf("one statement applied more than once: %+v", r)
	}
	if len(r.Allows) == 1 {
		allows = r.Allows[0].Matches
	}
	if len(r.Denies) == 1 {
		denies = r.Denies[0].Matches
	}
	return allows, denies
}

// The principal forms of iam-evaluation.md 2.3 and 5.4: a session ARN, a
// user ARN or unique id, a role ARN or unique id (which names the role's
// sessions, iam-enforcement-surfaces.md 6.2), the account as an id or a
// root ARN, "*" with and without an aws:PrincipalArn condition, and a
// service. A statement naming the requester several ways lists every way,
// in the order the Match variants are declared.
func TestEvaluateReportsWhichPrincipalFormMatched(t *testing.T) {
	principalARN := `,"Condition":{"ArnEquals":{"aws:PrincipalArn":"` + exRoleARN + `"}}`
	withPrincipalARN := contextOf(one("aws:PrincipalArn", exRoleARN))
	cases := []struct {
		name      string
		principal string
		condition string
		requester policydoc.Requester
		ctx       *policydoc.Context
		want      []policydoc.Match
	}{
		{"the session ARN names that session", `"Principal":{"AWS":"` + sessionARN + `"}`, "", exSession, nil, []policydoc.Match{policydoc.MatchSession{}}},
		{"the role ARN names its session", `"Principal":{"AWS":"` + exRoleARN + `"}`, "", exSession, nil, []policydoc.Match{policydoc.MatchRole{}}},
		{"the role's unique id names its session", `"Principal":{"AWS":"` + exRoleID + `"}`, "", exSession, nil, []policydoc.Match{policydoc.MatchRole{}}},
		{"the user ARN names the user", `"Principal":{"AWS":"` + exUserARN + `"}`, "", exUser, nil, []policydoc.Match{policydoc.MatchUser{}}},
		{"the user's unique id names the user", `"Principal":{"AWS":"` + exUserID + `"}`, "", exUser, nil, []policydoc.Match{policydoc.MatchUser{}}},
		{"the account root ARN names a user of the account", `"Principal":{"AWS":"` + acctRootARN + `"}`, "", exUser, nil, []policydoc.Match{policydoc.MatchAccount{}}},
		{"the account id names a session of the account", `"Principal":{"AWS":"` + acct + `"}`, "", exSession, nil, []policydoc.Match{policydoc.MatchAccount{}}},
		{"the account root ARN names the root", `"Principal":{"AWS":"` + acctRootARN + `"}`, "", exRoot, nil, []policydoc.Match{policydoc.MatchAccount{}}},
		{`"*" names a user`, `"Principal":"*"`, "", exUser, nil, []policydoc.Match{policydoc.MatchWildcard{}}},
		{`{"AWS":"*"} names a session`, `"Principal":{"AWS":"*"}`, "", exSession, nil, []policydoc.Match{policydoc.MatchWildcard{}}},
		{
			`"*" with an aws:PrincipalArn condition names the principal`, `"Principal":"*"`, principalARN, exSession, withPrincipalARN,
			[]policydoc.Match{policydoc.MatchWildcard{PrincipalARNCondition: true}},
		},
		{"a Service value names that service", `"Principal":{"Service":"sns.amazonaws.com"}`, "", snsSvc, nil, []policydoc.Match{policydoc.MatchService{}}},
		{
			"a statement naming the session three ways lists each", `"Principal":{"AWS":["` + acct + `","` + exRoleARN + `","` + sessionARN + `"]}`, "", exSession, nil,
			[]policydoc.Match{policydoc.MatchSession{}, policydoc.MatchRole{}, policydoc.MatchAccount{}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			allows, _ := matchesOf(t, queuePolicy("Allow", c.principal, c.condition), c.requester, c.ctx, false)
			if !reflect.DeepEqual(allows, c.want) {
				t.Errorf("Matches = %#v, want %#v", allows, c.want)
			}
		})
	}
}

// Principal values name a principal exactly: no partial wildcards, user
// names case-sensitive, and another account's root does not name this
// account's principals (iam-evaluation.md 5.4, S-PRINCIPAL). A session ARN
// names only that session, not other sessions of the role (2.3).
func TestEvaluatePrincipalValuesNameExactly(t *testing.T) {
	cases := []struct {
		name      string
		principal string
		requester policydoc.Requester
	}{
		{"a wildcard inside a user ARN", `"Principal":{"AWS":"arn:aws:iam::111122223333:user/*"}`, exUser},
		{"a user name in another case", `"Principal":{"AWS":"arn:aws:iam::111122223333:user/ExampleUser"}`, exUser},
		{"another account's root", `"Principal":{"AWS":"arn:aws:iam::444455556666:root"}`, exUser},
		{"another session of the role", `"Principal":{"AWS":"arn:aws:sts::111122223333:assumed-role/examplerole/other"}`, exSession},
		{"another service", `"Principal":{"Service":"events.amazonaws.com"}`, snsSvc},
		{"a user ARN for a service", `"Principal":{"AWS":"` + exUserARN + `"}`, snsSvc},
		{"a user ARN for an anonymous caller", `"Principal":{"AWS":"` + exUserARN + `"}`, policydoc.AnonymousRequester{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if allows, _ := matchesOf(t, queuePolicy("Allow", c.principal, ""), c.requester, nil, false); allows != nil {
				t.Errorf("the statement applies with %#v, want it not to name %#v", allows, c.requester)
			}
		})
	}
}

// An anonymous caller is named only by "*" or {"AWS":"*"}, which are
// equivalent (iam-evaluation.md 2.10, S-PRINCIPAL #principal-anonymous).
func TestEvaluateWildcardNamesAnAnonymousCaller(t *testing.T) {
	for _, principal := range []string{`"Principal":"*"`, `"Principal":{"AWS":"*"}`} {
		allows, _ := matchesOf(t, queuePolicy("Allow", principal, ""), policydoc.AnonymousRequester{}, nil, false)
		if want := []policydoc.Match{policydoc.MatchWildcard{}}; !reflect.DeepEqual(allows, want) {
			t.Errorf("%s: Matches = %#v, want %#v", principal, allows, want)
		}
	}
}

// Whether "*" names a service principal is UNVERIFIED (iam-evaluation.md
// 2.9, IAM-EVAL-33): switch iam.wildcard-principal.service decides.
func TestEvaluateWildcardForAServiceFollowsItsSwitch(t *testing.T) {
	var want []policydoc.Match
	switch v := switchValue(t, "iam.wildcard-principal.service"); v {
	case "matches":
		want = []policydoc.Match{policydoc.MatchWildcard{}}
	case "excludes":
		want = nil
	default:
		t.Fatalf("iam.wildcard-principal.service = %q, a value this test does not know (iam-evaluation.md 12.3)", v)
	}
	allows, _ := matchesOf(t, queuePolicy("Allow", `"Principal":"*"`, ""), snsSvc, nil, false)
	if !reflect.DeepEqual(allows, want) {
		t.Errorf(`"*" for a service principal: Matches = %#v, want %#v`, allows, want)
	}
}

// A statement's principal element decides where it may apply: an identity
// policy never names a principal (S-PRINCIPAL "Identity-based policies
// cannot contain Principal", iam-enforcement-surfaces.md 6.2) and every
// resource-policy statement names one (S-GRAMMAR, iam-evaluation.md 5.1),
// so a statement of the wrong form applies to nothing. An identity-policy
// statement matches as MatchNoPrincipal.
func TestEvaluatePrincipalElementDecidesTheDocumentKind(t *testing.T) {
	withPrincipal := parseDoc(t, queuePolicy("Allow", `"Principal":"*"`, ""))
	without := parseDoc(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"`+sqsQueue+`"}]}`)
	req := policydoc.Request{Requester: exUser, Action: "sqs:SendMessage", Resource: sqsQueue}

	if r := policydoc.Evaluate(withPrincipal, policydoc.IdentityPolicy, req); len(r.Allows) != 0 {
		t.Errorf("an identity policy statement with a Principal applies: %+v", r)
	}
	if r := policydoc.Evaluate(without, policydoc.ResourcePolicy, req); len(r.Allows) != 0 {
		t.Errorf("a resource policy statement without a Principal applies: %+v", r)
	}
	r := policydoc.Evaluate(without, policydoc.IdentityPolicy, req)
	want := []policydoc.Applied{{Statement: policydoc.StatementRef{Index: 0}, Matches: []policydoc.Match{policydoc.MatchNoPrincipal{}}}}
	if !reflect.DeepEqual(r.Allows, want) || len(r.Denies) != 0 {
		t.Errorf("identity policy: Evaluate = %+v, want Allows %+v", r, want)
	}
}

// Allows and Denies list the applying statements in document order, each
// with its index and Sid (iam-evaluation.md 2.1: statements combine with a
// logical OR, so every applying statement counts).
func TestEvaluateListsApplyingStatementsInOrder(t *testing.T) {
	d := parseDoc(t, `{"Version":"2012-10-17","Statement":[`+
		`{"Sid":"First","Effect":"Allow","Action":"sqs:*","Resource":"*"},`+
		`{"Sid":"Other","Effect":"Allow","Action":"s3:*","Resource":"*"},`+
		`{"Effect":"Deny","Action":"sqs:Send*","Resource":"*"},`+
		`{"Sid":"Last","Effect":"Allow","Action":"sqs:SendMessage","Resource":"`+sqsQueue+`"}]}`)
	r := policydoc.Evaluate(d, policydoc.IdentityPolicy, policydoc.Request{Requester: exUser, Action: "sqs:SendMessage", Resource: sqsQueue})
	noPrincipal := []policydoc.Match{policydoc.MatchNoPrincipal{}}
	wantAllows := []policydoc.Applied{
		{Statement: policydoc.StatementRef{Index: 0, Sid: "First"}, Matches: noPrincipal},
		{Statement: policydoc.StatementRef{Index: 3, Sid: "Last"}, Matches: noPrincipal},
	}
	wantDenies := []policydoc.Applied{{Statement: policydoc.StatementRef{Index: 2}, Matches: noPrincipal}}
	if !reflect.DeepEqual(r.Allows, wantAllows) || !reflect.DeepEqual(r.Denies, wantDenies) {
		t.Errorf("Evaluate = %+v, want Allows %+v and Denies %+v", r, wantAllows, wantDenies)
	}
}

// NotPrincipal "must be used with Deny" (iam-evaluation.md 5.4,
// S-NOTPRIN): an Allow with NotPrincipal never applies. A Deny with
// NotPrincipal applies, as MatchNotPrincipal, to anyone it does not exempt.
// Which listed identities exempt a principal is UNVERIFIED (IAM-EVAL-18):
// switch iam.not-principal.match decides.
func TestEvaluateNotPrincipalFollowsItsSwitch(t *testing.T) {
	if allows, _ := matchesOf(t, queuePolicy("Allow", `"NotPrincipal":{"AWS":"arn:aws:iam::444455556666:root"}`, ""), exUser, nil, false); allows != nil {
		t.Errorf("an Allow with NotPrincipal applies with %#v, want never", allows)
	}

	mode := switchValue(t, "iam.not-principal.match")
	var exemptBoth, exemptAlone bool
	switch mode {
	case "account-and-principal":
		exemptBoth, exemptAlone = true, false
	case "any-listed":
		exemptBoth, exemptAlone = true, true
	default:
		t.Fatalf("iam.not-principal.match = %q, a value this test does not know (iam-evaluation.md 12.3)", mode)
	}
	notDenied := func(exempt bool) []policydoc.Match {
		if exempt {
			return nil
		}
		return []policydoc.Match{policydoc.MatchNotPrincipal{}}
	}
	cases := []struct {
		name      string
		listed    string
		requester policydoc.Requester
		want      []policydoc.Match
	}{
		{"a user listed with its account", `["` + exUserARN + `","` + acctRootARN + `"]`, exUser, notDenied(exemptBoth)},
		{"a user listed without its account", `["` + exUserARN + `"]`, exUser, notDenied(exemptAlone)},
		{"a session whose role is listed with its account", `["` + exRoleARN + `","` + acctRootARN + `"]`, exSession, notDenied(exemptBoth)},
		{"a user not listed", `["arn:aws:iam::111122223333:user/other","` + acctRootARN + `"]`, exUser, []policydoc.Match{policydoc.MatchNotPrincipal{}}},
		{"an anonymous caller", `["` + exUserARN + `","` + acctRootARN + `"]`, policydoc.AnonymousRequester{}, []policydoc.Match{policydoc.MatchNotPrincipal{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, denies := matchesOf(t, queuePolicy("Deny", `"NotPrincipal":{"AWS":`+c.listed+`}`, ""), c.requester, nil, false)
			if !reflect.DeepEqual(denies, c.want) {
				t.Errorf("iam.not-principal.match %s: Deny Matches = %#v, want %#v", mode, denies, c.want)
			}
		})
	}
}

// "The NotPrincipal element with a Deny effect will always deny any IAM
// principal that has a permissions boundary policy attached, regardless of
// the values specified in the NotPrincipal element" (iam-evaluation.md 2.8,
// vector S13).
func TestEvaluateNotPrincipalNeverExemptsABoundedPrincipal(t *testing.T) {
	listed := `"NotPrincipal":{"AWS":["` + exUserARN + `","` + exRoleARN + `","` + sessionARN + `","` + acctRootARN + `"]}`
	for _, requester := range []policydoc.Requester{exUser, exSession} {
		_, denies := matchesOf(t, queuePolicy("Deny", listed, ""), requester, nil, true)
		if want := []policydoc.Match{policydoc.MatchNotPrincipal{}}; !reflect.DeepEqual(denies, want) {
			t.Errorf("%#v with a boundary: Deny Matches = %#v, want %#v", requester, denies, want)
		}
	}
}

// A key AWS would send that understudy cannot compute evaluates as absent
// by the truth table of iam-evaluation.md 6.14, and Evaluate reports it once
// per statement that tested it, spelled as the document spells it, so the
// caller can journal it (iam-enforcement-surfaces.md 12.2; the spec's
// settled row 2: never assumed to hold). Every clause of a statement whose
// principal, action and resource matched is evaluated, so a second
// unsupplied key is reported even after the first clause failed.
func TestEvaluateReportsUnsuppliedKeys(t *testing.T) {
	d := parseDoc(t, `{"Version":"2012-10-17","Statement":[`+
		`{"Sid":"Positive","Effect":"Allow","Action":"*","Resource":"*","Condition":{"IpAddress":{"aws:SourceIP":"203.0.113.0/24"},"StringEquals":{"s3:prefix":"home/"}}},`+
		`{"Sid":"Negated","Effect":"Deny","Action":"*","Resource":"*","Condition":{"NotIpAddress":{"aws:SourceIp":"203.0.113.0/24"}}},`+
		`{"Sid":"Elsewhere","Effect":"Allow","Action":"sqs:*","Resource":"*","Condition":{"IpAddress":{"aws:SourceIp":"203.0.113.0/24"}}}]}`)
	ctx := policydoc.NewContext("s3")
	ctx.Unsupplied("aws:SourceIp")
	r := policydoc.Evaluate(d, policydoc.IdentityPolicy, policydoc.Request{Requester: exUser, Action: "s3:ListBucket", Resource: "arn:aws:s3:::b", Context: ctx})

	// As absent: the positive IpAddress fails, the negated one holds.
	if len(r.Allows) != 0 {
		t.Errorf("Allows = %+v, want none: an unsupplied key is absent for a positive operator", r.Allows)
	}
	if len(r.Denies) != 1 || r.Denies[0].Statement != (policydoc.StatementRef{Index: 1, Sid: "Negated"}) {
		t.Errorf("Denies = %+v, want statement 1: an unsupplied key is absent for a negated operator", r.Denies)
	}
	want := []policydoc.UnsuppliedKey{
		{Key: "aws:SourceIP", Statement: policydoc.StatementRef{Index: 0, Sid: "Positive"}},
		{Key: "s3:prefix", Statement: policydoc.StatementRef{Index: 0, Sid: "Positive"}},
		{Key: "aws:SourceIp", Statement: policydoc.StatementRef{Index: 1, Sid: "Negated"}},
	}
	if !sameUnsupplied(r.Unsupplied, want) {
		t.Errorf("Unsupplied = %+v, want %+v (the sqs statement's action does not match, so it is not reported)", r.Unsupplied, want)
	}
}

// sameUnsupplied compares as multisets: the order of the reports is not
// part of what Evaluate promises.
func sameUnsupplied(got, want []policydoc.UnsuppliedKey) bool {
	if len(got) != len(want) {
		return false
	}
	rest := slices.Clone(want)
	for _, g := range got {
		i := slices.Index(rest, g)
		if i < 0 {
			return false
		}
		rest = slices.Delete(rest, i, i+1)
	}
	return true
}

// A policy variable naming an unsupplied key has no value (7.3) and is
// reported like a condition key.
func TestEvaluateReportsUnsuppliedVariables(t *testing.T) {
	d := parseDoc(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"arn:aws:s3:::b/${aws:ResourceTag/team}/*"}]}`)
	ctx := policydoc.NewContext("s3")
	ctx.UnsuppliedFamily("aws:ResourceTag/")
	r := policydoc.Evaluate(d, policydoc.IdentityPolicy, policydoc.Request{Requester: exUser, Action: "s3:GetObject", Resource: "arn:aws:s3:::b/blue/k", Context: ctx})
	if len(r.Allows) != 0 {
		t.Errorf("Allows = %+v, want none: a Resource whose variable has no value matches nothing (iam-evaluation.md 7.3)", r.Allows)
	}
	want := []policydoc.UnsuppliedKey{{Key: "aws:ResourceTag/team", Statement: policydoc.StatementRef{Index: 0}}}
	if !reflect.DeepEqual(r.Unsupplied, want) {
		t.Errorf("Unsupplied = %+v, want %+v", r.Unsupplied, want)
	}
}

// The request context: key names match case-insensitively
// (iam-evaluation.md 6.2), a later call replaces an earlier one, a key of
// the action's own service never set is Unsupplied and any other key never
// set is Absent (contract of NewContext), and UnsuppliedFamily marks every
// unset key of a prefix.
func TestContextPresence(t *testing.T) {
	c := policydoc.NewContext("s3")
	check := func(key string, wantPresence policydoc.Presence, want policydoc.ContextValue) {
		t.Helper()
		got, presence := c.Lookup(key)
		same := slices.Equal(got.Values, want.Values) && got.Multivalued == want.Multivalued
		if presence != wantPresence || (presence == policydoc.Present && !same) {
			t.Errorf("Lookup(%q) = %+v, %d; want %+v, %d", key, got, presence, want, wantPresence)
		}
	}
	check("s3:prefix", policydoc.Unsupplied, policydoc.ContextValue{})
	check("aws:username", policydoc.Absent, policydoc.ContextValue{})
	check("sqs:MessageGroupId", policydoc.Absent, policydoc.ContextValue{})

	c.Set("aws:username", "bob")
	check("AWS:UserName", policydoc.Present, policydoc.ContextValue{Values: []string{"bob"}})
	c.SetMulti("aws:TagKeys")
	check("aws:tagkeys", policydoc.Present, policydoc.ContextValue{Values: []string{}, Multivalued: true})
	c.SetMulti("aws:TagKeys", "a", "b")
	check("aws:TagKeys", policydoc.Present, policydoc.ContextValue{Values: []string{"a", "b"}, Multivalued: true})
	c.Absent("s3:prefix")
	check("s3:prefix", policydoc.Absent, policydoc.ContextValue{})
	c.Unsupplied("aws:SourceIp")
	check("aws:sourceip", policydoc.Unsupplied, policydoc.ContextValue{})
	c.Set("aws:SourceIp", "203.0.113.5")
	check("aws:SourceIp", policydoc.Present, policydoc.ContextValue{Values: []string{"203.0.113.5"}})

	c.UnsuppliedFamily("aws:ResourceTag/")
	check("aws:ResourceTag/Owner", policydoc.Unsupplied, policydoc.ContextValue{})
	c.Set("aws:resourcetag/owner", "ana")
	check("aws:ResourceTag/Owner", policydoc.Present, policydoc.ContextValue{Values: []string{"ana"}})
	check("aws:RequestTag/Owner", policydoc.Absent, policydoc.ContextValue{})

	var none *policydoc.Context
	if _, p := none.Lookup("aws:username"); p != policydoc.Absent {
		t.Errorf("a nil context's Lookup = %d, want Absent", p)
	}
	if _, p := policydoc.NewContext("").Lookup("s3:prefix"); p != policydoc.Absent {
		t.Errorf(`NewContext("") Lookup of a service key = %d, want Absent`, p)
	}
}

// RequesterOf reads the identifiers an ARN carries: a root ARN is the
// account's root, a user ARN a user, an assumed-role ARN a session whose
// role ARN is arn:aws:iam::<account>:role/<role> (iam-evaluation.md 3.4).
func TestRequesterOf(t *testing.T) {
	cases := []struct {
		caller policydoc.Caller
		want   policydoc.Requester
	}{
		{policydoc.AWSCaller{ARN: acctRootARN}, policydoc.RootRequester{Account: acct}},
		{policydoc.AWSCaller{ARN: exUserARN}, policydoc.UserRequester{Account: acct, ARN: exUserARN}},
		{policydoc.AWSCaller{ARN: sessionARN}, policydoc.SessionRequester{Account: acct, ARN: sessionARN, RoleARN: exRoleARN}},
		{policydoc.ServiceCaller{Name: "sns.amazonaws.com"}, snsSvc},
		{policydoc.Anonymous{}, policydoc.AnonymousRequester{}},
		{nil, policydoc.AnonymousRequester{}},
	}
	for _, c := range cases {
		if got := policydoc.RequesterOf(c.caller); !reflect.DeepEqual(got, c.want) {
			t.Errorf("RequesterOf(%#v) = %#v, want %#v", c.caller, got, c.want)
		}
	}
}

// Variables exist only in 2012-10-17 documents: in 2008-10-17, or with no
// Version, ${aws:username} is literal text (iam-evaluation.md 5.5, S-VERSION,
// S-VARS #policy-vars-using-variables), in conditions as in resources.
func TestEvaluateVariablesAreLiteralOutside2012(t *testing.T) {
	for _, version := range []string{`"Version":"2008-10-17",`, ""} {
		d := parseDoc(t, `{`+version+`"Statement":[{"Effect":"Allow","Action":"*","Resource":"*","Condition":{"StringEquals":{"aws:ResourceTag/Owner":"${aws:username}"}}}]}`)
		applies := func(owner string) bool {
			r := policydoc.Evaluate(d, policydoc.IdentityPolicy, policydoc.Request{
				Requester: exUser, Action: "s3:GetObject", Resource: "arn:aws:s3:::b/k",
				Context: contextOf(one("aws:username", "richard"), one("aws:ResourceTag/Owner", owner)),
			})
			return len(r.Allows) == 1
		}
		if applies("richard") {
			t.Errorf("%q: the variable was substituted, want literal text", version)
		}
		if !applies("${aws:username}") {
			t.Errorf("%q: the literal text ${aws:username} does not match itself", version)
		}
	}
}

// The documented forms of the remaining operator families: a decimal
// numeric value ("to an integer or decimal value", S-OPS
// #Conditions_Numeric), a date in epoch seconds ("or in epoch (UNIX)
// time", #Conditions_Date), and every arithmetic comparison.
func TestEvaluateNumericAndDateForms(t *testing.T) {
	runCondVectors(t, "6.4 and 6.5", []condVector{
		{"decimal below", "Allow", `{"NumericLessThan":{"s3:max-keys":"10.5"}}`, []kv{one("s3:max-keys", "10")}, true},
		{"decimal above", "Allow", `{"NumericLessThan":{"s3:max-keys":"10.5"}}`, []kv{one("s3:max-keys", "11")}, false},
		{"equals", "Allow", `{"NumericEquals":{"s3:max-keys":"10"}}`, []kv{one("s3:max-keys", "10")}, true},
		{"greater than", "Allow", `{"NumericGreaterThan":{"s3:max-keys":"10"}}`, []kv{one("s3:max-keys", "10")}, false},
		{"greater than or equal", "Allow", `{"NumericGreaterThanEquals":{"s3:max-keys":"10"}}`, []kv{one("s3:max-keys", "10")}, true},
		{"epoch policy value", "Allow", `{"DateGreaterThan":{"aws:CurrentTime":"1577836801"}}`, []kv{one("aws:CurrentTime", "2020-06-01T00:00:00Z")}, true},
		{"date less than", "Allow", `{"DateLessThan":{"aws:CurrentTime":"2020-01-01T00:00:00Z"}}`, []kv{one("aws:CurrentTime", "2020-06-01T00:00:00Z")}, false},
		{"date equals", "Allow", `{"DateEquals":{"aws:CurrentTime":"2020-06-01T00:00:00Z"}}`, []kv{one("aws:CurrentTime", "2020-06-01T00:00:00Z")}, true},
		{"arn like across the resource", "Allow", `{"ArnLike":{"aws:SourceArn":"arn:aws:s3:::uploads*"}}`, []kv{one("aws:SourceArn", "arn:aws:s3:::uploads-2026")}, true},
		{"arn case", "Allow", `{"ArnEquals":{"aws:SourceArn":"arn:aws:s3:::Uploads"}}`, []kv{one("aws:SourceArn", "arn:aws:s3:::uploads")}, false},
	})
}

// Bool with a policy variable: S-OPS lists variables as supported, S-VARS
// excludes them (iam-evaluation.md 6.6, 11 C3, IAM-EVAL-01); switch
// iam.condition.bool-variables decides.
func TestEvaluateBoolVariablesFollowTheirSwitch(t *testing.T) {
	var want bool
	switch v := switchValue(t, "iam.condition.bool-variables"); v {
	case "substituted":
		want = true
	case "literal":
		want = false
	default:
		t.Fatalf("iam.condition.bool-variables = %q, a value this test does not know (iam-evaluation.md 12.3)", v)
	}
	ctx := contextOf(one("aws:SecureTransport", "true"), one("aws:PrincipalTag/secure", "true"))
	if got := condHolds(t, "Allow", `{"Bool":{"aws:SecureTransport":"${aws:PrincipalTag/secure}"}}`, ctx); got != want {
		t.Errorf("Bool with a variable resolving to true: applies = %t, want %t", got, want)
	}
}

// A multivalued key under a plain operator is UNVERIFIED (iam-evaluation.md
// 6.12, IAM-EVAL-02): switch iam.condition.multivalued-plain-operator.
func TestEvaluateMultivaluedPlainOperatorFollowsItsSwitch(t *testing.T) {
	var someMatch, allMatch bool
	switch v := switchValue(t, "iam.condition.multivalued-plain-operator"); v {
	case "any-value":
		someMatch, allMatch = true, true
	case "all-values":
		someMatch, allMatch = false, true
	case "no-match":
		someMatch, allMatch = false, false
	default:
		t.Fatalf("iam.condition.multivalued-plain-operator = %q, a value this test does not know (iam-evaluation.md 12.3)", v)
	}
	const cond = `{"StringEquals":{"aws:TagKeys":["environment","team"]}}`
	if got := condHolds(t, "Allow", cond, contextOf(many("aws:TagKeys", "environment", "dept"))); got != someMatch {
		t.Errorf("one of two values matches: applies = %t, want %t", got, someMatch)
	}
	if got := condHolds(t, "Allow", cond, contextOf(many("aws:TagKeys", "environment", "team"))); got != allMatch {
		t.Errorf("both values match: applies = %t, want %t", got, allMatch)
	}
}

// Null on a present empty string is UNVERIFIED (iam-evaluation.md 6.11,
// IAM-EVAL-05): switch iam.condition.empty-value.
func TestEvaluateNullOnAnEmptyValueFollowsItsSwitch(t *testing.T) {
	var isNull bool
	switch v := switchValue(t, "iam.condition.empty-value"); v {
	case "null":
		isNull = true
	case "value":
		isNull = false
	default:
		t.Fatalf("iam.condition.empty-value = %q, a value this test does not know (iam-evaluation.md 12.3)", v)
	}
	empty := contextOf(one("aws:SourceIdentity", ""))
	if got := condHolds(t, "Allow", `{"Null":{"aws:SourceIdentity":"true"}}`, empty); got != isNull {
		t.Errorf(`Null "true" on "": applies = %t, want %t`, got, isNull)
	}
	if got := condHolds(t, "Allow", `{"Null":{"aws:SourceIdentity":"false"}}`, empty); got == isNull {
		t.Errorf(`Null "false" on "": applies = %t, want %t`, got, !isNull)
	}
}

// An IPv6 value without a prefix length is UNVERIFIED (iam-evaluation.md
// 6.8, 11 C8, IAM-EVAL-07): switch iam.condition.ipv6-default-prefix.
// An IPv4 value without one is /32, as documented.
func TestEvaluateAddressesWithoutAPrefix(t *testing.T) {
	var wideMatches bool
	switch v := switchValue(t, "iam.condition.ipv6-default-prefix"); v {
	case "128":
		wideMatches = false
	case "32":
		wideMatches = true
	default:
		t.Fatalf("iam.condition.ipv6-default-prefix = %q, a value this test does not know (iam-evaluation.md 12.3)", v)
	}
	runCondVectors(t, "6.8", []condVector{
		{"ipv4 itself", "Allow", `{"IpAddress":{"aws:SourceIp":"203.0.113.7"}}`, []kv{one("aws:SourceIp", "203.0.113.7")}, true},
		{"ipv4 neighbour", "Allow", `{"IpAddress":{"aws:SourceIp":"203.0.113.7"}}`, []kv{one("aws:SourceIp", "203.0.113.8")}, false},
		{"ipv6 itself", "Allow", `{"IpAddress":{"aws:SourceIp":"2001:db8::1"}}`, []kv{one("aws:SourceIp", "2001:db8::1")}, true},
		{"ipv6 in the same /32", "Allow", `{"IpAddress":{"aws:SourceIp":"2001:db8::1"}}`, []kv{one("aws:SourceIp", "2001:db8:ffff::2")}, wideMatches},
	})
}

// A NotResource entry whose variable has no value is UNVERIFIED
// (iam-evaluation.md 7.3, IAM-EVAL-13); it follows iam.variables.unresolved
// like any unresolved value: under value-null the entry matches nothing,
// so it excludes nothing.
func TestEvaluateUnresolvedNotResourceFollowsItsSwitch(t *testing.T) {
	var want bool
	switch v := switchValue(t, "iam.variables.unresolved"); v {
	case "value-null":
		want = true
	case "statement-void":
		want = false
	default:
		t.Fatalf("iam.variables.unresolved = %q, a value this test does not know (iam-evaluation.md 12.3)", v)
	}
	d := parseDoc(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:*","NotResource":"arn:aws:s3:::home/${aws:username}/*"}]}`)
	r := policydoc.Evaluate(d, policydoc.IdentityPolicy, policydoc.Request{Requester: exUser, Action: "s3:GetObject", Resource: "arn:aws:s3:::home/ana/k", Context: contextOf()})
	if got := len(r.Denies) == 1; got != want {
		t.Errorf("Deny NotResource with an unresolved variable applies = %t, want %t", got, want)
	}
}

// A request on no resource ("*") is matched by a Resource of "*" and not by
// an ARN pattern, even one ending in a wildcard: an action with no resource
// type needs "*" in the Resource element (Service Authorization Reference
// conventions, https://docs.aws.amazon.com/service-authorization/latest/reference/reference_policies_actions-resources-contextkeys.html).
func TestEvaluateRequestOnNoResource(t *testing.T) {
	cases := []struct {
		pattern string
		want    bool
	}{
		{"*", true},
		{"arn:aws:sqs:*:*:*", false},
	}
	for _, c := range cases {
		d := parseDoc(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:ListQueues","Resource":"`+c.pattern+`"}]}`)
		r := policydoc.Evaluate(d, policydoc.IdentityPolicy, policydoc.Request{Requester: exUser, Action: "sqs:ListQueues", Resource: "*"})
		if got := len(r.Allows) == 1; got != c.want {
			t.Errorf("Resource %s on a request on no resource applies = %t, want %t", c.pattern, got, c.want)
		}
	}
}
