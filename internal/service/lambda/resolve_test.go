package lambda_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/protocol/restjson"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
)

// TestGetFunctionIdentifierForms: every FunctionName form of
// lambda-api.md 3 names the same function, and the forms outside the
// caller's region and account answer with the recorded errors.
func TestGetFunctionIdentifierForms(t *testing.T) {
	const arn = "arn:aws:lambda:us-east-1:000000000000:function:fn"
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("fn")
		call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		call(h, lambdaapi.OpCreateAlias, &lambdaapi.CreateAliasInput{FunctionName: "fn", Name: "live", FunctionVersion: "1"})
		found := []struct {
			name, version, arn string
		}{
			{"fn", "$LATEST", arn},
			{"fn:live", "1", arn + ":live"},
			{"000000000000:function:fn", "$LATEST", arn},
			{arn, "$LATEST", arn},
			{arn + ":1", "1", arn + ":1"},
			{decodedName(t, url.PathEscape(arn)), "$LATEST", arn},
		}
		for _, f := range found {
			c := call(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: f.name}).Configuration
			if *c.FunctionName != "fn" || *c.Version != f.version || *c.FunctionArn != f.arn {
				t.Errorf("GetFunction(%s) = %s %s %s; want fn %s %s", f.name, *c.FunctionName, *c.Version, *c.FunctionArn, f.version, f.arn)
			}
		}
		errorIs(t, fail(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "arn:aws:lambda:ap-southeast-1:000000000000:function:fn"}),
			"ResourceNotFoundException", "Functions from 'ap-southeast-1' are not reachable in this region ('us-east-1')")
		for _, other := range []string{"arn:aws:lambda:us-east-1:111111111111:function:fn", "111111111111:function:fn"} {
			errorIs(t, fail(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: other}), "AccessDeniedException", "")
		}
		errorIs(t, fail(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn:1", Qualifier: new("live")}),
			"InvalidParameterValueException", "The derived qualifier from the function name does not match the specified qualifier.")
		errorIs(t, fail(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn:missing"}),
			"ResourceNotFoundException", "Function not found: "+arn+":missing")
		errorIs(t, fail(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "nope"}),
			"ResourceNotFoundException", "Function not found: arn:aws:lambda:us-east-1:000000000000:function:nope")
	})
}

// decodedName is the FunctionName the restJson1 codec reads from a
// GetFunction request whose path carries escaped.
func decodedName(tb testing.TB, escaped string) string {
	tb.Helper()
	op, ok := lambdaapi.Service().Operation("GetFunction")
	if !ok {
		tb.Fatal("no GetFunction descriptor")
	}
	r := httptest.NewRequestWithContext(tb.Context(), http.MethodGet, "/2015-03-31/functions/"+escaped, http.NoBody)
	var in lambdaapi.GetFunctionInput
	if err := (restjson.Codec{}).Decode(r, op, body{}, &in); err != nil {
		tb.Fatalf("decode: %s", err.Message)
	}
	return in.FunctionName
}

// body is an empty request body.
type body struct{}

func (body) Open() (io.ReadCloser, error)  { return io.NopCloser(bytes.NewReader(nil)), nil }
func (body) Len() (int64, bool)            { return 0, true }
func (body) Trailer(string) (string, bool) { return "", false }
