package apigatewayv2_test

import (
	"maps"
	"net/http"
	"testing"
)

// The guide's route selection table (apigateway-http.md 4.3) served
// through the data plane: each request reaches the function with the
// route key it selected and that route's path parameters, decoded (4.4,
// 5.1), and $default catches what no other route matches.
func TestDataPlaneSelectsAsTheGuideTable(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /pets/dog/1", "GET /pets/dog/{id}", "GET /pets/{proxy+}", "ANY /{proxy+}", "$default")
		for _, c := range []struct {
			method, path, key string
			params            map[string]string
		}{
			{http.MethodGet, "/pets/dog/1", "GET /pets/dog/1", nil},
			{http.MethodGet, "/pets/dog/2", "GET /pets/dog/{id}", map[string]string{"id": "2"}},
			{http.MethodGet, "/pets/cat/1", "GET /pets/{proxy+}", map[string]string{"proxy": "cat/1"}},
			{http.MethodPost, "/test/5", "ANY /{proxy+}", map[string]string{"proxy": "test/5"}},
			{http.MethodGet, "/pets/dog/a%20b", "GET /pets/dog/{id}", map[string]string{"id": "a b"}},
			{http.MethodGet, "/", "$default", nil},
		} {
			before := len(h.fn.invocations())
			if rec := h.send(h.request(c.method, pathURL(id, c.path), "")); rec.Code != http.StatusOK {
				t.Errorf("%s %s = %d %s, want the function's 200", c.method, c.path, rec.Code, rec.Body)
				continue
			}
			calls := h.fn.invocations()
			if len(calls) != before+1 {
				t.Errorf("%s %s made %d invocations, want 1", c.method, c.path, len(calls)-before)
				continue
			}
			ev := calls[len(calls)-1].event(h)
			if got := strs(ev["pathParameters"]); ev["routeKey"] != c.key || !maps.Equal(got, c.params) {
				t.Errorf("%s %s reached routeKey %v with pathParameters %v, want %s %v", c.method, c.path, ev["routeKey"], got, c.key, c.params)
			}
		}
	})
}
