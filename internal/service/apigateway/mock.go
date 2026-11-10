package apigateway

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// toMock is a request a method sends to its MOCK integration.
type toMock struct {
	owner        aws.Scope
	apiID        string
	resourcePath string
	methodKey    string
	integration  Integration
}

func (toMock) isSelection() {}

// The passthrough behaviors (apigateway-rest.md 6.2).
const (
	passthroughWhenNoTemplates = "WHEN_NO_TEMPLATES"
	passthroughNever           = "NEVER"
)

// headerParamPrefix is the only responseParameters target a MOCK response
// sets: a method response header (apigateway-rest.md 3.5).
const headerParamPrefix = "method.response.header."

// runMock answers a request with its MOCK integration
// (apigateway-rest.md 6.2, 6.3, 6.7): the request template the
// Content-Type selects (or the request body under passthrough) gives the
// status, which selects an integration response; its static
// responseParameters and literal response template make the response.
// Anything else understudy would have to render or evaluate answers 501.
func runMock(w http.ResponseWriter, r *http.Request, t toMock, body []byte) *awserr.Error {
	sw := MockSelectionSwitch().Value
	where := fmt.Sprintf("method %s %s of REST API %s", t.methodKey, t.resourcePath, t.apiID)
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		// "API Gateway defaults to application/json" (apigateway-rest.md 6.1).
		contentType = "application/json"
	}
	rendered, refused, ok := requestTemplate(t.integration, contentType, body, sw.MediaTypeEssence)
	if !ok {
		return writeAnswer(w, sw.UnsupportedMediaType)
	}
	if refused != "" {
		return apigatewayapi.ErrNotImplemented(fmt.Sprintf(
			"understudy does not render mapping templates with VTL: the request template for %s of the MOCK integration of %s contains '$', '#' or '\\', "+
				"so no integration ran and nothing was changed. Use a literal request template, such as {\"statusCode\": 200}.", refused, where))
	}
	status, hasStatus := MockStatus(rendered)
	resp, err := selectResponse(t.integration.Responses, status, hasStatus, sw)
	switch {
	case err != "":
		return apigatewayapi.ErrNotImplemented(fmt.Sprintf(
			"understudy runs selectionPattern as Go RE2, which refuses %s of the integration responses of %s, so no integration ran and nothing was changed. "+
				"Rewrite the pattern without lookarounds, backreferences or possessive quantifiers.", err, where))
	case resp == nil:
		return writeAnswer(w, ConfigurationError())
	case resp.ContentHandling != nil:
		return apigatewayapi.ErrNotImplemented(fmt.Sprintf(
			"understudy does not apply an integration response's contentHandling (%s): integration response %s of %s sets it, "+
				"so the response was not sent and nothing was changed. Remove contentHandling from the integration response.", *resp.ContentHandling, resp.StatusCode, where))
	}
	code, cerr := strconv.Atoi(resp.StatusCode)
	if cerr != nil {
		return awserr.Internalf(fmt.Errorf("apigateway: integration response %q of %s is not a status code, which PutIntegrationResponse refuses", resp.StatusCode, where))
	}
	headers, refusedParam := staticHeaders(resp.ResponseParameters)
	if refusedParam != "" {
		return apigatewayapi.ErrNotImplemented(fmt.Sprintf(
			"understudy sets only static responseParameters ('value'): %s of integration response %s of %s maps %q, "+
				"so the response was not sent and nothing was changed. Use a static value in single quotes.", refusedParam, resp.StatusCode, where, resp.ResponseParameters[refusedParam]))
	}
	mediaType, template := responseTemplate(resp.ResponseTemplates, r.Header.Get("Accept"), sw)
	if !IsLiteralTemplate(template) {
		return apigatewayapi.ErrNotImplemented(fmt.Sprintf(
			"understudy does not render mapping templates with VTL: the response template for %s of integration response %s of %s contains '$', '#' or '\\', "+
				"so the response was not sent and nothing was changed. Use a literal response template.", mediaType, resp.StatusCode, where))
	}
	// AWS sent application/json whichever template it chose (APIGW1-11,
	// recorded 2026-11-03); a static Content-Type response parameter
	// replaces it.
	w.Header().Set("Content-Type", "application/json")
	for _, name := range slices.Sorted(maps.Keys(headers)) {
		w.Header().Set(name, headers[name])
	}
	w.WriteHeader(code)
	// A failed write means the client went away; nothing else can be sent.
	_, _ = io.WriteString(w, template) //nolint:errcheck,gosec // the client is gone when this fails; the body is the API owner's configured template, sent as AWS sends it
	return nil
}

// requestTemplate applies the passthrough table (apigateway-rest.md 6.2):
// the rendered text is the template the content type selects, or the body
// under pass-through; ok false is a 415 cell. A selected template that is
// not literal is refused, named by its media type.
func requestTemplate(i Integration, contentType string, body []byte, essence bool) (rendered, refused string, ok bool) {
	if key, found := findMediaType(i.RequestTemplates, contentType, essence); found {
		t := i.RequestTemplates[key]
		if !IsLiteralTemplate(t) {
			return "", key, true
		}
		return t, "", true
	}
	switch i.PassthroughBehavior {
	case passthroughNever:
		return "", "", false
	case passthroughWhenNoTemplates:
		if len(i.RequestTemplates) > 0 {
			return "", "", false
		}
	}
	return string(body), "", true
}

// findMediaType finds the key of templates that names mediaType: by
// type/subtype, case-insensitively, when essence; else byte for byte
// (switch apigateway.mock.selection MediaTypeEssence).
func findMediaType(templates map[string]string, mediaType string, essence bool) (string, bool) {
	if !essence {
		_, ok := templates[mediaType]
		return mediaType, ok
	}
	want := mediaEssence(mediaType)
	for _, k := range slices.Sorted(maps.Keys(templates)) {
		if mediaEssence(k) == want {
			return k, true
		}
	}
	return "", false
}

// mediaEssence is a media type's type/subtype, lowercase, without
// parameters.
func mediaEssence(v string) string {
	if t, _, err := mime.ParseMediaType(v); err == nil {
		return t
	}
	t, _, _ := strings.Cut(v, ";")
	return strings.ToLower(strings.TrimSpace(t))
}

// selectResponse selects the integration response for status
// (apigateway-rest.md 6.7): the first patterned response whose
// selectionPattern matches the status text, in the switch's order, else
// the default (no or an empty pattern). With no status only the default
// is selected (inferred). refused names a pattern
// Go RE2 cannot compile; nil and "" is no response to select.
func selectResponse(responses map[string]IntegrationResponse, status int, hasStatus bool, sw MockSelection) (selected *IntegrationResponse, refused string) {
	all := slices.Collect(maps.Values(responses))
	slices.SortFunc(all, func(a, b IntegrationResponse) int {
		pa, pb := deref(a.SelectionPattern, ""), deref(b.SelectionPattern, "")
		order := cmp.Compare(pb, pa)
		if sw.PatternOrder == PatternsLongestFirst {
			order = cmp.Or(cmp.Compare(len(pb), len(pa)), order)
		}
		return cmp.Or(order, cmp.Compare(a.StatusCode, b.StatusCode))
	})
	var def *IntegrationResponse
	text := strconv.Itoa(status)
	for i := range all {
		p := deref(all[i].SelectionPattern, "")
		if p == "" {
			if def == nil {
				def = &all[i]
			}
			continue
		}
		if !hasStatus {
			continue
		}
		expr := p
		if sw.PatternFullMatch {
			expr = `^(?:` + p + `)$`
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Sprintf("selectionPattern %q of integration response %s", p, all[i].StatusCode)
		}
		if re.MatchString(text) {
			return &all[i], ""
		}
	}
	return def, ""
}

// staticHeaders reads responseParameters: each method.response.header.<name>
// mapped to '<static>' sets that header to the text inside the quotes
// (apigateway-rest.md 3.5). refused names the first parameter of any
// other form.
func staticHeaders(params map[string]string) (headers map[string]string, refused string) {
	headers = make(map[string]string, len(params))
	for _, k := range slices.Sorted(maps.Keys(params)) {
		v := params[k]
		name, ok := strings.CutPrefix(k, headerParamPrefix)
		if !ok || name == "" || len(v) < 2 || v[0] != '\'' || v[len(v)-1] != '\'' {
			return nil, k
		}
		headers[name] = v[1 : len(v)-1]
	}
	return headers, ""
}

// responseTemplate chooses the selected response's template as APIGW1-11
// recorded (2026-11-03; apigateway-rest.md 6.7): the one whose media type
// is the whole Accept value, so a later entry of a list is not matched,
// else application/json, else the first by media type. No templates is an
// empty body; mediaType is the chosen key.
func responseTemplate(templates map[string]string, accept string, sw MockSelection) (mediaType, template string) {
	if len(templates) == 0 {
		return "", ""
	}
	if accept != "" {
		if k, found := findMediaType(templates, accept, sw.MediaTypeEssence); found {
			return k, templates[k]
		}
	}
	if k, found := findMediaType(templates, "application/json", sw.MediaTypeEssence); found {
		return k, templates[k]
	}
	k := slices.Min(slices.Collect(maps.Keys(templates)))
	return k, templates[k]
}
