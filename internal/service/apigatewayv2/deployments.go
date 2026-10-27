package apigatewayv2

import (
	"context"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// createDeployment snapshots the API's routes and integrations
// (apigateway-http.md finding 7, 3.5). With StageName the stage serves the
// new deployment from now on; an AutoDeploy stage follows the
// front.answers switch's ExplicitOnAutoDeploy, and an API with no routes
// its EmptyAPI (both UNVERIFIED, APIGW2-04).
func (s *Service) createDeployment(_ context.Context, c *kernel.Call, in *apigatewayv2api.CreateDeploymentInput) (*apigatewayv2api.CreateDeploymentOutput, *awserr.Error) {
	rules := FrontAnswersSwitch().Value.Deployments
	d, err := writeAPI(s, c, in.ApiId, func(a *API, now clock.Instant) (Deployment, *awserr.Error) {
		stage := -1
		if in.StageName != nil {
			i, err := a.stageIndex(*in.StageName)
			if err != nil {
				return Deployment{}, err
			}
			if a.Stages[i].AutoDeploy {
				if err := rules.ExplicitOnAutoDeploy.answer(
					"stage %q has autoDeploy enabled and deploys every change itself; create the deployment without stageName, or turn the stage's autoDeploy off", *in.StageName); err != nil {
					return Deployment{}, err
				}
			}
			stage = i
		}
		if len(a.Routes) == 0 {
			if err := rules.EmptyAPI.answer("API %s has no routes to deploy; create a route first", a.ID); err != nil {
				return Deployment{}, err
			}
		}
		d := a.deploy(now, s.deploymentID, false, in.Description)
		if stage >= 0 {
			a.serve(stage, d.ID, now)
		}
		return d, nil
	})
	if err != nil {
		return nil, err
	}
	out := apigatewayv2api.CreateDeploymentOutput(d.output())
	return &out, nil
}

// updateDeployment replaces a deployment's description, the one member it
// has that can change (apigateway-http.md 3.5).
func (s *Service) updateDeployment(_ context.Context, c *kernel.Call, in *apigatewayv2api.UpdateDeploymentInput) (*apigatewayv2api.UpdateDeploymentOutput, *awserr.Error) {
	d, err := writeAPI(s, c, in.ApiId, func(a *API, _ clock.Instant) (Deployment, *awserr.Error) {
		i, err := a.deploymentIndex(in.DeploymentId)
		if err != nil {
			return Deployment{}, err
		}
		if in.Description != nil {
			a.Deployments = slices.Clone(a.Deployments)
			a.Deployments[i].Description = clonePtr(in.Description)
		}
		return a.Deployments[i], nil
	})
	if err != nil {
		return nil, err
	}
	out := apigatewayv2api.UpdateDeploymentOutput(d.output())
	return &out, nil
}

// deleteDeployment removes a deployment. One a stage serves follows the
// front.answers switch's DeleteInUse (the REST rule, apigateway-rest.md
// 2.4, whose recorded text the refusal reuses; UNVERIFIED for HTTP APIs,
// APIGW2-04); where it is allowed, the stages lose their deployment and
// answer NoDeployment.
func (s *Service) deleteDeployment(_ context.Context, c *kernel.Call, in *apigatewayv2api.DeleteDeploymentInput) (*apigatewayv2api.DeleteDeploymentOutput, *awserr.Error) {
	if _, err := writeAPI(s, c, in.ApiId, func(a *API, now clock.Instant) (struct{}, *awserr.Error) {
		i, err := a.deploymentIndex(in.DeploymentId)
		if err != nil {
			return struct{}{}, err
		}
		var users []string
		for j := range a.Stages {
			if a.Stages[j].Deployment == in.DeploymentId {
				users = append(users, a.Stages[j].Name)
			}
		}
		if len(users) > 0 {
			if err := FrontAnswersSwitch().Value.Deployments.DeleteInUse.answer(
				"Active stages pointing to this deployment must be moved or deleted (stages %s serve deployment %s)", strings.Join(users, ", "), in.DeploymentId); err != nil {
				return struct{}{}, err
			}
			for j := range a.Stages {
				if a.Stages[j].Deployment == in.DeploymentId {
					a.serve(j, "", now)
				}
			}
		}
		a.Deployments = slices.Delete(slices.Clone(a.Deployments), i, i+1)
		return struct{}{}, nil
	}); err != nil {
		return nil, err
	}
	return &apigatewayv2api.DeleteDeploymentOutput{}, nil
}

// getDeployment answers one deployment of an API.
func (s *Service) getDeployment(_ context.Context, c *kernel.Call, in *apigatewayv2api.GetDeploymentInput) (*apigatewayv2api.GetDeploymentOutput, *awserr.Error) {
	d, err := readAPI(s, c.Scope(), in.ApiId, func(a *API) (Deployment, *awserr.Error) {
		i, err := a.deploymentIndex(in.DeploymentId)
		if err != nil {
			return Deployment{}, err
		}
		return a.Deployments[i], nil
	})
	if err != nil {
		return nil, err
	}
	return new(d.output()), nil
}

// getDeployments lists an API's deployments in creation order (inferred,
// APIGW2-03), automatic ones included, a page at a time.
func (s *Service) getDeployments(_ context.Context, c *kernel.Call, in *apigatewayv2api.GetDeploymentsInput) (*apigatewayv2api.GetDeploymentsOutput, *awserr.Error) {
	deployments, err := readAPI(s, c.Scope(), in.ApiId, func(a *API) ([]Deployment, *awserr.Error) {
		return slices.Clone(a.Deployments), nil
	})
	if err != nil {
		return nil, err
	}
	items, next, err := page(deployments, func(d Deployment) uint64 { return d.Seq }, in.MaxResults, in.NextToken)
	if err != nil {
		return nil, err
	}
	out := &apigatewayv2api.GetDeploymentsOutput{Items: make([]apigatewayv2api.Deployment, 0, len(items)), NextToken: next}
	for i := range items {
		out.Items = append(out.Items, apigatewayv2api.Deployment(items[i].output()))
	}
	return out, nil
}

// answer is how a call the rule governs is met: nil when the rule allows
// it; BadRequestException with the text format describes when it refuses
// it (the text is inferred, APIGW2-01).
func (r DeploymentRule) answer(format string, args ...any) *awserr.Error {
	if r != Refused {
		return nil
	}
	return errBadRequest(format, args...)
}

// deploymentIndex is the index of the deployment id names; an id the API
// does not hold is NotFoundException.
func (a *API) deploymentIndex(id string) (int, *awserr.Error) {
	i := slices.IndexFunc(a.Deployments, func(d Deployment) bool { return d.ID == id })
	if i < 0 {
		return 0, errNotFound("API %s has no deployment %s", a.ID, id)
	}
	return i, nil
}

// output is the deployment as GetDeployment answers it: DEPLOYED at once,
// since the snapshot is taken inside the call (apigateway-http.md 3.5;
// Terraform waits for DEPLOYED, 9.5), AutoDeployed always, Description
// when set.
func (d *Deployment) output() apigatewayv2api.GetDeploymentOutput {
	return apigatewayv2api.GetDeploymentOutput{
		AutoDeployed:     new(d.AutoDeployed),
		CreatedDate:      new(d.Created.Wire()),
		DeploymentId:     new(d.ID),
		DeploymentStatus: new(apigatewayv2api.DeploymentStatusDeployed),
		Description:      clonePtr(d.Description),
	}
}
