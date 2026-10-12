package furl

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
)

// Response is what a function URL answers.
type Response struct {
	Status int
	Header http.Header
	// Cookies are sent as one Set-Cookie header each.
	Cookies []string
	Body    []byte
}

// MalformedError is a function output that Lambda cannot turn into an HTTP
// response; the URL answers it with BadGateway.
type MalformedError struct {
	// Problem says what is wrong, as a clause: `statusCode "teapot" is not
	// an integer from 200 to 599`.
	Problem string
}

func (e *MalformedError) Error() string {
	return "the function's response cannot be sent as an HTTP response: " + e.Problem
}

// Infer turns a function's output into the URL's response
// (lambda-events.md 7.4, lambda-api.md 10). A JSON object with a
// statusCode is a structured response: statusCode, headers, cookies, a
// string body and isBase64Encoded. Any other JSON is the body of a 200
// application/json response: a JSON string as the string itself, as
// recorded against AWS, anything else re-serialized compactly. Without a
// Content-Type header the response is application/json.
func Infer(payload []byte) (Response, error) {
	var value any
	var b bytes.Buffer
	if json.Unmarshal(payload, &value) != nil || json.Compact(&b, payload) != nil {
		return Response{}, &MalformedError{Problem: "it is not JSON"}
	}
	resp := Response{Status: http.StatusOK, Header: http.Header{}, Body: b.Bytes()}
	var fields map[string]json.RawMessage
	switch v := value.(type) {
	case map[string]any:
		if _, ok := v["statusCode"]; ok && json.Unmarshal(payload, &fields) == nil {
			r, err := structured(fields)
			if err != nil {
				return Response{}, err
			}
			resp = r
		}
	case string:
		resp.Body = []byte(v)
	}
	if resp.Header.Get("Content-Type") == "" {
		resp.Header.Set("Content-Type", "application/json")
	}
	return resp, nil
}

// structured reads a structured response's members. Final statuses only:
// an informational one cannot end an HTTP exchange. A null body is an
// empty one.
func structured(fields map[string]json.RawMessage) (Response, error) {
	r := Response{Header: http.Header{}}
	if err := json.Unmarshal(fields["statusCode"], &r.Status); err != nil || r.Status < 200 || r.Status > 599 {
		return Response{}, &MalformedError{Problem: "statusCode " + string(fields["statusCode"]) + " is not an integer from 200 to 599"}
	}
	var headers map[string]string
	if err := member(fields, "headers", &headers); err != nil {
		return Response{}, &MalformedError{Problem: "headers is not an object of string values"}
	}
	for name, value := range headers {
		if !framing(name) {
			r.Header.Set(name, value)
		}
	}
	if err := member(fields, "cookies", &r.Cookies); err != nil {
		return Response{}, &MalformedError{Problem: "cookies is not an array of strings"}
	}
	var body *string
	if err := member(fields, "body", &body); err != nil {
		return Response{}, &MalformedError{Problem: "body is not a string"}
	}
	var encoded bool
	if err := member(fields, "isBase64Encoded", &encoded); err != nil {
		return Response{}, &MalformedError{Problem: "isBase64Encoded is not a boolean"}
	}
	if body == nil {
		return r, nil
	}
	r.Body = []byte(*body)
	if encoded {
		decoded, err := base64.StdEncoding.DecodeString(*body)
		if err != nil {
			return Response{}, &MalformedError{Problem: "isBase64Encoded is true but body is not base64"}
		}
		r.Body = decoded
	}
	return r, nil
}

// framing reports whether name, in any case, is a header that frames the
// HTTP exchange rather than describes the response: Lambda's front end
// sets these itself, so a function's value, such as a Content-Length of
// its base64 body, cannot truncate or break the response.
func framing(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Content-Length", "Transfer-Encoding", "Connection", "Keep-Alive", "Upgrade", "Trailer":
		return true
	}
	return false
}

// member decodes the member name of fields into v; an absent member leaves
// v as it is. Keys match exactly, unlike encoding/json's struct fields.
func member(fields map[string]json.RawMessage, name string, v any) error {
	raw, ok := fields[name]
	if !ok {
		return nil
	}
	return json.Unmarshal(raw, v)
}

// Write sends r on w, over any headers w already holds.
func (r Response) Write(w http.ResponseWriter) {
	h := w.Header()
	maps.Copy(h, r.Header)
	for _, c := range r.Cookies {
		h.Add("Set-Cookie", c)
	}
	w.WriteHeader(r.Status)
	// A failed write means the client went away; nothing else can be sent.
	_, _ = w.Write(r.Body) //nolint:errcheck // the client is gone when this fails
}

// BadGateway is the answer to a function error or a malformed response:
// 502 with the plain text body AWS sends (LocalStack's
// test_lambda_url_invocation_exception).
func BadGateway() Response {
	return Response{Status: http.StatusBadGateway, Header: http.Header{"Content-Type": {"text/plain"}}, Body: []byte("Internal Server Error")}
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
// AuthType NONE URL whose resource policy does not allow public
// lambda:InvokeFunctionUrl, or an unsigned request to an AWS_IAM URL.
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
