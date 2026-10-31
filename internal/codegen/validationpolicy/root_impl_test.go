package validationpolicy

import (
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
)

// apigatewayLike is a policy shaped like API Gateway's UpdateRestApi: a
// service Coral rule, the root updateRestApiInput, and the URI label
// restApiId unrooted.
func apigatewayLike() *validationPolicy {
	return &validationPolicy{
		paths: []validationPath{
			{segments: []string{"restApiId"}, param: "RestApiId"},
			{segments: []string{"patchOperations", "*", "member", "op"}, param: "PatchOperations"},
		},
		service:  []validationRule{{id: serviceCode, style: styleCoral}},
		root:     "updateRestApiInput",
		unrooted: []string{"restApiId"},
	}
}

// TestRootHeadsBodyMembersOnly: a body member is named under the root and
// a URI label bare (test_apigateway_api.snapshot.json:L4809 and
// L4062-L4086); the error's origin keeps the violations as reported, so
// the policy's own path matching is unchanged.
func TestRootHeadsBodyMembersOnly(t *testing.T) {
	vs := []awserr.Violation{
		{Path: awserr.Path{"patchOperations", "2", "member", "op"}, Value: text("wrong"), Constraint: awserr.Enum{Values: []string{"add"}}},
		{Path: awserr.Path{"restApiId"}, Value: awserr.RenderedNull{}, Constraint: awserr.Required{}},
	}
	err := apigatewayLike().render(vs)
	want := "2 validation errors detected: Value 'wrong' at 'updateRestApiInput.patchOperations.2.member.op' failed to satisfy constraint: " +
		"Member must satisfy enum value set: [add]; Value null at 'restApiId' failed to satisfy constraint: Member must not be null"
	if err.Message != want {
		t.Fatalf("render = %q\nwant %q", err.Message, want)
	}
	if o, ok := err.Origin.(awserr.Validation); !ok || !reflect.DeepEqual(o.Violations, vs) {
		t.Fatalf("origin = %+v, want the violations unchanged", err.Origin)
	}
}

// TestNoRootLeavesPathsBare: without a root the message is AWS's common
// grammar over the paths as reported.
func TestNoRootLeavesPathsBare(t *testing.T) {
	p := apigatewayLike()
	p.root = ""
	err := p.render([]awserr.Violation{{Path: awserr.Path{"patchOperations", "1", "member", "op"}, Value: text("x"), Constraint: awserr.Enum{Values: []string{"add"}}}})
	want := "1 validation error detected: Value 'x' at 'patchOperations.1.member.op' failed to satisfy constraint: Member must satisfy enum value set: [add]"
	if err.Message != want {
		t.Fatalf("render = %q\nwant %q", err.Message, want)
	}
}
