package httpevent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
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
	return InferV2With(payload, V2Defaults{})
}

// V2Defaults are what InferV2With assumes for a member a structured
// response leaves out, where the front ends differ. The zero value is a
// function URL's, which InferV2 applies.
type V2Defaults struct {
	// ContentType is the Content-Type of a structured response that sets
	// none; empty means application/json.
	ContentType string
}

// InferV2With is InferV2 with the defaults d in place of a function URL's
// (apigateway-http.md 5.3, 4.5). d.ContentType applies only to a
// structured response with no Content-Type header in any case; any other
// output is always application/json.
func InferV2With(payload []byte, d V2Defaults) (Response, error) {
	var value any
	var b bytes.Buffer
	if json.Unmarshal(payload, &value) != nil || json.Compact(&b, payload) != nil {
		return Response{}, &MalformedError{Problem: "it is not JSON"}
	}
	resp := Response{Status: http.StatusOK, Header: http.Header{}, Body: b.Bytes()}
	contentType := "application/json"
	var fields map[string]json.RawMessage
	switch v := value.(type) {
	case map[string]any:
		if _, ok := v["statusCode"]; ok && json.Unmarshal(payload, &fields) == nil {
			r, err := structured(fields)
			if err != nil {
				return Response{}, err
			}
			resp = r
			if d.ContentType != "" {
				contentType = d.ContentType
			}
		}
	case string:
		resp.Body = []byte(v)
	}
	if resp.Header.Get("Content-Type") == "" {
		resp.Header.Set("Content-Type", contentType)
	}
	return resp, nil
}

// structured reads a structured 2.0 response's members: statusCode,
// headers, cookies, body and isBase64Encoded.
func structured(fields map[string]json.RawMessage) (Response, error) {
	status, err := statusOf(fields)
	if err != nil {
		return Response{}, err
	}
	r := Response{Status: status, Header: http.Header{}}
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
	if r.Body, err = bodyOf(fields); err != nil {
		return Response{}, err
	}
	return r, nil
}

// statusOf reads a response's statusCode: final statuses only, since an
// informational one cannot end an HTTP exchange.
func statusOf(fields map[string]json.RawMessage) (int, error) {
	var status int
	if err := json.Unmarshal(fields["statusCode"], &status); err != nil || status < 200 || status > 599 {
		return 0, &MalformedError{Problem: "statusCode " + string(fields["statusCode"]) + " is not an integer from 200 to 599"}
	}
	return status, nil
}

// bodyOf reads a response's body and isBase64Encoded: a string body, null
// or absent being empty, decoded when isBase64Encoded is true.
func bodyOf(fields map[string]json.RawMessage) ([]byte, error) {
	var body *string
	if err := member(fields, "body", &body); err != nil {
		return nil, &MalformedError{Problem: "body is not a string"}
	}
	var encoded bool
	if err := member(fields, "isBase64Encoded", &encoded); err != nil {
		return nil, &MalformedError{Problem: "isBase64Encoded is not a boolean"}
	}
	if body == nil {
		return nil, nil
	}
	if !encoded {
		return []byte(*body), nil
	}
	decoded, err := base64.StdEncoding.DecodeString(*body)
	if err != nil {
		return nil, &MalformedError{Problem: "isBase64Encoded is true but body is not base64"}
	}
	return decoded, nil
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
