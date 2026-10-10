package policydoc_test

import (
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/policydoc"
)

func TestRenderIsDeterministic(t *testing.T) {
	in := []policydoc.Grant{
		{Actions: []string{"sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ReceiveMessage"}, Resources: []string{queueARN}},
		{Actions: []string{"logs:PutLogEvents"}, Resources: []string{"b", "a", "b"}},
		{Actions: []string{"sqs:DeleteMessage", "sqs:ReceiveMessage"}, Resources: []string{queueARN}},
		{Actions: []string{"sqs:PurgeQueue"}},
		{Resources: []string{queueARN}},
	}
	want := policydoc.Document{
		Version: policydoc.Version20121017,
		Statements: []policydoc.Statement{
			{Effect: policydoc.Allow, Action: policydoc.Action{"logs:PutLogEvents"}, Resource: policydoc.Resource{"a", "b"}},
			{Effect: policydoc.Allow, Action: policydoc.Action{"sqs:DeleteMessage", "sqs:ReceiveMessage"}, Resource: policydoc.Resource{queueARN}},
		},
	}
	got, ok := policydoc.Render(in)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("Render = %s, %v\nwant %s, true", policydoc.Encode(got), ok, policydoc.Encode(want))
	}
	if first := []string{"sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ReceiveMessage"}; !reflect.DeepEqual(in[0].Actions, first) {
		t.Errorf("Render changed its input to %v", in[0].Actions)
	}
}

// IAM rejects a policy without statements, so Render reports that it has
// none to write rather than returning one.
func TestRenderOfNothingIsNoPolicy(t *testing.T) {
	for _, grants := range [][]policydoc.Grant{nil, {{Actions: []string{"sqs:SendMessage"}}}} {
		if doc, ok := policydoc.Render(grants); ok {
			t.Errorf("Render(%v) = %s, want no policy", grants, policydoc.Encode(doc))
		}
	}
}
