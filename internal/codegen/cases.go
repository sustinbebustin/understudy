package codegen

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

// The compliance case traits (smithy-protocol-tests.md 2).
const (
	traitRequestTests   = "smithy.test#httpRequestTests"
	traitResponseTests  = "smithy.test#httpResponseTests"
	traitMalformedTests = "smithy.test#httpMalformedRequestTests"
)

// CasesIR are a corpus service's server-runnable cases: appliesTo absent or
// server, for a protocol the service speaks, malformed entries expanded by
// their testParameters.
type CasesIR struct {
	Requests  []RequestCaseIR
	Responses []ResponseCaseIR
	Malformed []MalformedCaseIR
}

// RequestCaseIR is one request case.
type RequestCaseIR struct {
	Op   *OperationIR
	Case requestCase
}

// ResponseCaseIR is one response case: of an operation's output, or of an
// error structure (Error set) through an operation that lists it.
type ResponseCaseIR struct {
	Op    *OperationIR
	Error *ErrorIR
	Case  responseCase
}

// MalformedCaseIR is one expanded malformed-request case.
type MalformedCaseIR struct {
	Op   *OperationIR
	Case malformedCase
}

type requestCase struct {
	ID                 string            `json:"id"`
	Protocol           string            `json:"protocol"`
	Method             string            `json:"method"`
	URI                string            `json:"uri"`
	Host               string            `json:"host"`
	ResolvedHost       string            `json:"resolvedHost"`
	QueryParams        []string          `json:"queryParams"`
	ForbidQueryParams  []string          `json:"forbidQueryParams"`
	RequireQueryParams []string          `json:"requireQueryParams"`
	Headers            map[string]string `json:"headers"`
	ForbidHeaders      []string          `json:"forbidHeaders"`
	RequireHeaders     []string          `json:"requireHeaders"`
	Body               string            `json:"body"`
	BodyMediaType      string            `json:"bodyMediaType"`
	Params             json.RawMessage   `json:"params"`
	Tags               []string          `json:"tags"`
	AppliesTo          string            `json:"appliesTo"`
}

type responseCase struct {
	ID             string            `json:"id"`
	Protocol       string            `json:"protocol"`
	Code           int               `json:"code"`
	Headers        map[string]string `json:"headers"`
	ForbidHeaders  []string          `json:"forbidHeaders"`
	RequireHeaders []string          `json:"requireHeaders"`
	Body           string            `json:"body"`
	BodyMediaType  string            `json:"bodyMediaType"`
	Params         json.RawMessage   `json:"params"`
	VendorParams   struct {
		Code string `json:"code"`
		Type string `json:"type"`
	} `json:"vendorParams"`
	Tags      []string `json:"tags"`
	AppliesTo string   `json:"appliesTo"`
}

type malformedCase struct {
	ID       string `json:"id"`
	Protocol string `json:"protocol"`
	Request  struct {
		Method        string            `json:"method"`
		URI           string            `json:"uri"`
		Host          string            `json:"host"`
		QueryParams   []string          `json:"queryParams"`
		Headers       map[string]string `json:"headers"`
		Body          string            `json:"body"`
		BodyMediaType string            `json:"bodyMediaType"`
	} `json:"request"`
	Response struct {
		Code    int               `json:"code"`
		Headers map[string]string `json:"headers"`
		Body    *struct {
			MediaType string `json:"mediaType"`
			Assertion struct {
				Contents     *string `json:"contents"`
				MessageRegex *string `json:"messageRegex"`
			} `json:"assertion"`
		} `json:"body"`
	} `json:"response"`
	Tags           []string            `json:"tags"`
	Documentation  string              `json:"documentation"`
	TestParameters map[string][]string `json:"testParameters"`
}

// cases collects a corpus service's cases from its operations and error
// structures.
func (b *builder) cases() error {
	if _, corpus := b.cfg.Destination.(CorpusPackage); !corpus {
		return nil
	}
	c := &CasesIR{}
	for _, o := range b.ir.Operations {
		traits := b.model.Shapes[o.ID].Traits
		if err := b.requestCases(c, o, traits[traitRequestTests]); err != nil {
			return err
		}
		if err := b.responseCases(c, o, nil, traits[traitResponseTests]); err != nil {
			return err
		}
		if err := b.malformedCases(c, o, traits[traitMalformedTests]); err != nil {
			return err
		}
	}
	for _, e := range b.ir.Errors {
		raw, ok := b.model.Shapes[e.Shape.ID].Traits[traitResponseTests]
		if !ok {
			continue
		}
		i := slices.IndexFunc(b.ir.Operations, func(o *OperationIR) bool { return slices.Contains(o.Errors, e) })
		if i < 0 {
			return failf(StageIR, string(e.Shape.ID), "has response cases, but no operation of %s lists it", b.ir.Shape)
		}
		if err := b.responseCases(c, b.ir.Operations[i], e, raw); err != nil {
			return err
		}
	}
	b.ir.Cases = c
	return nil
}

// speaks reports whether a case's protocol trait id is one of the service's
// protocols; a case for another protocol belongs to another service.
func (b *builder) speaks(protocol string) bool {
	p, ok := protocolTraits()[protocol]
	return ok && slices.Contains(b.ir.Protocols, p)
}

func (b *builder) requestCases(c *CasesIR, o *OperationIR, raw json.RawMessage) error {
	if raw == nil {
		return nil
	}
	var cs []requestCase
	if err := json.Unmarshal(raw, &cs); err != nil {
		return failf(StageIR, string(o.ID), "%s: %w", traitRequestTests, err)
	}
	for i := range cs {
		if cs[i].AppliesTo != "client" && b.speaks(cs[i].Protocol) {
			c.Requests = append(c.Requests, RequestCaseIR{Op: o, Case: cs[i]})
		}
	}
	return nil
}

func (b *builder) responseCases(c *CasesIR, o *OperationIR, e *ErrorIR, raw json.RawMessage) error {
	if raw == nil {
		return nil
	}
	var cs []responseCase
	if err := json.Unmarshal(raw, &cs); err != nil {
		return failf(StageIR, string(o.ID), "%s: %w", traitResponseTests, err)
	}
	for i := range cs {
		if cs[i].AppliesTo != "client" && b.speaks(cs[i].Protocol) {
			c.Responses = append(c.Responses, ResponseCaseIR{Op: o, Error: e, Case: cs[i]})
		}
	}
	return nil
}

func (b *builder) malformedCases(c *CasesIR, o *OperationIR, raw json.RawMessage) error {
	if raw == nil {
		return nil
	}
	var cs []malformedCase
	if err := json.Unmarshal(raw, &cs); err != nil {
		return failf(StageIR, string(o.ID), "%s: %w", traitMalformedTests, err)
	}
	for i := range cs {
		if !b.speaks(cs[i].Protocol) {
			continue
		}
		expanded, err := expandMalformed(&cs[i])
		if err != nil {
			return failf(StageIR, string(o.ID), "%s %s: %w", traitMalformedTests, cs[i].ID, err)
		}
		for j := range expanded {
			c.Malformed = append(c.Malformed, MalformedCaseIR{Op: o, Case: expanded[j]})
		}
	}
	return nil
}

// expandMalformed turns a parameterized malformed case into one case per
// parameter index, ids suffixed _case<i>, with every string of the request,
// the response and the tags interpolated (smithy-protocol-tests.md 2.2). A
// case without testParameters is returned as written: its strings are not
// processed, so $$ stays $$.
func expandMalformed(mc *malformedCase) ([]malformedCase, error) {
	if len(mc.TestParameters) == 0 {
		return []malformedCase{*mc}, nil
	}
	n := -1
	for name, vals := range mc.TestParameters {
		if n >= 0 && len(vals) != n {
			return nil, fmt.Errorf("testParameters %s has %d values where the others have %d", name, len(vals), n)
		}
		n = len(vals)
	}
	out := make([]malformedCase, 0, n)
	for i := range n {
		params := map[string]string{}
		for name, vals := range mc.TestParameters {
			params[name] = vals[i]
		}
		x, err := interpolateCase(mc, params)
		if err != nil {
			return nil, err
		}
		x.ID = mc.ID + "_case" + strconv.Itoa(i)
		x.TestParameters = nil
		out = append(out, x)
	}
	return out, nil
}

func interpolateCase(mc *malformedCase, params map[string]string) (malformedCase, error) {
	var err error
	sub := func(s string) string {
		if err != nil {
			return s
		}
		var out string
		out, err = interpolate(s, params)
		return out
	}
	x := *mc
	x.Request.URI = sub(mc.Request.URI)
	x.Request.Host = sub(mc.Request.Host)
	x.Request.Body = sub(mc.Request.Body)
	x.Request.BodyMediaType = sub(mc.Request.BodyMediaType)
	x.Request.QueryParams = subAll(mc.Request.QueryParams, sub)
	x.Request.Headers = subMap(mc.Request.Headers, sub)
	x.Response.Headers = subMap(mc.Response.Headers, sub)
	if mc.Response.Body != nil {
		body := *mc.Response.Body
		body.MediaType = sub(body.MediaType)
		if body.Assertion.Contents != nil {
			v := sub(*body.Assertion.Contents)
			body.Assertion.Contents = &v
		}
		if body.Assertion.MessageRegex != nil {
			v := sub(*body.Assertion.MessageRegex)
			body.Assertion.MessageRegex = &v
		}
		x.Response.Body = &body
	}
	x.Tags = subAll(mc.Tags, sub)
	x.Documentation = sub(mc.Documentation)
	return x, err
}

func subAll(in []string, sub func(string) string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = sub(s)
	}
	return out
}

func subMap(in map[string]string, sub func(string) string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[sub(k)] = sub(v)
	}
	return out
}

// interpolate expands Smithy CodeWriter named arguments: $name:L is the
// value as written, $name:S the value as a quoted string literal, and $$ a
// dollar sign.
func interpolate(s string, params map[string]string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' {
			b.WriteByte('$')
			i++
			continue
		}
		end := strings.IndexByte(s[i+1:], ':')
		if end < 0 || i+1+end+1 >= len(s) {
			return "", fmt.Errorf("%q: a $ starts no $name:L or $name:S argument", s)
		}
		name, format := s[i+1:i+1+end], s[i+1+end+1]
		v, ok := params[name]
		if !ok {
			return "", fmt.Errorf("%q names parameter %q, which testParameters does not give", s, name)
		}
		switch format {
		case 'L':
			b.WriteString(v)
		case 'S':
			b.WriteString(javaString(v))
		default:
			return "", fmt.Errorf("%q: formatter %c is not L or S", s, format)
		}
		i += 1 + end + 1
	}
	return b.String(), nil
}

// javaString quotes s as the CodeWriter S formatter does: a Java string
// literal with backslash escapes and \uXXXX for anything outside printable
// ASCII.
func javaString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r >= 0x20 && r < 0x7f {
				b.WriteRune(r)
				continue
			}
			for _, u := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&b, `\u%04x`, u)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// emitCases writes cases_gen.go: the service's Suite as typed literals.
func emitCases(ir *ServiceIR) ([]byte, error) {
	f := newFile(ir, "cases_gen.go")
	f.use("gen", "ref")
	f.p("// Suite returns the compliance cases a server runs against %s.", ir.Shape)
	f.p("func Suite() gen.Suite {")
	f.p("\treturn gen.Suite{")
	f.p("\t\tService: Service(),")
	f.p("\t\tRequests: []gen.RequestCase{")
	for i := range ir.Cases.Requests {
		if err := f.requestCaseLit(&ir.Cases.Requests[i]); err != nil {
			return nil, err
		}
	}
	f.p("\t\t},")
	f.p("\t\tResponses: []gen.ResponseCase{")
	for i := range ir.Cases.Responses {
		if err := f.responseCaseLit(&ir.Cases.Responses[i]); err != nil {
			return nil, err
		}
	}
	f.p("\t\t},")
	f.p("\t\tMalformed: []gen.MalformedCase{")
	for i := range ir.Cases.Malformed {
		f.malformedCaseLit(&ir.Cases.Malformed[i])
	}
	f.p("\t\t},")
	f.p("\t}")
	f.p("}")
	return f.bytes()
}

func (f *file) requestCaseLit(c *RequestCaseIR) error {
	rc := &c.Case
	params, err := f.paramsLit(c.Op.Input, rc.Params, c.Op.Name+" case "+rc.ID)
	if err != nil {
		return err
	}
	f.p("\t\t\t{")
	f.p("\t\t\t\tID: %s, Protocol: ref.%s, Operation: &desc%s, AppliesTo: gen.%s,", goString(rc.ID), protocolConst(protocolTraits()[rc.Protocol]), c.Op.Name, appliesToConst(rc.AppliesTo))
	f.p("\t\t\t\tMethod: %s, URI: %s,", goString(rc.Method), goString(rc.URI))
	f.optString("Host", rc.Host)
	f.optString("ResolvedHost", rc.ResolvedHost)
	f.optStrings("QueryParams", rc.QueryParams)
	f.optStrings("ForbidQueryParams", rc.ForbidQueryParams)
	f.optStrings("RequireQueryParams", rc.RequireQueryParams)
	f.headers(rc.Headers)
	f.optStrings("ForbidHeaders", rc.ForbidHeaders)
	f.optStrings("RequireHeaders", rc.RequireHeaders)
	f.optString("Body", rc.Body)
	f.optString("BodyMediaType", rc.BodyMediaType)
	f.p("\t\t\t\tParams: &%s,", params)
	f.optStrings("Tags", rc.Tags)
	f.p("\t\t\t},")
	return nil
}

func (f *file) responseCaseLit(c *ResponseCaseIR) error {
	rc := &c.Case
	shape := c.Op.Output
	if c.Error != nil {
		shape = c.Error.Shape
	}
	params, err := f.paramsLit(shape, rc.Params, c.Op.Name+" case "+rc.ID)
	if err != nil {
		return err
	}
	f.p("\t\t\t{")
	f.p("\t\t\t\tID: %s, Protocol: ref.%s, Operation: &desc%s, AppliesTo: gen.%s,", goString(rc.ID), protocolConst(protocolTraits()[rc.Protocol]), c.Op.Name, appliesToConst(rc.AppliesTo))
	f.p("\t\t\t\tCode: %d,", rc.Code)
	f.headers(rc.Headers)
	f.optStrings("ForbidHeaders", rc.ForbidHeaders)
	f.optStrings("RequireHeaders", rc.RequireHeaders)
	f.optString("Body", rc.Body)
	f.optString("BodyMediaType", rc.BodyMediaType)
	if c.Error == nil {
		f.p("\t\t\t\tOutcome: gen.Output{Value: &%s},", params)
	} else {
		code := ""
		if rc.VendorParams.Code != "" || rc.VendorParams.Type != "" {
			code = fmt.Sprintf(", Code: %s, Type: %s", goString(rc.VendorParams.Code), goString(rc.VendorParams.Type))
		}
		f.p("\t\t\t\tOutcome: gen.Error{ID: Identity%s(), Value: &%s%s},", c.Error.Shape.GoName, params, code)
	}
	f.optStrings("Tags", rc.Tags)
	f.p("\t\t\t},")
	return nil
}

func (f *file) malformedCaseLit(c *MalformedCaseIR) {
	mc := &c.Case
	f.p("\t\t\t{")
	f.p("\t\t\t\tID: %s, Protocol: ref.%s, Operation: &desc%s,", goString(mc.ID), protocolConst(protocolTraits()[mc.Protocol]), c.Op.Name)
	f.p("\t\t\t\tRequest: gen.MalformedRequest{")
	f.p("\t\t\t\t\tMethod: %s, URI: %s,", goString(mc.Request.Method), goString(mc.Request.URI))
	f.optString("Host", mc.Request.Host)
	f.optStrings("QueryParams", mc.Request.QueryParams)
	f.headers(mc.Request.Headers)
	f.optString("Body", mc.Request.Body)
	f.optString("BodyMediaType", mc.Request.BodyMediaType)
	f.p("\t\t\t\t},")
	f.p("\t\t\t\tResponse: gen.MalformedResponse{")
	f.p("\t\t\t\t\tCode: %d,", mc.Response.Code)
	f.headers(mc.Response.Headers)
	body := "gen.NoBodyAssertion{}"
	if b := mc.Response.Body; b != nil {
		switch {
		case b.Assertion.Contents != nil:
			body = fmt.Sprintf("gen.BodyContents{MediaType: %s, Contents: %s}", goString(b.MediaType), goString(*b.Assertion.Contents))
		case b.Assertion.MessageRegex != nil:
			body = fmt.Sprintf("gen.BodyMessageRegex{MediaType: %s, Pattern: %s}", goString(b.MediaType), goString(*b.Assertion.MessageRegex))
		}
	}
	f.p("\t\t\t\t\tBody: %s,", body)
	f.p("\t\t\t\t},")
	f.optStrings("Tags", mc.Tags)
	f.p("\t\t\t},")
}

// paramsLit renders a case's params as a literal of shape s; absent params
// are the zero value.
func (f *file) paramsLit(s *ShapeIR, raw json.RawMessage, subject string) (string, error) {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	return f.exampleLit(s, raw, subject)
}

func (f *file) optString(field, v string) {
	if v != "" {
		f.p("\t\t\t\t%s: %s,", field, goString(v))
	}
}

func (f *file) optStrings(field string, vs []string) {
	if len(vs) == 0 {
		return
	}
	q := make([]string, len(vs))
	for i, v := range vs {
		q[i] = goString(v)
	}
	f.p("\t\t\t\t%s: []string{%s},", field, strings.Join(q, ", "))
}

// headers writes a case's Headers field, keys sorted, when it has any.
func (f *file) headers(h map[string]string) {
	if len(h) == 0 {
		return
	}
	parts := make([]string, 0, len(h))
	for _, k := range slices.Sorted(maps.Keys(h)) {
		parts = append(parts, goString(k)+": "+goString(h[k]))
	}
	f.p("\t\t\t\tHeaders: map[string]string{%s},", strings.Join(parts, ", "))
}

func appliesToConst(s string) string {
	if s == "server" {
		return "ServerOnly"
	}
	return "Both"
}
