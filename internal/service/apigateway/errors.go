package apigateway

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The control plane's error identities are the generated ones
// (apigateway-rest.md 2.4). Texts marked recorded are AWS's; the others are
// inferred and name the constraint.

// errAPINotFound answers a REST API id the caller's account and region do
// not hold (recorded, apigateway-rest.md 2.4).
func errAPINotFound(scope aws.Scope, id string) *awserr.Error {
	return apigatewayapi.ErrNotFoundException(fmt.Sprintf("Invalid API identifier specified %s:%s", scope.Account, id))
}

// errResourceNotFound answers a resource id the API does not hold
// (recorded, apigateway-rest.md 2.4).
func errResourceNotFound() *awserr.Error {
	return apigatewayapi.ErrNotFoundException("Invalid Resource identifier specified")
}

// errMethodNotFound answers a method the resource does not have (recorded,
// apigateway-rest.md 2.4).
func errMethodNotFound() *awserr.Error {
	return apigatewayapi.ErrNotFoundException("Invalid Method identifier specified")
}

// errIntegrationNotFound answers a method that has no integration
// (recorded, apigateway-rest.md 2.4).
func errIntegrationNotFound() *awserr.Error {
	return apigatewayapi.ErrNotFoundException("Invalid Integration identifier specified")
}

// errResponseNotFound answers a status code the method or integration has
// no response for (recorded, apigateway-rest.md 2.4).
func errResponseNotFound() *awserr.Error {
	return apigatewayapi.ErrNotFoundException("Invalid Response status code specified")
}

// errDeploymentNotFound answers a deployment id the API does not hold
// (inferred, in the shape of the recorded identifier texts of
// apigateway-rest.md 2.4).
func errDeploymentNotFound() *awserr.Error {
	return apigatewayapi.ErrNotFoundException("Invalid Deployment identifier specified")
}

// errStageNotFound answers a stage name the API does not have (APIGW1-06,
// recorded 2026-11-03 for GetStage and DeleteStage).
func errStageNotFound() *awserr.Error {
	return apigatewayapi.ErrNotFoundException("Invalid stage identifier specified")
}

func errBadRequest(format string, args ...any) *awserr.Error {
	return apigatewayapi.ErrBadRequestException(fmt.Sprintf(format, args...))
}

// member is a request member and whether the request sets it.
type member struct {
	name string
	set  bool
}

// refuseUnimplemented refuses the first member the request sets that
// understudy does not accept on op yet: 501 naming it, nothing changed
// (CLAUDE.md invariant 3; the unit's conflict row 4). nil when none is set.
func refuseUnimplemented(op string, ms ...member) *awserr.Error {
	for _, m := range ms {
		if m.set {
			return apigatewayapi.ErrNotImplemented(fmt.Sprintf(
				"understudy does not implement %s on %s yet; nothing was changed. Send the request without %s.", m.name, op, m.name))
		}
	}
	return nil
}
