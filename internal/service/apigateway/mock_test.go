package apigateway_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/service/apigateway"
)

// integrationResponse is one integration response of a MOCK method.
type integrationResponse struct {
	status, pattern string
	params          map[string]string
	templates       map[string]string
	contentHandling bool
}

// mock is a MOCK method's configuration: request templates, passthrough
// behavior ("" leaves the default) and integration responses in creation
// order.
type mock struct {
	templates   map[string]string
	passthrough string
	responses   []integrationResponse
}

// mockMethod puts GET on a new resource part of a fresh API with a MOCK
// integration per m, deploys it to stage s and returns the API id.
func (h *harness) mockMethod(part string, m mock) string {
	h.Helper()
	id, root := h.api("t")
	res := h.resource(id, root, part)
	h.method(id, res, "GET")
	in := &apigatewayapi.PutIntegrationInput{RestApiId: id, ResourceId: res, HttpMethod: "GET", Type: apigatewayapi.IntegrationTypeMock, RequestTemplates: m.templates}
	if m.passthrough != "" {
		in.PassthroughBehavior = new(m.passthrough)
	}
	must(h, apigatewayapi.OpPutIntegration, in)
	for _, r := range m.responses {
		must(h, apigatewayapi.OpPutMethodResponse, &apigatewayapi.PutMethodResponseInput{RestApiId: id, ResourceId: res, HttpMethod: "GET", StatusCode: r.status})
		ir := &apigatewayapi.PutIntegrationResponseInput{RestApiId: id, ResourceId: res, HttpMethod: "GET", StatusCode: r.status, ResponseParameters: r.params, ResponseTemplates: r.templates}
		if r.pattern != "" {
			ir.SelectionPattern = new(r.pattern)
		}
		if r.contentHandling {
			ir.ContentHandling = new(apigatewayapi.ContentHandlingStrategyConvertToText)
		}
		must(h, apigatewayapi.OpPutIntegrationResponse, ir)
	}
	h.deploy(id)
	return id
}

// getWith sends a GET of the stage path with the given headers.
func (h *harness) getWith(id, path string, header map[string]string, body string) *http.Response {
	h.Helper()
	r := h.request(http.MethodGet, pathURL(id, path), body)
	for k, v := range header {
		r.Header.Set(k, v)
	}
	return h.send(r).Result()
}

const literalStatus = `{"statusCode": 200}`

// A literal MOCK request template selects its status, and the selected
// integration response's static responseParameters and literal response
// template make the response (aws-recorded,
// test_apigateway_basic.snapshot.json:L154-L161; aws-doc
// API_IntegrationResponse: "a static value enclosed within a pair of
// single quotes"; apigateway-rest.md 3.5, 6.3); the unquoted-key form
// works and selectionPattern 2\d{2} matches it (aws-recorded,
// test_apigateway_integrations.snapshot.json:L1062-L1083); with no response
// template the body is empty (test_apigateway_integrations.py:L620-L628);
// Content-Type is the template's media type, application/json by default
// (aws-doc api-gateway-known-issues, 4.8).
func TestMockLiteralTemplates(t *testing.T) {
	run(t, func(h *harness) {
		id := h.mockMethod("cors", mock{
			templates: map[string]string{"application/json": literalStatus},
			responses: []integrationResponse{{status: "200", params: map[string]string{"method.response.header.Access-Control-Allow-Origin": "'*'"}, templates: map[string]string{"application/json": `{"ok":true}`}}},
		})
		rec := h.get(id, "/s/cors")
		if rec.Code != http.StatusOK || rec.Body.String() != `{"ok":true}` || rec.Header().Get("Access-Control-Allow-Origin") != "*" || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("MOCK = %d %v %q, want 200 with Access-Control-Allow-Origin * and the literal template", rec.Code, rec.Header(), rec.Body)
		}
		wantRequestIDs(t, rec, "MOCK")
		empty := h.mockMethod("empty", mock{
			templates: map[string]string{"application/json": `{statusCode: 200}`},
			responses: []integrationResponse{{status: "200", pattern: `2\d{2}`}},
		})
		if rec := h.get(empty, "/s/empty"); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
			t.Errorf("MOCK without a response template = %d %q, want 200 with an empty body", rec.Code, rec.Body)
		}
	})
}

// The status a MOCK request template renders selects the integration
// response whose selectionPattern matches it, the default otherwise
// (aws-doc API_IntegrationResponse and handle-errors-in-lambda-integration,
// apigateway-rest.md 6.7).
func TestMockSelectsByStatus(t *testing.T) {
	run(t, func(h *harness) {
		id := h.mockMethod("r", mock{
			templates: map[string]string{"application/json": `{"statusCode": 404}`},
			responses: []integrationResponse{{status: "200"}, {status: "404", pattern: `4\d{2}`, templates: map[string]string{"application/json": "missing"}}},
		})
		if rec := h.get(id, "/s/r"); rec.Code != http.StatusNotFound || rec.Body.String() != "missing" {
			t.Errorf("status 404 template = %d %q, want the 404 response", rec.Code, rec.Body)
		}
	})
}

// What APIGW1 recorded of MOCK selection (2026-11-03; apigateway-rest.md
// 4.7, 6.7): the 415 answer's body and error type (09); the response
// template whose media type is the whole Accept value, else the first by
// name, sent as application/json whatever the template (11); and of two
// matching selection patterns neither the lower status nor the first
// created (12).
func TestMockSelectionAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		never := h.mockMethod("never", mock{templates: map[string]string{"application/json": literalStatus}, passthrough: "NEVER", responses: []integrationResponse{{status: "200"}}})
		resp := h.getWith(never, "/s/never", map[string]string{"Content-Type": "application/xml"}, "x")
		if body := readBody(t, resp); resp.StatusCode != http.StatusUnsupportedMediaType || body != `{"message": "Unsupported Media Type"}` ||
			resp.Header.Get("X-Amzn-Errortype") != "BadRequestException" || resp.Header.Get("Content-Type") != "application/json" {
			t.Errorf("415 = %d %v %q, want 415 BadRequestException application/json {\"message\": \"Unsupported Media Type\"}", resp.StatusCode, resp.Header, body)
		}

		accept := h.mockMethod("accept", mock{
			templates: map[string]string{"application/json": literalStatus},
			responses: []integrationResponse{{status: "200", templates: map[string]string{"application/xml": "<r>xml</r>", "text/plain": "plain"}}},
		})
		for value, want := range map[string]string{
			"application/xml": "<r>xml</r>", "text/plain": "plain", "application/json": "<r>xml</r>", "": "<r>xml</r>", "text/html, text/plain": "<r>xml</r>",
		} {
			headers := map[string]string{}
			if value != "" {
				headers["Accept"] = value
			}
			resp := h.getWith(accept, "/s/accept", headers, "")
			if body, ct := readBody(t, resp), resp.Header.Get("Content-Type"); body != want || ct != "application/json" {
				t.Errorf("Accept %q = %q %s, want %q application/json", value, body, ct, want)
			}
		}

		for _, c := range []struct {
			name      string
			responses []integrationResponse
			want      int
		}{
			{"tie-a", []integrationResponse{{status: "200"}, {status: "400", pattern: `4\d{2}`}, {status: "404", pattern: "404"}}, http.StatusBadRequest},
			{"tie-b", []integrationResponse{{status: "200"}, {status: "404", pattern: "404"}, {status: "400", pattern: `4\d{2}`}}, http.StatusBadRequest},
			{"tie-c", []integrationResponse{{status: "200"}, {status: "499", pattern: `4\d{2}`}, {status: "401", pattern: `40\d`}}, 499},
		} {
			id := h.mockMethod(c.name, mock{templates: map[string]string{"application/json": `{"statusCode": 404}`}, responses: c.responses})
			if rec := h.get(id, "/s/"+c.name); rec.Code != c.want {
				t.Errorf("%s = %d, want %d", c.name, rec.Code, c.want)
			}
		}
	})
}

// Anchoring, order, template choice and media type matching are switch
// apigateway.mock.selection (APIGW1-09, -11, -12): the test reads each
// field and asserts understudy follows it.
func TestMockSelectionFollowsTheSwitch(t *testing.T) {
	sw := apigateway.MockSelectionSwitch().Value
	run(t, func(h *harness) {
		anchored := h.mockMethod("anchor", mock{
			templates: map[string]string{"application/json": `{"statusCode": 404}`},
			responses: []integrationResponse{{status: "200"}, {status: "400", pattern: "0"}},
		})
		want := http.StatusBadRequest
		if sw.PatternFullMatch {
			want = http.StatusOK
		}
		if rec := h.get(anchored, "/s/anchor"); rec.Code != want {
			t.Errorf("pattern 0 against 404 = %d, want %d (PatternFullMatch %v)", rec.Code, want, sw.PatternFullMatch)
		}
		// .+ sorts before and is shorter than 4\d\d (PatternOrder).
		ordered := h.mockMethod("order", mock{
			templates: map[string]string{"application/json": `{"statusCode": 404}`},
			responses: []integrationResponse{{status: "500", pattern: `4\d\d`}, {status: "400", pattern: `.+`}, {status: "200"}},
		})
		if rec := h.get(ordered, "/s/order"); rec.Code != http.StatusInternalServerError {
			t.Errorf("two matching patterns = %d, want 500 (PatternOrder %v)", rec.Code, sw.PatternOrder)
		}
		essence := h.mockMethod("essence", mock{
			templates: map[string]string{"application/json": literalStatus}, passthrough: "NEVER",
			responses: []integrationResponse{{status: "200"}},
		})
		resp := h.getWith(essence, "/s/essence", map[string]string{"Content-Type": "Application/JSON; charset=utf-8"}, "x")
		readBody(t, resp)
		want = sw.UnsupportedMediaType.Status
		if sw.MediaTypeEssence {
			want = http.StatusOK
		}
		if resp.StatusCode != want {
			t.Errorf("Content-Type Application/JSON; charset=utf-8 = %d, want %d (MediaTypeEssence %v)", resp.StatusCode, want, sw.MediaTypeEssence)
		}
	})
}

func readBody(tb testing.TB, resp *http.Response) string {
	tb.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		tb.Fatal(err)
	}
	return string(b)
}

// Passthrough behavior and 415, the documented table (aws-doc
// integration-passthrough-behaviors, apigateway-rest.md 6.2): a request
// with no Content-Type is application/json; the 415 cells answer switch
// apigateway.mock.selection's UnsupportedMediaType (its text is APIGW1-09);
// a pass-through renders the request body, whose statusCode then selects.
func TestMockPassthroughTable(t *testing.T) {
	unsupported := apigateway.MockSelectionSwitch().Value.UnsupportedMediaType
	jsonOnly := map[string]string{"application/json": literalStatus}
	xmlOnly := map[string]string{"application/xml": literalStatus}
	for _, c := range []struct {
		name        string
		templates   map[string]string
		passthrough string
		contentType string
		want        int // 0: the 415 answer
	}{
		{"json template, no type, WHEN_NO_MATCH", jsonOnly, "WHEN_NO_MATCH", "", 200},
		{"json template, no type, NEVER", jsonOnly, "NEVER", "", 200},
		{"json template, xml, WHEN_NO_MATCH", jsonOnly, "WHEN_NO_MATCH", "application/xml", 201},
		{"json template, xml, WHEN_NO_TEMPLATES", jsonOnly, "WHEN_NO_TEMPLATES", "application/xml", 0},
		{"json template, xml, NEVER", jsonOnly, "NEVER", "application/xml", 0},
		{"xml template, json, WHEN_NO_MATCH", xmlOnly, "WHEN_NO_MATCH", "application/json", 201},
		{"xml template, json, NEVER", xmlOnly, "NEVER", "application/json", 0},
		{"no templates, WHEN_NO_TEMPLATES", nil, "WHEN_NO_TEMPLATES", "text/plain", 201},
		{"no templates, NEVER", nil, "NEVER", "text/plain", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			run(t, func(h *harness) {
				id := h.mockMethod("r", mock{templates: c.templates, passthrough: c.passthrough, responses: []integrationResponse{
					{status: "200"}, {status: "201", pattern: "201"},
				}})
				r := h.request(http.MethodGet, pathURL(id, "/s/r"), `{"statusCode": 201}`)
				if c.contentType != "" {
					r.Header.Set("Content-Type", c.contentType)
				}
				rec := h.send(r)
				if c.want == 0 {
					wantAnswer(t, rec, unsupported, c.name)
					return
				}
				if rec.Code != c.want {
					t.Errorf("%s = %d %s, want %d", c.name, rec.Code, rec.Body, c.want)
				}
			})
		})
	}
}

// What MOCK cannot run answers 501 naming it, journaled (spec 6b: "no
// template with $, # or \ ever renders; each answers 501 and is
// journaled"; contract 7.7 step 11): a VTL request template, the recorded
// refusal check's VTL response template (localstack@8b9a79f
// test_apigateway_basic.snapshot.json:L154-L262), a selectionPattern RE2
// refuses, contentHandling on the selected response and a non-static
// responseParameter. A selection with no default and no match is the
// configuration error 500 (aws-doc supported-gateway-response-types
// API_CONFIGURATION_ERROR; aws-recorded body, apigateway-rest.md 4.7).
func TestMockRefusals(t *testing.T) {
	run(t, func(h *harness) {
		for _, c := range []struct {
			name, setting string
			m             mock
		}{
			{"a VTL request template", "template", mock{
				templates: map[string]string{"application/json": `#set($s = 200){"statusCode": $s}`},
				responses: []integrationResponse{{status: "200"}},
			}},
			{"a VTL response template", "template", mock{
				templates: map[string]string{"application/json": literalStatus},
				responses: []integrationResponse{{status: "200", templates: map[string]string{"application/json": `{"statusCode": 200, "id": $input.params().path.id}`}}},
			}},
			{"an escape in a response template", "template", mock{
				templates: map[string]string{"application/json": literalStatus},
				responses: []integrationResponse{{status: "200", templates: map[string]string{"application/json": `a\nb`}}},
			}},
			{"a lookaround selectionPattern", "(?=2)", mock{
				templates: map[string]string{"application/json": literalStatus},
				responses: []integrationResponse{{status: "200", pattern: `(?=2)\d+`}},
			}},
			{"contentHandling on the selected response", "contentHandling", mock{
				templates: map[string]string{"application/json": literalStatus},
				responses: []integrationResponse{{status: "200", contentHandling: true}},
			}},
			{"a non-static responseParameter", "method.response.header.X", mock{
				templates: map[string]string{"application/json": literalStatus},
				responses: []integrationResponse{{status: "200", params: map[string]string{"method.response.header.X": "integration.response.header.X"}}},
			}},
		} {
			id := h.mockMethod("r", c.m)
			want501(t, h.get(id, "/s/r"), c.setting, c.name)
			h.wantJournaled501(c.name)
		}
		id := h.mockMethod("r", mock{
			templates: map[string]string{"application/json": literalStatus},
			responses: []integrationResponse{{status: "404", pattern: `4\d{2}`}},
		})
		wantAnswer(t, h.get(id, "/s/r"), apigateway.ConfigurationError(), "no default and no match")
	})
}
