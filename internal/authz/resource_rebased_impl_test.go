package authz_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// CheckResource on policydoc.Evaluate beyond the blind tests: the explicit
// deny names the first applying Deny statement, a key of the action's own
// service that the request lacks reads as absent, and a statement without a
// principal never applies in a resource policy (iam-evaluation.md 5.1).
func TestCheckResourceOnTheEvaluator(t *testing.T) {
	const fn = "arn:aws:lambda:us-east-1:111122223333:function:web"
	invoke := registry.IAMAction{Prefix: "lambda", Name: "InvokeFunctionUrl"}
	anon := policydoc.Anonymous{}

	twoDenies := parse(t, `{"Version":"2012-10-17","Statement":[`+
		`{"Effect":"Allow","Principal":"*","Action":"lambda:*","Resource":"*"},`+
		`{"Sid":"First","Effect":"Deny","Principal":"*","Action":"lambda:InvokeFunctionUrl","Resource":"*"},`+
		`{"Sid":"Second","Effect":"Deny","Principal":"*","Action":"lambda:*","Resource":"*"}]}`)
	got := authz.CheckResource(twoDenies, authz.ResourceRequest{Caller: anon, Action: invoke, Resource: fn})
	if want := (policydoc.ExplicitDeny{Statement: policydoc.StatementRef{Index: 1, Sid: "First"}}); got != want {
		t.Errorf("two Denies: CheckResource = %#v, want %#v", got, want)
	}

	// lambda:FunctionUrlAuthType is a key AWS sends with every URL request;
	// one the caller leaves out evaluates as absent, so StringEquals fails
	// and StringNotEquals holds (iam-evaluation.md 6.14).
	equals := parse(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"lambda:InvokeFunctionUrl","Resource":"*","Condition":{"StringEquals":{"lambda:FunctionUrlAuthType":"NONE"}}}]}`)
	if got := authz.CheckResource(equals, authz.ResourceRequest{Caller: anon, Action: invoke, Resource: fn}); kind(got) != "ImplicitDeny" {
		t.Errorf("StringEquals on a missing service key: CheckResource = %#v, want ImplicitDeny", got)
	}
	notEquals := parse(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"lambda:InvokeFunctionUrl","Resource":"*","Condition":{"StringNotEquals":{"lambda:FunctionUrlAuthType":"AWS_IAM"}}}]}`)
	if got := authz.CheckResource(notEquals, authz.ResourceRequest{Caller: anon, Action: invoke, Resource: fn}); kind(got) != "Allowed" {
		t.Errorf("StringNotEquals on a missing service key: CheckResource = %#v, want Allowed", got)
	}

	// policydoc.Parse accepts a statement without a principal; in a resource
	// policy it names no one.
	noPrincipal := parse(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:InvokeFunctionUrl","Resource":"*"}]}`)
	if got := authz.CheckResource(noPrincipal, authz.ResourceRequest{Caller: anon, Action: invoke, Resource: fn}); kind(got) != "ImplicitDeny" {
		t.Errorf("a statement without a principal: CheckResource = %#v, want ImplicitDeny", got)
	}
}
