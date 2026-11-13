package furl

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/httpevent"
)

// Response is what a function URL answers.
type Response = httpevent.Response

// MalformedError is a function output that Lambda cannot turn into an HTTP
// response; the URL answers it with BadGateway.
type MalformedError = httpevent.MalformedError

// Infer turns a function's output into the URL's response
// (lambda-events.md 7.4, lambda-api.md 10) with httpevent.InferV2.
func Infer(payload []byte) (Response, error) {
	return httpevent.InferV2(payload)
}

// Write sends r on w, the answer to a request for method, as a function
// URL frames it: always with a Content-Length, so a large body is never
// chunked (lambda/R13/02-resp-big.json), and to HEAD with Content-Length
// 0 and no body, whatever body r holds (lambda/R13/01-head.json). HTTP
// APIs keep httpevent.Response.Write's framing.
func Write(w http.ResponseWriter, method string, r Response) {
	if method == http.MethodHead {
		r.Body = nil
	}
	r.Header = r.Header.Clone()
	if r.Header == nil {
		r.Header = http.Header{}
	}
	r.Header.Set("Content-Length", strconv.Itoa(len(r.Body)))
	r.Write(w)
}

// BadGateway is the answer to a function error, a timeout or a malformed
// response: 502 Internal Server Error, labelled application/json although
// the body is not JSON (lambda/R13/02-resp-raise.json,
// 02-resp-timeout.json, 02-resp-sc99.json).
func BadGateway() Response {
	return Response{Status: http.StatusBadGateway, Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte("Internal Server Error")}
}

// forbiddenHelp is the message of a 403 whose URL exists (lambda-events.md
// 7.2, test_lambda_permission_url_invocation).
const forbiddenHelp = "Forbidden. For troubleshooting Function URL authorization issues, see: https://docs.aws.amazon.com/lambda/latest/dg/urls-auth.html"

// UnknownURL is the answer to a url id no function URL has: 403 with
// {"Message":null} and x-amzn-ErrorType AccessDeniedException
// (test_lambda_url_non_existing_url).
func UnknownURL() Response {
	return denied("AccessDeniedException", []byte(`{"Message":null}`))
}

// Forbidden is the answer to a request the URL's auth type refuses: an
// AuthType NONE URL whose resource policy does not allow a public invoke
// through the URL, or an unsigned request to an AWS_IAM URL.
func Forbidden() Response {
	return Denied("AccessDeniedException", forbiddenHelp)
}

// Denied is a 403 of errorType with message, as Lambda's front end words
// its refusals: {"Message":"..."} with x-amzn-ErrorType.
func Denied(errorType, message string) Response {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(struct{ Message string }{message}) //nolint:errcheck,errchkjson // a string member cannot fail to encode: invalid UTF-8 is replaced, not refused
	return denied(errorType, bytes.TrimSuffix(b.Bytes(), []byte("\n")))
}

func denied(errorType string, body []byte) Response {
	return Response{
		Status: http.StatusForbidden,
		Header: http.Header{"Content-Type": {"application/json"}, "X-Amzn-Errortype": {errorType}},
		Body:   body,
	}
}
