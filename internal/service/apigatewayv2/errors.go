package apigatewayv2

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The control plane's error identities are the overlay's: a constraint
// failure is BadRequestException 400, inferred (apigateway-http.md 2.3,
// 13.5). No v2 error text is recorded, so every text here is inferred
// (APIGW2-01) and names the constraint.

// errAPINotFound answers an API id the caller's account and region do not
// hold, in the not-found text REST APIs record (apigateway-http.md 2.3).
func errAPINotFound(scope aws.Scope, id string) *awserr.Error {
	return apigatewayv2api.ErrNotFoundException(fmt.Sprintf("Invalid API identifier specified %s:%s", scope.Account, id))
}

func errBadRequest(format string, args ...any) *awserr.Error {
	return apigatewayv2api.ErrBadRequestException(fmt.Sprintf(format, args...))
}

func errNotFound(format string, args ...any) *awserr.Error {
	return apigatewayv2api.ErrNotFoundException(fmt.Sprintf(format, args...))
}

func errConflict(format string, args ...any) *awserr.Error {
	return apigatewayv2api.ErrConflictException(fmt.Sprintf(format, args...))
}

// errNotImplemented refuses a valid request understudy does not emulate:
// what names it, and instead says what to do (CLAUDE.md invariant 3; the
// unit's conflict row 4).
func errNotImplemented(op, what, instead string) *awserr.Error {
	return apigatewayv2api.ErrNotImplemented(fmt.Sprintf("understudy does not implement %s on %s yet; nothing was changed. %s", what, op, instead))
}

// member is a request member and whether the request sets it.
type member struct {
	name string
	set  bool
}

// refuseMembers answers NotImplemented naming the first member of ms the
// request sets, or nil when it sets none.
func refuseMembers(op string, ms ...member) *awserr.Error {
	for _, m := range ms {
		if m.set {
			return errNotImplemented(op, m.name, "Send the request without "+m.name+".")
		}
	}
	return nil
}
