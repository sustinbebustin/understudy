package policydoc_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// The worked examples of iam-evaluation.md 9 that one document decides:
// condition operators (J), several keys and values (K), multivalued keys
// (L, M), single-valued keys (N1 to N4), key and value case (P), policy
// variables (Q), action and resource patterns (R) and the NotAction and
// NotResource examples (S1 to S12). Each row cites its vector id; the
// section of 9 that lists it names the AWS page anchor it comes from.
//
// A condition vector is policydoc.Evaluate of a one-statement 2012-10-17
// identity policy {Effect, Action "*", Resource "*", Condition} with the
// vector's request context; cond=true means the statement applies, so it
// is in Allows (Denies for a Deny vector). A pattern vector puts the
// pattern in Action or Resource and "*" in the other.

// evalUser is the requester of the identity-policy vectors. An identity
// policy has no principal element, so who asks changes nothing
// (iam-evaluation.md 5.1).
var evalUser = policydoc.UserRequester{Account: "111122223333", ARN: "arn:aws:iam::111122223333:user/vector", ID: "AIDAVECTOREXAMPLE0001"}

// kv is one request context key: single-valued unless multi.
type kv struct {
	key   string
	vals  []string
	multi bool
}

func one(key, value string) kv             { return kv{key: key, vals: []string{value}} }
func many(key string, values ...string) kv { return kv{key: key, vals: values, multi: true} }

// contextOf is a request context holding kvs, every other key absent.
func contextOf(kvs ...kv) *policydoc.Context {
	c := policydoc.NewContext("")
	for _, e := range kvs {
		if e.multi {
			c.SetMulti(e.key, e.vals...)
			continue
		}
		c.Set(e.key, e.vals[0])
	}
	return c
}

// failer is what the helpers need of a *testing.T or a *rapid.T.
type failer interface {
	Helper()
	Fatalf(format string, args ...any)
}

func parseDoc(t failer, text string) policydoc.Document {
	t.Helper()
	doc, err := policydoc.Parse([]byte(text))
	if err != nil {
		t.Fatalf("parse %s: %v", text, err)
	}
	return doc
}

// conditionDoc is the condition vectors' document.
func conditionDoc(effect, condition string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"` + effect + `","Action":"*","Resource":"*","Condition":` + condition + `}]}`
}

// condHolds reports whether the one statement of conditionDoc applies
// under ctx.
func condHolds(t failer, effect, condition string, ctx *policydoc.Context) bool {
	t.Helper()
	r := policydoc.Evaluate(parseDoc(t, conditionDoc(effect, condition)), policydoc.IdentityPolicy, policydoc.Request{
		Requester: evalUser, Action: "s3:ListBucket", Resource: "arn:aws:s3:::vector-bucket", Context: ctx,
	})
	applied, wrong := r.Allows, r.Denies
	if effect == "Deny" {
		applied, wrong = r.Denies, r.Allows
	}
	if len(wrong) != 0 || len(applied) > 1 {
		t.Fatalf("Evaluate of one %s statement = %+v, want it in %ss at most once", effect, r, effect)
	}
	return len(applied) == 1
}

// switchValue is the value of the evaluation switch name, which governs a
// behavior the research leaves UNVERIFIED (iam-evaluation.md 12.3).
func switchValue(t failer, name string) string {
	t.Helper()
	for _, e := range authz.Switches() {
		if e.Name == name {
			return e.Value
		}
	}
	t.Fatalf("authz.Switches() registers no %s (iam-evaluation.md 12.3)", name)
	return ""
}

type condVector struct {
	id     string
	effect string
	cond   string
	ctx    []kv
	want   bool
}

func runCondVectors(t *testing.T, section string, vectors []condVector) {
	t.Helper()
	for _, v := range vectors {
		t.Run(v.id, func(t *testing.T) {
			if got := condHolds(t, v.effect, v.cond, contextOf(v.ctx...)); got != v.want {
				t.Errorf("iam-evaluation.md %s vector %s: %s %s under %+v applies = %t, want %t", section, v.id, v.effect, v.cond, v.ctx, got, v.want)
			}
		})
	}
}

// J: condition operators, iam-evaluation.md 9.10 (S-OPS, one anchor per
// operator family). J14 to J16 carry the derived condition of 11 C1.
func TestEvaluateConditionOperatorVectors(t *testing.T) {
	const (
		jobCategory = `{"StringEquals":{"aws:PrincipalTag/job-category":"iamuser-admin"}}`
		homePrefix  = `{"StringLike":{"s3:prefix":["home/","home/${aws:username}/"]}}`
		envTags     = `{"StringEqualsIgnoreCase":{"aws:RequestTag/environment":["preprod","storage"]},"ForAnyValue:StringEquals":{"aws:TagKeys":"environment"}}`
		orgPaths    = `{"ForAllValues:StringNotLike":{"aws:ResourceOrgPaths":["o-acorg/r-acroot/ou-acroot-mediaou/","o-acorg/r-acroot/ou-acroot-sportsou/*"]}}`
		maxKeys     = `{"NumericLessThanEquals":{"s3:max-keys":"10"}}`
		issued      = `{"DateGreaterThan":{"aws:TokenIssueTime":"2020-01-01T00:00:01Z"}}`
		insecure    = `{"Bool":{"aws:SecureTransport":"false"}}`
		binary      = `{"BinaryEquals":{"key":"QmluYXJ5VmFsdWVJbkJhc2U2NA=="}}`
		ipv4        = `{"IpAddress":{"aws:SourceIp":"203.0.113.0/24"}}`
		ipMixed     = `{"IpAddress":{"aws:SourceIp":["203.0.113.0/24","2001:DB8:1234:5678::/64"]}}`
		topicARN    = "arn:aws:sns:us-west-2:123456789012:TOPIC-ID"
		sourceARN   = `{"ArnEquals":{"aws:SourceArn":"` + topicARN + `"}}`
		logArns     = `{"ForAllValues:ArnLike":{"logs:LogGeneratingResourceArns":["arn:aws:cloudfront::123456789012:distribution/*","arn:aws:cloudfront::123456789012:distribution/support*"]}}`
		typeIfAny   = `{"StringLikeIfExists":{"ec2:InstanceType":["t1.*","t2.*","m3.*"]}}`
		typeLike    = `{"StringLike":{"ec2:InstanceType":["t1.*","t2.*","m3.*"]}}`
		issuedSet   = `{"Null":{"aws:TokenIssueTime":"false"}}`
		dist        = "arn:aws:cloudfront::123456789012:distribution/"
	)
	runCondVectors(t, "9.10", []condVector{
		{"J1", "Allow", jobCategory, []kv{one("aws:PrincipalTag/job-category", "iamuser-admin")}, true},
		{"J2", "Allow", jobCategory, []kv{one("aws:PrincipalTag/job-category", "dev-ops")}, false},
		{"J3", "Allow", jobCategory, nil, false},
		// J4, J5: the values become home/ and home/<username>/.
		{"J4 own home", "Allow", homePrefix, []kv{one("aws:username", "martha_rivera"), one("s3:prefix", "home/martha_rivera/")}, true},
		{"J4 another home", "Allow", homePrefix, []kv{one("aws:username", "martha_rivera"), one("s3:prefix", "home/nikki_wolf/")}, false},
		{"J5 own home", "Allow", homePrefix, []kv{one("aws:username", "nikki_wolf"), one("s3:prefix", "home/nikki_wolf/")}, true},
		{"J5 another home", "Allow", homePrefix, []kv{one("aws:username", "nikki_wolf"), one("s3:prefix", "home/martha_rivera/")}, false},
		{"J6", "Allow", homePrefix, []kv{one("s3:prefix", "home/x/")}, false},
		{"J7", "Allow", envTags, []kv{many("aws:TagKeys", "environment"), one("aws:RequestTag/environment", "preprod")}, true},
		{"J8", "Allow", envTags, []kv{many("aws:TagKeys", "environment", "costcenter"), one("aws:RequestTag/environment", "PreProd")}, true},
		{"J9", "Allow", envTags, []kv{many("aws:TagKeys", "Environment"), one("aws:RequestTag/Environment", "preprod")}, false},
		{"J10", "Allow", envTags, []kv{many("aws:TagKeys", "costcenter"), one("aws:RequestTag/environment", "preprod")}, false},
		{"J11", "Allow", envTags, []kv{one("aws:RequestTag/environment", "storage")}, false},
		{"J12", "Allow", envTags, []kv{many("aws:TagKeys", "environment")}, false},
		{"J13", "Allow", envTags, nil, false},
		{"J14", "Deny", orgPaths, []kv{many("aws:ResourceOrgPaths", "o-acorg/r-acroot/ou-acroot-sportsou/costcenter/")}, false},
		{"J15", "Deny", orgPaths, []kv{many("aws:ResourceOrgPaths", "o-acorg/r-acroot/ou-acroot-mediaou/costcenter/")}, true},
		{"J16", "Deny", orgPaths, nil, true},
		{"J17", "Allow", maxKeys, []kv{one("s3:max-keys", "10")}, true},
		{"J18", "Allow", maxKeys, []kv{one("s3:max-keys", "11")}, false},
		{"J19", "Allow", issued, nil, false},
		{"J20", "Allow", issued, []kv{one("aws:TokenIssueTime", "2020-06-01T00:00:00Z")}, true},
		{"J21", "Deny", insecure, []kv{one("aws:SecureTransport", "false")}, true},
		{"J22", "Deny", insecure, []kv{one("aws:SecureTransport", "true")}, false},
		{"J23", "Deny", insecure, nil, false},
		{"J24", "Allow", binary, []kv{one("key", "QmluYXJ5VmFsdWVJbkJhc2U2NA==")}, true},
		{"J25", "Allow", binary, []kv{one("key", "ASIAIOSFODNN7EXAMPLE")}, false},
		{"J26", "Allow", binary, nil, false},
		{"J27", "Allow", ipv4, []kv{one("aws:SourceIp", "203.0.113.1")}, true},
		{"J28", "Allow", ipv4, []kv{one("aws:SourceIp", "198.51.100.1")}, false},
		{"J29", "Allow", ipMixed, []kv{one("aws:SourceIp", "2001:db8:1234:5678::1")}, true},
		{"J30", "Allow", sourceARN, []kv{one("aws:SourceArn", topicARN)}, true},
		{"J31", "Allow", sourceARN, []kv{one("aws:SourceArn", "arn:aws:sns:us-west-2:777788889999:TOPIC-ID")}, false},
		{"J32", "Allow", sourceARN, nil, false},
		{"J33", "Allow", logArns, []kv{many("logs:LogGeneratingResourceArns", dist+"costcenter")}, true},
		{"J34", "Allow", logArns, []kv{many("logs:LogGeneratingResourceArns", dist+"costcenter", dist+"support2025")}, true},
		{"J35", "Allow", logArns, []kv{many("logs:LogGeneratingResourceArns", dist+"costcenter", dist+"admin")}, true},
		{"J36", "Allow", logArns, []kv{many("logs:LogGeneratingResourceArns", "arn:aws:cloudfront::777788889999:distribution/costcenter")}, false},
		{"J37", "Allow", logArns, nil, true},
		{"J38", "Allow", typeIfAny, []kv{one("ec2:InstanceType", "t1.micro")}, true},
		{"J39", "Allow", typeIfAny, []kv{one("ec2:InstanceType", "m2.micro")}, false},
		{"J40", "Allow", typeIfAny, nil, true},
		{"J41", "Allow", typeLike, nil, false},
		{"J42", "Allow", issuedSet, []kv{one("aws:TokenIssueTime", "2020-06-01T00:00:00Z")}, true},
		{"J43", "Allow", issuedSet, nil, false},
	})
}

// J6 with s3:prefix home/: the first value needs no variable, the second
// has none to resolve. Whether the unresolved value voids the clause or
// only itself is UNVERIFIED (iam-evaluation.md 7.3, IAM-EVAL-16), so the
// answer follows switch iam.variables.unresolved.
func TestEvaluateUnresolvedValueFollowsItsSwitch(t *testing.T) {
	const homePrefix = `{"StringLike":{"s3:prefix":["home/","home/${aws:username}/"]}}`
	var want bool
	switch v := switchValue(t, "iam.variables.unresolved"); v {
	case "value-null":
		want = true // the unresolved value matches nothing; home/ still matches
	case "statement-void":
		want = false
	default:
		t.Fatalf("iam.variables.unresolved = %q, a value this test does not know (iam-evaluation.md 12.3)", v)
	}
	if got := condHolds(t, "Allow", homePrefix, contextOf(one("s3:prefix", "home/"))); got != want {
		t.Errorf("J6 with s3:prefix home/: applies = %t, want %t under iam.variables.unresolved", got, want)
	}
}

// K: several keys and values, iam-evaluation.md 9.11 (S-MULTI).
func TestEvaluateMultipleKeysAndValuesVectors(t *testing.T) {
	const (
		tags  = `"StringEquals":{"aws:PrincipalTag/department":["finance","hr","legal"],"aws:PrincipalTag/role":["audit","security"]}`
		users = `{"aws:PrincipalArn":["arn:aws:iam::222222222222:user/Ana","arn:aws:iam::222222222222:user/Mary"]}`
		and   = `{` + tags + `,"ArnLike":` + users + `}`
		nor   = `{` + tags + `,"ArnNotLike":` + users + `}`
		mary  = "arn:aws:iam::222222222222:user/Mary"
		nikki = "arn:aws:iam::222222222222:user/Nikki"
	)
	dept := func(v string) kv { return one("aws:PrincipalTag/department", v) }
	role := func(v string) kv { return one("aws:PrincipalTag/role", v) }
	arn := func(v string) kv { return one("aws:PrincipalArn", v) }
	runCondVectors(t, "9.11", []condVector{
		{"K1", "Allow", and, []kv{dept("legal"), role("audit"), arn(mary)}, true},
		{"K2", "Allow", and, []kv{dept("hr"), role("audit"), arn(nikki)}, false},
		{"K3", "Allow", and, []kv{dept("hr"), role("payroll"), arn(mary)}, false},
		{"K4", "Allow", and, []kv{dept("hr"), arn(mary)}, false},
		{"K5", "Allow", and, []kv{arn(mary)}, false},
		{"K6", "Allow", nor, []kv{dept("legal"), role("audit"), arn(nikki)}, true},
		{"K7", "Allow", nor, []kv{dept("hr"), role("audit"), arn(mary)}, false},
		{"K8", "Allow", nor, []kv{dept("hr"), role("payroll"), arn(nikki)}, false},
		{"K9", "Allow", nor, []kv{dept("hr"), arn(nikki)}, false},
		{"K10", "Allow", nor, []kv{arn(nikki)}, false},
	})
}

// L, M: multivalued keys, iam-evaluation.md 9.12 (S-SETS, S-MVEX).
func TestEvaluateMultivaluedKeyVectors(t *testing.T) {
	const (
		all   = `{"ForAllValues:StringEquals":{"aws:TagKeys":["environment","cost-center"]},"Null":{"aws:TagKeys":"false"}}`
		anyOf = `{"ForAnyValue:StringEquals":{"aws:TagKeys":["environment","cost-center"]}}`
		key1  = `{"ForAllValues:StringNotLike":{"aws:TagKeys":"key1*"}}`
		key2  = `{"ForAllValues:StringNotLike":{"aws:TagKeys":["key1*","key2*"]}}`
		snap  = `{"ForAnyValue:StringEquals":{"aws:TagKeys":"webserver"}}`
	)
	keys := func(v ...string) []kv { return []kv{many("aws:TagKeys", v...)} }
	runCondVectors(t, "9.12", []condVector{
		{"L1", "Allow", all, keys("environment"), true},
		{"L2", "Allow", all, keys("cost-center"), true},
		{"L3", "Allow", all, keys("environment", "cost-center"), true},
		{"L4", "Allow", all, keys("environment", "dept"), false},
		{"L5", "Allow", all, nil, false},
		{"L6", "Allow", anyOf, keys("environment"), true},
		{"L7", "Allow", anyOf, keys("cost-center"), true},
		{"L8", "Allow", anyOf, keys("environment", "cost-center"), true},
		{"L9", "Allow", anyOf, keys("environment", "dept"), true},
		{"L10", "Allow", anyOf, keys("dept"), false},
		{"L11", "Allow", anyOf, nil, false},
		{"M1", "Deny", key1, keys("key1:legal"), false},
		{"M2", "Deny", key1, keys("key1:hr", "key1:personnel"), false},
		{"M3", "Deny", key1, keys("key2:audit"), true},
		{"M4", "Deny", key1, nil, true},
		{"M5", "Deny", key2, keys("key1:legal"), false},
		{"M6", "Deny", key2, keys("key1:hr", "key1:personnel"), false},
		{"M7", "Deny", key2, keys("key1:hr", "key2:audit"), false},
		{"M8", "Deny", key2, keys("key3:legal"), true},
		{"M9", "Deny", key2, nil, true},
		{"M10", "Deny", snap, keys("webserver"), true},
		{"M11", "Deny", snap, keys("environment", "webserver", "test"), true},
		{"M12", "Deny", snap, keys("environment", "test"), false},
		{"M13", "Deny", snap, nil, false},
	})
}

// N1 to N4: single-valued keys, iam-evaluation.md 9.13 (S-SVEX).
func TestEvaluateSingleValuedKeyVectors(t *testing.T) {
	const (
		ecs     = `{"StringEquals":{"aws:RequestTag/environment":["production","prod-backup"]},"ArnEquals":{"ecs:cluster":["arn:aws:ecs:us-east-1:111122223333:cluster/default1","arn:aws:ecs:us-east-1:111122223333:cluster/default2"]}}`
		cluster = "arn:aws:ecs:us-east-1:111122223333:cluster/"
	)
	runCondVectors(t, "9.13", []condVector{
		{"N1", "Allow", ecs, []kv{one("aws:RequestTag/environment", "production"), one("ecs:cluster", cluster+"default1")}, true},
		{"N2", "Allow", ecs, []kv{one("aws:RequestTag/environment", "prod-backup"), one("ecs:cluster", cluster+"default2")}, true},
		{"N3", "Allow", ecs, []kv{one("aws:RequestTag/webserver", "production"), one("ecs:cluster", cluster+"default2")}, false},
		{"N4", "Allow", ecs, []kv{one("ecs:cluster", cluster+"default2")}, false},
	})
}

// P: key and value case, iam-evaluation.md 9.14 (S-COND, S-ABAC, S-OPS).
// P8: the policy's aws:ResourceTag/Owner names the request's tag key owner,
// since key names are not case-sensitive (6.2, 6.13).
func TestEvaluateKeyAndValueCaseVectors(t *testing.T) {
	const (
		john     = `{"StringEquals":{"aws:username":"john"}}`
		johnFold = `{"StringEqualsIgnoreCase":{"aws:username":"john"}}`
		sourceIP = `{"IpAddress":{"aws:SourceIP":"203.0.113.0/24"}}`
		mfaAge   = `{"NumericLessThanEquals":{"aws:MultiFactorAuthAge":"3600"}}`
		owner    = `{"StringEquals":{"aws:ResourceTag/Owner":"${aws:username}"}}`
	)
	runCondVectors(t, "9.14", []condVector{
		{"P1", "Allow", john, []kv{one("aws:username", "john")}, true},
		{"P2", "Allow", john, []kv{one("aws:username", "John")}, false},
		{"P3", "Allow", johnFold, []kv{one("aws:username", "John")}, true},
		{"P4", "Allow", sourceIP, []kv{one("aws:SourceIp", "203.0.113.5")}, true},
		{"P5", "Allow", mfaAge, []kv{one("aws:MultiFactorAuthAge", "2800")}, true},
		{"P6", "Allow", mfaAge, nil, false},
		{"P7", "Allow", owner, []kv{one("aws:username", "richard"), one("aws:ResourceTag/Owner", "richard")}, true},
		{"P8", "Allow", owner, []kv{one("aws:username", "richard"), one("aws:ResourceTag/owner", "richard")}, true},
		{"P9", "Allow", owner, []kv{one("aws:username", "richard"), one("aws:ResourceTag/Owner", "Richard")}, false},
	})
}

// resourceApplies reports whether {Allow, Action "*", Resource pattern} of
// the given Version element (`"Version":"...",` or "") applies to resource.
func resourceApplies(t failer, version, pattern, resource string, ctx *policydoc.Context) bool {
	t.Helper()
	d := parseDoc(t, `{`+version+`"Statement":[{"Effect":"Allow","Action":"*","Resource":"`+pattern+`"}]}`)
	r := policydoc.Evaluate(d, policydoc.IdentityPolicy, policydoc.Request{Requester: evalUser, Action: "s3:GetObject", Resource: resource, Context: ctx})
	return len(r.Allows) == 1
}

// actionApplies reports whether {Allow, Action pattern, Resource "*"}
// applies to action.
func actionApplies(t failer, pattern, action string) bool {
	t.Helper()
	d := parseDoc(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"`+pattern+`","Resource":"*"}]}`)
	r := policydoc.Evaluate(d, policydoc.IdentityPolicy, policydoc.Request{Requester: evalUser, Action: action, Resource: "arn:aws:s3:::vector-bucket"})
	return len(r.Allows) == 1
}

const v2012 = `"Version":"2012-10-17",`

// Q: policy variables, iam-evaluation.md 9.15 (S-VARS, S-RESOURCE,
// S-VERSION, S-ABAC).
func TestEvaluatePolicyVariableVectors(t *testing.T) {
	const table = "arn:aws:dynamodb:us-east-2:111122223333:table/"
	resources := []struct {
		id       string
		version  string
		pattern  string
		ctx      []kv
		resource string
		want     bool
	}{
		{"Q1", v2012, table + "${aws:username}", []kv{one("aws:username", "bob")}, table + "bob", true},
		{"Q2", v2012, table + "${aws:username}", []kv{one("aws:username", "bob")}, table + "alice", false},
		{"Q3", `"Version":"2008-10-17",`, table + "${aws:username}", []kv{one("aws:username", "bob")}, table + "bob", false},
		{"Q4", "", table + "${aws:username}", []kv{one("aws:username", "bob")}, table + "bob", false},
		{"Q7", v2012, "arn:aws:s3:::amzn-s3-demo-bucket-${aws:PrincipalTag/team, 'company-wide'}", []kv{one("aws:PrincipalTag/team", "yellow")}, "arn:aws:s3:::amzn-s3-demo-bucket-yellow", true},
		{"Q8", v2012, "arn:aws:s3:::amzn-s3-demo-bucket-${aws:PrincipalTag/team, 'company-wide'}", nil, "arn:aws:s3:::amzn-s3-demo-bucket-company-wide", true},
		{"Q9", v2012, "arn:aws:s3:::amzn-s3-demo-bucket/${aws:PrincipalTag/team}/*", nil, "arn:aws:s3:::amzn-s3-demo-bucket/x/report.txt", false},
		{"Q10", v2012, "arn:aws:sqs:us-east-2:111122223333:${aws:PrincipalTag/team}-queue", []kv{one("aws:PrincipalTag/team", "qa")}, "arn:aws:sqs:us-east-2:111122223333:qa-queue", true},
		{"Q13 escape", v2012, "arn:aws:s3:::b/a${*}b", nil, "arn:aws:s3:::b/a*b", true},
		{"Q13 no wildcard", v2012, "arn:aws:s3:::b/a${*}b", nil, "arn:aws:s3:::b/axb", false},
		{
			"Q14", v2012,
			"arn:aws:sns:*:*:${aws:PrincipalTag/access-project}-${aws:PrincipalTag/access-application}-${aws:PrincipalTag/access-environment}-*",
			[]kv{one("aws:PrincipalTag/access-project", "alpha"), one("aws:PrincipalTag/access-application", "web"), one("aws:PrincipalTag/access-environment", "prod")},
			"arn:aws:sns:us-east-1:111122223333:alpha-web-prod-topic1", true,
		},
	}
	for _, v := range resources {
		t.Run(v.id, func(t *testing.T) {
			if got := resourceApplies(t, v.version, v.pattern, v.resource, contextOf(v.ctx...)); got != v.want {
				t.Errorf("iam-evaluation.md 9.15 vector %s: Resource %s matches %s = %t, want %t", v.id, v.pattern, v.resource, got, v.want)
			}
		})
	}

	// Q5, Q6: a Deny whose negated condition names a variable with no value
	// denies ("Access is explicitly denied when aws:principaltag/Team is not
	// set", #policy-vars-no-value); with the tag set to the object's value
	// it does not.
	team := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:GetObject","Resource":"arn:aws:s3:::amzn-s3-demo-bucket/*",` +
		`"Condition":{"StringNotEquals":{"s3:ExistingObjectTag/Team":"${aws:PrincipalTag/Team}"}}}]}`
	denies := func(ctx *policydoc.Context) bool {
		r := policydoc.Evaluate(parseDoc(t, team), policydoc.IdentityPolicy, policydoc.Request{
			Requester: evalUser, Action: "s3:GetObject", Resource: "arn:aws:s3:::amzn-s3-demo-bucket/k", Context: ctx,
		})
		return len(r.Denies) == 1
	}
	if !denies(contextOf(one("s3:ExistingObjectTag/Team", "blue"))) {
		t.Error("iam-evaluation.md 9.15 vector Q5: the Deny does not apply without aws:PrincipalTag/Team, want ExplicitDeny")
	}
	if denies(contextOf(one("s3:ExistingObjectTag/Team", "blue"), one("aws:PrincipalTag/Team", "blue"))) {
		t.Error("iam-evaluation.md 9.15 vector Q6: the Deny applies with aws:PrincipalTag/Team equal to the object's tag, want not denied")
	}

	runCondVectors(t, "9.15", []condVector{
		{"Q11", "Allow", `{"StringLike":{"s3:prefix":["${aws:PrincipalTag/team}/*"]}}`, []kv{one("aws:PrincipalTag/team", "blue"), one("s3:prefix", "blue/x")}, true},
		{"Q12", "Allow", `{"StringEquals":{"s3:ExistingObjectTag/owner":"${aws:PrincipalTag/owner}"}}`, []kv{one("aws:PrincipalTag/owner", "ana"), one("s3:ExistingObjectTag/owner", "ana")}, true},
	})
}

// R: action and resource patterns, iam-evaluation.md 9.16 (S-ACTION,
// S-RESOURCE, S-ARNS, S-GRAMMAR).
func TestEvaluatePatternVectors(t *testing.T) {
	actions := []struct {
		id, pattern, action string
	}{
		{"R1", "iam:*AccessKey*", "iam:CreateAccessKey"},
		{"R2", "iam:*AccessKey*", "iam:DeleteAccessKey"},
		{"R3", "iam:*AccessKey*", "iam:ListAccessKeys"},
		{"R4", "iam:*AccessKey*", "iam:UpdateAccessKey"},
		{"R5", "IAM:listaccesskeys", "iam:ListAccessKeys"},
		{"R6", "SQS:SendMessage", "sqs:SendMessage"},
		{"R7", "s3:*", "s3:GetObject"},
	}
	for _, v := range actions {
		t.Run(v.id, func(t *testing.T) {
			if !actionApplies(t, v.pattern, v.action) {
				t.Errorf("iam-evaluation.md 9.16 vector %s: Action %s does not match %s, want a match", v.id, v.pattern, v.action)
			}
		})
	}

	const (
		bucket = "arn:aws:s3:::amzn-s3-demo-bucket"
		test   = bucket + "/*/test/*"
	)
	resources := []struct {
		id, pattern string
		values      []string
		want        bool
	}{
		{"R8", test, []string{bucket + "/1/test/object.jpg"}, true},
		{"R9", test, []string{bucket + "/1/2/test/object.jpg"}, true},
		{"R10", test, []string{bucket + "/1/2/test/3/object.jpg"}, true},
		{"R11", test, []string{bucket + "/1/2/3/test/4/object.jpg"}, true},
		{"R12", test, []string{bucket + "/1///test///object.jpg"}, true},
		{"R13", test, []string{bucket + "/1/test/.jpg"}, true},
		{"R14", test, []string{bucket + "//test/object.jpg"}, true},
		{"R15", test, []string{bucket + "/1/test/"}, true},
		{"R16", test, []string{bucket + "/1-test/object.jpg"}, false},
		{"R17", test, []string{bucket + "/test/object.jpg"}, false},
		{"R18", test, []string{bucket + "/1/2/test.jpg"}, false},
		{"R19", "arn:aws:iam::123456789012:role/*", []string{"arn:aws:iam::123456789012:role/anything"}, true},
		{"R20", "arn:aws:iam::123456789012:role/service-*", []string{"arn:aws:iam::123456789012:role/service-role1", "arn:aws:iam::123456789012:role/service-test"}, true},
		{"R21", bucket + "/????-test", []string{bucket + "/1234-test", bucket + "/2024-test", bucket + "/a100-test"}, true},
		{"R22", "arn:aws:iam::123456789012:user/accounting/*", []string{"arn:aws:iam::123456789012:user/accounting/ana"}, true},
		{"R23", "arn:aws:sqs", []string{"arn:aws:sqs:us-east-1:111122223333:q"}, true},
		{"R24", "arn:aws:iam::111122223333:user/Bob", []string{"arn:aws:iam::111122223333:user/bob"}, false},
		{"R25", "arn:aws:qbusiness:*:123456789012:*", []string{"arn:aws:qbusiness:us-east-1:123456789012:application/a1"}, true},
	}
	for _, v := range resources {
		t.Run(v.id, func(t *testing.T) {
			for _, value := range v.values {
				if got := resourceApplies(t, v2012, v.pattern, value, nil); got != v.want {
					t.Errorf("iam-evaluation.md 9.16 vector %s: identity-policy Resource %s matches %s = %t, want %t", v.id, v.pattern, value, got, v.want)
				}
			}
		})
	}
}

// docDecision is the decision one identity policy makes alone: an applying
// Deny is an explicit deny, else an applying Allow allows, else the
// request is implicitly denied (iam-evaluation.md 2.11, 5.1).
func docDecision(r policydoc.Result) string {
	switch {
	case len(r.Denies) > 0:
		return "ExplicitDeny"
	case len(r.Allows) > 0:
		return "Allow"
	}
	return "ImplicitDeny"
}

// S1 to S12: NotAction and NotResource, iam-evaluation.md 9.17
// (S-NOTACTION, S-NOTRES, S-OPS #Conditions_IfExists).
func TestEvaluateNotActionAndNotResourceVectors(t *testing.T) {
	const (
		notDeleteBucket = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","NotAction":"s3:DeleteBucket","Resource":"arn:aws:s3:::*"}]}`
		notIAM          = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","NotAction":"iam:*","Resource":"*"}]}`
		mfa             = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"},` +
			`{"Effect":"Deny","NotAction":"iam:*","Resource":"*","Condition":{"BoolIfExists":{"aws:MultiFactorAuthPresent":"false"}}}]}`
		payrollDeny  = `{"Effect":"Deny","Action":"s3:*","NotResource":["arn:aws:s3:::HRBucket/Payroll","arn:aws:s3:::HRBucket/Payroll/*"]}`
		payroll      = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"},` + payrollDeny + `]}`
		payrollAlone = `{"Version":"2012-10-17","Statement":[` + payrollDeny + `]}`
		queue        = "arn:aws:sqs:us-east-1:111122223333:q"
		object       = "arn:aws:s3:::b/k"
	)
	vectors := []struct {
		id, doc, action, resource string
		ctx                       []kv
		want                      string
	}{
		{"S1", notDeleteBucket, "s3:DeleteBucket", "arn:aws:s3:::b", nil, "ImplicitDeny"},
		{"S2", notDeleteBucket, "s3:ListBucket", "arn:aws:s3:::b", nil, "Allow"},
		{"S3", notDeleteBucket, "sqs:SendMessage", queue, nil, "ImplicitDeny"},
		{"S4", notIAM, "sqs:SendMessage", queue, nil, "Allow"},
		{"S5", notIAM, "iam:CreateUser", "arn:aws:iam::111122223333:user/new", nil, "ImplicitDeny"},
		{"S6", mfa, "s3:GetObject", object, []kv{one("aws:MultiFactorAuthPresent", "false")}, "ExplicitDeny"},
		{"S7", mfa, "s3:GetObject", object, []kv{one("aws:MultiFactorAuthPresent", "true")}, "Allow"},
		{"S8", mfa, "s3:GetObject", object, nil, "ExplicitDeny"},
		{"S9", mfa, "iam:ListUsers", "arn:aws:iam::111122223333:user/u", []kv{one("aws:MultiFactorAuthPresent", "false")}, "Allow"},
		{"S10", payroll, "s3:GetObject", "arn:aws:s3:::HRBucket/Payroll/x", nil, "Allow"},
		{"S11", payroll, "s3:GetObject", "arn:aws:s3:::HRBucket/Other/x", nil, "ExplicitDeny"},
		{"S12", payrollAlone, "s3:GetObject", "arn:aws:s3:::HRBucket/Payroll/x", nil, "ImplicitDeny"},
	}
	for _, v := range vectors {
		t.Run(v.id, func(t *testing.T) {
			r := policydoc.Evaluate(parseDoc(t, v.doc), policydoc.IdentityPolicy, policydoc.Request{
				Requester: evalUser, Action: v.action, Resource: v.resource, Context: contextOf(v.ctx...),
			})
			if got := docDecision(r); got != v.want {
				t.Errorf("iam-evaluation.md 9.17 vector %s: %s on %s = %s (%+v), want %s", v.id, v.action, v.resource, got, r, v.want)
			}
		})
	}
}
