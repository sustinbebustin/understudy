package httpevent_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/httpevent"
)

// genPayload draws function output: raw bytes, a JSON string, or an
// object with or without each member of a structured 2.0 response.
func genPayload() *rapid.Generator[[]byte] {
	return rapid.OneOf(
		rapid.SliceOfN(rapid.Byte(), 0, 32),
		rapid.Map(rapid.StringMatching(`"[ -~]{0,12}"`), func(s string) []byte { return []byte(s) }),
		rapid.Custom(func(t *rapid.T) []byte {
			fields := map[string]any{}
			if rapid.Bool().Draw(t, "has status") {
				fields["statusCode"] = rapid.SampledFrom([]any{200, 201, 404, 599, 600, "200"}).Draw(t, "status")
			}
			if rapid.Bool().Draw(t, "has headers") {
				fields["headers"] = rapid.SampledFrom([]any{
					nil,
					map[string]any{"X-One": "1"},
					map[string]any{"content-type": "text/html"},
					map[string]any{"Content-Type": "image/png"},
					map[string]any{"CONTENT-TYPE": "a/b"},
					map[string]any{"x": 1},
				}).Draw(t, "headers")
			}
			if rapid.Bool().Draw(t, "has body") {
				fields["body"] = rapid.SampledFrom([]any{"hello", "aGVsbG8=", 7, nil}).Draw(t, "body")
			}
			if rapid.Bool().Draw(t, "has base64") {
				fields["isBase64Encoded"] = rapid.SampledFrom([]any{true, false}).Draw(t, "base64")
			}
			if rapid.Bool().Draw(t, "has cookies") {
				fields["cookies"] = rapid.SampledFrom([]any{nil, []any{"a=1", "b=2"}}).Draw(t, "cookies")
			}
			b, err := json.Marshal(fields)
			if err != nil {
				t.Fatalf("marshal %v: %v", fields, err)
			}
			return b
		}),
	)
}

func sameResponse(a, b httpevent.Response) bool {
	return a.Status == b.Status && reflect.DeepEqual(a.Header, b.Header) && reflect.DeepEqual(a.Cookies, b.Cookies) && bytes.Equal(a.Body, b.Body)
}

// The zero V2Defaults are a function URL's, so InferV2With with them
// answers exactly as InferV2, errors included: function URL output does
// not change (lambda-events.md 7.4).
func TestInferV2WithZeroDefaultsIsInferV2(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := genPayload().Draw(t, "payload")
		want, werr := httpevent.InferV2(p)
		got, gerr := httpevent.InferV2With(p, httpevent.V2Defaults{})
		if (werr == nil) != (gerr == nil) || (werr != nil && werr.Error() != gerr.Error()) {
			t.Fatalf("InferV2With(%s, {}) error = %v, InferV2 error = %v", p, gerr, werr)
		}
		if werr == nil && !sameResponse(got, want) {
			t.Fatalf("InferV2With(%s, {}) = %+v, InferV2 = %+v", p, got, want)
		}
	})
}

// hasContentType reports whether a structured response's headers name a
// Content-Type in any case.
func hasContentType(p []byte) bool {
	var v struct {
		StatusCode json.RawMessage `json:"statusCode"`
		Headers    map[string]any  `json:"headers"`
	}
	if json.Unmarshal(p, &v) != nil {
		return false
	}
	for name := range v.Headers {
		if strings.EqualFold(name, "Content-Type") {
			return true
		}
	}
	return false
}

// A default Content-Type applies only where a structured response (an
// object with statusCode) names none in any case; everything else answers
// as InferV2 does. Output without a statusCode is always application/json
// ("content-type is application/json", apigateway-http.md 5.3). The HTTP
// API front passes the apigatewayv2.payload.unrecorded switch's
// DefaultContentType; the value here is arbitrary, since the rule is the
// parameter's.
func TestInferV2WithAppliesTheDefaultContentTypeToStructuredResponsesOnly(t *testing.T) {
	const ct = "text/x-understudy-default"
	rapid.Check(t, func(t *rapid.T) {
		p := genPayload().Draw(t, "payload")
		plain, perr := httpevent.InferV2(p)
		got, gerr := httpevent.InferV2With(p, httpevent.V2Defaults{ContentType: ct})
		if (perr == nil) != (gerr == nil) {
			t.Fatalf("InferV2With(%s) error = %v, InferV2 error = %v", p, gerr, perr)
		}
		if perr != nil {
			return
		}
		var obj map[string]json.RawMessage
		structured := json.Unmarshal(p, &obj) == nil && obj != nil && obj["statusCode"] != nil
		switch {
		case structured && !hasContentType(p):
			if got.Header.Get("Content-Type") != ct {
				t.Fatalf("InferV2With(%s) Content-Type = %q, want the default %q", p, got.Header.Get("Content-Type"), ct)
			}
			got.Header.Del("Content-Type")
			plain.Header.Del("Content-Type")
			if !sameResponse(got, plain) {
				t.Fatalf("InferV2With(%s) = %+v, want InferV2's %+v apart from Content-Type", p, got, plain)
			}
		default:
			if !sameResponse(got, plain) {
				t.Fatalf("InferV2With(%s) = %+v, want InferV2's %+v", p, got, plain)
			}
		}
	})
}

// The documented examples: a bare string and an object without statusCode
// are 200 application/json responses whatever the default, the string's
// body the string itself (apigateway-http.md 5.3, the guide's
// interpretation table; conflict row 6 of lambda-events.md 7.4 settled for
// the bare string).
func TestInferV2WithKeepsTheInferredContentType(t *testing.T) {
	for _, p := range []string{`"Hello from Lambda!"`, `{ "message": "Hello from Lambda!" }`} {
		got, err := httpevent.InferV2With([]byte(p), httpevent.V2Defaults{ContentType: "text/plain; charset=utf-8"})
		if err != nil {
			t.Fatalf("InferV2With(%s): %v", p, err)
		}
		if got.Status != 200 || got.Header.Get("Content-Type") != "application/json" {
			t.Errorf("InferV2With(%s) = %d %q, want 200 application/json", p, got.Status, got.Header.Get("Content-Type"))
		}
		if p[0] == '"' && string(got.Body) != "Hello from Lambda!" {
			t.Errorf("InferV2With(%s) body = %q, want Hello from Lambda!", p, got.Body)
		}
	}
}
