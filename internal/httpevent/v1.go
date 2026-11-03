package httpevent

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// V1Front is the front end whose payload format 1.0 event EventV1 builds:
// the fronts differ in header case, which value of a repeated header or
// query parameter the single-value maps keep, and the path they report.
//
//sumtype:decl
type V1Front interface{ isV1Front() }

// HTTPAPI is an API Gateway HTTP API (apigateway-http.md 5.2): header
// names in the client's case but accept, the last value of a repeated
// header and the first of a repeated query parameter in the single-value
// maps, and the path as sent.
type HTTPAPI struct{}

func (HTTPAPI) isV1Front() {}

// REST is an API Gateway REST API (apigateway-rest.md 5.1): no version
// key, header names as received, the last value of a repeated header and
// query parameter in the single-value maps, path relative to the stage
// and left encoded, and requestContext naming the matched resource and
// the stage's deployment.
type REST struct {
	// ResourceID is requestContext.resourceId.
	ResourceID string
	// ResourcePath is resource and requestContext.resourcePath: the matched
	// resource's template, such as /{proxy+}.
	ResourcePath string
	// DeploymentID is requestContext.deploymentId.
	DeploymentID string
	// ExtendedRequestID is requestContext.extendedRequestId, which a REST
	// API reports beside the requestId UUID.
	ExtendedRequestID string
	// BodylessEmpty gives a request without a body the event body ""
	// rather than null, and BodylessBase64 its isBase64Encoded: the
	// caller's apigateway.proxy.unrecorded choices (APIGW1-16). The zero
	// value is null and false.
	BodylessEmpty, BodylessBase64 bool
}

func (REST) isV1Front() {}

// errNoFront is EventV1's answer to a nil V1Front: a caller bug, since the
// event's shape depends on the front.
var errNoFront = errors.New("httpevent: EventV1's V1Front is nil; pass HTTPAPI{} for an API Gateway HTTP API or REST{} for a REST API")

// eventV1 is the payload format version 1.0 request event, with its keys
// in the order of the HTTP API guide's example (apigateway-http.md 5.2).
// Unlike 2.0, an absent map or body is null, not left out.
type eventV1 struct {
	Version                         string              `json:"version"`
	Resource                        string              `json:"resource"`
	Path                            string              `json:"path"`
	HTTPMethod                      string              `json:"httpMethod"`
	Headers                         map[string]string   `json:"headers"`
	MultiValueHeaders               map[string][]string `json:"multiValueHeaders"`
	QueryStringParameters           map[string]string   `json:"queryStringParameters"`
	MultiValueQueryStringParameters map[string][]string `json:"multiValueQueryStringParameters"`
	RequestContext                  requestContextV1    `json:"requestContext"`
	PathParameters                  map[string]string   `json:"pathParameters"`
	StageVariables                  map[string]string   `json:"stageVariables"`
	Body                            *string             `json:"body"`
	IsBase64Encoded                 bool                `json:"isBase64Encoded"`
}

// requestContextV1 has no authorizer: the guide example's
// {claims, scopes} is a JWT authorizer's, and a NONE route sends none
// (APIGW2-08, recorded 2026-11-03; apigateway-http.md 5.2). What an
// AWS_IAM route sends under 1.0 is unrecorded.
type requestContextV1 struct {
	AccountID    string `json:"accountId"`
	APIID        string `json:"apiId"`
	DomainName   string `json:"domainName"`
	DomainPrefix string `json:"domainPrefix"`
	// ExtendedRequestID is the request id: an HTTP API mints one id per
	// request (APIGW2-08, recorded 2026-11-03).
	ExtendedRequestID string          `json:"extendedRequestId"`
	HTTPMethod        string          `json:"httpMethod"`
	Identity          identityHTTPAPI `json:"identity"`
	Path              string          `json:"path"`
	Protocol          string          `json:"protocol"`
	RequestID         string          `json:"requestId"`
	RequestTime       string          `json:"requestTime"`
	RequestTimeEpoch  int64           `json:"requestTimeEpoch"`
	// ResourceID is the matched route key, where the guide's example shows
	// null (APIGW2-08, recorded 2026-11-03).
	ResourceID   string `json:"resourceId"`
	ResourcePath string `json:"resourcePath"`
	Stage        string `json:"stage"`
}

// identityHTTPAPI is an HTTP API's 1.0 requestContext.identity: identityV1
// with cognitoAmr, null, after caller, as AWS sends it (APIGW2-08, recorded
// 2026-11-03); a REST API's has no cognitoAmr (APIGW1-16).
type identityHTTPAPI struct {
	AccessKey                     *string   `json:"accessKey"`
	AccountID                     *string   `json:"accountId"`
	Caller                        *string   `json:"caller"`
	CognitoAMR                    *[]string `json:"cognitoAmr"`
	CognitoAuthenticationProvider *string   `json:"cognitoAuthenticationProvider"`
	CognitoAuthenticationType     *string   `json:"cognitoAuthenticationType"`
	CognitoIdentityID             *string   `json:"cognitoIdentityId"`
	CognitoIdentityPoolID         *string   `json:"cognitoIdentityPoolId"`
	PrincipalOrgID                *string   `json:"principalOrgId"`
	SourceIP                      string    `json:"sourceIp"`
	User                          *string   `json:"user"`
	UserAgent                     string    `json:"userAgent"`
	UserARN                       *string   `json:"userArn"`
}

// identityV1 is requestContext.identity: null members but the caller's
// address and user agent without an authorizer, and the signer's
// access key, account, caller, user and ARN under IAMAuthorizer. The
// guide example's clientCert belongs to mutual TLS on a custom domain,
// which understudy does not serve.
type identityV1 struct {
	AccessKey                     *string `json:"accessKey"`
	AccountID                     *string `json:"accountId"`
	Caller                        *string `json:"caller"`
	CognitoAuthenticationProvider *string `json:"cognitoAuthenticationProvider"`
	CognitoAuthenticationType     *string `json:"cognitoAuthenticationType"`
	CognitoIdentityID             *string `json:"cognitoIdentityId"`
	CognitoIdentityPoolID         *string `json:"cognitoIdentityPoolId"`
	PrincipalOrgID                *string `json:"principalOrgId"`
	SourceIP                      string  `json:"sourceIp"`
	User                          *string `json:"user"`
	UserAgent                     string  `json:"userAgent"`
	UserARN                       *string `json:"userArn"`
}

// EventV1 builds the payload format version 1.0 event for r as front f
// reports it (apigateway-http.md 5.2): header names as v1HeaderNames and
// the front's own in canonical case (APIGW2-08, recorded), every value
// of a repeated header in multiValueHeaders and one in headers, every
// URL-decoded value of a repeated query parameter in
// multiValueQueryStringParameters and one in queryStringParameters, path
// as sent, and the body by EventV2's text rule. Cookie stays a header.
// The bytes have no trailing newline and keep &, < and > unescaped.
//
// For HTTPAPI, headers and queryStringParameters keep a repeated name's
// last value (APIGW2-08, recorded 2026-11-03; the guide's example keeps
// the first query value, 5.2, 12 item 6: the
// apigatewayv2.payload.unrecorded switch's V1Header and V1Query). resource
// and requestContext.resourcePath are the path of c.RouteKey and
// requestContext.resourceId c.RouteKey itself (recorded); for the $default
// route both are $default (inferred).
func EventV1(r Request, c Context, f V1Front) ([]byte, error) {
	switch f := f.(type) {
	case HTTPAPI:
	case REST:
		return eventREST(r, c, f)
	case nil:
		return nil, errNoFront
	}
	identity := identityHTTPAPI{SourceIP: r.SourceIP, UserAgent: r.Header.Get("User-Agent")}
	switch a := c.Authorizer.(type) {
	case IAMAuthorizer:
		identity.AccessKey, identity.AccountID, identity.Caller = new(a.AccessKey), new(a.AccountID), new(a.CallerID)
		identity.User, identity.UserARN = new(a.UserID), new(a.UserARN)
	case NoAuthorizer:
	case nil:
		return nil, errNoAuthorizer
	}
	multiHeaders := v1HeaderNames(r.Header)
	for name, value := range httpAPIFrontHeaders(r, c.TraceID) {
		multiHeaders[http.CanonicalHeaderKey(name)] = []string{value}
	}
	headers := make(map[string]string, len(multiHeaders))
	for name, values := range multiHeaders {
		if len(values) == 0 {
			// A name without a value field was never sent.
			delete(multiHeaders, name)
			continue
		}
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
	resource := c.RouteKey
	if _, path, ok := strings.Cut(c.RouteKey, " "); ok {
		resource = path
	}
	ev := eventV1{
		Version:                         "1.0",
		Resource:                        resource,
		Path:                            r.RawPath,
		HTTPMethod:                      r.Method,
		Headers:                         headers,
		MultiValueHeaders:               multiHeaders,
		QueryStringParameters:           query,
		MultiValueQueryStringParameters: multiQuery,
		RequestContext: requestContextV1{
			AccountID:         c.AccountID,
			APIID:             c.APIID,
			DomainName:        c.DomainName,
			DomainPrefix:      c.DomainPrefix,
			ExtendedRequestID: c.RequestID,
			HTTPMethod:        r.Method,
			Identity:          identity,
			Path:              r.RawPath,
			Protocol:          r.Protocol,
			RequestID:         c.RequestID,
			RequestTime:       c.At.UTC().Format(timeLayout),
			RequestTimeEpoch:  c.At.UnixMilli(),
			ResourceID:        c.RouteKey,
			ResourcePath:      resource,
			Stage:             c.Stage,
		},
		PathParameters: nilIfEmpty(c.PathParameters),
		StageVariables: nilIfEmpty(c.StageVariables),
	}
	ev.Body, ev.IsBase64Encoded = eventBody(r)
	return encode(ev)
}

// v1HeaderNames is h under the names an HTTP API's 1.0 event reports: the
// client's spelling, except accept, which arrives lowercased (APIGW2-08,
// recorded 2026-11-03; apigateway-http.md 5.2). net/http has canonicalized
// the client's names, so a client that sent another case is reported in
// canonical case. Spellings that canonicalize alike merge in sorted order.
func v1HeaderNames(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for _, name := range slices.Sorted(maps.Keys(h)) {
		key := http.CanonicalHeaderKey(name)
		if key == "Accept" {
			key = "accept"
		}
		out[key] = append(out[key], h[name]...)
	}
	return out
}

// httpAPIFrontHeaders are the headers an HTTP API's front adds, in lower
// case: frontHeaders, content-length, the body's length, "0" without one,
// and x-forwarded-for the client's own values, if any, then the caller's
// address, joined by ", " (APIGW2-07, recorded 2026-11-03: 01-get,
// 14-client-xff; apigateway-http.md 5.1, 5.2). A function URL adds no
// content-length (lambda-events.md 7.3). The 1.0 event's x-forwarded-for
// is taken to follow the 2.0 rule (inferred: APIGW2-08 sent none).
func httpAPIFrontHeaders(r Request, trace string) map[string]string {
	h := frontHeaders(r, trace)
	h["content-length"] = strconv.Itoa(len(r.Body))
	if sent := r.Header.Values("X-Forwarded-For"); len(sent) > 0 {
		h["x-forwarded-for"] = strings.Join(append(slices.Clone(sent), r.SourceIP), ", ")
	}
	return h
}

// nilIfEmpty is m, or nil when m is empty, so the 1.0 event writes null.
func nilIfEmpty(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}

// InferV1For turns a function's output into the HTTP response under
// payload format 1.0 as front f maps it. HTTPAPI is InferV1. REST follows
// apigateway-rest.md 5.2: a missing statusCode is 200, a string of digits
// is a status, a key outside the five members, a non-list
// multiValueHeaders value or a bare string is malformed, header values
// that are booleans or numbers are stringified, isBase64Encoded does not
// decode the body, and a response without a Content-Type gets
// application/json.
func InferV1For(payload []byte, f V1Front) (Response, error) {
	switch f.(type) {
	case HTTPAPI:
		return InferV1(payload)
	case REST:
		return inferREST(payload)
	case nil:
		return Response{}, errNoFront
	}
	return Response{}, errNoFront
}

// InferV1 turns a function's output into the HTTP response under payload
// format 1.0 (apigateway-http.md 5.4). The format has no inference: the
// output must be a JSON object with statusCode, an integer from 200 to
// 599; headers an object of strings or null; multiValueHeaders an object
// of string arrays or null; body a string or null; isBase64Encoded a
// boolean, true decoding body. Anything else is a *MalformedError. Other
// keys are ignored, framing headers dropped as in InferV2, and no
// Content-Type is added: the front applies its own default.
//
// headers and multiValueHeaders merge per name, case-insensitively: the
// multiValueHeaders values first, then the headers value unless it is
// already one of them, each value its own header field. That is REST's
// recorded merge (apigateway-rest.md 5.2); an HTTP API's is UNVERIFIED
// (APIGW2-09), as is its answer to output without statusCode.
func InferV1(payload []byte) (Response, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return Response{}, &MalformedError{Problem: "it is not a JSON object, which payload format 1.0 requires"}
	}
	if _, ok := fields["statusCode"]; !ok {
		return Response{}, &MalformedError{Problem: "it has no statusCode, which payload format 1.0 requires"}
	}
	status, err := statusOf(fields)
	if err != nil {
		return Response{}, err
	}
	var single map[string]string
	if err := member(fields, "headers", &single); err != nil {
		return Response{}, &MalformedError{Problem: "headers is not an object of string values"}
	}
	var multi map[string][]string
	if err := member(fields, "multiValueHeaders", &multi); err != nil {
		return Response{}, &MalformedError{Problem: "multiValueHeaders is not an object of string arrays"}
	}
	body, err := bodyOf(fields)
	if err != nil {
		return Response{}, err
	}
	r := Response{Status: status, Header: http.Header{}, Body: body}
	// Sorted, so spellings of one name that differ in case merge in one
	// order.
	for _, name := range slices.Sorted(maps.Keys(multi)) {
		if !framing(name) {
			for _, v := range multi[name] {
				r.Header.Add(name, v)
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(single)) {
		if v := single[name]; !framing(name) && !slices.Contains(r.Header.Values(name), v) {
			r.Header.Add(name, v)
		}
	}
	return r, nil
}
