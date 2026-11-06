package authz_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// CheckResource answers from one resource's stored policy alone. These
// tests hold for any evaluator that follows AWS's single-policy rules
// (iam-evaluation.md 2.2, 5), so they constrain 5b's semantics as well as
// 5a's. They avoid the two cases the research leaves open: a condition key
// the request does not carry (iam-evaluation.md 6.14, settled by 5b) and
// whether "*" admits a service principal (IAM-EVAL-33).

const (
	queue    = "arn:aws:sqs:us-east-1:000000000000:orders"
	topic    = "arn:aws:sns:us-east-1:000000000000:events"
	other    = "arn:aws:sns:us-east-1:000000000000:other"
	role     = "arn:aws:iam::000000000000:role/app"
	user     = "arn:aws:iam::000000000000:user/u"
	function = "arn:aws:lambda:us-east-1:000000000000:function:web"
)

var sendMessage = registry.IAMAction{Prefix: "sqs", Name: "SendMessage"}

// checker is the function under test: authz.CheckResource.
type checker func(doc *policydoc.Document, r authz.ResourceRequest) policydoc.Decision

// fataler is what parse needs of a *testing.T or a *rapid.T.
type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

func parse(t fataler, text string) *policydoc.Document {
	t.Helper()
	doc, err := policydoc.Parse([]byte(text))
	if err != nil {
		t.Fatalf("parse %s: %v", text, err)
	}
	return &doc
}

// kind is a decision's variant, the part every evaluator must agree on.
func kind(d policydoc.Decision) string {
	if d == nil {
		return "nil"
	}
	return reflect.TypeOf(d).Name()
}

func TestCheckResource(t *testing.T) { checkCases(t, authz.CheckResource) }

func checkCases(t *testing.T, check checker) {
	t.Helper()
	serviceGrant := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},` +
		`"Action":"sqs:SendMessage","Resource":"` + queue + `","Condition":{"ArnEquals":{"aws:SourceArn":"` + topic + `"}}}]}`
	roleGrant := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"` + role + `"},"Action":"SQS:sendmessage","Resource":"` + queue + `"}]}`
	denyOverAllow := `{"Version":"2012-10-17","Statement":[` +
		`{"Effect":"Allow","Principal":{"AWS":"` + role + `"},"Action":"sqs:*","Resource":"*"},` +
		`{"Sid":"NoSend","Effect":"Deny","Principal":{"AWS":"` + role + `"},"Action":"sqs:SendMessage","Resource":"` + queue + `"}]}`
	public := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"` + queue + `"}]}`
	// The statement the Lambda console adds for a function URL with
	// AuthType NONE (iam-enforcement-surfaces.md 7.4; lambda-api.md 7.6).
	functionURL := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"lambda:InvokeFunctionUrl",` +
		`"Resource":"` + function + `","Condition":{"StringEquals":{"lambda:FunctionUrlAuthType":"NONE"}}}]}`
	invokeURL := registry.IAMAction{Prefix: "lambda", Name: "InvokeFunctionUrl"}
	sns := policydoc.ServiceCaller{Name: "sns.amazonaws.com"}

	cases := []struct {
		name string
		doc  string // "" is a resource with no policy
		req  authz.ResourceRequest
		want string
	}{
		{
			// Nothing but a resource policy can allow a service principal
			// or an anonymous caller (iam-evaluation.md 2.9, 2.10).
			name: "a resource without a policy allows no one",
			req:  authz.ResourceRequest{Caller: sns, Action: sendMessage, Resource: queue, Keys: map[string]string{"aws:SourceArn": topic}},
			want: "ImplicitDeny",
		},
		{
			name: "a service principal acting for the named source is allowed (iam-evaluation.md 2.9)",
			doc:  serviceGrant,
			req:  authz.ResourceRequest{Caller: sns, Action: sendMessage, Resource: queue, Keys: map[string]string{"aws:SourceArn": topic}},
			want: "Allowed",
		},
		{
			// A condition on a key the request carries with another value
			// is false, so the statement does not apply (iam-evaluation.md 5.1, 6.1).
			name: "a service principal acting for another source is not",
			doc:  serviceGrant,
			req:  authz.ResourceRequest{Caller: sns, Action: sendMessage, Resource: queue, Keys: map[string]string{"aws:SourceArn": other}},
			want: "ImplicitDeny",
		},
		{
			name: "condition key names match case-insensitively (iam-evaluation.md 6.2)",
			doc:  serviceGrant,
			req:  authz.ResourceRequest{Caller: sns, Action: sendMessage, Resource: queue, Keys: map[string]string{"AWS:SourceArn": other}},
			want: "ImplicitDeny",
		},
		{
			name: "another service principal is not named",
			doc:  serviceGrant,
			req:  authz.ResourceRequest{Caller: policydoc.ServiceCaller{Name: "events.amazonaws.com"}, Action: sendMessage, Resource: queue, Keys: map[string]string{"aws:SourceArn": topic}},
			want: "ImplicitDeny",
		},
		{
			name: "actions match case-insensitively (iam-evaluation.md 5.2)",
			doc:  roleGrant,
			req:  authz.ResourceRequest{Caller: policydoc.AWSCaller{ARN: role}, Action: sendMessage, Resource: queue},
			want: "Allowed",
		},
		{
			name: "a principal not named is not allowed (iam-evaluation.md 5.4)",
			doc:  roleGrant,
			req:  authz.ResourceRequest{Caller: policydoc.AWSCaller{ARN: user}, Action: sendMessage, Resource: queue},
			want: "ImplicitDeny",
		},
		{
			name: "an action not listed is not allowed",
			doc:  roleGrant,
			req:  authz.ResourceRequest{Caller: policydoc.AWSCaller{ARN: role}, Action: registry.IAMAction{Prefix: "sqs", Name: "DeleteQueue"}, Resource: queue},
			want: "ImplicitDeny",
		},
		{
			name: "another resource is not allowed (iam-evaluation.md 5.3)",
			doc:  roleGrant,
			req:  authz.ResourceRequest{Caller: policydoc.AWSCaller{ARN: role}, Action: sendMessage, Resource: queue + "-dlq"},
			want: "ImplicitDeny",
		},
		{
			name: "an explicit deny wins over an allow (iam-evaluation.md 2.2, step 1)",
			doc:  denyOverAllow,
			req:  authz.ResourceRequest{Caller: policydoc.AWSCaller{ARN: role}, Action: sendMessage, Resource: queue},
			want: "ExplicitDeny",
		},
		{
			name: "the allow still covers what the deny does not",
			doc:  denyOverAllow,
			req:  authz.ResourceRequest{Caller: policydoc.AWSCaller{ARN: role}, Action: registry.IAMAction{Prefix: "sqs", Name: "ReceiveMessage"}, Resource: queue},
			want: "Allowed",
		},
		{
			name: `"*" grants an anonymous caller (iam-evaluation.md 2.10)`,
			doc:  public,
			req:  authz.ResourceRequest{Caller: policydoc.Anonymous{}, Action: sendMessage, Resource: queue},
			want: "Allowed",
		},
		{
			name: "a nil caller is anonymous",
			doc:  public,
			req:  authz.ResourceRequest{Action: sendMessage, Resource: queue},
			want: "Allowed",
		},
		{
			name: "a named principal does not grant an anonymous caller (iam-evaluation.md 2.10)",
			doc:  roleGrant,
			req:  authz.ResourceRequest{Caller: policydoc.Anonymous{}, Action: sendMessage, Resource: queue},
			want: "ImplicitDeny",
		},
		{
			name: "a function URL with AuthType NONE admits an anonymous caller",
			doc:  functionURL,
			req:  authz.ResourceRequest{Caller: policydoc.Anonymous{}, Action: invokeURL, Resource: function, Keys: map[string]string{"lambda:FunctionUrlAuthType": "NONE"}},
			want: "Allowed",
		},
		{
			name: "the NONE statement does not admit an AWS_IAM URL's request",
			doc:  functionURL,
			req:  authz.ResourceRequest{Caller: policydoc.Anonymous{}, Action: invokeURL, Resource: function, Keys: map[string]string{"lambda:FunctionUrlAuthType": "AWS_IAM"}},
			want: "ImplicitDeny",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var doc *policydoc.Document
			if c.doc != "" {
				doc = parse(t, c.doc)
			}
			if got := check(doc, c.req); kind(got) != c.want {
				t.Errorf("CheckResource = %#v, want %s", got, c.want)
			}
		})
	}
}

// The vocabulary the properties draw policies and requests from: concrete
// resources and callers, with patterns, account principals and conditions
// in the policies.
var (
	principals = []string{`"*"`, `{"AWS":"` + role + `"}`, `{"AWS":"` + user + `"}`, `{"AWS":"000000000000"}`, `{"Service":"sns.amazonaws.com"}`}
	patterns   = []string{"sqs:SendMessage", "sqs:*", "sqs:Send*", "s3:GetObject", "*", "lambda:InvokeFunctionUrl"}
	resources  = []string{queue, "arn:aws:sqs:*:000000000000:orders", "*", "arn:aws:s3:::docs/*", function}
	conditions = []string{"", `,"Condition":{"ArnEquals":{"aws:SourceArn":"` + topic + `"}}`}
	actions    = []registry.IAMAction{sendMessage, {Prefix: "sqs", Name: "ReceiveMessage"}, {Prefix: "s3", Name: "GetObject"}, {Prefix: "lambda", Name: "InvokeFunctionUrl"}}
	targets    = []string{queue, "arn:aws:s3:::docs/a.txt", function}
	callers    = []policydoc.Caller{policydoc.AWSCaller{ARN: role}, policydoc.AWSCaller{ARN: user}, policydoc.Anonymous{}, policydoc.ServiceCaller{Name: "sns.amazonaws.com"}}
)

// statement draws one statement's JSON.
func statement(t *rapid.T, label string) string {
	effect := rapid.SampledFrom([]string{"Allow", "Deny"}).Draw(t, label+" effect")
	return fmt.Sprintf(`{"Effect":%q,"Principal":%s,"Action":%q,"Resource":%q%s}`,
		effect,
		rapid.SampledFrom(principals).Draw(t, label+" principal"),
		rapid.SampledFrom(patterns).Draw(t, label+" action"),
		rapid.SampledFrom(resources).Draw(t, label+" resource"),
		rapid.SampledFrom(conditions).Draw(t, label+" condition"))
}

// statements draws one to four statements: IAM refuses an empty Statement
// list (policydoc.Parse does too).
func statements(t *rapid.T) []string {
	n := rapid.IntRange(1, 4).Draw(t, "statements")
	out := make([]string, n)
	for i := range out {
		out[i] = statement(t, fmt.Sprint("statement ", i))
	}
	return out
}

func document(stmts []string) string {
	return `{"Version":"2012-10-17","Statement":[` + strings.Join(stmts, ",") + `]}`
}

func request(t *rapid.T, callerPool []policydoc.Caller) authz.ResourceRequest {
	r := authz.ResourceRequest{
		Caller:   rapid.SampledFrom(callerPool).Draw(t, "caller"),
		Action:   rapid.SampledFrom(actions).Draw(t, "action"),
		Resource: rapid.SampledFrom(targets).Draw(t, "resource"),
	}
	// The source key is always carried, so no clause meets an absent key.
	r.Keys = map[string]string{"aws:SourceArn": rapid.SampledFrom([]string{topic, other}).Draw(t, "source")}
	return r
}

// A resource with no policy allows no request (iam-evaluation.md 2.9,
// 2.10: only a resource policy grants a service principal or an anonymous
// caller; for an IAM identity a resource policy is the only source this
// check reads).
func TestPropertyNoPolicyAllowsNothing(t *testing.T) {
	rapid.Check(t, noPolicyAllowsNothing(authz.CheckResource))
}

func noPolicyAllowsNothing(check checker) func(*rapid.T) {
	return func(t *rapid.T) {
		r := request(t, callers)
		if got := check(nil, r); kind(got) != "ImplicitDeny" {
			t.Fatalf("CheckResource(nil, %+v) = %#v, want ImplicitDeny", r, got)
		}
	}
}

// An applicable Deny decides, whatever else the policy allows
// (iam-evaluation.md 2.2, step 1): adding an unconditional Deny of every
// action on every resource to anyone ("*", which names every IAM identity
// and anonymous callers, iam-evaluation.md 2.10, 5.4) makes every request
// of an IAM identity or an anonymous caller an explicit deny.
func TestPropertyAnUnconditionalDenyDecides(t *testing.T) {
	rapid.Check(t, anUnconditionalDenyDecides(authz.CheckResource))
}

func anUnconditionalDenyDecides(check checker) func(*rapid.T) {
	return func(t *rapid.T) {
		stmts := statements(t)
		at := rapid.IntRange(0, len(stmts)).Draw(t, "deny position")
		stmts = append(stmts[:at], append([]string{`{"Effect":"Deny","Principal":"*","Action":"*","Resource":"*"}`}, stmts[at:]...)...)
		doc := parse(t, document(stmts))
		r := request(t, []policydoc.Caller{policydoc.AWSCaller{ARN: role}, policydoc.AWSCaller{ARN: user}, policydoc.Anonymous{}})
		if got := check(doc, r); kind(got) != "ExplicitDeny" {
			t.Fatalf("CheckResource(%s, %+v) = %#v, want ExplicitDeny", document(stmts), r, got)
		}
	}
}

// Action names are case-insensitive, prefix and name alike
// (iam-evaluation.md 5.2: "iam:ListAccessKeys is the same as
// IAM:listaccesskeys"): changing their case in the policy changes no
// decision.
func TestPropertyActionCaseChangesNoDecision(t *testing.T) {
	rapid.Check(t, actionCaseChangesNoDecision(authz.CheckResource))
}

func actionCaseChangesNoDecision(check checker) func(*rapid.T) {
	return func(t *rapid.T) {
		stmts := statements(t)
		folded := make([]string, len(stmts))
		upper := rapid.Bool().Draw(t, "upper")
		for i, s := range stmts {
			folded[i] = foldActions(s, upper)
		}
		r := request(t, callers)
		a := check(parse(t, document(stmts)), r)
		b := check(parse(t, document(folded)), r)
		if a == nil || kind(a) != kind(b) {
			t.Fatalf("%s: %#v; with actions folded %s: %#v", document(stmts), a, document(folded), b)
		}
	}
}

// foldActions changes the case of the Action value of a statement drawn by
// statement.
func foldActions(stmt string, upper bool) string {
	const key = `"Action":"`
	i := strings.Index(stmt, key) + len(key)
	j := i + strings.Index(stmt[i:], `"`)
	action := stmt[i:j]
	if upper {
		action = strings.ToUpper(action)
	} else {
		action = strings.ToLower(action)
	}
	return stmt[:i] + action + stmt[j:]
}

// A statement that names exactly the caller, the action and the resource,
// with no condition, applies (iam-evaluation.md 5.1), so a policy of that
// one Allow allows the request.
func TestPropertyAnExactGrantAllows(t *testing.T) {
	rapid.Check(t, anExactGrantAllows(authz.CheckResource))
}

func anExactGrantAllows(check checker) func(*rapid.T) {
	return func(t *rapid.T) {
		r := request(t, []policydoc.Caller{policydoc.AWSCaller{ARN: role}, policydoc.AWSCaller{ARN: user}, policydoc.ServiceCaller{Name: "sns.amazonaws.com"}})
		principal := `"*"`
		switch c := r.Caller.(type) {
		case policydoc.AWSCaller:
			principal = `{"AWS":"` + c.ARN + `"}`
		case policydoc.ServiceCaller:
			principal = `{"Service":"` + c.Name + `"}`
		case policydoc.Anonymous:
		}
		text := document([]string{fmt.Sprintf(`{"Effect":"Allow","Principal":%s,"Action":%q,"Resource":%q}`, principal, r.Action.String(), r.Resource)})
		if got := check(parse(t, text), r); kind(got) != "Allowed" {
			t.Fatalf("CheckResource(%s, %+v) = %#v, want Allowed", text, r, got)
		}
	}
}
