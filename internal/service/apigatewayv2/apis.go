package apigatewayv2

import (
	"context"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// maxNameLen is the longest API name (apigateway-http.md 13.5).
const maxNameLen = 128

// createAPI creates an HTTP API with a name and nothing else.
func (s *Service) createAPI(_ context.Context, c *kernel.Call, in *apigatewayv2api.CreateApiInput) (*apigatewayv2api.CreateApiOutput, *awserr.Error) {
	if in.ProtocolType != apigatewayv2api.ProtocolTypeHttp {
		return nil, errNotImplemented("CreateApi", "protocolType "+string(in.ProtocolType), "WebSocket APIs are not emulated; create an HTTP API (protocolType HTTP).")
	}
	if err := refuseMembers("CreateApi",
		member{"apiKeySelectionExpression", in.ApiKeySelectionExpression != nil},
		member{"corsConfiguration", in.CorsConfiguration != nil},
		member{"credentialsArn", in.CredentialsArn != nil},
		member{"description", in.Description != nil},
		member{"disableExecuteApiEndpoint", in.DisableExecuteApiEndpoint != nil},
		member{"disableSchemaValidation", in.DisableSchemaValidation != nil},
		member{"ipAddressType", in.IpAddressType != nil},
		member{"routeKey", in.RouteKey != nil},
		member{"routeSelectionExpression", in.RouteSelectionExpression != nil},
		member{"tags", in.Tags != nil},
		member{"target", in.Target != nil},
		member{"version", in.Version != nil},
	); err != nil {
		return nil, err
	}
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > maxNameLen {
		return nil, errBadRequest("name must be 1 to %d characters long, not %d", maxNameLen, n)
	}
	scope := c.Scope()
	id := s.newAPIID(scope)
	api, err := kernel.Write(c, s.cells, scope, func(st *State, now clock.Instant, _ *kernel.Tx) (API, *awserr.Error) {
		a := API{ID: id, Name: in.Name, Created: now}
		st.APIs[id] = a
		return a, nil
	})
	if err != nil {
		s.index.release(id)
		return nil, err
	}
	// Set again in case a Reset rebuilt the index between the reservation
	// and the write.
	s.index.set(id, scope)
	c.Touch(apiARN(scope, id))
	return &apigatewayv2api.CreateApiOutput{
		ApiId: new(id), Name: new(api.Name), ProtocolType: new(apigatewayv2api.ProtocolTypeHttp), CreatedDate: new(api.Created.Wire()),
	}, nil
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

// updateAPI runs fn on the API id of the call's scope inside one write;
// an id the scope does not hold is NotFoundException.
func updateAPI[R any](s *Service, c *kernel.Call, id string, fn func(a *API, now clock.Instant) (R, *awserr.Error)) (R, *awserr.Error) {
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
	if err == nil {
		c.Touch(apiARN(scope, id))
	}
	return r, err
}
