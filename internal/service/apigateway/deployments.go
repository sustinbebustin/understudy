package apigateway

import (
	"context"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/apigateway/patch"
)

// createDeployment snapshots the API's resources, methods and integrations
// and, with stageName, creates that stage on the snapshot or repoints it
// (apigateway-rest.md 3.6, 12.5; both paths recorded). stageDescription,
// cacheClusterEnabled, cacheClusterSize, tracingEnabled and variables are
// the stage's: they set those members of the stage stageName names, and
// without stageName there is no stage for them (the API Reference
// describes each as the stage's, API_CreateDeployment). On a stage that
// already exists the variables are merged into its own (inferred).
func (s *Service) createDeployment(_ context.Context, c *kernel.Call, in *apigatewayapi.CreateDeploymentInput) (*apigatewayapi.Deployment, *awserr.Error) {
	if err := refuseUnimplemented("CreateDeployment", member{"canarySettings", in.CanarySettings != nil}); err != nil {
		return nil, err
	}
	if in.StageName != nil {
		if err := checkStageName(*in.StageName); err != nil {
			return nil, err
		}
	}
	if err := checkStageVariables(in.Variables); err != nil {
		return nil, err
	}
	scope := c.Scope()
	d, err := kernel.Write(c, s.cells, scope, func(st *State, now clock.Instant, _ *kernel.Tx) (Deployment, *awserr.Error) {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return Deployment{}, errAPINotFound(scope, in.RestApiId)
		}
		if err := a.deployable(); err != nil {
			return Deployment{}, err
		}
		id := string(s.ids.APIGatewayDeploymentID())
		for a.hasChild(id) {
			id = string(s.ids.APIGatewayDeploymentID())
		}
		d := Deployment{
			ID: id, Seq: a.NextDeploymentSeq, Description: clonePtr(in.Description), Created: now, Resources: cloneResources(a.Resources),
			BinaryMediaTypes: slices.Clone(a.BinaryMediaTypes),
		}
		a.NextDeploymentSeq++
		a.Deployments = append(slices.Clone(a.Deployments), d)
		if in.StageName != nil {
			if err := a.deployToStage(in, id, now); err != nil {
				return Deployment{}, err
			}
		}
		st.APIs[a.ID] = a
		return d, nil
	})
	if err != nil {
		return nil, err
	}
	return new(d.output(false)), nil
}

// deployToStage points the stage in.StageName names at deployment id,
// creating it when the API has no such stage, and sets the stage members
// the request carries.
func (a *RestAPI) deployToStage(in *apigatewayapi.CreateDeploymentInput, id string, now clock.Instant) *awserr.Error {
	stage, i, exists := a.stage(*in.StageName)
	if exists {
		stage = stage.clone()
	} else {
		if err := a.roomForStage(); err != nil {
			return err
		}
		stage = Stage{Name: *in.StageName, Created: now}
	}
	stage.Deployment, stage.Updated = id, now
	if in.StageDescription != nil {
		stage.Description = clonePtr(in.StageDescription)
	}
	stage.CacheClusterEnabled = deref(in.CacheClusterEnabled, stage.CacheClusterEnabled)
	if in.CacheClusterSize != nil {
		stage.CacheClusterSize = clonePtr(in.CacheClusterSize)
	}
	stage.TracingEnabled = deref(in.TracingEnabled, stage.TracingEnabled)
	if len(in.Variables) > 0 {
		vars := maps.Clone(stage.Variables)
		if vars == nil {
			vars = map[string]string{}
		}
		maps.Copy(vars, in.Variables)
		if err := checkStageVariables(vars); err != nil {
			return err
		}
		stage.Variables = vars
	}
	a.Stages = slices.Clone(a.Stages)
	if exists {
		a.Stages[i] = stage
	} else {
		a.Stages = append(a.Stages, stage)
	}
	return nil
}

// deployable refuses a deployment of an API with no methods, or with a
// method that has no integration (APIGW1-05, recorded 2026-11-03).
func (a *RestAPI) deployable() *awserr.Error {
	methods := 0
	for _, r := range a.Resources {
		for _, m := range r.Methods {
			if m.Integration == nil {
				return errBadRequest("No integration defined for method")
			}
			methods++
		}
	}
	if methods == 0 {
		return errBadRequest("The REST API doesn't contain any methods")
	}
	return nil
}

// output is the deployment as the deployment operations answer it: id,
// description when set, createdDate (recorded, apigateway-rest.md 3.6),
// and apiSummary when asked for.
func (d *Deployment) output(summary bool) apigatewayapi.Deployment {
	out := apigatewayapi.Deployment{Id: new(d.ID), Description: clonePtr(d.Description), CreatedDate: new(d.Created.Wire())}
	if summary {
		// apiSummary maps each resource path to its methods' authorization
		// type and apiKeyRequired ([S] Deployment$apiSummary, MethodSnapshot).
		out.ApiSummary = map[string]map[string]apigatewayapi.MethodSnapshot{}
		for _, r := range d.Resources {
			if len(r.Methods) == 0 {
				continue
			}
			methods := make(map[string]apigatewayapi.MethodSnapshot, len(r.Methods))
			for verb, m := range r.Methods {
				methods[verb] = apigatewayapi.MethodSnapshot{AuthorizationType: new(m.Authorization), ApiKeyRequired: m.APIKeyRequired}
			}
			out.ApiSummary[r.Path] = methods
		}
	}
	return out
}

// getDeployment answers one deployment; embed takes "apisummary" ([S]
// GetDeploymentRequest$embed), anything else is refused (text inferred).
func (s *Service) getDeployment(_ context.Context, c *kernel.Call, in *apigatewayapi.GetDeploymentInput) (*apigatewayapi.Deployment, *awserr.Error) {
	summary := false
	for _, e := range in.Embed {
		if e != "apisummary" {
			return nil, errBadRequest("Invalid embed value %q: GetDeployment embeds only apisummary", e)
		}
		summary = true
	}
	scope := c.Scope()
	type found struct {
		out apigatewayapi.Deployment
		err *awserr.Error
	}
	got := readState(s, scope, func(st *State) found {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return found{err: errAPINotFound(scope, in.RestApiId)}
		}
		d, ok := a.deployment(in.DeploymentId)
		if !ok {
			return found{err: errDeploymentNotFound()}
		}
		return found{out: d.output(summary)}
	})
	if got.err != nil {
		return nil, got.err
	}
	return &got.out, nil
}

// getDeployments pages the API's deployments in creation order (inferred).
func (s *Service) getDeployments(_ context.Context, c *kernel.Call, in *apigatewayapi.GetDeploymentsInput) (*apigatewayapi.GetDeploymentsOutput, *awserr.Error) {
	scope := c.Scope()
	type found struct {
		out apigatewayapi.GetDeploymentsOutput
		err *awserr.Error
	}
	got := readState(s, scope, func(st *State) found {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return found{err: errAPINotFound(scope, in.RestApiId)}
		}
		items, next, err := page(a.Deployments, func(d Deployment) uint64 { return d.Seq }, in.Limit, in.Position)
		if err != nil {
			return found{err: err}
		}
		out := apigatewayapi.GetDeploymentsOutput{Items: make([]apigatewayapi.Deployment, 0, len(items)), Position: next}
		for i := range items {
			out.Items = append(out.Items, items[i].output(false))
		}
		return found{out: out}
	})
	if got.err != nil {
		return nil, got.err
	}
	return &got.out, nil
}

// updateDeployment applies UpdateDeployment's patch operations: replace
// /description (apigateway-rest.md 3.8). The snapshot itself never changes.
func (s *Service) updateDeployment(_ context.Context, c *kernel.Call, in *apigatewayapi.UpdateDeploymentInput) (*apigatewayapi.Deployment, *awserr.Error) {
	scope := c.Scope()
	ops := patch.FromModel(in.PatchOperations)
	d, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (Deployment, *awserr.Error) {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return Deployment{}, errAPINotFound(scope, in.RestApiId)
		}
		i := slices.IndexFunc(a.Deployments, func(d Deployment) bool { return d.ID == in.DeploymentId })
		if i < 0 {
			return Deployment{}, errDeploymentNotFound()
		}
		d, err := deploymentPatch().Apply(a.Deployments[i], ops)
		if err != nil {
			return Deployment{}, err
		}
		a.Deployments = slices.Clone(a.Deployments)
		a.Deployments[i] = d
		st.APIs[a.ID] = a
		return d, nil
	})
	if err != nil {
		return nil, err
	}
	return new(d.output(false)), nil
}

// deploymentPatch is UpdateDeployment's path table: replace /description
// only (apigateway-rest.md 3.8); the refusal texts are inferred.
func deploymentPatch() patch.Table[Deployment] {
	replace := apigatewayapi.OpReplace
	return patch.Table[Deployment]{
		Rules: []patch.Rule[Deployment]{
			{Path: "/description", Ops: []apigatewayapi.Op{replace}, Apply: func(d *Deployment, op patch.Operation, _ []string) *awserr.Error {
				d.Description = clonePtr(op.Value)
				return nil
			}},
		},
		UnknownPath: patch.InvalidPath,
		OpText:      patch.OpsOneOf(replace),
		// The table writes only the description; the snapshot is shared,
		// never written.
		Clone: func(d Deployment) Deployment {
			d.Description = clonePtr(d.Description)
			return d
		},
	}
}

// deleteDeployment deletes a deployment no stage serves; one a stage
// points at is refused (recorded, apigateway-rest.md 2.4, 3.6).
func (s *Service) deleteDeployment(_ context.Context, c *kernel.Call, in *apigatewayapi.DeleteDeploymentInput) (*apigatewayapi.DeleteDeploymentOutput, *awserr.Error) {
	scope := c.Scope()
	_, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return struct{}{}, errAPINotFound(scope, in.RestApiId)
		}
		if _, ok := a.deployment(in.DeploymentId); !ok {
			return struct{}{}, errDeploymentNotFound()
		}
		if slices.ContainsFunc(a.Stages, func(st Stage) bool { return st.Deployment == in.DeploymentId }) {
			return struct{}{}, errBadRequest("Active stages pointing to this deployment must be moved or deleted")
		}
		a.Deployments = slices.DeleteFunc(slices.Clone(a.Deployments), func(d Deployment) bool { return d.ID == in.DeploymentId })
		st.APIs[a.ID] = a
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &apigatewayapi.DeleteDeploymentOutput{}, nil
}
