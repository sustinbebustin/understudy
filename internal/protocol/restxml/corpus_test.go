package restxml_test

import (
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocol/restxml"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen"
	corpus "github.com/sustinbebustin/understudy/internal/protocoltest/gen/restxml"
)

// Server cases from the Smithy restXml corpus that pin the query-params
// and prefix-header maps: every query parameter and every matching header
// lands in its map, even one an httpQuery or httpHeader member also binds.
func TestCorpusRequests(t *testing.T) {
	cases := map[string]gen.RequestCase{}
	for _, c := range corpus.Suite().Requests {
		cases[c.ID] = c
	}
	for _, id := range []string{
		"RestXmlServersPutAllQueryParamsInMap",
		"RestXmlServersQueryParamsStringListMap",
		"HttpEmptyPrefixHeadersRequestServer",
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
		if err := (restxml.Codec{}).Decode(r, c.Operation, edgeBody{r}, got); err != nil {
			t.Errorf("%s: %v", id, err.AsError())
			continue
		}
		if !reflect.DeepEqual(got, c.Params) {
			t.Errorf("%s: Decode = %+v\nwant %+v", id, got, c.Params)
		}
	}
}

// interElementSpace is the indentation between tags in the corpus bodies,
// which the writer does not produce.
var interElementSpace = regexp.MustCompile(`>\s+<`)

// Server cases from the Smithy restXml corpus that pin the response
// document: Smithy ignores httpQuery on an output, so the member is
// written to the body (IgnoreQueryParamsInResponse); list members, map
// keys and values, and flattened map entries declare their own
// xmlNamespace, and a nested structure does not repeat the root's
// (XmlNamespaces, XmlLists, RestXmlXmlMapWithXmlNamespace,
// RestXmlFlattenedXmlMapWithXmlNamespace). Bodies are compared byte for
// byte once the corpus's indentation is removed.
func TestCorpusResponses(t *testing.T) {
	cases := map[string]gen.ResponseCase{}
	for _, c := range corpus.Suite().Responses {
		cases[c.ID] = c
	}
	for _, id := range []string{
		"IgnoreQueryParamsInResponse",
		"XmlNamespaces",
		"XmlLists",
		"RestXmlXmlMapWithXmlNamespace",
		"RestXmlFlattenedXmlMapWithXmlNamespace",
	} {
		c, ok := cases[id]
		if !ok {
			t.Fatalf("the corpus has no response case %s", id)
		}
		out, ok := c.Outcome.(gen.Output)
		if !ok {
			t.Fatalf("%s: outcome %T is not an output", id, c.Outcome)
		}
		resp, err := restxml.Codec{}.Encode(nil, c.Operation, requestID, out.Value, http.StatusOK)
		if err != nil {
			t.Errorf("%s: %v", id, err.AsError())
			continue
		}
		want := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + interElementSpace.ReplaceAllString(strings.TrimSpace(c.Body), "><")
		if got := readAll(t, resp.Body); resp.Status != c.Code || got != want || resp.Header.Get("Content-Type") != c.Headers["Content-Type"] {
			t.Errorf("%s: %d %v\n%s\nwant\n%s", id, resp.Status, resp.Header, got, want)
		}
	}
}

// Server cases from the Smithy restXml corpus whose output has no members:
// "Empty output serializes no payload", so the body is empty and carries
// no Content-Type. The generated cases leave the empty body unasserted, so
// it is checked here.
func TestCorpusEmptyOutputs(t *testing.T) {
	cases := map[string]gen.ResponseCase{}
	for _, c := range corpus.Suite().Responses {
		cases[c.ID] = c
	}
	for _, id := range []string{"EmptyInputAndEmptyOutput", "NoInputAndOutput"} {
		c, ok := cases[id]
		if !ok {
			t.Fatalf("the corpus has no response case %s", id)
		}
		out, ok := c.Outcome.(gen.Output)
		if !ok {
			t.Fatalf("%s: outcome %T is not an output", id, c.Outcome)
		}
		resp, err := restxml.Codec{}.Encode(nil, c.Operation, requestID, out.Value, http.StatusOK)
		if err != nil {
			t.Errorf("%s: %v", id, err.AsError())
			continue
		}
		if got := readAll(t, resp.Body); resp.Status != c.Code || got != "" || resp.Header.Get("Content-Type") != "" {
			t.Errorf("%s: %d %v %q, want %d with no body and no Content-Type", id, resp.Status, resp.Header, got, c.Code)
		}
	}
}
