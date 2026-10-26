package restxml_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/restxml"
	corpus "github.com/sustinbebustin/understudy/internal/protocoltest/gen/restxml"
)

func complexError(message string) *awserr.Error {
	return corpus.ErrComplexErrorMembers(message, corpus.ComplexError{
		Header:   new("Header"),
		TopLevel: new("Top level"),
		Nested:   &corpus.ComplexNestedErrorData{Foo: new("bar")},
	})
}

// Inside <Error>, the value's members follow Code and Message and precede
// the error's context fields (smithy-protocol-tests.md 11.2: the error
// structure's members, as an output writes them).
func TestMembersFollowTheMessageInsideError(t *testing.T) {
	e := complexError("Hi")
	e.Fields = append(e.Fields, awserr.Field{Name: "Extra", Value: "x"})
	resp := restxml.Codec{}.EncodeError(newRequest(http.MethodPost, "/GreetingWithErrors", "", nil), corpus.Service(), "foo-id", e)
	got := readAll(t, resp.Body)
	const want = "<Error><Type>Sender</Type><Code>ComplexError</Code><Message>Hi</Message>" +
		"<TopLevel>Top level</TopLevel><Nested><Foo>bar</Foo></Nested><Extra>x</Extra></Error>"
	if !strings.Contains(got, want) {
		t.Errorf("body = %s\nwant it to hold %s", got, want)
	}
}

// A HEAD answer has no body, but the value's header-bound member is a
// header and still goes out (smithy-protocol-tests.md 11.2 case 5).
func TestHeadErrorKeepsTheHeaderMember(t *testing.T) {
	resp := restxml.Codec{}.EncodeError(newRequest(http.MethodHead, "/GreetingWithErrors", "", nil), corpus.Service(), "foo-id", complexError(""))
	if resp.Status != http.StatusForbidden || resp.Header.Get("X-Header") != "Header" {
		t.Errorf("HEAD error = %d with X-Header %q, want 403 with Header", resp.Status, resp.Header.Get("X-Header"))
	}
	if body := readAll(t, resp.Body); body != "" {
		t.Errorf("HEAD error body = %q, want none", body)
	}
}
