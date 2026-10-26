package httpevent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
)

// Response is what the HTTP front end answers.
type Response struct {
	Status int
	Header http.Header
	// Cookies are sent as one Set-Cookie header each.
	Cookies []string
	Body    []byte
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

// MalformedError is a function output the front end cannot turn into an
// HTTP response; the front end answers it with its own error, such as a
// function URL's 502.
type MalformedError struct {
	// Problem says what is wrong, as a clause: `statusCode "teapot" is not
	// an integer from 200 to 599`.
	Problem string
}

func (e *MalformedError) Error() string {
	return "the function's response cannot be sent as an HTTP response: " + e.Problem
}

// InferV2 turns a function's output into the HTTP response under payload
// format 2.0 (lambda-events.md 7.4, lambda-api.md 10, apigateway-http.md
// 5.3). A JSON object with a statusCode is a structured response:
// statusCode, headers, cookies, a string body and isBase64Encoded. Any
// other JSON is the body of a 200 application/json response: a JSON string
// as the string itself, as recorded against AWS for function URLs,
// anything else re-serialized compactly. Without a Content-Type header the
// response is application/json.
func InferV2(payload []byte) (Response, error) {
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

// V2Defaults are what InferV2With assumes for a member a structured
// response leaves out, where the front ends differ. The zero value is a
// function URL's, which InferV2 applies.
type V2Defaults struct {
	// ContentType is the Content-Type of a structured response that sets
	// none; empty means application/json.
	ContentType string
}

// errV2DefaultsNotImplemented is InferV2With's answer until it is
// implemented.
var errV2DefaultsNotImplemented = errors.New("httpevent: InferV2With is not implemented yet")

// InferV2With is InferV2 with the defaults d in place of a function URL's
// (apigateway-http.md 5.3, 4.5). It is not implemented yet and returns an
// error.
func InferV2With(_ []byte, _ V2Defaults) (Response, error) {
	return Response{}, errV2DefaultsNotImplemented
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
// HTTP exchange rather than describes the response: the front end sets
// these itself, so a function's value, such as a Content-Length of its
// base64 body, cannot truncate or break the response.
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
