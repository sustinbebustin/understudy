package furl

import (
	"net/http"
	"strconv"
	"strings"
)

// CORS is a function URL's CORS configuration (lambda-events.md 7.1).
type CORS struct {
	AllowCredentials bool
	AllowHeaders     []string
	AllowMethods     []string
	AllowOrigins     []string
	ExposeHeaders    []string
	// MaxAge is Access-Control-Max-Age in seconds; nil sends none.
	MaxAge *int32
}

// Preflight answers r when it is a CORS preflight request: OPTIONS with
// Origin and Access-Control-Request-Method. Lambda answers preflights
// itself with only the configured headers, and the function does not run
// (lambda-events.md 7.4). An origin the configuration does not allow gets
// no CORS headers, which the browser reads as a refusal. The status is
// 200; AWS's is UNVERIFIED.
func (c CORS) Preflight(r *http.Request) (Response, bool) {
	origin := r.Header.Get("Origin")
	if r.Method != http.MethodOptions || origin == "" || r.Header.Get("Access-Control-Request-Method") == "" {
		return Response{}, false
	}
	resp := Response{Status: http.StatusOK, Header: http.Header{}}
	if !c.allow(resp.Header, origin) {
		return resp, true
	}
	set(resp.Header, "Access-Control-Allow-Methods", c.AllowMethods)
	set(resp.Header, "Access-Control-Allow-Headers", c.AllowHeaders)
	if c.MaxAge != nil {
		resp.Header.Set("Access-Control-Max-Age", strconv.Itoa(int(*c.MaxAge)))
	}
	return resp, true
}

// Decorate adds the CORS headers for a request from origin to a function's
// response: Access-Control-Allow-Origin, -Allow-Credentials and
// -Expose-Headers when the configuration allows origin. Configured values
// replace any the function set, so none is sent twice. A request without
// an Origin header is not a CORS request and gets none.
func (c CORS) Decorate(resp *Response, origin string) {
	if origin == "" || !c.allow(resp.Header, origin) {
		return
	}
	set(resp.Header, "Access-Control-Expose-Headers", c.ExposeHeaders)
}

// allow sets Access-Control-Allow-Origin, and -Allow-Credentials when
// configured, on h when origin is allowed: "*" when every origin is, else
// the origin itself with Vary: Origin. It reports whether origin is allowed.
func (c CORS) allow(h http.Header, origin string) bool {
	allowed := ""
	for _, o := range c.AllowOrigins {
		if o == "*" {
			allowed = "*"
			break
		}
		if strings.EqualFold(o, origin) {
			allowed = origin
		}
	}
	if allowed == "" {
		return false
	}
	h.Set("Access-Control-Allow-Origin", allowed)
	if allowed != "*" {
		h.Add("Vary", "Origin")
	}
	if c.AllowCredentials {
		h.Set("Access-Control-Allow-Credentials", "true")
	}
	return true
}

// set sets name to values joined with commas; no values sets nothing.
func set(h http.Header, name string, values []string) {
	if len(values) > 0 {
		h.Set(name, strings.Join(values, ","))
	}
}
