package restxml_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocol/restxml"
	"github.com/sustinbebustin/understudy/internal/protocoltest"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen"
	corpus "github.com/sustinbebustin/understudy/internal/protocoltest/gen/restxml"
)

// The restXml ComplexError case cannot pass on any server: its body carries
// <Message>Hi</Message>, which aws.protocoltests.restxml#ComplexError does
// not model and the params do not set, so it is skipped as a corpus bug
// (smithy-protocol-tests.md 11.3, 14.1). The rest of the case still holds a
// server to the nested error members: the same value answers the case's
// status, header-bound member and body, without <Message>.
func TestComplexErrorMembersWithoutMessage(t *testing.T) {
	var c gen.ResponseCase
	for _, x := range corpus.Suite().Responses {
		if x.ID == "ComplexError" {
			c = x
		}
	}
	if c.ID == "" {
		t.Fatal("the corpus has no response case ComplexError (third_party/smithy-protocol-tests/model.json L33284)")
	}
	// The case's body without the element no server can produce
	// (smithy-protocol-tests.md 14.1).
	const unmodeled = "\n      <Message>Hi</Message>"
	if !strings.Contains(c.Body, unmodeled) {
		t.Fatalf("the ComplexError case body no longer carries %q; revisit the corpus-bug skip:\n%s", unmodeled, c.Body)
	}
	want := strings.Replace(c.Body, unmodeled, "", 1)

	// The case's params (third_party/smithy-protocol-tests/model.json L33286-L33292).
	e := corpus.ErrComplexErrorMembers("", corpus.ComplexError{
		Header:   new("Header"),
		TopLevel: new("Top level"),
		Nested:   &corpus.ComplexNestedErrorData{Foo: new("bar")},
	})
	r := newRequest(http.MethodPost, "/GreetingWithErrors", "", nil)
	resp := restxml.Codec{}.EncodeError(r, corpus.Service(), "foo-id", e)
	got := readAll(t, resp.Body)

	// Status 403 (@httpError) and the headers the case lists
	// (third_party/smithy-protocol-tests/model.json L33293-L33297;
	// smithy-protocol-tests.md 11.2 case 5).
	if resp.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.Status)
	}
	if err := protocoltest.CompareHeaders(map[string]string{"Content-Type": "application/xml", "X-Header": "Header"}, resp.Header); err != nil {
		t.Errorf("headers %v: %v", resp.Header, err)
	}
	if err := protocoltest.CompareBody("application/xml", nil, []byte(want), []byte(got)); err != nil {
		t.Errorf("body: %v\ngot  %s\nwant %s", err, got, want)
	}
	// A server sends only modeled members (smithy-protocol-tests.md 11.3):
	// no invented message, and the header-bound member stays out of the
	// body (11.2).
	for _, none := range []string{"<Message>", "<Header>"} {
		if strings.Contains(got, none) {
			t.Errorf("body carries %s, which the value does not set in the body:\n%s", none, got)
		}
	}
}
