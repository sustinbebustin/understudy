package apigateway_test

import (
	"net/http"
	"testing"
)

// The data plane serves the resource the matcher picks and puts its
// bindings in the event: a variable resource binds its one segment, and a
// literal resource without a method is backed out of to a greedy one that
// has it (apigateway-rest.md 4.3, finding 4; the event members 5.1).
func TestInvokeRoutesThroughTheMatcher(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.proxyAPI("/items/{id}", "/{proxy+}")
		paths := h.paths(id)
		for _, c := range []struct {
			path, resource, param, value string
		}{
			{"/s/items/7", "/items/{id}", "id", "7"},
			{"/s/items", "/{proxy+}", "proxy", "items"},
		} {
			if rec := h.get(id, c.path); rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d %s, want the function's 200", c.path, rec.Code, rec.Body)
			}
			calls := h.fn.invocations()
			ev := calls[len(calls)-1].event(h)
			rc := obj(h, ev, "requestContext")
			if ev["resource"] != c.resource || rc["resourcePath"] != c.resource || rc["resourceId"] != paths[c.resource] {
				t.Errorf("GET %s: resource %v, resourcePath %v, resourceId %v; want %s (%s)", c.path, ev["resource"], rc["resourcePath"], rc["resourceId"], c.resource, paths[c.resource])
			}
			if p := obj(h, ev, "pathParameters"); len(p) != 1 || p[c.param] != c.value {
				t.Errorf("GET %s: pathParameters %v, want %s=%s", c.path, p, c.param, c.value)
			}
		}
	})
}
