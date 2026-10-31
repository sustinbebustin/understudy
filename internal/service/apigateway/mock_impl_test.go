package apigateway_test

import (
	"fmt"
	"net/http"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/apigateway"
)

// A pass-through renders the request body, which is not a template: a
// body holding '$' is not refused, and its statusCode selects
// (apigateway-rest.md 6.2, 6.3).
func TestMockPassthroughBodyIsNotATemplate(t *testing.T) {
	run(t, func(h *harness) {
		id := h.mockMethod("r", mock{
			templates: map[string]string{"application/json": `{"statusCode": 200}`},
			responses: []integrationResponse{{status: "200"}, {status: "201", pattern: "201"}},
		})
		r := h.request(http.MethodGet, pathURL(id, "/s/r"), `{"statusCode": 201, "price": "$5"}`)
		r.Header.Set("Content-Type", "application/xml")
		if rec := h.send(r); rec.Code != http.StatusCreated {
			t.Errorf("pass-through of a body with $ = %d %s, want 201", rec.Code, rec.Body)
		}
	})
}

// Only the selected response's template is rendered: a VTL template on a
// response the status does not select is never reached
// (apigateway-rest.md 6.7, 12.7).
func TestMockRefusesOnlyTheSelectedTemplate(t *testing.T) {
	run(t, func(h *harness) {
		id := h.mockMethod("r", mock{
			templates: map[string]string{"application/json": `{"statusCode": 200}`},
			responses: []integrationResponse{
				{status: "200", templates: map[string]string{"application/json": "ok"}},
				{status: "500", pattern: `5\d{2}`, templates: map[string]string{"application/json": `$input.body`}},
			},
		})
		if rec := h.get(id, "/s/r"); rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Errorf("MOCK = %d %q, want 200 ok", rec.Code, rec.Body)
		}
	})
}

// A template without a statusCode selects the default integration
// response (contract 7.7 step 11c, inferred; asserted by status).
func TestMockWithoutStatusSelectsTheDefault(t *testing.T) {
	run(t, func(h *harness) {
		id := h.mockMethod("r", mock{
			templates: map[string]string{"application/json": `{}`},
			responses: []integrationResponse{{status: "201"}, {status: "400", pattern: `.*`}},
		})
		if rec := h.get(id, "/s/r"); rec.Code != http.StatusCreated {
			t.Errorf("MOCK without statusCode = %d %s, want the default 201", rec.Code, rec.Body)
		}
	})
}

// A responseParameters target other than a method response header, or a
// value that is not a static 'value', answers 501 naming it and is
// journaled (contract 7.7 step 11e).
func TestMockRefusesNonHeaderParameters(t *testing.T) {
	run(t, func(h *harness) {
		for _, p := range []map[string]string{
			{"method.response.body": "'x'"},
			{"method.response.header.X": "'unterminated"},
		} {
			id := h.mockMethod("r", mock{
				templates: map[string]string{"application/json": `{"statusCode": 200}`},
				responses: []integrationResponse{{status: "200", params: p}},
			})
			for k := range p {
				want501(t, h.get(id, "/s/r"), k, k)
				h.wantJournaled501(k)
			}
		}
	})
}

// Several response templates of which none matches Accept or
// application/json and none is the only one: no template is chosen, which
// is API_CONFIGURATION_ERROR (aws-doc supported-gateway-response-types,
// apigateway-rest.md 6.7; the choice itself is switch
// apigateway.mock.selection TemplateByAccept).
func TestMockNoChosenTemplateIsAConfigurationError(t *testing.T) {
	run(t, func(h *harness) {
		id := h.mockMethod("r", mock{
			templates: map[string]string{"application/json": `{"statusCode": 200}`},
			responses: []integrationResponse{{status: "200", templates: map[string]string{"text/plain": "T", "application/xml": "X"}}},
		})
		rec := h.getWith(id, "/s/r", map[string]string{"Accept": "text/html"}, "")
		if rec.StatusCode != apigateway.ConfigurationError().Status {
			t.Errorf("no chosen template = %d, want %d", rec.StatusCode, apigateway.ConfigurationError().Status)
		}
		readBody(t, rec)
	})
}

// Every spelling of a status 100..599 the lenient parse accepts gives
// that status (apigateway-rest.md 6.3: quoted and unquoted keys).
func TestMockStatusProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(100, 599).Draw(t, "status")
		form := rapid.SampledFrom([]string{`{"statusCode": %d}`, `{statusCode:%d}`, `{"statusCode" : "%d", "x": 1}`, "{\n\t\"statusCode\":\n%d\n}"}).Draw(t, "form")
		in := fmt.Sprintf(form, n)
		if got, ok := apigateway.MockStatus(in); !ok || got != n {
			t.Fatalf("MockStatus(%q) = %d, %v; want %d", in, got, ok, n)
		}
	})
}
