package protocoltest_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/protocoltest"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// wireRequest is a request a case sends, as the corpus writes it.
type wireRequest struct {
	method, uri string
	query       []string
	headers     map[string]string
	host        string
	// body is the corpus text: base64 for a binary media type.
	body, mediaType string
}

// build returns the HTTP request to the bench at base: the query pieces
// appended as written, a CBOR body decoded from base64, and the Host the
// client sends to.
func (w wireRequest) build(ctx context.Context, base string) (*http.Request, error) {
	target := base + w.uri
	if len(w.query) > 0 {
		target += "?" + strings.Join(w.query, "&")
	}
	body := []byte(w.body)
	if w.binary() {
		raw, err := base64.StdEncoding.DecodeString(w.body)
		if err != nil {
			return nil, fmt.Errorf("the case's CBOR body is not base64: %w", err)
		}
		body = raw
	}
	r, err := http.NewRequestWithContext(ctx, w.method, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("the case's request does not parse: %w", err)
	}
	r.Host = "localhost"
	if w.host != "" {
		r.Host = w.host
	}
	// net/http names itself unless told not to; a case's request carries
	// only the headers the case lists.
	r.Header.Set("User-Agent", "")
	for name, value := range w.headers {
		r.Header.Set(name, value)
	}
	return r, nil
}

// binary reports whether the body is base64 in the corpus: its media type,
// or failing that its Content-Type, is CBOR.
func (w wireRequest) binary() bool {
	mediaType := w.mediaType
	for name, value := range w.headers {
		if mediaType == "" && strings.EqualFold(name, "Content-Type") {
			mediaType = value
		}
	}
	mt, _, err := mime.ParseMediaType(mediaType)
	return err == nil && mt == "application/cbor"
}

func requestOf(c *gen.RequestCase) wireRequest {
	host := c.ResolvedHost
	if host == "" {
		host = c.Host
	}
	return wireRequest{
		method: c.Method, uri: c.URI, query: c.QueryParams, headers: c.Headers, host: host,
		body: c.Body, mediaType: c.BodyMediaType,
	}
}

// requestIDHeaders carry the request id the server mints; a case's value
// for them is a client's fixture, so only their presence is compared.
var requestIDHeaders = []string{"X-Amzn-Requestid", "X-Amz-Request-Id"}

// runRequest sends a request case: the handler must be entered once, with
// an input equal to the case's params.
func (b *bench) runRequest(ctx context.Context, c *gen.RequestCase) error {
	b.echo.script(nil)
	resp, err := b.serve(ctx, requestOf(c))
	if err != nil {
		return err
	}
	in := b.echo.entered()
	if len(in) != 1 {
		return fmt.Errorf("the handler was entered %d times; the response was %s", len(in), resp)
	}
	return diffInput(c.Params, in[0])
}

// runResponse makes the handler answer with the case's outcome and
// compares the response with the case: status, headers, and the body as
// its media type says.
func (b *bench) runResponse(ctx context.Context, c *gen.ResponseCase, req wireRequest) error {
	b.echo.script(c.Outcome)
	resp, err := b.serve(ctx, req)
	if err != nil {
		return err
	}
	if in := b.echo.entered(); len(in) != 1 {
		return fmt.Errorf("the handler was entered %d times; the response was %s", len(in), resp)
	}
	if resp.code != c.Code {
		return &protocoltest.MismatchError{Where: "status", Want: strconv.Itoa(c.Code), Got: resp.String()}
	}
	headers := maps.Clone(c.Headers)
	require := c.RequireHeaders
	for name := range headers {
		for _, id := range requestIDHeaders {
			if strings.EqualFold(name, id) {
				delete(headers, name)
				require = append(require, name)
			}
		}
	}
	if err := protocoltest.CompareHeaders(headers, resp.header); err != nil {
		return err
	}
	if err := protocoltest.CompareHeaderPresence(c.ForbidHeaders, require, resp.header); err != nil {
		return err
	}
	return compareResponseBody(c.Body, c.BodyMediaType, bodyShape(c), resp.body)
}

// bodyShape is the schema of a response case's body: the operation's
// output, or nil for an error, which *awserr.Error never gives a map.
func bodyShape(c *gen.ResponseCase) *smithy.Schema {
	if _, ok := c.Outcome.(gen.Output); ok {
		return c.Operation.Output
	}
	return nil
}

// compareResponseBody compares a body when the case states one; a case
// with a media type and an empty body asserts the body is absent.
func compareResponseBody(want, mediaType string, shape *smithy.Schema, got []byte) error {
	switch {
	case want != "":
		return protocoltest.CompareBody(mediaType, shape, []byte(want), got)
	case mediaType != "" && len(got) > 0:
		return &protocoltest.MismatchError{Where: "body", Want: "no body", Got: fmt.Sprintf("%q", got)}
	default:
		return nil
	}
}

// runMalformed sends a malformed case: the handler must not be entered,
// and the response must carry the case's status and x-amzn-errortype.
// The body is not compared: Smithy's validation message text is not
// AWS's (error-matrix.md finding 2).
func (b *bench) runMalformed(ctx context.Context, c *gen.MalformedCase) error {
	b.echo.script(nil)
	resp, err := b.serve(ctx, wireRequest{
		method: c.Request.Method, uri: c.Request.URI, query: c.Request.QueryParams, headers: c.Request.Headers,
		host: c.Request.Host, body: c.Request.Body, mediaType: c.Request.BodyMediaType,
	})
	if err != nil {
		return err
	}
	if in := b.echo.entered(); len(in) != 0 {
		return fmt.Errorf("the handler was entered with %#v; the request must be rejected before it", in[0])
	}
	if resp.code != c.Response.Code {
		return &protocoltest.MismatchError{Where: "status", Want: strconv.Itoa(c.Response.Code), Got: resp.String()}
	}
	for name, value := range c.Response.Headers {
		if strings.EqualFold(name, "X-Amzn-Errortype") {
			return protocoltest.CompareHeaders(map[string]string{name: value}, resp.header)
		}
	}
	return nil
}

// caseRequests returns, per operation, its first request case that is not
// skipped, which a server is known to accept.
func caseRequests(s *gen.Suite, skips protocoltest.Skips) map[*registry.Operation]wireRequest {
	out := map[*registry.Operation]wireRequest{}
	for i := range s.Requests {
		c := &s.Requests[i]
		_, skipped := skips.Lookup(protocoltest.Key{Protocol: c.Protocol, Kind: protocoltest.Request, ID: c.ID})
		if _, ok := out[c.Operation]; !ok && !skipped {
			out[c.Operation] = requestOf(c)
		}
	}
	return out
}

// responseRequest is the request op's response cases send: its request
// case from caseRequests, else minimalRequest's.
func responseRequest(cases map[*registry.Operation]wireRequest, op *registry.Operation) (wireRequest, error) {
	if r, ok := cases[op]; ok {
		return r, nil
	}
	return minimalRequest(op)
}

// minimalRequest is the smallest request naming op over its service's
// first protocol whose input passes required-member validation, so the
// handler is entered (smithy-protocol-tests.md 10.6, 14.6). It fails when
// the input requires more than smallestInput fills, or a structure outside
// an awsJson body, the one body it writes.
func minimalRequest(op *registry.Operation) (wireRequest, error) {
	svc := op.Service
	p := svc.Protocols[0]
	in, err := smallestInput(op, op.Input, "")
	if err != nil {
		return wireRequest{}, err
	}
	if len(in) > 0 && p != ref.AWSJSON10 && p != ref.AWSJSON11 {
		return wireRequest{}, fmt.Errorf("the bench has no request for %s: its input requires %s, and minimalRequest writes a required structure only in an awsJson body, not in %s; add a request case for the operation or teach minimalRequest the protocol",
			op.ID, strings.Join(slices.Sorted(maps.Keys(in)), ", "), p)
	}
	switch p {
	case ref.AWSJSON10, ref.AWSJSON11:
		body, err := json.Marshal(in)
		if err != nil {
			return wireRequest{}, fmt.Errorf("encoding the minimal input of %s: %w", op.ID, err)
		}
		version := strings.TrimPrefix(string(p), "awsjson")
		return wireRequest{method: http.MethodPost, uri: "/", body: string(body), headers: map[string]string{
			"Content-Type": "application/x-amz-json-" + version[:1] + "." + version[2:],
			"X-Amz-Target": svc.JSONTargetPrefix + "." + op.ID.Name,
		}}, nil
	case ref.AWSQuery:
		return wireRequest{
			method: http.MethodPost, uri: "/",
			body:    url.Values{"Action": {op.ID.Name}, "Version": {svc.QueryVersion}}.Encode(),
			headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		}, nil
	case ref.RPCv2CBOR:
		return wireRequest{
			method: http.MethodPost, uri: "/service/" + svc.Shape.Name() + "/operation/" + op.ID.Name,
			headers: map[string]string{"Smithy-Protocol": "rpc-v2-cbor", "Content-Type": "application/cbor", "Accept": "application/cbor"},
		}, nil
	case ref.RESTJSON1, ref.RESTXML, ref.Raw:
		return restRequest(op), nil
	default:
		return restRequest(op), nil
	}
}

// smallestInput is the smallest value of structure s, at member path
// ("" for the input itself), that passes required-member validation, as
// awsJson writes it: each required member without a default is an empty
// structure but for its own required members, recursively. A member the
// server defaults needs no value (smithy-protocol-tests.md 10.2), and
// restRequest fills a REST label. A required member of any other kind
// fails, since the bench does not choose values for the handler
// (smithy-protocol-tests.md 14.6).
func smallestInput(op *registry.Operation, s *smithy.Schema, path string) (map[string]any, error) {
	out := map[string]any{}
	if s == nil {
		return out, nil
	}
	for _, m := range s.Members {
		_, label := m.HTTP.(smithy.HTTPLabel)
		if !m.Required || hasDefault(m) || label {
			continue
		}
		at := m.Name
		if path != "" {
			at = path + "." + m.Name
		}
		if m.Target.Type != smithy.TypeStructure {
			return nil, fmt.Errorf("the bench has no request for %s: its input member %s is a required %s without a default, and minimalRequest fills only required structures (smithy-protocol-tests.md 14.6); add a request case for the operation or teach minimalRequest the member",
				op.ID, at, m.Target.Type)
		}
		v, err := smallestInput(op, m.Target, at)
		if err != nil {
			return nil, err
		}
		out[m.Name] = v
	}
	return out, nil
}

// hasDefault reports whether m has a modeled default; an explicit null
// default removes one (smithy.Member.Default).
func hasDefault(m *smithy.Member) bool {
	_, null := m.Default.(document.Null)
	return m.Default != nil && !null
}

// restRequest fills op's URI pattern with placeholder labels and its
// literal query.
func restRequest(op *registry.Operation) wireRequest {
	var path strings.Builder
	for _, seg := range op.HTTP.Path {
		path.WriteByte('/')
		switch s := seg.(type) {
		case registry.Literal:
			path.WriteString(url.PathEscape(s.Value))
		case registry.Label:
			path.WriteString("label")
		case registry.Greedy:
			path.WriteString("greedy/label")
		}
	}
	if path.Len() == 0 {
		path.WriteByte('/')
	}
	var query []string
	for _, q := range op.HTTP.Query {
		switch q := q.(type) {
		case registry.QueryPresent:
			query = append(query, url.QueryEscape(q.Key))
		case registry.QueryEquals:
			query = append(query, url.QueryEscape(q.Key)+"="+url.QueryEscape(q.Value))
		}
	}
	return wireRequest{method: op.HTTP.Method, uri: path.String(), query: query}
}
