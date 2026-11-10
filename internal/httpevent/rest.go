package httpevent

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// eventRESTV1 is a REST API's payload format 1.0 request event
// (apigateway-rest.md 5.1): the 1.0 members without a version key.
type eventRESTV1 struct {
	Resource                        string              `json:"resource"`
	Path                            string              `json:"path"`
	HTTPMethod                      string              `json:"httpMethod"`
	Headers                         map[string]string   `json:"headers"`
	MultiValueHeaders               map[string][]string `json:"multiValueHeaders"`
	QueryStringParameters           map[string]string   `json:"queryStringParameters"`
	MultiValueQueryStringParameters map[string][]string `json:"multiValueQueryStringParameters"`
	PathParameters                  map[string]string   `json:"pathParameters"`
	StageVariables                  map[string]string   `json:"stageVariables"`
	RequestContext                  requestContextREST  `json:"requestContext"`
	Body                            *string             `json:"body"`
	IsBase64Encoded                 bool                `json:"isBase64Encoded"`
}

// requestContextREST is a REST API event's requestContext
// (apigateway-rest.md 5.1). A NONE method's has no authorizer key.
type requestContextREST struct {
	AccountID         string     `json:"accountId"`
	APIID             string     `json:"apiId"`
	DeploymentID      string     `json:"deploymentId"`
	DomainName        string     `json:"domainName"`
	DomainPrefix      string     `json:"domainPrefix"`
	ExtendedRequestID string     `json:"extendedRequestId"`
	HTTPMethod        string     `json:"httpMethod"`
	Identity          identityV1 `json:"identity"`
	Path              string     `json:"path"`
	Protocol          string     `json:"protocol"`
	RequestID         string     `json:"requestId"`
	RequestTime       string     `json:"requestTime"`
	RequestTimeEpoch  int64      `json:"requestTimeEpoch"`
	ResourceID        string     `json:"resourceId"`
	ResourcePath      string     `json:"resourcePath"`
	Stage             string     `json:"stage"`
}

// eventREST builds a REST API's 1.0 event (apigateway-rest.md 5.1, 4.2,
// 4.5). r.RawPath is the escaped path after the host, the stage segment
// included. Header names are reported as r.Header holds them: net/http has
// canonicalized them, where AWS keeps the client's spelling (a deviation
// InvokeRestApi's coverage note declares). headers keeps the last value of
// a repeated header (inferred, APIGW1-16), queryStringParameters the last
// of a repeated parameter (recorded). A request without a body gives body
// null, or "" with f.BodylessEmpty, and isBase64Encoded f.BodylessBase64 (switch
// apigateway.proxy.unrecorded, APIGW1-16); any other body is sent as text,
// invalid UTF-8 replaced by U+FFFD (inferred, APIGW1-16), with
// isBase64Encoded false, since a stage with binaryMediaTypes is refused
// before an event is built.
func eventREST(r Request, c Context, f REST) ([]byte, error) {
	identity := identityV1{SourceIP: r.SourceIP, UserAgent: r.Header.Get("User-Agent")}
	switch c.Authorizer.(type) {
	case NoAuthorizer:
	case IAMAuthorizer:
		return nil, errRESTAuthorizer
	case nil:
		return nil, errNoAuthorizer
	}
	multiHeaders := make(map[string][]string, len(r.Header))
	for name, values := range r.Header {
		if len(values) > 0 {
			multiHeaders[name] = slices.Clone(values)
		}
	}
	for name, value := range restFrontHeaders(r, c.TraceID) {
		for held := range multiHeaders {
			if http.CanonicalHeaderKey(held) == name {
				delete(multiHeaders, held)
			}
		}
		multiHeaders[name] = []string{value}
	}
	headers := make(map[string]string, len(multiHeaders))
	for name, values := range multiHeaders {
		headers[name] = values[len(values)-1]
	}
	multiQuery := queryValues(r.RawQuery)
	var query map[string]string
	if multiQuery != nil {
		query = make(map[string]string, len(multiQuery))
		for name, values := range multiQuery {
			query[name] = values[len(values)-1]
		}
	}
	path, contextPath := restPaths(r.RawPath)
	var body *string
	encoded := false
	switch {
	case len(r.Body) > 0:
		body = new(strings.ToValidUTF8(string(r.Body), "�"))
	case f.BodylessEmpty:
		body, encoded = new(""), f.BodylessBase64
	default:
		encoded = f.BodylessBase64
	}
	return encode(eventRESTV1{
		Resource:                        f.ResourcePath,
		Path:                            path,
		HTTPMethod:                      r.Method,
		Headers:                         headers,
		MultiValueHeaders:               multiHeaders,
		QueryStringParameters:           query,
		MultiValueQueryStringParameters: multiQuery,
		PathParameters:                  nilIfEmpty(c.PathParameters),
		StageVariables:                  nilIfEmpty(c.StageVariables),
		RequestContext: requestContextREST{
			AccountID:         c.AccountID,
			APIID:             c.APIID,
			DeploymentID:      f.DeploymentID,
			DomainName:        c.DomainName,
			DomainPrefix:      c.DomainPrefix,
			ExtendedRequestID: f.ExtendedRequestID,
			HTTPMethod:        r.Method,
			Identity:          identity,
			Path:              contextPath,
			Protocol:          r.Protocol,
			RequestID:         c.RequestID,
			RequestTime:       c.At.UTC().Format(timeLayout),
			RequestTimeEpoch:  c.At.UnixMilli(),
			ResourceID:        f.ResourceID,
			ResourcePath:      f.ResourcePath,
			Stage:             c.Stage,
		},
		Body:            body,
		IsBase64Encoded: encoded,
	})
}

// errRESTAuthorizer is EventV1's answer to a REST event with an IAM
// authorizer: the REST front answers AWS_IAM methods 501 before building
// an event, so this is a caller bug.
var errRESTAuthorizer = errors.New("httpevent: the REST front builds events for NONE methods only; an AWS_IAM method is answered 501 before any event is built")

// restFrontHeaders are the headers a REST API's front adds over the
// client's, in canonical form (apigateway-rest.md 5.1).
func restFrontHeaders(r Request, trace string) map[string]string {
	h := map[string]string{
		"Host":              r.Host,
		"X-Forwarded-For":   r.SourceIP,
		"X-Forwarded-Proto": r.Scheme,
		"X-Forwarded-Port":  r.Port,
	}
	if trace != "" {
		h["X-Amzn-Trace-Id"] = trace
	}
	return h
}

// restPaths are the event's path and requestContext.path for the escaped
// request path raw, stage segment included (apigateway-rest.md 4.2 table,
// recorded): path drops the leading slashes, the stage segment and the
// slashes after it, then starts with one slash; requestContext.path
// collapses the leading slashes to one. Interior and trailing slashes stay
// and nothing is decoded.
func restPaths(raw string) (path, contextPath string) {
	trimmed := strings.TrimLeft(raw, "/")
	_, rest, _ := strings.Cut(trimmed, "/")
	return "/" + strings.TrimLeft(rest, "/"), "/" + trimmed
}

// inferREST maps a function's output to the response of a REST API's
// Lambda proxy integration (apigateway-rest.md 5.2, recorded unless noted):
// a JSON object of at most statusCode, headers, multiValueHeaders, body and
// isBase64Encoded; statusCode absent is 200, an integer or a string of
// decimal digits from 100 to 599 (range inferred); header values that are
// booleans or numbers are their JSON text, objects and arrays malformed
// (inferred); multiValueHeaders and headers merge as InferV1 merges them;
// isBase64Encoded does not decode the body; no Content-Type gives
// application/json (4.8). Anything else is a *MalformedError.
func inferREST(payload []byte) (Response, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return Response{}, &MalformedError{Problem: "it is not a JSON object, which a REST API's Lambda proxy integration requires"}
	}
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		switch name {
		case "statusCode", "headers", "multiValueHeaders", "body", "isBase64Encoded":
		default:
			return Response{}, &MalformedError{Problem: "it has the key " + strconv.Quote(name) + ", which is not one of statusCode, headers, multiValueHeaders, body and isBase64Encoded"}
		}
	}
	status, err := restStatus(fields)
	if err != nil {
		return Response{}, err
	}
	var single map[string]json.RawMessage
	if err := member(fields, "headers", &single); err != nil {
		return Response{}, &MalformedError{Problem: "headers is not an object"}
	}
	var multi map[string][]json.RawMessage
	if err := member(fields, "multiValueHeaders", &multi); err != nil {
		return Response{}, &MalformedError{Problem: "multiValueHeaders is not an object of arrays"}
	}
	var body *string
	if err := member(fields, "body", &body); err != nil {
		return Response{}, &MalformedError{Problem: "body is not a string"}
	}
	var encoded bool
	if err := member(fields, "isBase64Encoded", &encoded); err != nil {
		return Response{}, &MalformedError{Problem: "isBase64Encoded is not a boolean"}
	}
	header, err := restHeaders(single, multi)
	if err != nil {
		return Response{}, err
	}
	r := Response{Status: status, Header: header}
	if body != nil {
		r.Body = []byte(*body)
	}
	if r.Header.Get("Content-Type") == "" {
		r.Header.Set("Content-Type", "application/json")
	}
	return r, nil
}

// restHeaders merges a proxy response's multiValueHeaders and headers as
// InferV1 merges them: the multi values first, then the single value
// unless it is already one of them, names case-insensitive. Sorted, so
// spellings of one name that differ in case merge in one order.
func restHeaders(single map[string]json.RawMessage, multi map[string][]json.RawMessage) (http.Header, error) {
	h := http.Header{}
	for _, name := range slices.Sorted(maps.Keys(multi)) {
		for _, raw := range multi[name] {
			v, err := restHeaderValue(name, raw)
			if err != nil {
				return nil, err
			}
			if !framing(name) {
				h.Add(name, v)
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(single)) {
		v, err := restHeaderValue(name, single[name])
		if err != nil {
			return nil, err
		}
		if !framing(name) && !slices.Contains(h.Values(name), v) {
			h.Add(name, v)
		}
	}
	return h, nil
}

// restStatus reads statusCode: absent is 200; a JSON integer or a string
// of decimal digits from 100 to 599 is the status.
func restStatus(fields map[string]json.RawMessage) (int, error) {
	raw, ok := fields["statusCode"]
	if !ok {
		return http.StatusOK, nil
	}
	text := string(bytes.TrimSpace(raw))
	var s string
	if json.Unmarshal(raw, &s) == nil {
		text = s
	}
	status, err := strconv.Atoi(text)
	if err != nil || strings.ContainsAny(text, "+-") || status < 100 || status > 599 {
		return 0, &MalformedError{Problem: "statusCode " + string(raw) + " is not a status from 100 to 599"}
	}
	return status, nil
}

// restHeaderValue is a response header's value: a string as is, a boolean
// or a number as its JSON text.
func restHeaderValue(name string, raw json.RawMessage) (string, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		switch v.(type) {
		case bool, float64:
			return string(bytes.TrimSpace(raw)), nil
		}
	}
	return "", &MalformedError{Problem: "header " + strconv.Quote(name) + " has the value " + string(raw) + ", which is not a string, a boolean or a number"}
}
