package restjson_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/restjson"
	corpus "github.com/sustinbebustin/understudy/internal/protocoltest/gen/restjson"
)

// errorBody encodes e and reads its body.
func errorBody(t *testing.T, e *awserr.Error) (resp restjson.Response, body string) {
	t.Helper()
	resp = restjson.Codec{}.EncodeError(newRequest(http.MethodGet, "/", "", nil), nil, requestID, e)
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

// A Lambda error's Type travels as a member of the value exactly as it
// does as a field: members, then fields, then the message, so moving a
// call site from Fields to the value changes no byte (Type User,
// error-matrix.md 5.6 T6).
func TestLambdaTypeAsMemberOrField(t *testing.T) {
	const message = "Function not found: arn:aws:lambda:us-east-1:000000000000:function:f"
	asField := lambdaapi.ErrResourceNotFoundException(message)
	asField.Fields = []awserr.Field{{Name: "Type", Value: "User"}}
	asMember := lambdaapi.ErrResourceNotFoundExceptionMembers(message, lambdaapi.ResourceNotFoundException{Type: new("User")})
	_, field := errorBody(t, asField)
	resp, member := errorBody(t, asMember)
	want := `{"Type":"User","Message":"` + message + `"}`
	if field != want || member != want {
		t.Errorf("as a field %s, as a member %s; want both %s", field, member, want)
	}
	if resp.Status != http.StatusNotFound {
		t.Errorf("status = %d, want the identity's 404", resp.Status)
	}
}

// The value's body members come first under their jsonName, then the
// error's fields; its httpHeader member goes out as a header and stays
// out of the body (smithy-protocol-tests.md 11.2 case 4).
func TestMembersThenFieldsWithTheHeaderMemberAsAHeader(t *testing.T) {
	e := corpus.ErrComplexErrorMembers(corpus.ComplexError{
		Header:   new("Header"),
		TopLevel: new("Top level"),
		Nested:   &corpus.ComplexNestedErrorData{Foo: new("bar")},
	})
	e.Fields = append(e.Fields, awserr.Field{Name: "Extra", Value: "x"})
	resp, body := errorBody(t, e)
	if want := `{"TopLevel":"Top level","Nested":{"Fooooo":"bar"},"Extra":"x"}`; body != want {
		t.Errorf("body = %s, want %s", body, want)
	}
	if got := resp.Header.Get("X-Header"); got != "Header" {
		t.Errorf("X-Header = %q, want Header", got)
	}
	if got := resp.Header.Get("X-Amzn-Errortype"); got != "ComplexError" {
		t.Errorf("X-Amzn-Errortype = %q, want ComplexError", got)
	}
}
