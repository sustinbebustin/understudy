package apigatewayv2

import (
	"context"
	"maps"
	"regexp"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// defaultStage is the stage served from the base of the API's URL
// (apigateway-http.md 3.4).
const defaultStage = "$default"

// stageName is a stage name other than $default: "alphanumeric characters,
// hyphens, and underscores ... Maximum length is 128 characters" (the stage
// API reference, apigateway-http.md 2.4).
var stageName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// createStage creates a stage; with AutoDeploy it serves the API's routes
// before the call returns.
func (s *Service) createStage(_ context.Context, c *kernel.Call, in *apigatewayv2api.CreateStageInput) (*apigatewayv2api.CreateStageOutput, *awserr.Error) {
	if err := refuseMembers("CreateStage",
		member{"accessLogSettings", in.AccessLogSettings != nil},
		member{"clientCertificateId", in.ClientCertificateId != nil},
		member{"defaultRouteSettings", in.DefaultRouteSettings != nil},
		member{"deploymentId", in.DeploymentId != nil},
		member{"description", in.Description != nil},
		member{"routeSettings", in.RouteSettings != nil},
		member{"stageVariables", in.StageVariables != nil},
	); err != nil {
		return nil, err
	}
	if in.StageName != defaultStage && !stageName.MatchString(in.StageName) {
		return nil, errBadRequest("stageName %q is not $default or 1 to 128 letters, digits, '-' and '_'", in.StageName)
	}
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	auto := in.AutoDeploy != nil && *in.AutoDeploy
	st, err := writeAPI(s, c, in.ApiId, func(a *API, now clock.Instant) (Stage, *awserr.Error) {
		if _, ok := a.stage(in.StageName); ok {
			return Stage{}, errConflict("the API already has a stage named %q", in.StageName)
		}
		a.Stages = append(slices.Clone(a.Stages), Stage{Name: in.StageName, AutoDeploy: auto, Created: now, Updated: now, Tags: maps.Clone(in.Tags)})
		if auto {
			a.deployTo(len(a.Stages)-1, now, s.deploymentID)
		}
		return a.Stages[len(a.Stages)-1], nil
	})
	if err != nil {
		return nil, err
	}
	out := &apigatewayv2api.CreateStageOutput{
		StageName: new(st.Name), AutoDeploy: new(st.AutoDeploy),
		CreatedDate: new(st.Created.Wire()), LastUpdatedDate: new(st.Updated.Wire()),
		Tags: tagsOut(st.Tags),
	}
	if st.Deployment != "" {
		out.DeploymentId = new(st.Deployment)
		out.LastDeploymentStatusMessage = new(st.LastDeploymentStatusMessage)
	}
	return out, nil
}
