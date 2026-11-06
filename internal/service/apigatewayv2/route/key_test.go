package route_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2/route"
)

// methods are a route key's methods: the seven $context.httpMethod takes
// plus ANY (apigateway-http.md 4.4, the logging variables page and the
// routes page).
var methods = []route.Method{
	route.MethodAny, route.MethodDelete, route.MethodGet, route.MethodHead,
	route.MethodOptions, route.MethodPatch, route.MethodPost, route.MethodPut,
}

// The route keys the HTTP API guide's route selection table names
// (apigateway-http.md 4.3, https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-routes.html):
// $default, literal paths, {name} variables and a greedy {proxy+} last.
func TestParseKeyReadsTheDocumentedKeys(t *testing.T) {
	for key, want := range map[string]route.Key{
		"$default":           route.Default{},
		"GET /pets":          route.Keyed{Method: route.MethodGet, Segments: []route.Segment{route.Literal{Value: "pets"}}},
		"GET /pets/dog/1":    route.Keyed{Method: route.MethodGet, Segments: []route.Segment{route.Literal{Value: "pets"}, route.Literal{Value: "dog"}, route.Literal{Value: "1"}}},
		"GET /pets/dog/{id}": route.Keyed{Method: route.MethodGet, Segments: []route.Segment{route.Literal{Value: "pets"}, route.Literal{Value: "dog"}, route.Var{Name: "id"}}},
		"GET /pets/{proxy+}": route.Keyed{Method: route.MethodGet, Segments: []route.Segment{route.Literal{Value: "pets"}, route.Greedy{Name: "proxy"}}},
		"ANY /{proxy+}":      route.Keyed{Method: route.MethodAny, Segments: []route.Segment{route.Greedy{Name: "proxy"}}},
	} {
		got, err := route.ParseKey(key)
		if err != nil {
			t.Errorf("ParseKey(%q) = %v", key, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ParseKey(%q) = %#v, want %#v", key, got, want)
		}
	}
}

// Every route key method is accepted (apigateway-http.md 4.4).
func TestParseKeyAcceptsEveryMethod(t *testing.T) {
	for _, m := range methods {
		key := string(m) + " /orders/{id}"
		got, err := route.ParseKey(key)
		if err != nil {
			t.Errorf("ParseKey(%q) = %v", key, err)
			continue
		}
		if k, ok := got.(route.Keyed); !ok || k.Method != m {
			t.Errorf("ParseKey(%q) = %#v, want method %s", key, got, m)
		}
	}
}

// A route key is $default or "a combination of an HTTP method and resource
// path" (the routes API reference, apigateway-http.md 2.4), and a greedy
// variable "must be at the end of the resource path" (the routes page,
// 2.4): anything else is refused with a *KeyError naming the key. The
// error's text is inferred (APIGW2-01), so only its key is checked.
func TestParseKeyRefusesWhatTheDocsExclude(t *testing.T) {
	for _, key := range []string{
		"",
		"GET",
		"/pets",
		"GET pets",
		"GET /{proxy+}/x",
		"GET /pets/{proxy+}/{id}",
		"$DEFAULT",
	} {
		got, err := route.ParseKey(key)
		if err == nil {
			t.Errorf("ParseKey(%q) = %#v, want a *KeyError", key, got)
			continue
		}
		if err.Key != key || err.Problem == "" || !strings.Contains(err.Error(), fmt.Sprintf("%q", key)) {
			t.Errorf("ParseKey(%q) error = %#v (%q), want its Key, a Problem and the quoted key in the text", key, err, err.Error())
		}
	}
}

// genKey draws a route key by the documented grammar: a method, then
// literal and {name} segments, and optionally a greedy {name+} last, names
// distinct within the key.
func genKey() *rapid.Generator[route.Keyed] {
	return rapid.Custom(func(t *rapid.T) route.Keyed {
		k := route.Keyed{Method: rapid.SampledFrom(methods).Draw(t, "method")}
		n := rapid.IntRange(1, 4).Draw(t, "segments")
		for i := range n {
			if rapid.Bool().Draw(t, fmt.Sprintf("var %d", i)) {
				k.Segments = append(k.Segments, route.Var{Name: fmt.Sprintf("v%d", i)})
				continue
			}
			k.Segments = append(k.Segments, route.Literal{Value: rapid.StringMatching(`[a-z0-9]{1,6}`).Draw(t, fmt.Sprintf("literal %d", i))})
		}
		if rapid.Bool().Draw(t, "greedy") {
			k.Segments = append(k.Segments, route.Greedy{Name: "proxy"})
		}
		return k
	})
}

// writePath writes a key's path as a client writes it.
func writePath(k route.Keyed) string {
	var b strings.Builder
	for _, s := range k.Segments {
		b.WriteByte('/')
		switch s := s.(type) {
		case route.Literal:
			b.WriteString(s.Value)
		case route.Var:
			b.WriteString("{" + s.Name + "}")
		case route.Greedy:
			b.WriteString("{" + s.Name + "+}")
		}
	}
	return b.String()
}

// Every key written by the documented grammar parses to the key it was
// written from, and Path writes it back, so a stored key echoes byte for
// byte (Terraform reads route_key back, apigateway-http.md 9.2).
func TestParseKeyRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		want := genKey().Draw(t, "key")
		text := string(want.Method) + " " + writePath(want)
		got, err := route.ParseKey(text)
		if err != nil {
			t.Fatalf("ParseKey(%q) = %v", text, err)
		}
		k, ok := got.(route.Keyed)
		if !ok || !reflect.DeepEqual(k, want) {
			t.Fatalf("ParseKey(%q) = %#v, want %#v", text, got, want)
		}
		if p := k.Path(); p != writePath(want) {
			t.Fatalf("Path() of %q = %q", text, p)
		}
	})
}

// ParseKey answers any string with a key or a *KeyError naming it, never a
// panic; an accepted key has a greedy variable last or not at all.
func TestParseKeyTotal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.OneOf(
			rapid.String(),
			rapid.StringMatching(`(GET|ANY|get|FETCH)? ?/?([a-z{}+]{0,4}/?){0,4}`),
		).Draw(t, "key")
		got, err := route.ParseKey(s)
		if err != nil {
			if err.Key != s {
				t.Fatalf("ParseKey(%q) error names key %q", s, err.Key)
			}
			return
		}
		k, ok := got.(route.Keyed)
		if !ok {
			return
		}
		for i, seg := range k.Segments {
			if _, greedy := seg.(route.Greedy); greedy && i != len(k.Segments)-1 {
				t.Fatalf("ParseKey(%q) = %#v: greedy variable before the end", s, k)
			}
		}
	})
}
