package authz_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// CheckResource on the enforcement evaluator: a condition key the request
// does not carry follows the truth table of iam-evaluation.md 6.14 instead
// of being assumed to hold, every operator family is evaluated, and a role
// ARN names its sessions (2.3, iam-enforcement-surfaces.md 6.2). Nothing is
// assumed, so Allowed.Assumed and Allowed.Unsettled stay empty.
func TestCheckResourceFollowsTheAbsentKeyRule(t *testing.T) {
	const (
		fn      = "arn:aws:lambda:us-east-1:111122223333:function:web"
		session = "arn:aws:sts::111122223333:assumed-role/app/s1"
	)
	invoke := registry.IAMAction{Prefix: "lambda", Name: "InvokeFunction"}
	sns := policydoc.ServiceCaller{Name: "sns.amazonaws.com"}
	statement := func(effect, condition string) string {
		return `{"Version":"2012-10-17","Statement":[{"Effect":"` + effect + `","Principal":{"Service":"sns.amazonaws.com"},"Action":"lambda:InvokeFunction","Resource":"` + fn + `","Condition":` + condition + `}]}`
	}
	cases := []struct {
		name string
		doc  string
		req  authz.ResourceRequest
		want string
	}{
		{
			"a positive operator on an absent key is false",
			statement("Allow", `{"ArnLike":{"aws:SourceArn":"arn:aws:sns:us-east-1:111122223333:*"}}`),
			authz.ResourceRequest{Caller: sns, Action: invoke, Resource: fn},
			"ImplicitDeny",
		},
		{
			"a negated operator on an absent key is true",
			statement("Allow", `{"StringNotEquals":{"aws:SourceAccount":"444455556666"}}`),
			authz.ResourceRequest{Caller: sns, Action: invoke, Resource: fn},
			"Allowed",
		},
		{
			"a Deny with a negated operator on an absent key denies",
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"lambda:InvokeFunction","Resource":"` + fn + `"},` +
				`{"Effect":"Deny","Principal":"*","Action":"lambda:InvokeFunction","Resource":"` + fn + `","Condition":{"StringNotEquals":{"aws:SourceAccount":"111122223333"}}}]}`,
			authz.ResourceRequest{Caller: sns, Action: invoke, Resource: fn},
			"ExplicitDeny",
		},
		{
			"IfExists on an absent key is true",
			statement("Allow", `{"ArnLikeIfExists":{"aws:SourceArn":"arn:aws:sns:us-east-1:111122223333:*"}}`),
			authz.ResourceRequest{Caller: sns, Action: invoke, Resource: fn},
			"Allowed",
		},
		{
			"a numeric operator is evaluated",
			statement("Allow", `{"NumericLessThan":{"test:Count":"10"}}`),
			authz.ResourceRequest{Caller: sns, Action: invoke, Resource: fn, Keys: map[string]string{"test:Count": "11"}},
			"ImplicitDeny",
		},
		{
			"a role ARN names the role's session",
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:role/app"},"Action":"lambda:InvokeFunction","Resource":"` + fn + `"}]}`,
			authz.ResourceRequest{Caller: policydoc.AWSCaller{ARN: session}, Action: invoke, Resource: fn},
			"Allowed",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := authz.CheckResource(parse(t, c.doc), c.req)
			if kind(got) != c.want {
				t.Fatalf("CheckResource = %#v, want %s", got, c.want)
			}
			if a, ok := got.(policydoc.Allowed); ok && (len(a.Assumed) != 0 || len(a.Unsettled) != 0) {
				t.Errorf("CheckResource = %#v, want nothing assumed or unsettled", a)
			}
		})
	}
}
