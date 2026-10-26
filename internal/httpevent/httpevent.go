// Package httpevent builds the Lambda payload format 2.0 request event and
// reads the function's response, for Lambda function URLs and API Gateway
// HTTP APIs alike (lambda-events.md 7.3, 7.4; apigateway-http.md 5.1, 5.3).
// It is pure and imports nothing in the repository: the caller resolves
// the route, authorizes the request and invokes the function.
//
// Decisions the research leaves open, which a function URL's recorded
// behavior settles (internal/service/lambda/furl):
//
//   - The event's keys follow the guide example's order. A request without
//     a body has no body key; one without a query, cookies, route variables
//     or stage variables has no queryStringParameters, cookies,
//     pathParameters or stageVariables key. requestContext.authentication
//     is never reported.
//   - Header names are lowercased and repeated headers joined by commas;
//     every Cookie header is split into cookies, one element per
//     name=value pair (apigateway-http.md 5.1 leaves the split UNVERIFIED,
//     APIGW2-07). The front end's own headers are set over the client's.
//   - A body is text when its content type is text/*, JSON, XML or
//     JavaScript and it is valid UTF-8; anything else, form posts
//     included, is base64.
//   - A JSON string output is the bare string. A response whose statusCode
//     is not an integer from 200 to 599, whose headers are not strings, or
//     whose body is not a string is malformed, as is output that is not
//     JSON. The framing headers a function returns (Content-Length,
//     Transfer-Encoding, Connection, Keep-Alive, Upgrade, Trailer, in any
//     case) are dropped.
package httpevent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Request is the HTTP request the event reports.
type Request struct {
	Method string
	// RawPath is the escaped path as sent, without any path fallback's
	// prefix; RawQuery the query as sent, without its "?".
	RawPath  string
	RawQuery string
	// Protocol is the request's protocol, such as HTTP/1.1.
	Protocol string
	// Host is the Host header, which net/http keeps out of Header.
	Host   string
	Header http.Header
	// SourceIP is the address of the client's TCP connection.
	SourceIP string
	// Scheme and Port are what the x-forwarded-proto and x-forwarded-port
	// headers the front end adds report: how and on which port the client
	// reached understudy.
	Scheme string
	Port   string
	Body   []byte
}

// Context is what the event reports beyond the request.
type Context struct {
	// AccountID is requestContext.accountId.
	AccountID string
	// APIID, DomainName and DomainPrefix are requestContext's apiId,
	// domainName and domainPrefix.
	APIID        string
	DomainName   string
	DomainPrefix string
	// RouteKey is routeKey and requestContext.routeKey; Stage is
	// requestContext.stage.
	RouteKey string
	Stage    string
	// PathParameters and StageVariables are left out of the event when
	// empty.
	PathParameters map[string]string
	StageVariables map[string]string
	// RequestID is requestContext.requestId.
	RequestID string
	// At is the request time on the emulator clock.
	At time.Time
	// TraceID is reported as the x-amzn-trace-id header when not empty.
	TraceID string
	// Authorizer is required: NoAuthorizer for a request without one.
	Authorizer Authorizer
}

// Authorizer is what requestContext.authorizer reports.
//
//sumtype:decl
type Authorizer interface{ isAuthorizer() }

// NoAuthorizer leaves requestContext.authorizer out.
type NoAuthorizer struct{}

// IAMAuthorizer is requestContext.authorizer.iam: the SigV4 signer of the
// request.
type IAMAuthorizer struct {
	AccessKey string
	AccountID string
	CallerID  string
	UserARN   string
	UserID    string
}

func (NoAuthorizer) isAuthorizer()  {}
func (IAMAuthorizer) isAuthorizer() {}

// errNoAuthorizer is EventV2's answer to a Context whose Authorizer is
// nil: a caller bug, since every request either has an authorizer or has
// NoAuthorizer.
var errNoAuthorizer = errors.New("httpevent: Context.Authorizer is nil; set NoAuthorizer{} for a request without an authorizer")

// event is the payload format version 2.0 request event, with its keys in
// the order of the guide's example (apigateway-http.md 5.1).
type event struct {
	Version               string            `json:"version"`
	RouteKey              string            `json:"routeKey"`
	RawPath               string            `json:"rawPath"`
	RawQueryString        string            `json:"rawQueryString"`
	Cookies               []string          `json:"cookies,omitempty"`
	Headers               map[string]string `json:"headers"`
	QueryStringParameters map[string]string `json:"queryStringParameters,omitempty"`
	RequestContext        requestContext    `json:"requestContext"`
	Body                  *string           `json:"body,omitempty"`
	PathParameters        map[string]string `json:"pathParameters,omitempty"`
	IsBase64Encoded       bool              `json:"isBase64Encoded"`
	StageVariables        map[string]string `json:"stageVariables,omitempty"`
}

type requestContext struct {
	AccountID    string      `json:"accountId"`
	APIID        string      `json:"apiId"`
	Authorizer   *authorizer `json:"authorizer,omitempty"`
	DomainName   string      `json:"domainName"`
	DomainPrefix string      `json:"domainPrefix"`
	HTTP         httpContext `json:"http"`
	RequestID    string      `json:"requestId"`
	RouteKey     string      `json:"routeKey"`
	Stage        string      `json:"stage"`
	Time         string      `json:"time"`
	TimeEpoch    int64       `json:"timeEpoch"`
}

type authorizer struct {
	IAM iamAuthorizer `json:"iam"`
}

type iamAuthorizer struct {
	AccessKey       string    `json:"accessKey"`
	AccountID       string    `json:"accountId"`
	CallerID        string    `json:"callerId"`
	CognitoIdentity *struct{} `json:"cognitoIdentity"`
	PrincipalOrgID  *string   `json:"principalOrgId"`
	UserARN         string    `json:"userArn"`
	UserID          string    `json:"userId"`
}

type httpContext struct {
	Method    string `json:"method"`
	Path      string `json:"path"`
	Protocol  string `json:"protocol"`
	SourceIP  string `json:"sourceIp"`
	UserAgent string `json:"userAgent"`
}

// timeLayout is requestContext.time: 12/Mar/2020:19:03:58 +0000.
const timeLayout = "02/Jan/2006:15:04:05 -0700"

// EventV2 builds the payload format version 2.0 event for r
// (lambda-events.md 7.3, apigateway-http.md 5.1): header names lowercase
// with repeated headers joined by commas, every Cookie header split into
// cookies, repeated query parameters URL-decoded and joined by commas,
// rawPath and rawQueryString as sent, and a body that is text for a text
// content type and base64 otherwise. The bytes have no trailing newline
// and keep &, < and > unescaped.
func EventV2(r Request, c Context) ([]byte, error) {
	headers, cookies := eventHeaders(r, c.TraceID)
	ev := event{
		Version:               "2.0",
		RouteKey:              c.RouteKey,
		RawPath:               r.RawPath,
		RawQueryString:        r.RawQuery,
		Cookies:               cookies,
		Headers:               headers,
		QueryStringParameters: queryParameters(r.RawQuery),
		RequestContext: requestContext{
			AccountID:    c.AccountID,
			APIID:        c.APIID,
			DomainName:   c.DomainName,
			DomainPrefix: c.DomainPrefix,
			HTTP: httpContext{
				Method:    r.Method,
				Path:      r.RawPath,
				Protocol:  r.Protocol,
				SourceIP:  r.SourceIP,
				UserAgent: r.Header.Get("User-Agent"),
			},
			RequestID: c.RequestID,
			RouteKey:  c.RouteKey,
			Stage:     c.Stage,
			Time:      c.At.UTC().Format(timeLayout),
			TimeEpoch: c.At.UnixMilli(),
		},
		PathParameters: c.PathParameters,
		StageVariables: c.StageVariables,
	}
	switch a := c.Authorizer.(type) {
	case IAMAuthorizer:
		ev.RequestContext.Authorizer = &authorizer{IAM: iamAuthorizer{
			AccessKey: a.AccessKey, AccountID: a.AccountID, CallerID: a.CallerID,
			UserARN: a.UserARN, UserID: a.UserID,
		}}
	case NoAuthorizer:
	case nil:
		return nil, errNoAuthorizer
	}
	if len(r.Body) > 0 {
		body := string(r.Body)
		if !isText(r.Header.Get("Content-Type"), r.Body) {
			body = base64.StdEncoding.EncodeToString(r.Body)
			ev.IsBase64Encoded = true
		}
		ev.Body = &body
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	// rawQueryString and bodies keep &, < and > as sent.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(ev); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// eventHeaders lowercases r's header names and joins repeated headers
// with commas; Cookie headers become cookies, one element per name=value
// pair. The headers the front end adds are set over the client's: host,
// x-forwarded-for, x-forwarded-proto, x-forwarded-port and, with a trace,
// x-amzn-trace-id.
func eventHeaders(r Request, trace string) (headers map[string]string, cookies []string) {
	headers = map[string]string{}
	for name, values := range r.Header {
		lower := strings.ToLower(name)
		if lower == "cookie" {
			for _, v := range values {
				cookies = append(cookies, splitCookies(v)...)
			}
			continue
		}
		if prev, ok := headers[lower]; ok {
			values = append([]string{prev}, values...)
		}
		headers[lower] = strings.Join(values, ",")
	}
	headers["host"] = r.Host
	headers["x-forwarded-for"] = r.SourceIP
	headers["x-forwarded-proto"] = r.Scheme
	headers["x-forwarded-port"] = r.Port
	if trace != "" {
		headers["x-amzn-trace-id"] = trace
	}
	return headers, cookies
}

// splitCookies splits a Cookie header into its name=value pairs.
func splitCookies(header string) []string {
	var out []string
	for part := range strings.SplitSeq(header, ";") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// queryParameters decodes a raw query into queryStringParameters, the
// values of a repeated parameter joined with commas in the order sent;
// nil when the query is empty. A pair whose escapes are malformed is left
// out, as url.ParseQuery leaves it out.
func queryParameters(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	out := map[string]string{}
	for pair := range strings.SplitSeq(raw, "&") {
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		key, kerr := url.QueryUnescape(k)
		value, verr := url.QueryUnescape(v)
		if kerr != nil || verr != nil {
			continue
		}
		if prev, ok := out[key]; ok {
			out[key] = prev + "," + value
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isText reports whether a body of contentType reaches the function as
// text: valid UTF-8 of a text/*, JSON, XML or JavaScript media type.
// Anything else, form posts included, is base64 (lambda-events.md 7.3:
// LocalStack's recorded multipart form arrived base64, text/plain and JSON
// as text; the exact set is UNVERIFIED).
func isText(contentType string, body []byte) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil || !utf8.Valid(body) {
		return false
	}
	if strings.HasPrefix(mt, "text/") || strings.HasSuffix(mt, "+json") || strings.HasSuffix(mt, "+xml") {
		return true
	}
	return slices.Contains([]string{"application/json", "application/xml", "application/javascript"}, mt)
}
