package apigatewayv2

import (
	"cmp"
	"context"
	"maps"
	"net"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// Lengths of an API's members, in characters (apigateway-http.md 13.5).
const (
	maxNameLen        = 128
	maxDescriptionLen = 1024
	maxVersionLen     = 64
)

// The selection expressions of an HTTP API. AWS returns the unbraced route
// selection expression and Terraform sends it, while the API Reference
// says it "must be ${request.method} ${request.path}"; both are accepted
// and echoed as sent (apigateway-http.md 2.4, 12 item 5). The defaults are
// what AWS's create-api example answers (3.1).
const (
	defaultRouteSelection  = "$request.method $request.path"
	bracedRouteSelection   = "${request.method} ${request.path}"
	defaultAPIKeySelection = "$request.header.x-api-key"
)

// checkCreateAPI refuses what CreateApi cannot create, at step 7: a
// WebSocket API or quick create, which understudy does not emulate, and a
// member outside its limits.
func checkCreateAPI(in *apigatewayv2api.CreateApiInput) *awserr.Error {
	const op = "CreateApi"
	if in.ProtocolType == apigatewayv2api.ProtocolTypeWebsocket {
		return errNotImplemented(op, "protocolType WEBSOCKET", "WebSocket APIs are not emulated; create an HTTP API (protocolType HTTP).")
	}
	if err := refuseQuickCreate(op, in.Target, in.RouteKey, in.CredentialsArn); err != nil {
		return err
	}
	if err := checkAPIMembers(&in.Name, in.Description, in.Version, in.RouteSelectionExpression); err != nil {
		return err
	}
	return checkTags("tags", in.Tags)
}

// checkUpdateAPI is checkCreateAPI for the members an UpdateApi carries.
func checkUpdateAPI(in *apigatewayv2api.UpdateApiInput) *awserr.Error {
	if err := refuseQuickCreate("UpdateApi", in.Target, in.RouteKey, in.CredentialsArn); err != nil {
		return err
	}
	return checkAPIMembers(in.Name, in.Description, in.Version, in.RouteSelectionExpression)
}

// refuseQuickCreate refuses quick create: Target, RouteKey and
// CredentialsArn make CreateApi build an integration, a route and an
// automatically deploying $default stage, and UpdateApi change them
// (apigateway-http.md 3.1). understudy does not emulate it (the unit's
// conflict row 4).
func refuseQuickCreate(op string, target, routeKey, credentials *string) *awserr.Error {
	for _, m := range []member{{"target", target != nil}, {"routeKey", routeKey != nil}, {"credentialsArn", credentials != nil}} {
		if m.set {
			return errNotImplemented(op, m.name+" (quick create)",
				"Leave out target, routeKey and credentialsArn, and add the integration, route and $default stage with CreateIntegration, CreateRoute and CreateStage.")
		}
	}
	return nil
}

// checkAPIMembers checks the members of an API that are present: name
// 1..128, description 0..1024 and version 1..64 characters (13.5), and a
// route selection expression of an HTTP API. The texts are inferred
// (APIGW2-01) and name the constraint.
func checkAPIMembers(name, description, version, routeSelection *string) *awserr.Error {
	for _, l := range []struct {
		member         string
		value          *string
		minLen, maxLen int
	}{
		{"name", name, 1, maxNameLen},
		{"description", description, 0, maxDescriptionLen},
		{"version", version, 1, maxVersionLen},
	} {
		if l.value == nil {
			continue
		}
		if err := checkLength(l.member, *l.value, l.minLen, l.maxLen); err != nil {
			return err
		}
	}
	if r := routeSelection; r != nil && *r != defaultRouteSelection && *r != bracedRouteSelection {
		return errBadRequest("routeSelectionExpression %q is not valid for an HTTP API: it must be %s", *r, bracedRouteSelection)
	}
	return nil
}

// checkLength refuses a member whose length in characters is outside
// minLen..maxLen.
func checkLength(member, value string, minLen, maxLen int) *awserr.Error {
	if n := utf8.RuneCountInString(value); n < minLen || n > maxLen {
		return errBadRequest("%s must be %d to %d characters long, not %d", member, minLen, maxLen, n)
	}
	return nil
}

// createAPI creates an HTTP API with the defaults AWS fills (3.1).
func (s *Service) createAPI(_ context.Context, c *kernel.Call, in *apigatewayv2api.CreateApiInput) (*apigatewayv2api.CreateApiOutput, *awserr.Error) {
	scope := c.Scope()
	id := s.newAPIID(scope)
	api, err := kernel.Write(c, s.cells, scope, func(st *State, now clock.Instant, _ *kernel.Tx) (API, *awserr.Error) {
		a := API{
			ID: id, Seq: st.NextSeq, Name: in.Name, Description: clonePtr(in.Description), Version: clonePtr(in.Version),
			RouteSelectionExpression:  deref(in.RouteSelectionExpression, defaultRouteSelection),
			APIKeySelectionExpression: deref(in.ApiKeySelectionExpression, defaultAPIKeySelection),
			CORS:                      corsIn(in.CorsConfiguration),
			DisableExecuteAPIEndpoint: deref(in.DisableExecuteApiEndpoint, false),
			DisableSchemaValidation:   clonePtr(in.DisableSchemaValidation),
			IPAddressType:             string(deref(in.IpAddressType, apigatewayv2api.IpAddressTypeIpv4)),
			Tags:                      maps.Clone(in.Tags),
			Created:                   now,
		}
		// UpdateApi's refusal (APIGW2-13 step 10), taken to hold for
		// CreateApi too (inferred).
		if err := a.CORS.check(); err != nil {
			return API{}, err
		}
		st.NextSeq++
		st.APIs[id] = a
		// Set again in case a Reset dropped the reservation before the
		// write. Inside the write, the gate's shared side orders it against
		// Reset and Import; the index lock is a leaf, never held while a
		// cell lock is taken.
		s.index.set(id, scope)
		return a, nil
	})
	if err != nil {
		s.index.release(id)
		return nil, err
	}
	out := apigatewayv2api.CreateApiOutput(api.output(scope, c.Endpoint()))
	return &out, nil
}

// newAPIID draws an API id no account holds and reserves it for scope.
func (s *Service) newAPIID(scope aws.Scope) string {
	for {
		id := string(s.ids.APIGatewayAPIID())
		if s.index.reserve(id, scope) {
			return id
		}
	}
}

// getAPI answers one API of the caller's account and region.
func (s *Service) getAPI(_ context.Context, c *kernel.Call, in *apigatewayv2api.GetApiInput) (*apigatewayv2api.GetApiOutput, *awserr.Error) {
	scope := c.Scope()
	out := readState(s, scope, func(st *State) *apigatewayv2api.GetApiOutput {
		a, ok := st.APIs[in.ApiId]
		if !ok {
			return nil
		}
		return new(a.output(scope, c.Endpoint()))
	})
	if out == nil {
		return nil, errAPINotFound(scope, in.ApiId)
	}
	return out, nil
}

// getAPIs lists the APIs of the caller's account and region in creation
// order (inferred, APIGW2-03), a page at a time.
func (s *Service) getAPIs(_ context.Context, c *kernel.Call, in *apigatewayv2api.GetApisInput) (*apigatewayv2api.GetApisOutput, *awserr.Error) {
	scope := c.Scope()
	apis := readState(s, scope, func(st *State) []API {
		return slices.SortedFunc(maps.Values(st.APIs), func(x, y API) int { return cmp.Or(cmp.Compare(x.Seq, y.Seq), cmp.Compare(x.ID, y.ID)) })
	})
	items, next, err := page(apis, func(a API) uint64 { return a.Seq }, in.MaxResults, in.NextToken)
	if err != nil {
		return nil, err
	}
	out := &apigatewayv2api.GetApisOutput{Items: make([]apigatewayv2api.Api, 0, len(items)), NextToken: next}
	for i := range items {
		out.Items = append(out.Items, items[i].item(scope, c.Endpoint()))
	}
	return out, nil
}

// updateAPI replaces the members an UpdateApi carries and keeps the rest; a
// CORS configuration merges into the stored one (mergeCORS).
func (s *Service) updateAPI(_ context.Context, c *kernel.Call, in *apigatewayv2api.UpdateApiInput) (*apigatewayv2api.UpdateApiOutput, *awserr.Error) {
	scope := c.Scope()
	api, err := writeAPI(s, c, in.ApiId, func(a *API, _ clock.Instant) (API, *awserr.Error) {
		a.Name = deref(in.Name, a.Name)
		a.RouteSelectionExpression = deref(in.RouteSelectionExpression, a.RouteSelectionExpression)
		a.APIKeySelectionExpression = deref(in.ApiKeySelectionExpression, a.APIKeySelectionExpression)
		a.DisableExecuteAPIEndpoint = deref(in.DisableExecuteApiEndpoint, a.DisableExecuteAPIEndpoint)
		a.IPAddressType = string(deref(in.IpAddressType, apigatewayv2api.IpAddressType(a.IPAddressType)))
		if in.Description != nil {
			a.Description = clonePtr(in.Description)
		}
		if in.Version != nil {
			a.Version = clonePtr(in.Version)
		}
		if in.CorsConfiguration != nil {
			stored := a.CORS
			if stored == nil {
				stored = &CORS{}
			}
			a.CORS = mergeCORS(stored, in.CorsConfiguration)
			if err := a.CORS.check(); err != nil {
				return API{}, err
			}
		}
		if in.DisableSchemaValidation != nil {
			a.DisableSchemaValidation = clonePtr(in.DisableSchemaValidation)
		}
		return *a, nil
	})
	if err != nil {
		return nil, err
	}
	out := apigatewayv2api.UpdateApiOutput(api.output(scope, c.Endpoint()))
	return &out, nil
}

// deleteAPI removes an API with its routes, integrations, stages and
// deployments, and frees its id.
func (s *Service) deleteAPI(_ context.Context, c *kernel.Call, in *apigatewayv2api.DeleteApiInput) (*apigatewayv2api.DeleteApiOutput, *awserr.Error) {
	scope := c.Scope()
	_, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if _, ok := st.APIs[in.ApiId]; !ok {
			return struct{}{}, errAPINotFound(scope, in.ApiId)
		}
		delete(st.APIs, in.ApiId)
		// Inside the write, so a snapshot load that restores the API
		// cannot land between the delete and the release.
		s.index.release(in.ApiId)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &apigatewayv2api.DeleteApiOutput{}, nil
}

// deleteCORS removes an API's CORS configuration, so GetApi answers none
// (apigateway-http.md 3.7, 9.1); an API without one answers the same
// (inferred).
func (s *Service) deleteCORS(_ context.Context, c *kernel.Call, in *apigatewayv2api.DeleteCorsConfigurationInput) (*apigatewayv2api.DeleteCorsConfigurationOutput, *awserr.Error) {
	if _, err := writeAPI(s, c, in.ApiId, func(a *API, _ clock.Instant) (struct{}, *awserr.Error) {
		a.CORS = nil
		return struct{}{}, nil
	}); err != nil {
		return nil, err
	}
	return &apigatewayv2api.DeleteCorsConfigurationOutput{}, nil
}

// writeAPI runs fn on the API id of the call's scope inside one write;
// an id the scope does not hold is NotFoundException.
func writeAPI[R any](s *Service, c *kernel.Call, id string, fn func(a *API, now clock.Instant) (R, *awserr.Error)) (R, *awserr.Error) {
	scope := c.Scope()
	r, err := kernel.Write(c, s.cells, scope, func(st *State, now clock.Instant, _ *kernel.Tx) (R, *awserr.Error) {
		var zero R
		a, ok := st.APIs[id]
		if !ok {
			return zero, errAPINotFound(scope, id)
		}
		r, err := fn(&a, now)
		if err != nil {
			return zero, err
		}
		st.APIs[id] = a
		return r, nil
	})
	return r, err
}

// readAPI runs fn on the API id of scope without creating its cell; an id
// the scope does not hold is NotFoundException. What fn returns must not
// alias the state.
func readAPI[R any](s *Service, scope aws.Scope, id string, fn func(a *API) (R, *awserr.Error)) (R, *awserr.Error) {
	type answer struct {
		r   R
		err *awserr.Error
	}
	got := readState(s, scope, func(st *State) answer {
		a, ok := st.APIs[id]
		if !ok {
			return answer{err: errAPINotFound(scope, id)}
		}
		r, err := fn(&a)
		return answer{r, err}
	})
	return got.r, got.err
}

// output is the API as GetApi answers it, with the ApiEndpoint of the
// endpoint the client addressed (apigateway-http.md 3.1, 9.1): Tags,
// DisableExecuteApiEndpoint and IpAddressType always, CorsConfiguration,
// Description and Version only when set, and no ApiGatewayManaged,
// Warnings or ImportInfo, which quick create and import set.
func (a *API) output(scope aws.Scope, e kernel.Endpoint) apigatewayv2api.GetApiOutput {
	return apigatewayv2api.GetApiOutput{
		ApiEndpoint:               new(apiEndpoint(e, scope, a.ID)),
		ApiId:                     new(a.ID),
		ApiKeySelectionExpression: new(a.APIKeySelectionExpression),
		CorsConfiguration:         a.CORS.wire(),
		CreatedDate:               new(a.Created.Wire()),
		Description:               clonePtr(a.Description),
		DisableSchemaValidation:   clonePtr(a.DisableSchemaValidation),
		DisableExecuteApiEndpoint: new(a.DisableExecuteAPIEndpoint),
		IpAddressType:             new(apigatewayv2api.IpAddressType(a.IPAddressType)),
		Name:                      new(a.Name),
		ProtocolType:              new(apigatewayv2api.ProtocolTypeHttp),
		RouteSelectionExpression:  new(a.RouteSelectionExpression),
		Tags:                      tagsOut(a.Tags),
		Version:                   clonePtr(a.Version),
	}
}

// item is the API as GetApis lists it, the members of output.
func (a *API) item(scope aws.Scope, e kernel.Endpoint) apigatewayv2api.Api {
	o := a.output(scope, e)
	return apigatewayv2api.Api{
		ApiEndpoint: o.ApiEndpoint, ApiId: o.ApiId, ApiKeySelectionExpression: o.ApiKeySelectionExpression,
		CorsConfiguration: o.CorsConfiguration, CreatedDate: o.CreatedDate, Description: o.Description,
		DisableSchemaValidation: o.DisableSchemaValidation, DisableExecuteApiEndpoint: o.DisableExecuteApiEndpoint,
		IpAddressType: o.IpAddressType, Name: a.Name, ProtocolType: apigatewayv2api.ProtocolTypeHttp,
		RouteSelectionExpression: a.RouteSelectionExpression, Tags: o.Tags, Version: o.Version,
	}
}

// apiEndpoint is an API's ApiEndpoint: <scheme>://<api-id>.execute-api.
// <region>.localhost[:<port>] on the scheme and port the client addressed,
// with no trailing slash (apigateway-http.md 2.5, 13.1.4). AWS names
// amazonaws.com, which understudy never answers (11.3, ADR 0013).
func apiEndpoint(e kernel.Endpoint, scope aws.Scope, id string) string {
	scheme := e.Scheme
	if scheme == "" {
		scheme = "http"
	}
	host := id + ".execute-api." + scope.Region.String() + ".localhost"
	if _, port, err := net.SplitHostPort(e.Host); err == nil && port != "" {
		host = net.JoinHostPort(host, port)
	}
	return scheme + "://" + host
}

// corsIn is a request's CORS configuration as stored: a copy, nil lists
// kept nil and empty ones empty, origins and headers lowercased and
// methods uppercased, as AWS stores them (APIGW2-03, recorded 2026-11-03:
// steps 09 and 10; apigateway-http.md 3.7).
func corsIn(c *apigatewayv2api.Cors) *CORS {
	if c == nil {
		return nil
	}
	return mergeCORS(&CORS{}, c)
}

// mergeCORS is old with each member c carries replaced, normalized as
// corsIn says: an UpdateApi's corsConfiguration merges into the stored
// one (APIGW2-13, recorded 2026-11-03: step 11 kept the stored
// allowCredentials; apigateway-http.md 3.7).
func mergeCORS(old *CORS, c *apigatewayv2api.Cors) *CORS {
	out := *old
	recase := func(list []string, f func(string) string) []string {
		if list == nil {
			return nil
		}
		out := make([]string, len(list))
		for i, v := range list {
			out[i] = f(v)
		}
		return out
	}
	if c.AllowCredentials != nil {
		out.AllowCredentials = clonePtr(c.AllowCredentials)
	}
	if c.AllowHeaders != nil {
		out.AllowHeaders = recase(c.AllowHeaders, strings.ToLower)
	}
	if c.AllowMethods != nil {
		out.AllowMethods = recase(c.AllowMethods, strings.ToUpper)
	}
	if c.AllowOrigins != nil {
		out.AllowOrigins = recase(c.AllowOrigins, strings.ToLower)
	}
	if c.ExposeHeaders != nil {
		out.ExposeHeaders = recase(c.ExposeHeaders, strings.ToLower)
	}
	if c.MaxAge != nil {
		out.MaxAge = clonePtr(c.MaxAge)
	}
	return &out
}

// check refuses a CORS configuration that allows credentials for every
// origin, with AWS's text (APIGW2-13, recorded 2026-11-03: step 10).
func (c *CORS) check() *awserr.Error {
	if c != nil && deref(c.AllowCredentials, false) && slices.Contains(c.AllowOrigins, "*") {
		return errBadRequest("allow-credentials is not supported if 'allow-origin' is *")
	}
	return nil
}

// wire is the stored CORS configuration as an output carries it, a copy.
func (c *CORS) wire() *apigatewayv2api.Cors {
	if c == nil {
		return nil
	}
	return &apigatewayv2api.Cors{
		AllowCredentials: clonePtr(c.AllowCredentials), AllowHeaders: slices.Clone(c.AllowHeaders),
		AllowMethods: slices.Clone(c.AllowMethods), AllowOrigins: slices.Clone(c.AllowOrigins),
		ExposeHeaders: slices.Clone(c.ExposeHeaders), MaxAge: clonePtr(c.MaxAge),
	}
}

// clonePtr copies the value p points at, nil for nil, so state never shares
// a request's or an output's memory.
func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// deref is the value p points at, or def when p is nil.
func deref[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}
