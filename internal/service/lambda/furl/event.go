package furl

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Request is the HTTP request a function URL received, as the request
// event reports it.
type Request struct {
	Method string
	// RawPath is the escaped path as sent, without the path fallback's
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
	// headers Lambda's front end adds report: how and on which port the
	// client reached understudy.
	Scheme string
	Port   string
	Body   []byte
}

// Context is what the event's requestContext reports beyond the request.
type Context struct {
	// URLID is the url id, reported as apiId and domainPrefix.
	URLID      string
	DomainName string
	// RequestID is the invocation's request id.
	RequestID string
	// At is the request time on the emulator clock.
	At time.Time
	// TraceID is the request's X-Ray trace: the X-Amzn-Trace-Id it carries
	// or, without one, a trace derived from the request id and the
	// admission time; the function runs under the same trace. The
	// x-amzn-trace-id header reports it when it is not empty.
	TraceID string
	Caller  Caller
}

// Caller is who the request event says made the request.
//
//sumtype:decl
type Caller interface{ isCaller() }

// Public is the caller of a URL with AuthType NONE: accountId is
// "anonymous" and there is no authorizer.
type Public struct{}

// IAMCaller is the SigV4 signer of a request to a URL with AuthType
// AWS_IAM, reported under requestContext.authorizer.iam.
type IAMCaller struct {
	AccessKey string
	AccountID string
	// ID is the principal's unique id, reported as both callerId and
	// userId: a user's AIDA id, a role session's AROA id with its session
	// name, or a root's account.
	ID      string
	UserARN string
}

func (Public) isCaller()    {}
func (IAMCaller) isCaller() {}

// event is the payload format version 2.0 request event, with its keys in
// the order the Lambda guide's example uses (lambda-events.md 7.3).
// pathParameters, stageVariables and requestContext.authentication are
// left out, as the guide allows and LocalStack's recorded echoes show.
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
	IsBase64Encoded       bool              `json:"isBase64Encoded"`
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

// defaultRoute is the placeholder routeKey and stage of a function URL.
const defaultRoute = "$default"

// timeLayout is requestContext.time: 12/Mar/2020:19:03:58 +0000.
const timeLayout = "02/Jan/2006:15:04:05 -0700"

// Event builds the payload format version 2.0 event for r
// (lambda-events.md 7.3): header names lowercase with repeated headers
// joined by commas, every Cookie header split into cookies, repeated query
// parameters URL-decoded and joined by commas, rawPath and rawQueryString
// as sent, and a body that is text for a text content type and base64
// otherwise. A request without a body has no body key.
func Event(r Request, c Context) ([]byte, error) {
	headers, cookies := eventHeaders(r, c.TraceID)
	ev := event{
		Version:               "2.0",
		RouteKey:              defaultRoute,
		RawPath:               r.RawPath,
		RawQueryString:        r.RawQuery,
		Cookies:               cookies,
		Headers:               headers,
		QueryStringParameters: queryParameters(r.RawQuery),
		RequestContext: requestContext{
			AccountID:    "anonymous",
			APIID:        c.URLID,
			DomainName:   c.DomainName,
			DomainPrefix: c.URLID,
			HTTP: httpContext{
				Method:    r.Method,
				Path:      r.RawPath,
				Protocol:  r.Protocol,
				SourceIP:  r.SourceIP,
				UserAgent: r.Header.Get("User-Agent"),
			},
			RequestID: c.RequestID,
			RouteKey:  defaultRoute,
			Stage:     defaultRoute,
			Time:      c.At.UTC().Format(timeLayout),
			TimeEpoch: c.At.UnixMilli(),
		},
	}
	switch caller := c.Caller.(type) {
	case IAMCaller:
		ev.RequestContext.AccountID = caller.AccountID
		ev.RequestContext.Authorizer = &authorizer{IAM: iamAuthorizer{
			AccessKey: caller.AccessKey, AccountID: caller.AccountID, CallerID: caller.ID,
			UserARN: caller.UserARN, UserID: caller.ID,
		}}
	case Public:
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
// pair. The headers Lambda's front end adds are set over the client's:
// host, x-forwarded-for, x-forwarded-proto, x-forwarded-port and, with a
// trace, x-amzn-trace-id.
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
