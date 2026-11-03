package apigatewayv2

import (
	"context"
	"maps"
	"regexp"
	"slices"
	"strings"

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

// Stage variables: "names can have alphanumeric and underscore characters,
// and the values must match [A-Za-z0-9-._~:/?#&=,]+" (the stage API
// reference, apigateway-http.md 2.4); a stage holds 100 variables, keys up
// to 64 characters and values up to 512 (the HTTP API quotas page, 3.9).
var (
	stageVariableName  = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	stageVariableValue = regexp.MustCompile(`^[A-Za-z0-9\-._~:/?#&=,]{1,512}$`)
)

// Stage quotas (the HTTP API quotas page, apigateway-http.md 3.9): "Stages
// per API: 10", 100 stage variables, and the 3 KB access log template,
// read as 3072 bytes (inferred).
const (
	maxStages          = 10
	maxStageVariables  = 100
	maxAccessLogFormat = 3072
)

// accessLogRequestID is the variable an access log format "must include at
// least" (the stage API reference, apigateway-http.md 2.4).
const accessLogRequestID = "$context.requestId"

// checkCreateStage refuses, at step 7, a CreateStage understudy cannot
// store: see checkStageMembers; a stage name must be $default or match
// stageName, and a stage variable may not be empty.
func checkCreateStage(in *apigatewayv2api.CreateStageInput) *awserr.Error {
	if err := checkStageMembers("CreateStage", in.ClientCertificateId, in.AccessLogSettings); err != nil {
		return err
	}
	if in.StageName != defaultStage && !stageName.MatchString(in.StageName) {
		// AWS's text, which omits the hyphen the docs allow (APIGW2-01 step
		// 15 sent bad.name; whether a hyphen is accepted is UNVERIFIED).
		return errBadRequest("Stage name only allows a-zA-Z0-9_")
	}
	if len(in.StageVariables) > maxStageVariables {
		return errBadRequest("a stage holds at most %d stage variables, not %d", maxStageVariables, len(in.StageVariables))
	}
	if err := checkStageVariables(in.StageVariables, false); err != nil {
		return err
	}
	return checkTags(in.Tags)
}

// checkUpdateStage is checkCreateStage for the members an UpdateStage
// carries, where a stage variable sent empty is removed (apigateway-http.md
// 3.4); the count is checked against the merged variables.
func checkUpdateStage(in *apigatewayv2api.UpdateStageInput) *awserr.Error {
	if err := checkStageMembers("UpdateStage", in.ClientCertificateId, in.AccessLogSettings); err != nil {
		return err
	}
	return checkStageVariables(in.StageVariables, true)
}

// checkStageMembers checks the stage members create and update share:
// ClientCertificateId is WebSocket-only (apigateway-http.md 3.4), a valid
// member understudy does not emulate, so it answers NotImplemented (the
// unit's conflict row 4); an access log format must include
// $context.requestId and fit the 3 KB template quota (2.4, 3.9). The texts
// are inferred (APIGW2-01).
func checkStageMembers(op string, clientCertificate *string, accessLog *apigatewayv2api.AccessLogSettings) *awserr.Error {
	if clientCertificate != nil {
		return errNotImplemented(op, "clientCertificateId (WebSocket APIs only)", "Send the request without clientCertificateId.")
	}
	if accessLog == nil || accessLog.Format == nil {
		return nil
	}
	format := *accessLog.Format
	if !strings.Contains(format, accessLogRequestID) {
		return errBadRequest("accessLogSettings.format must include at least %s", accessLogRequestID)
	}
	if len(format) > maxAccessLogFormat {
		return errBadRequest("accessLogSettings.format is %d bytes; an access log format is at most %d", len(format), maxAccessLogFormat)
	}
	return nil
}

// checkStageVariables checks each variable's name and value; removing
// lets a value be empty, which UpdateStage reads as removing the variable.
func checkStageVariables(vars map[string]string, removing bool) *awserr.Error {
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		if !stageVariableName.MatchString(name) {
			return errBadRequest("stage variable name %q is not 1 to 64 letters, digits and '_'", name)
		}
		if v := vars[name]; (!removing || v != "") && !stageVariableValue.MatchString(v) {
			return errBadRequest("the value of stage variable %q is not 1 to 512 of the characters A-Z a-z 0-9 - . _ ~ : / ? # & = ,", name)
		}
	}
	return nil
}

// createStage creates a stage. With AutoDeploy it serves the API's routes
// before the call returns; a DeploymentId must name a deployment of the
// API, and on an AutoDeploy stage follows the front.answers switch's
// ExplicitOnAutoDeploy.
func (s *Service) createStage(_ context.Context, c *kernel.Call, in *apigatewayv2api.CreateStageInput) (*apigatewayv2api.CreateStageOutput, *awserr.Error) {
	auto := deref(in.AutoDeploy, false)
	st, err := writeAPI(s, c, in.ApiId, func(a *API, now clock.Instant) (Stage, *awserr.Error) {
		if _, ok := a.stage(in.StageName); ok {
			return Stage{}, errConflict("Stage already exists") // APIGW2-01 step 08
		}
		if len(a.Stages) >= maxStages {
			return Stage{}, errBadRequest("the API already has %d stages, the most an HTTP API may have", maxStages)
		}
		st := Stage{
			Name: in.StageName, Seq: a.nextChild(), AutoDeploy: auto, Description: clonePtr(in.Description),
			Created: now, Updated: now,
			Variables:            maps.Clone(in.StageVariables),
			AccessLog:            accessLogIn(in.AccessLogSettings),
			DefaultRouteSettings: routeSettingsIn(in.DefaultRouteSettings),
			RouteSettings:        routeSettingsMapIn(in.RouteSettings),
			Tags:                 maps.Clone(in.Tags),
		}
		if in.DeploymentId != nil {
			if err := a.explicitDeployment(st, *in.DeploymentId); err != nil {
				return Stage{}, err
			}
			st.Deployment = *in.DeploymentId
		}
		a.Stages = append(slices.Clone(a.Stages), st)
		if auto && in.DeploymentId == nil {
			a.deployTo(len(a.Stages)-1, now, s.deploymentID)
		}
		return a.Stages[len(a.Stages)-1], nil
	})
	if err != nil {
		return nil, err
	}
	out := apigatewayv2api.CreateStageOutput(st.output())
	return &out, nil
}

// updateStage replaces the members an UpdateStage carries and keeps the
// rest; stage variables merge, and one sent empty is removed
// (apigateway-http.md 3.4). Turning AutoDeploy on deploys at once.
func (s *Service) updateStage(_ context.Context, c *kernel.Call, in *apigatewayv2api.UpdateStageInput) (*apigatewayv2api.UpdateStageOutput, *awserr.Error) {
	st, err := writeAPI(s, c, in.ApiId, func(a *API, now clock.Instant) (Stage, *awserr.Error) {
		i, err := a.stageIndex(in.StageName)
		if err != nil {
			return Stage{}, err
		}
		st := a.Stages[i].clone()
		turnedOn := deref(in.AutoDeploy, st.AutoDeploy) && !st.AutoDeploy
		st.AutoDeploy = deref(in.AutoDeploy, st.AutoDeploy)
		if in.DeploymentId != nil {
			if err := a.explicitDeployment(st, *in.DeploymentId); err != nil {
				return Stage{}, err
			}
			st.Deployment = *in.DeploymentId
			st.LastDeploymentStatusMessage = ""
		}
		if err := st.update(in); err != nil {
			return Stage{}, err
		}
		st.Updated = now
		a.Stages = slices.Clone(a.Stages)
		a.Stages[i] = st
		if turnedOn && in.DeploymentId == nil {
			a.deployTo(i, now, s.deploymentID)
		}
		return a.Stages[i], nil
	})
	if err != nil {
		return nil, err
	}
	out := apigatewayv2api.UpdateStageOutput(st.output())
	return &out, nil
}

// explicitDeployment checks a DeploymentId given to the stage st, as it
// will be after the call: it must name a deployment of the API
// (NotFoundException, inferred), and on an AutoDeploy stage, whose
// DeploymentId "Can't be updated" (the stage API reference,
// apigateway-http.md 3.4), the front.answers switch's ExplicitOnAutoDeploy
// decides (UNVERIFIED, APIGW2-04).
func (a *API) explicitDeployment(st Stage, id string) *awserr.Error {
	if _, err := a.deploymentIndex(id); err != nil {
		return err
	}
	if st.AutoDeploy {
		return FrontAnswersSwitch().Value.Deployments.ExplicitOnAutoDeploy.answer(
			"stage %q has autoDeploy enabled, so its deploymentId can't be set; turn autoDeploy off to serve deployment %s", st.Name, id)
	}
	return nil
}

// update replaces the description and settings an UpdateStage carries and
// merges its stage variables into the stage's, which may then hold at
// most 100.
func (st *Stage) update(in *apigatewayv2api.UpdateStageInput) *awserr.Error {
	if in.Description != nil {
		st.Description = clonePtr(in.Description)
	}
	if in.AccessLogSettings != nil {
		st.AccessLog = accessLogIn(in.AccessLogSettings)
	}
	if in.DefaultRouteSettings != nil {
		st.DefaultRouteSettings = routeSettingsIn(in.DefaultRouteSettings)
	}
	if in.RouteSettings != nil {
		st.RouteSettings = routeSettingsMapIn(in.RouteSettings)
	}
	if in.StageVariables != nil {
		st.Variables = mergeVariables(st.Variables, in.StageVariables)
		if len(st.Variables) > maxStageVariables {
			return errBadRequest("stage %q would hold %d stage variables; a stage holds at most %d", st.Name, len(st.Variables), maxStageVariables)
		}
	}
	return nil
}

// mergeVariables is vars with update applied: a variable sent empty is
// removed, any other set (apigateway-http.md 3.4).
func mergeVariables(vars, update map[string]string) map[string]string {
	out := maps.Clone(vars)
	if out == nil {
		out = map[string]string{}
	}
	for name, v := range update {
		if v == "" {
			delete(out, name)
			continue
		}
		out[name] = v
	}
	return out
}

// deleteStage removes a stage; the deployments it served stay.
func (s *Service) deleteStage(_ context.Context, c *kernel.Call, in *apigatewayv2api.DeleteStageInput) (*apigatewayv2api.DeleteStageOutput, *awserr.Error) {
	if _, err := writeAPI(s, c, in.ApiId, func(a *API, _ clock.Instant) (struct{}, *awserr.Error) {
		i, err := a.stageIndex(in.StageName)
		if err != nil {
			return struct{}{}, err
		}
		a.Stages = slices.Delete(slices.Clone(a.Stages), i, i+1)
		return struct{}{}, nil
	}); err != nil {
		return nil, err
	}
	return &apigatewayv2api.DeleteStageOutput{}, nil
}

// deleteAccessLogSettings removes a stage's access log settings (2.2).
func (s *Service) deleteAccessLogSettings(_ context.Context, c *kernel.Call, in *apigatewayv2api.DeleteAccessLogSettingsInput) (*apigatewayv2api.DeleteAccessLogSettingsOutput, *awserr.Error) {
	if err := s.changeStage(c, in.ApiId, in.StageName, func(st *Stage) *awserr.Error {
		st.AccessLog = nil
		return nil
	}); err != nil {
		return nil, err
	}
	return &apigatewayv2api.DeleteAccessLogSettingsOutput{}, nil
}

// deleteRouteSettings removes the settings of one route key from a stage
// (2.2); a key the stage has no settings for is NotFoundException
// (inferred).
func (s *Service) deleteRouteSettings(_ context.Context, c *kernel.Call, in *apigatewayv2api.DeleteRouteSettingsInput) (*apigatewayv2api.DeleteRouteSettingsOutput, *awserr.Error) {
	if err := s.changeStage(c, in.ApiId, in.StageName, func(st *Stage) *awserr.Error {
		if _, ok := st.RouteSettings[in.RouteKey]; !ok {
			return errNotFound("stage %q has no route settings for route key %q", st.Name, in.RouteKey)
		}
		delete(st.RouteSettings, in.RouteKey)
		return nil
	}); err != nil {
		return nil, err
	}
	return &apigatewayv2api.DeleteRouteSettingsOutput{}, nil
}

// changeStage runs fn on a copy of a stage of the API, with maps of its
// own, and stores the copy.
func (s *Service) changeStage(c *kernel.Call, apiID, name string, fn func(st *Stage) *awserr.Error) *awserr.Error {
	_, err := writeAPI(s, c, apiID, func(a *API, now clock.Instant) (struct{}, *awserr.Error) {
		i, err := a.stageIndex(name)
		if err != nil {
			return struct{}{}, err
		}
		st := a.Stages[i].clone()
		if err := fn(&st); err != nil {
			return struct{}{}, err
		}
		st.Updated = now
		a.Stages = slices.Clone(a.Stages)
		a.Stages[i] = st
		return struct{}{}, nil
	})
	return err
}

// getStage answers one stage of an API.
func (s *Service) getStage(_ context.Context, c *kernel.Call, in *apigatewayv2api.GetStageInput) (*apigatewayv2api.GetStageOutput, *awserr.Error) {
	st, err := readAPI(s, c.Scope(), in.ApiId, func(a *API) (Stage, *awserr.Error) {
		i, err := a.stageIndex(in.StageName)
		if err != nil {
			return Stage{}, err
		}
		return a.Stages[i].clone(), nil
	})
	if err != nil {
		return nil, err
	}
	return new(st.output()), nil
}

// getStages lists an API's stages in creation order (inferred, APIGW2-03),
// a page at a time.
func (s *Service) getStages(_ context.Context, c *kernel.Call, in *apigatewayv2api.GetStagesInput) (*apigatewayv2api.GetStagesOutput, *awserr.Error) {
	stages, err := readAPI(s, c.Scope(), in.ApiId, func(a *API) ([]Stage, *awserr.Error) { return cloned(a.Stages), nil })
	if err != nil {
		return nil, err
	}
	items, next, err := page(stages, func(st Stage) uint64 { return st.Seq }, in.MaxResults, in.NextToken)
	if err != nil {
		return nil, err
	}
	out := &apigatewayv2api.GetStagesOutput{Items: make([]apigatewayv2api.Stage, 0, len(items)), NextToken: next}
	for i := range items {
		out.Items = append(out.Items, items[i].item())
	}
	return out, nil
}

// stageIndex is the index of the stage name names; a name the API does
// not have is NotFoundException.
func (a *API) stageIndex(name string) (int, *awserr.Error) {
	i := slices.IndexFunc(a.Stages, func(st Stage) bool { return st.Name == name })
	if i < 0 {
		return 0, errStageNotFound()
	}
	return i, nil
}

// output is the stage as GetStage answers it (apigateway-http.md 3.4, 9.4):
// AutoDeploy, DefaultRouteSettings (DetailedMetricsEnabled false when not
// sent), RouteSettings, StageVariables and Tags always, the latter three
// {} when empty, which Terraform's empty plan needs; DeploymentId once
// there is one; LastDeploymentStatusMessage after an automatic deployment;
// AccessLogSettings and Description when set; no ApiGatewayManaged, which
// only quick create sets.
func (st *Stage) output() apigatewayv2api.GetStageOutput {
	out := apigatewayv2api.GetStageOutput{
		AccessLogSettings:    st.AccessLog.wire(),
		AutoDeploy:           new(st.AutoDeploy),
		CreatedDate:          new(st.Created.Wire()),
		DefaultRouteSettings: defaultRouteSettingsOut(st.DefaultRouteSettings),
		Description:          clonePtr(st.Description),
		LastUpdatedDate:      new(st.Updated.Wire()),
		RouteSettings:        routeSettingsMapOut(st.RouteSettings),
		StageName:            new(st.Name),
		StageVariables:       tagsOut(st.Variables),
		Tags:                 tagsOut(st.Tags),
	}
	if st.Deployment != "" {
		out.DeploymentId = new(st.Deployment)
	}
	if st.LastDeploymentStatusMessage != "" {
		out.LastDeploymentStatusMessage = new(st.LastDeploymentStatusMessage)
	}
	return out
}

// item is the stage as GetStages lists it, the members of output.
func (st *Stage) item() apigatewayv2api.Stage {
	o := st.output()
	return apigatewayv2api.Stage{
		AccessLogSettings: o.AccessLogSettings, AutoDeploy: o.AutoDeploy, CreatedDate: o.CreatedDate,
		DefaultRouteSettings: o.DefaultRouteSettings, DeploymentId: o.DeploymentId, Description: o.Description,
		LastDeploymentStatusMessage: o.LastDeploymentStatusMessage, LastUpdatedDate: o.LastUpdatedDate,
		RouteSettings: o.RouteSettings, StageName: st.Name, StageVariables: o.StageVariables, Tags: o.Tags,
	}
}

// accessLogIn is a request's access log settings as stored, a copy.
func accessLogIn(s *apigatewayv2api.AccessLogSettings) *AccessLogSettings {
	if s == nil {
		return nil
	}
	return &AccessLogSettings{DestinationARN: clonePtr(s.DestinationArn), Format: clonePtr(s.Format)}
}

// wire is the stored access log settings as an output carries them.
func (s *AccessLogSettings) wire() *apigatewayv2api.AccessLogSettings {
	if s == nil {
		return nil
	}
	return &apigatewayv2api.AccessLogSettings{DestinationArn: clonePtr(s.DestinationARN), Format: clonePtr(s.Format)}
}

// routeSettingsIn is a request's route settings as stored, a copy.
func routeSettingsIn(r *apigatewayv2api.RouteSettings) *RouteSettings {
	if r == nil {
		return nil
	}
	return &RouteSettings{
		DataTraceEnabled: clonePtr(r.DataTraceEnabled), DetailedMetricsEnabled: clonePtr(r.DetailedMetricsEnabled),
		LoggingLevel: clonePtr(r.LoggingLevel), ThrottlingBurstLimit: clonePtr(r.ThrottlingBurstLimit),
		ThrottlingRateLimit: clonePtr(r.ThrottlingRateLimit),
	}
}

// wire is the stored route settings as an output carries them.
func (r *RouteSettings) wire() apigatewayv2api.RouteSettings {
	return apigatewayv2api.RouteSettings{
		DataTraceEnabled: clonePtr(r.DataTraceEnabled), DetailedMetricsEnabled: clonePtr(r.DetailedMetricsEnabled),
		LoggingLevel: clonePtr(r.LoggingLevel), ThrottlingBurstLimit: clonePtr(r.ThrottlingBurstLimit),
		ThrottlingRateLimit: clonePtr(r.ThrottlingRateLimit),
	}
}

// defaultRouteSettingsOut is a stage's DefaultRouteSettings as an output
// carries it: always present, with DetailedMetricsEnabled false when it was
// not sent (the create-stage CLI example, apigateway-http.md 3.4).
func defaultRouteSettingsOut(r *RouteSettings) *apigatewayv2api.RouteSettings {
	out := apigatewayv2api.RouteSettings{}
	if r != nil {
		out = r.wire()
	}
	if out.DetailedMetricsEnabled == nil {
		out.DetailedMetricsEnabled = new(false)
	}
	return &out
}

// routeSettingsMapIn is a request's per-route settings as stored, a copy;
// nil stays nil.
func routeSettingsMapIn(m map[string]apigatewayv2api.RouteSettings) map[string]RouteSettings {
	if m == nil {
		return nil
	}
	out := make(map[string]RouteSettings, len(m))
	for key, r := range m {
		out[key] = *routeSettingsIn(&r)
	}
	return out
}

// routeSettingsMapOut is a stage's per-route settings as an output carries
// them: a copy, {} when there are none.
func routeSettingsMapOut(m map[string]RouteSettings) map[string]apigatewayv2api.RouteSettings {
	out := make(map[string]apigatewayv2api.RouteSettings, len(m))
	for key, r := range m {
		out[key] = r.wire()
	}
	return out
}
