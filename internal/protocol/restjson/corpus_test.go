package restjson_test

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocol/restjson"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen"
	corpus "github.com/sustinbebustin/understudy/internal/protocoltest/gen/restjson"
)

// Server cases from the Smithy restJson1 corpus that pin the query-params
// and prefix-header maps: every query parameter and every matching header
// lands in its map, even one an httpQuery or httpHeader member also binds.
func TestCorpusRequests(t *testing.T) {
	cases := map[string]gen.RequestCase{}
	for _, c := range corpus.Suite().Requests {
		cases[c.ID] = c
	}
	for _, id := range []string{
		"RestJsonServersPutAllQueryParamsInMap",
		"RestJsonServersQueryParamsStringListMap",
		"RestJsonHttpEmptyPrefixHeadersRequestServer",
		"RestJsonAllQueryStringTypes",
	} {
		c, ok := cases[id]
		if !ok {
			t.Fatalf("the corpus has no request case %s", id)
		}
		target := c.URI
		if len(c.QueryParams) > 0 {
			target += "?" + strings.Join(c.QueryParams, "&")
		}
		r := newRequest(c.Method, target, c.Body, c.Headers)
		got, ok := reflect.TypeAssert[smithy.Deserializable](reflect.New(reflect.TypeOf(c.Params).Elem()))
		if !ok {
			t.Fatalf("%s: %T is not a generated input", id, c.Params)
		}
		if err := (restjson.Codec{}).Decode(r, c.Operation, edgeBody{r}, got); err != nil {
			t.Errorf("%s: %v", id, err.AsError())
			continue
		}
		if !reflect.DeepEqual(got, c.Params) {
			t.Errorf("%s: Decode = %+v\nwant %+v", id, got, c.Params)
		}
	}
}

// Server cases from the Smithy restJson1 corpus that pin the response
// body rules: Smithy ignores httpQuery on an output, so the member is
// written to the body (RestJsonIgnoreQueryParamsInResponse), and an output
// whose members are all bound to headers or the status is still {} with
// Content-Type application/json (RestJsonGreetingWithErrors,
// RestJsonHttpResponseCode, RestJsonHttpResponseCodeRequired).
func TestCorpusResponses(t *testing.T) {
	cases := map[string]gen.ResponseCase{}
	for _, c := range corpus.Suite().Responses {
		cases[c.ID] = c
	}
	for _, id := range []string{
		"RestJsonIgnoreQueryParamsInResponse",
		"RestJsonGreetingWithErrors",
		"RestJsonHttpResponseCode",
		"RestJsonHttpResponseCodeRequired",
	} {
		c, ok := cases[id]
		if !ok {
			t.Fatalf("the corpus has no response case %s", id)
		}
		out, ok := c.Outcome.(gen.Output)
		if !ok {
			t.Fatalf("%s: outcome %T is not an output", id, c.Outcome)
		}
		resp, err := restjson.Codec{}.Encode(nil, c.Operation, requestID, out.Value, http.StatusOK)
		if err != nil {
			t.Errorf("%s: %v", id, err.AsError())
			continue
		}
		if got := readAll(t, resp.Body); resp.Status != c.Code || got != c.Body {
			t.Errorf("%s: %d %s, want %d %s", id, resp.Status, got, c.Code, c.Body)
		}
		for name, want := range c.Headers {
			if got := resp.Header.Get(name); got != want {
				t.Errorf("%s: header %s = %q, want %q", id, name, got, want)
			}
		}
	}
}
