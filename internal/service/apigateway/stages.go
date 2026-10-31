package apigateway

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/apigateway/patch"
)

// Stage limits (documented, apigateway-rest.md 2.6, 3.7): 10 stages per
// API, 100 variables per stage, names of up to 64 letters, digits and
// underscores, values of up to 512 characters of [A-Za-z0-9-._~:/?#&=,].
// The texts for breaking them are inferred.
const (
	maxStages         = 10
	maxStageVariables = 100
)

var (
	// stageNamePattern is a stage name (documented, apigateway-rest.md 3.7).
	stageNamePattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	stageVariableName  = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	stageVariableValue = regexp.MustCompile(`^[A-Za-z0-9\-._~:/?#&=,]{1,512}$`)
)

// checkStageName refuses a name outside the documented pattern; the text
// is inferred.
func checkStageName(name string) *awserr.Error {
	if !stageNamePattern.MatchString(name) {
		return errBadRequest("Stage name only allows a-zA-Z0-9_- and must be 1 to 128 characters long")
	}
	return nil
}

// checkStageVariables refuses variables over the documented limits.
func checkStageVariables(vars map[string]string) *awserr.Error {
	if len(vars) > maxStageVariables {
		return apigatewayapi.ErrLimitExceededException(fmt.Sprintf("a stage holds at most %d variables, not %d", maxStageVariables, len(vars)))
	}
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		if err := checkStageVariable(name, vars[name]); err != nil {
			return err
		}
	}
	return nil
}

// checkStageVariable refuses a variable name or value outside the
// documented patterns.
func checkStageVariable(name, value string) *awserr.Error {
	if !stageVariableName.MatchString(name) {
		return errBadRequest("stage variable name %q is not 1 to 64 letters, digits and '_'", name)
	}
	if !stageVariableValue.MatchString(value) {
		return errBadRequest("the value of stage variable %q is not 1 to 512 of the characters A-Z a-z 0-9 - . _ ~ : / ? # & = ,", name)
	}
	return nil
}

// roomForStage refuses an eleventh stage (documented quota, inferred
// text).
func (a *RestAPI) roomForStage() *awserr.Error {
	if len(a.Stages) >= maxStages {
		return apigatewayapi.ErrLimitExceededException(fmt.Sprintf("REST API %s already has %d stages, the most an API holds", a.ID, maxStages))
	}
	return nil
}

// errNoDeployment answers a deployment id the API does not hold where a
// stage names one (recorded for CreateStage with canary settings,
// apigateway-rest.md 3.7; held for the other paths).
func errNoDeployment() *awserr.Error { return errBadRequest("Deployment id does not exist") }

// output is the stage as the stage operations answer it, with the
// recorded defaults: cacheClusterStatus NOT_AVAILABLE, methodSettings {}
// when none, and no variables or tags key when there are none
// (apigateway-rest.md 2.3). No cache cluster runs, so the status stays
// NOT_AVAILABLE whatever cacheClusterEnabled says.
func (st *Stage) output() apigatewayapi.Stage {
	settings := make(map[string]apigatewayapi.MethodSetting, len(st.MethodSettings))
	for key, m := range st.MethodSettings {
		settings[key] = m.output()
	}
	var vars map[string]string
	if len(st.Variables) > 0 {
		vars = maps.Clone(st.Variables)
	}
	var accessLog *apigatewayapi.AccessLogSettings
	if st.AccessLog != nil {
		accessLog = &apigatewayapi.AccessLogSettings{Format: clonePtr(st.AccessLog.Format), DestinationArn: clonePtr(st.AccessLog.DestinationARN)}
	}
	return apigatewayapi.Stage{
		DeploymentId: new(st.Deployment), StageName: new(st.Name), Description: clonePtr(st.Description),
		ClientCertificateId: clonePtr(st.ClientCertificateID),
		CacheClusterEnabled: st.CacheClusterEnabled, CacheClusterSize: clonePtr(st.CacheClusterSize),
		CacheClusterStatus: new(apigatewayapi.CacheClusterStatusNotAvailable),
		MethodSettings:     settings, Variables: vars, AccessLogSettings: accessLog, TracingEnabled: st.TracingEnabled,
		Tags:        tagsOrNil(st.Tags),
		CreatedDate: new(st.Created.Wire()), LastUpdatedDate: new(st.Updated.Wire()),
	}
}

// output is the method setting as a stage carries it.
func (m *MethodSetting) output() apigatewayapi.MethodSetting {
	return apigatewayapi.MethodSetting{
		MetricsEnabled: m.MetricsEnabled, LoggingLevel: clonePtr(m.LoggingLevel), DataTraceEnabled: m.DataTraceEnabled,
		ThrottlingBurstLimit: m.ThrottlingBurstLimit, ThrottlingRateLimit: m.ThrottlingRateLimit,
		CachingEnabled: m.CachingEnabled, CacheTtlInSeconds: m.CacheTTLInSeconds, CacheDataEncrypted: m.CacheDataEncrypted,
		RequireAuthorizationForCacheControl:    m.RequireAuthorizationForCacheControl,
		UnauthorizedCacheControlHeaderStrategy: new(m.UnauthorizedCacheControlHeaderStrategy),
	}
}

// createStage creates a stage serving one of the API's deployments
// (apigateway-rest.md 3.7). canarySettings and documentationVersion
// answer NotImplemented: canaries are not served, and no documentation
// version can exist while documentation is unbound.
func (s *Service) createStage(_ context.Context, c *kernel.Call, in *apigatewayapi.CreateStageInput) (*apigatewayapi.Stage, *awserr.Error) {
	if err := refuseUnimplemented("CreateStage",
		member{"canarySettings", in.CanarySettings != nil},
		member{"documentationVersion", in.DocumentationVersion != nil},
	); err != nil {
		return nil, err
	}
	if err := checkStageName(in.StageName); err != nil {
		return nil, err
	}
	if err := checkStageVariables(in.Variables); err != nil {
		return nil, err
	}
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	scope := c.Scope()
	stage, err := kernel.Write(c, s.cells, scope, func(st *State, now clock.Instant, _ *kernel.Tx) (Stage, *awserr.Error) {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return Stage{}, errAPINotFound(scope, in.RestApiId)
		}
		if _, ok := a.deployment(in.DeploymentId); !ok {
			return Stage{}, errNoDeployment()
		}
		if _, _, exists := a.stage(in.StageName); exists {
			// Identity documented ([S] CreateStage errors); text inferred
			// (APIGW1-06).
			return Stage{}, apigatewayapi.ErrConflictException("Stage already exists")
		}
		if err := a.roomForStage(); err != nil {
			return Stage{}, err
		}
		stage := Stage{
			Name: in.StageName, Deployment: in.DeploymentId, Description: clonePtr(in.Description),
			CacheClusterEnabled: in.CacheClusterEnabled, CacheClusterSize: clonePtr(in.CacheClusterSize), TracingEnabled: in.TracingEnabled,
			Variables: maps.Clone(in.Variables), Tags: maps.Clone(in.Tags), Created: now, Updated: now,
		}
		a.Stages = append(slices.Clone(a.Stages), stage)
		st.APIs[a.ID] = a
		return stage, nil
	})
	if err != nil {
		return nil, err
	}
	return new(stage.output()), nil
}

// getStage answers one stage.
func (s *Service) getStage(_ context.Context, c *kernel.Call, in *apigatewayapi.GetStageInput) (*apigatewayapi.Stage, *awserr.Error) {
	scope := c.Scope()
	type found struct {
		out apigatewayapi.Stage
		err *awserr.Error
	}
	got := readState(s, scope, func(st *State) found {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return found{err: errAPINotFound(scope, in.RestApiId)}
		}
		stage, _, ok := a.stage(in.StageName)
		if !ok {
			return found{err: errStageNotFound()}
		}
		return found{out: stage.output()}
	})
	if got.err != nil {
		return nil, got.err
	}
	return &got.out, nil
}

// getStages answers the API's stages in creation order (inferred), only
// those serving deploymentId when it is given ([S] GetStagesRequest; not
// paginated, apigateway-rest.md 2.5).
func (s *Service) getStages(_ context.Context, c *kernel.Call, in *apigatewayapi.GetStagesInput) (*apigatewayapi.GetStagesOutput, *awserr.Error) {
	scope := c.Scope()
	type found struct {
		out apigatewayapi.GetStagesOutput
		err *awserr.Error
	}
	got := readState(s, scope, func(st *State) found {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return found{err: errAPINotFound(scope, in.RestApiId)}
		}
		out := apigatewayapi.GetStagesOutput{Item: []apigatewayapi.Stage{}}
		for i := range a.Stages {
			if in.DeploymentId == nil || a.Stages[i].Deployment == *in.DeploymentId {
				out.Item = append(out.Item, a.Stages[i].output())
			}
		}
		return found{out: out}
	})
	if got.err != nil {
		return nil, got.err
	}
	return &got.out, nil
}

// updateStage applies UpdateStage's patch operations, all or none
// (apigateway-rest.md 3.7, 3.8).
func (s *Service) updateStage(_ context.Context, c *kernel.Call, in *apigatewayapi.UpdateStageInput) (*apigatewayapi.Stage, *awserr.Error) {
	scope := c.Scope()
	ops := patch.FromModel(in.PatchOperations)
	stage, err := kernel.Write(c, s.cells, scope, func(st *State, now clock.Instant, _ *kernel.Tx) (Stage, *awserr.Error) {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return Stage{}, errAPINotFound(scope, in.RestApiId)
		}
		stage, i, ok := a.stage(in.StageName)
		if !ok {
			return Stage{}, errStageNotFound()
		}
		stage, err := stagePatch(&a).Apply(stage, ops)
		if err != nil {
			return Stage{}, err
		}
		stage.Updated = now
		a.Stages = slices.Clone(a.Stages)
		a.Stages[i] = stage
		st.APIs[a.ID] = a
		return stage, nil
	})
	if err != nil {
		return nil, err
	}
	return new(stage.output()), nil
}

// deleteStage deletes a stage; its deployment stays.
func (s *Service) deleteStage(_ context.Context, c *kernel.Call, in *apigatewayapi.DeleteStageInput) (*apigatewayapi.DeleteStageOutput, *awserr.Error) {
	scope := c.Scope()
	_, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return struct{}{}, errAPINotFound(scope, in.RestApiId)
		}
		if _, _, ok := a.stage(in.StageName); !ok {
			return struct{}{}, errStageNotFound()
		}
		a.Stages = slices.DeleteFunc(slices.Clone(a.Stages), func(st Stage) bool { return st.Name == in.StageName })
		st.APIs[a.ID] = a
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &apigatewayapi.DeleteStageOutput{}, nil
}

// unknownStagePath is UpdateStage's refusal of a path it does not know,
// byte for byte, the missing commas included (recorded,
// test_apigateway_common.snapshot.json:L393-L410, apigateway-rest.md 2.4).
func unknownStagePath(op patch.Operation) string {
	return "Invalid method setting path: " + op.Path + ". Must be one of: [/deploymentId, /description, /cacheClusterEnabled, /cacheClusterSize, /clientCertificateId, /accessLogSettings, /accessLogSettings/destinationArn, /accessLogSettings/format, /{resourcePath}/{httpMethod}/metrics/enabled, /{resourcePath}/{httpMethod}/logging/dataTrace, /{resourcePath}/{httpMethod}/logging/loglevel, /{resourcePath}/{httpMethod}/throttling/burstLimit/{resourcePath}/{httpMethod}/throttling/rateLimit/{resourcePath}/{httpMethod}/caching/ttlInSeconds, /{resourcePath}/{httpMethod}/caching/enabled, /{resourcePath}/{httpMethod}/caching/dataEncrypted, /{resourcePath}/{httpMethod}/caching/requireAuthorizationForCacheControl, /{resourcePath}/{httpMethod}/caching/unauthorizedCacheControlHeaderStrategy, /*/*/metrics/enabled, /*/*/logging/dataTrace, /*/*/logging/loglevel, /*/*/throttling/burstLimit /*/*/throttling/rateLimit /*/*/caching/ttlInSeconds, /*/*/caching/enabled, /*/*/caching/dataEncrypted, /*/*/caching/requireAuthorizationForCacheControl, /*/*/caching/unauthorizedCacheControlHeaderStrategy, /variables/{variable_name}, /tracingEnabled]"
}

// stagePatch is UpdateStage's path table for a stage of a (aws-doc
// patch-operations, apigateway-rest.md 3.7). A path it does not know
// answers the recorded unknownStagePath text; another op than the
// documented ones on a known path answers OpsOneOf (inferred). The
// documented /documentationVersion and /canarySettings paths answer
// NotImplemented, as on CreateStage; so does copying /deploymentId from
// /canarySettings/deploymentId. Every other path of two or more segments
// is a method setting.
func stagePatch(a *RestAPI) patch.Table[Stage] {
	replace, remove := apigatewayapi.OpReplace, apigatewayapi.OpRemove
	only := []apigatewayapi.Op{replace}
	every := []apigatewayapi.Op{apigatewayapi.OpAdd, remove, replace, apigatewayapi.OpMove, apigatewayapi.OpCopy, apigatewayapi.OpTest}
	notImplemented := func(name string) func(*Stage, patch.Operation, []string) *awserr.Error {
		return func(*Stage, patch.Operation, []string) *awserr.Error {
			return refuseUnimplemented("UpdateStage", member{name, true})
		}
	}
	flag := func(field func(*Stage) *bool) func(*Stage, patch.Operation, []string) *awserr.Error {
		return func(st *Stage, op patch.Operation, _ []string) *awserr.Error {
			v, err := patchBool(op)
			if err != nil {
				return err
			}
			*field(st) = v
			return nil
		}
	}
	rules := []patch.Rule[Stage]{
		{Path: "/deploymentId", Ops: []apigatewayapi.Op{replace, apigatewayapi.OpCopy}, Apply: func(st *Stage, op patch.Operation, _ []string) *awserr.Error {
			if op.Op == apigatewayapi.OpCopy {
				return refuseUnimplemented("UpdateStage", member{"canarySettings", true})
			}
			id := deref(op.Value, "")
			if _, ok := a.deployment(id); !ok {
				return errNoDeployment()
			}
			st.Deployment = id
			return nil
		}},
		{Path: "/description", Ops: only, Apply: func(st *Stage, op patch.Operation, _ []string) *awserr.Error {
			st.Description = clonePtr(op.Value)
			return nil
		}},
		{Path: "/cacheClusterEnabled", Ops: only, Apply: flag(func(st *Stage) *bool { return &st.CacheClusterEnabled })},
		{Path: "/cacheClusterSize", Ops: only, Apply: func(st *Stage, op patch.Operation, _ []string) *awserr.Error {
			size := apigatewayapi.CacheClusterSize(deref(op.Value, ""))
			if !size.Known() {
				// The text is inferred.
				return errBadRequest("Invalid cache cluster size specified: %s", size)
			}
			st.CacheClusterSize = &size
			return nil
		}},
		{Path: "/clientCertificateId", Ops: only, Apply: func(st *Stage, op patch.Operation, _ []string) *awserr.Error {
			st.ClientCertificateID = nil
			if v := deref(op.Value, ""); v != "" {
				st.ClientCertificateID = &v
			}
			return nil
		}},
		{Path: "/tracingEnabled", Ops: only, Apply: flag(func(st *Stage) *bool { return &st.TracingEnabled })},
		{Path: "/accessLogSettings", Ops: []apigatewayapi.Op{remove}, OpText: patch.OpsOneOf(remove), Apply: func(st *Stage, _ patch.Operation, _ []string) *awserr.Error {
			st.AccessLog = nil
			return nil
		}},
		{Path: "/accessLogSettings/destinationArn", Ops: only, Apply: func(st *Stage, op patch.Operation, _ []string) *awserr.Error {
			st.AccessLog = withAccessLog(st.AccessLog)
			st.AccessLog.DestinationARN = clonePtr(op.Value)
			return nil
		}},
		{Path: "/accessLogSettings/format", Ops: only, Apply: func(st *Stage, op patch.Operation, _ []string) *awserr.Error {
			st.AccessLog = withAccessLog(st.AccessLog)
			st.AccessLog.Format = clonePtr(op.Value)
			return nil
		}},
		{Path: "/variables/*", Ops: []apigatewayapi.Op{replace, remove}, OpText: patch.OpsOneOf(replace, remove), Apply: func(st *Stage, op patch.Operation, wild []string) *awserr.Error {
			name := wild[0]
			if op.Op == remove {
				st.Variables = deleted(st.Variables, name)
				return nil
			}
			value := deref(op.Value, "")
			if err := checkStageVariable(name, value); err != nil {
				return err
			}
			vars := maps.Clone(st.Variables)
			if vars == nil {
				vars = map[string]string{}
			}
			vars[name] = value
			if len(vars) > maxStageVariables {
				return checkStageVariables(vars)
			}
			st.Variables = vars
			return nil
		}},
		{Path: "/documentationVersion", Ops: only, Apply: notImplemented("documentationVersion")},
		{Path: "/canarySettings/**", Ops: every, Apply: notImplemented("canarySettings")},
		{Path: "/**", Ops: every, Apply: patchMethodSetting},
	}
	return patch.Table[Stage]{
		Rules:       rules,
		UnknownPath: unknownStagePath,
		OpText:      patch.OpsOneOf(replace),
		Clone:       func(st Stage) Stage { return st.clone() },
	}
}

// withAccessLog is a copy of l, or empty settings when l is nil.
func withAccessLog(l *AccessLog) *AccessLog {
	if l == nil {
		return &AccessLog{}
	}
	return &AccessLog{Format: clonePtr(l.Format), DestinationARN: clonePtr(l.DestinationARN)}
}

// defaultMethodSetting is a new method setting's values (recorded,
// test_apigateway_common.snapshot.json:L1059-L1070: throttlingBurstLimit
// 5000, throttlingRateLimit 10000.0, cacheTtlInSeconds 300,
// requireAuthorizationForCacheControl true, SUCCEED_WITH_RESPONSE_HEADER,
// the flags false, no loggingLevel).
func defaultMethodSetting() MethodSetting {
	return MethodSetting{
		ThrottlingBurstLimit: 5000, ThrottlingRateLimit: 10000, CacheTTLInSeconds: 300, RequireAuthorizationForCacheControl: true,
		UnauthorizedCacheControlHeaderStrategy: apigatewayapi.UnauthorizedCacheControlHeaderStrategySucceedWithResponseHeader,
	}
}

// allMethods is the method settings key of every method.
const allMethods = "*/*"

// patchMethodSetting applies an operation on a method-setting path
// (apigateway-rest.md 3.7): replace
// /<resource path>/<HTTP method>/<group>/<setting> sets one setting of the
// entry keyed "<resource path without its leading slash>/<method>" ("*/*"
// for all; the resource path's slashes plain or written ~1), and remove
// /<resource path>/<method> removes the whole entry (recorded for */*,
// test_apigateway_common.py:L1455-L1486). A new entry starts from the */*
// entry when there is one, else the defaults (the recorded test/GET entry
// took */*'s cachingEnabled; inferred beyond that). Anything else is an
// unknown path.
func patchMethodSetting(st *Stage, op patch.Operation, wild []string) *awserr.Error {
	if op.Op == apigatewayapi.OpRemove && len(wild) >= 2 {
		key := methodSettingKey(wild)
		if _, ok := st.MethodSettings[key]; !ok {
			// Recorded, trailing space included
			// (test_apigateway_common.snapshot.json:L1040-L1050).
			return errBadRequest("Cannot remove method setting %s because there is no method setting for this method ", key)
		}
		st.MethodSettings = maps.Clone(st.MethodSettings)
		delete(st.MethodSettings, key)
		return nil
	}
	n := len(wild)
	if op.Op != apigatewayapi.OpReplace || n < 4 {
		return apigatewayapi.ErrBadRequestException(unknownStagePath(op))
	}
	setting := wild[n-2] + "/" + wild[n-1]
	apply, ok := methodSettingFields()[setting]
	if !ok {
		return apigatewayapi.ErrBadRequestException(unknownStagePath(op))
	}
	key := methodSettingKey(wild[:n-2])
	m, ok := st.MethodSettings[key]
	if !ok {
		m = defaultMethodSetting()
		if all, found := st.MethodSettings[allMethods]; found {
			m = all
		}
	}
	m.LoggingLevel = clonePtr(m.LoggingLevel)
	if err := apply(&m, op); err != nil {
		return err
	}
	st.MethodSettings = maps.Clone(st.MethodSettings)
	if st.MethodSettings == nil {
		st.MethodSettings = map[string]MethodSetting{}
	}
	st.MethodSettings[key] = m
	return nil
}

// methodSettingKey is the key of the entry segs (the resource path's
// segments, then the method) name: the resource path without its leading
// slash, "/", the method.
func methodSettingKey(segs []string) string {
	resource := strings.TrimPrefix(strings.Join(segs[:len(segs)-1], "/"), "/")
	return resource + "/" + segs[len(segs)-1]
}

// methodSettingFields are the settings of a method setting, by the last
// two path segments (aws-doc patch-operations, UpdateStage; the recorded
// unknownStagePath list). The texts for a value of the wrong kind are
// inferred.
func methodSettingFields() map[string]func(*MethodSetting, patch.Operation) *awserr.Error {
	return map[string]func(*MethodSetting, patch.Operation) *awserr.Error{
		"metrics/enabled":   boolSetting(func(m *MethodSetting) *bool { return &m.MetricsEnabled }),
		"logging/dataTrace": boolSetting(func(m *MethodSetting) *bool { return &m.DataTraceEnabled }),
		"logging/loglevel": func(m *MethodSetting, op patch.Operation) *awserr.Error {
			switch v := deref(op.Value, ""); v {
			case "OFF", "ERROR", "INFO":
				m.LoggingLevel = &v
				return nil
			}
			return errBadRequest("Invalid patch value for %s: the logging level is OFF, ERROR or INFO", op.Path)
		},
		"throttling/burstLimit": func(m *MethodSetting, op patch.Operation) *awserr.Error {
			n, err := strconv.ParseInt(deref(op.Value, ""), 10, 32)
			if err != nil {
				return errBadRequest("Invalid patch value for %s: expected an integer", op.Path)
			}
			m.ThrottlingBurstLimit = int32(n)
			return nil
		},
		"throttling/rateLimit": func(m *MethodSetting, op patch.Operation) *awserr.Error {
			f, err := strconv.ParseFloat(deref(op.Value, ""), 64)
			if err != nil {
				return errBadRequest("Invalid patch value for %s: expected a number", op.Path)
			}
			m.ThrottlingRateLimit = f
			return nil
		},
		"caching/ttlInSeconds": func(m *MethodSetting, op patch.Operation) *awserr.Error {
			n, err := strconv.ParseInt(deref(op.Value, ""), 10, 32)
			if err != nil {
				return errBadRequest("Invalid patch value for %s: expected an integer", op.Path)
			}
			m.CacheTTLInSeconds = int32(n)
			return nil
		},
		"caching/enabled":                             boolSetting(func(m *MethodSetting) *bool { return &m.CachingEnabled }),
		"caching/dataEncrypted":                       boolSetting(func(m *MethodSetting) *bool { return &m.CacheDataEncrypted }),
		"caching/requireAuthorizationForCacheControl": boolSetting(func(m *MethodSetting) *bool { return &m.RequireAuthorizationForCacheControl }),
		"caching/unauthorizedCacheControlHeaderStrategy": func(m *MethodSetting, op patch.Operation) *awserr.Error {
			v := apigatewayapi.UnauthorizedCacheControlHeaderStrategy(deref(op.Value, ""))
			if !v.Known() {
				return errBadRequest("Invalid patch value for %s: %q is no unauthorized cache control header strategy", op.Path, v)
			}
			m.UnauthorizedCacheControlHeaderStrategy = v
			return nil
		},
	}
}

// boolSetting sets the flag field picks from a "true" or "false" value.
func boolSetting(field func(*MethodSetting) *bool) func(*MethodSetting, patch.Operation) *awserr.Error {
	return func(m *MethodSetting, op patch.Operation) *awserr.Error {
		v, err := patchBool(op)
		if err != nil {
			return err
		}
		*field(m) = v
		return nil
	}
}
