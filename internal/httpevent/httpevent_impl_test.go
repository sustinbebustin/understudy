package httpevent_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/httpevent"
)

// routeRequest is a POST with a body, a query and a cookie to a route of
// an HTTP API's named stage, so every optional key of the event is there.
func routeRequest() httpevent.Request {
	return httpevent.Request{
		Method: http.MethodPost, RawPath: "/prod/pets/1", RawQuery: "a=1", Protocol: "HTTP/1.1", Host: "a1b2c3.execute-api.us-east-1.localhost:4566",
		Header:   http.Header{"Content-Type": {"application/json"}, "Cookie": {"s=1"}},
		SourceIP: "127.0.0.1", Scheme: "http", Port: "4566", Body: []byte(`{"name":"rex"}`),
	}
}

func routeContext() httpevent.Context {
	return httpevent.Context{
		AccountID: "000000000000", APIID: "a1b2c3", DomainName: "a1b2c3.execute-api.us-east-1.localhost", DomainPrefix: "a1b2c3",
		RouteKey: "POST /pets/{id}", Stage: "prod", PathParameters: map[string]string{"id": "1"}, StageVariables: map[string]string{"color": "blue"},
		RequestID: "JKJaXmPLvHcESHA=", At: time.UnixMilli(1583817383220), Authorizer: httpevent.NoAuthorizer{},
	}
}

// topLevelKeys lists the keys of the JSON object raw in the order written.
func topLevelKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("event %s does not start an object: %v, %v", raw, tok, err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("event %s: %v", raw, err)
		}
		key, ok := tok.(string)
		if !ok {
			t.Fatalf("event %s: key token %v is not a string", raw, tok)
		}
		keys = append(keys, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("event %s: value of %s: %v", raw, key, err)
		}
	}
	return keys
}

// With every optional member present, the event's keys follow the order of
// the HTTP API guide's payload 2.0 example (apigateway-http.md 5.1), which
// puts pathParameters after body and stageVariables last.
func TestEventV2KeyOrder(t *testing.T) {
	raw, err := httpevent.EventV2(routeRequest(), routeContext())
	if err != nil {
		t.Fatalf("EventV2: %v", err)
	}
	want := []string{
		"version", "routeKey", "rawPath", "rawQueryString", "cookies", "headers", "queryStringParameters",
		"requestContext", "body", "pathParameters", "isBase64Encoded", "stageVariables",
	}
	if got := topLevelKeys(t, raw); !slices.Equal(got, want) {
		t.Errorf("keys = %v\nwant   %v\nfrom %s", got, want, raw)
	}
}

// Route and stage variables that are present but empty are left out, as
// absent ones are (apigateway-http.md 5.1: "omitted when the route has
// none", "omitted when none").
func TestEventV2OmitsEmptyVariables(t *testing.T) {
	c := routeContext()
	c.PathParameters, c.StageVariables = map[string]string{}, map[string]string{}
	raw, err := httpevent.EventV2(routeRequest(), c)
	if err != nil {
		t.Fatalf("EventV2: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("event %s: %v", raw, err)
	}
	for _, k := range []string{"pathParameters", "stageVariables"} {
		if v, ok := keys[k]; ok {
			t.Errorf("%s = %s, want it omitted when empty", k, v)
		}
	}
}

// An AWS_IAM route reports its signer under requestContext.authorizer.iam
// with all seven members, cognitoIdentity and principalOrgId null for an
// IAM user (apigateway-http.md 5.1); accountId of requestContext stays the
// context's.
func TestEventV2ReportsTheIAMAuthorizer(t *testing.T) {
	c := routeContext()
	c.Authorizer = httpevent.IAMAuthorizer{
		AccessKey: "AKIDEXAMPLE", AccountID: "111122223333", CallerID: "AIDACALLER",
		UserARN: "arn:aws:iam::111122223333:user/example-user", UserID: "AIDAUSER",
	}
	raw, err := httpevent.EventV2(routeRequest(), c)
	if err != nil {
		t.Fatalf("EventV2: %v", err)
	}
	var ev struct {
		RequestContext struct {
			AccountID  string `json:"accountId"`
			Authorizer struct {
				IAM map[string]json.RawMessage `json:"iam"`
			} `json:"authorizer"`
		} `json:"requestContext"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("event %s: %v", raw, err)
	}
	if ev.RequestContext.AccountID != "000000000000" {
		t.Errorf("requestContext.accountId = %q, want the context's 000000000000", ev.RequestContext.AccountID)
	}
	want := map[string]string{
		"accessKey": `"AKIDEXAMPLE"`, "accountId": `"111122223333"`, "callerId": `"AIDACALLER"`,
		"cognitoIdentity": `null`, "principalOrgId": `null`,
		"userArn": `"arn:aws:iam::111122223333:user/example-user"`, "userId": `"AIDAUSER"`,
	}
	got := map[string]string{}
	for k, v := range ev.RequestContext.Authorizer.IAM {
		got[k] = string(v)
	}
	if len(got) != len(want) {
		t.Errorf("authorizer.iam = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("authorizer.iam.%s = %s, want %s", k, got[k], v)
		}
	}
}

// A context without an authorizer is a caller bug, refused rather than
// guessed: NoAuthorizer is how a NONE route says it has none.
func TestEventV2RefusesANilAuthorizer(t *testing.T) {
	c := routeContext()
	c.Authorizer = nil
	if raw, err := httpevent.EventV2(routeRequest(), c); err == nil {
		t.Fatalf("EventV2 with a nil Authorizer = %s, want an error", raw)
	}
}
