package lambda

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// The documented limits the model is wider than (lambda-api.md finding 5,
// 4.1): ordinary functions run up to 900 s with up to 10,240 MB of memory
// and ephemeral storage.
const (
	maxTimeout          = 900
	maxMemorySize       = 10_240
	maxEphemeralStorage = 10_240
	// maxNameLength caps a bare function name; the model's 140 counts an
	// ARN's prefix too.
	maxNameLength = 64
	maxLayers     = 5
	// maxLayerARNLength is the model's LayerVersionArn length bound.
	maxLayerARNLength = 2048
	// maxTags is the resource tag limit (lambda-api.md 4.1).
	maxTags = 50
	// maxEnvironmentBytes caps the variables as their JSON object.
	maxEnvironmentBytes = 4096
)

// listedRuntimes is the runtime enum a creation answers with, in AWS's
// order (recorded 2026-01-12 for test_create_lambda_exceptions). A runtime
// outside it cannot be created.
func listedRuntimes() []string {
	return []string{
		"nodejs20.x", "python3.14", "provided.al2023", "python3.12", "python3.13", "nodejs24.x", "nodejs22.x", "java25",
		"python3.10", "python3.11", "java21", "ruby3.3", "ruby3.4", "ruby3.2", "python3.8", "python3.9", "java17",
		"nodejs16.x", "dotnet10", "dotnet8", "java11", "dotnet6", "nodejs18.x", "provided.al2", "java8.al2",
	}
}

// deprecatedRuntimes are the model's runtimes AWS no longer creates: the
// eight recorded with the deprecation text (lambda-api.md 4.2) and the
// older ones they superseded.
func deprecatedRuntimes() []string {
	return []string{
		"nodejs", "nodejs4.3", "nodejs4.3-edge", "nodejs6.10", "nodejs8.10", "nodejs10.x", "nodejs12.x", "nodejs14.x",
		"java8", "python2.7", "python3.6", "python3.7", "dotnetcore1.0", "dotnetcore2.0", "dotnetcore2.1", "dotnetcore3.1",
		"go1.x", "ruby2.5", "ruby2.7", "provided",
	}
}

// reservedVariables are the environment variables Lambda sets itself
// (lambda-api.md 9.4).
func reservedVariables() []string {
	return []string{
		"_HANDLER", "_X_AMZN_TRACE_ID", "AWS_DEFAULT_REGION", "AWS_REGION", "AWS_EXECUTION_ENV", "AWS_LAMBDA_FUNCTION_NAME",
		"AWS_LAMBDA_FUNCTION_MEMORY_SIZE", "AWS_LAMBDA_FUNCTION_VERSION", "AWS_LAMBDA_INITIALIZATION_TYPE",
		"AWS_LAMBDA_LOG_GROUP_NAME", "AWS_LAMBDA_LOG_STREAM_NAME", "AWS_ACCESS_KEY", "AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_LAMBDA_RUNTIME_API", "LAMBDA_TASK_ROOT", "LAMBDA_RUNTIME_DIR",
		"AWS_LAMBDA_MAX_CONCURRENCY", "AWS_LAMBDA_METADATA_API", "AWS_LAMBDA_METADATA_TOKEN",
	}
}

// architectureViolation is the rolled-up enum check of an architecture list
// the overlay leaves to the service, Architectures or a layer's
// CompatibleArchitectures as member names it, with the recorded composite
// text.
func architectureViolation(member string, archs []lambdaapi.Architecture) []awserr.Violation {
	for _, a := range archs {
		if !a.Known() {
			return []awserr.Violation{{
				Path:  awserr.Path{member},
				Value: awserr.RenderedText{Text: "[" + strings.Join(nameList(archs), ", ") + "]"},
				Constraint: awserr.Composite{Scope: awserr.ElementScope, Of: []awserr.MemberConstraint{
					awserr.Enum{Values: []string{string(lambdaapi.ArchitectureX8664), string(lambdaapi.ArchitectureArm64)}}, awserr.Required{},
				}},
			}}
		}
	}
	return nil
}

// limitViolations enforces the documented limits on the members the model
// bounds more widely.
func limitViolations(timeout, memory *int32, storage *lambdaapi.EphemeralStorage) []awserr.Violation {
	var vs []awserr.Violation
	over := func(path string, v int32, limit int) {
		if int(v) > limit {
			vs = append(vs, awserr.Violation{Path: awserr.Path(strings.Split(path, ".")), Value: awserr.RenderedText{Text: strconv.Itoa(int(v))}, Constraint: awserr.RangeMax{Max: strconv.Itoa(limit)}})
		}
	}
	if timeout != nil {
		over("timeout", *timeout, maxTimeout)
	}
	if memory != nil {
		over("memorySize", *memory, maxMemorySize)
	}
	if storage != nil {
		over("ephemeralStorage.size", storage.Size, maxEphemeralStorage)
	}
	return vs
}

// layerVersionARN is the model's LayerVersionArn pattern, which the overlay
// leaves to the service for a function's Layers, and layerVersionARNEcho
// the pattern as AWS prints it, without the anchors and the outer group
// (lambda-destinations-layers.md 6.4).
var layerVersionARN = regexp.MustCompile(`^((arn:(aws[a-zA-Z\-]*)?:lambda:(eusc-)?[a-z]{2}((-gov)|(-iso([a-z]?)))?-[a-z]+-\d{1}:\d{12}:layer:[a-zA-Z0-9\-_]+:[0-9]+)|` +
	`(arn:[a-zA-Z0-9\-]+:lambda:::awslayer:[a-zA-Z0-9\-_]+))$`)

const layerVersionARNEcho = `(arn:(aws[a-zA-Z-]*)?:lambda:(eusc-)?[a-z]{2}((-gov)|(-iso([a-z]?)))?-[a-z]+-\d{1}:\d{12}:layer:[a-zA-Z0-9-_]+:[0-9]+)|` +
	`(arn:[a-zA-Z0-9-]+:lambda:::awslayer:[a-zA-Z0-9-_]+)`

// layersViolation is the rolled-up check of a function's Layers against
// the version ARN pattern, ending in the not-null clause the generated
// constraint lacks (lambda-destinations-layers.md 7.1:
// test_lambda_api.snapshot.json:L9396-L9405). An entry outside the model's
// 1..2048 length is the generated validator's to report, which keeps the
// list from being reported twice; its text then lacks the pattern and
// not-null clauses.
func layersViolation(layers []string) []awserr.Violation {
	mismatch := false
	for _, l := range layers {
		if n := utf8.RuneCountInString(l); n < 1 || n > maxLayerARNLength {
			return nil
		}
		mismatch = mismatch || !layerVersionARN.MatchString(l)
	}
	if !mismatch {
		return nil
	}
	return []awserr.Violation{{
		Path:  awserr.Path{"layers"},
		Value: awserr.RenderedText{Text: "[" + strings.Join(layers, ", ") + "]"},
		Constraint: awserr.Composite{Scope: awserr.ElementScope, Of: []awserr.MemberConstraint{
			awserr.LengthMax{Max: maxLayerARNLength}, awserr.LengthMin{Min: 1}, awserr.Pattern{Echo: layerVersionARNEcho}, awserr.Required{},
		}},
	}}
}

func validateCreateFunction(in *lambdaapi.CreateFunctionInput) []awserr.Violation {
	return slices.Concat(architectureViolation("architectures", in.Architectures), limitViolations(in.Timeout, in.MemorySize, in.EphemeralStorage), layersViolation(in.Layers))
}

func validateUpdateConfiguration(in *lambdaapi.UpdateFunctionConfigurationInput) []awserr.Violation {
	return append(limitViolations(in.Timeout, in.MemorySize, in.EphemeralStorage), layersViolation(in.Layers)...)
}

// validateCreateMapping checks StartingPosition, which the overlay leaves
// to the service so its enum reads in the recorded order.
func validateCreateMapping(in *lambdaapi.CreateEventSourceMappingInput) []awserr.Violation {
	if p := in.StartingPosition; p != nil && !p.Known() {
		return []awserr.Violation{{
			Path:  awserr.Path{"startingPosition"},
			Value: awserr.RenderedText{Text: string(*p)},
			Constraint: awserr.Enum{Values: []string{
				string(lambdaapi.EventSourcePositionLatest), string(lambdaapi.EventSourcePositionAtTimestamp), string(lambdaapi.EventSourcePositionTrimHorizon),
			}},
		}}
	}
	return nil
}

func validateUpdateCode(in *lambdaapi.UpdateFunctionCodeInput) []awserr.Violation {
	return architectureViolation("architectures", in.Architectures)
}

// checkCreateFunction answers the creation rules no constraint trait
// carries, in InvalidParameterValueException's words.
func checkCreateFunction(in *lambdaapi.CreateFunctionInput) *awserr.Error {
	if name := bareName(in.FunctionName); len(name) > maxNameLength {
		return errNameTooLong(name)
	}
	packageType := smithy.Deref(in.PackageType)
	if packageType == "" {
		packageType = lambdaapi.PackageTypeZip
	}
	if err := checkRuntime(in.Runtime); err != nil {
		return err
	}
	// The layer count and repeats come before an image function's refusal
	// of any layer, the order of lambda-destinations-layers.md 7.1's checks
	// being unrecorded.
	if err := checkLayers(in.Layers); err != nil {
		return err
	}
	switch {
	case packageType == lambdaapi.PackageTypeImage && len(in.Layers) > 0:
		return errImageTakesNoLayers()
	case packageType == lambdaapi.PackageTypeImage && (in.Runtime != nil || in.Handler != nil):
		return errImageTakesNoRuntime()
	case packageType == lambdaapi.PackageTypeZip && (in.Runtime == nil || in.Handler == nil):
		return errZipNeedsRuntime()
	}
	if err := checkPublishTo("CreateFunction", in.PublishTo); err != nil {
		return err
	}
	if err := checkUnimplementedConfig("CreateFunction", in.TenancyConfig, in.DurableConfig); err != nil {
		return err
	}
	if err := checkEnvironment(in.Environment); err != nil {
		return err
	}
	if len(in.Tags) > maxTags {
		return errTooManyTags()
	}
	return nil
}

func checkUpdateConfiguration(in *lambdaapi.UpdateFunctionConfigurationInput) *awserr.Error {
	if err := checkRuntime(in.Runtime); err != nil {
		return err
	}
	if err := checkLayers(in.Layers); err != nil {
		return err
	}
	if err := checkUnimplementedConfig("UpdateFunctionConfiguration", nil, in.DurableConfig); err != nil {
		return err
	}
	return checkEnvironment(in.Environment)
}

// checkRuntime admits the runtimes AWS creates: the listed ones. A
// deprecated runtime gets the deprecation text, anything else the enum.
func checkRuntime(r *lambdaapi.Runtime) *awserr.Error {
	switch {
	case r == nil || slices.Contains(listedRuntimes(), string(*r)):
		return nil
	case slices.Contains(deprecatedRuntimes(), string(*r)):
		return errRuntimeDeprecated(string(*r))
	default:
		return errRuntime(string(*r))
	}
}

// checkEnvironment refuses reserved variables and an environment above
// 4 KB, measured as its JSON object.
func checkEnvironment(env *lambdaapi.Environment) *awserr.Error {
	if env == nil {
		return nil
	}
	var reserved []string
	for _, k := range slices.Sorted(maps.Keys(env.Variables)) {
		if slices.Contains(reservedVariables(), k) {
			reserved = append(reserved, k)
		}
	}
	if len(reserved) > 0 {
		return errEnvironmentReserved(reserved)
	}
	measured, err := jsonx.Marshal(env.Variables)
	if err != nil {
		return awserr.Internalf(err)
	}
	if len(measured) > maxEnvironmentBytes {
		return errEnvironmentTooLarge(string(measured))
	}
	return nil
}

// bareName is the function name inside a FunctionName member, without
// ARN, partial-ARN prefixes or qualifier; "" for an ARN that names no
// function, which the handler answers.
func bareName(s string) string {
	_, rest, err := splitFunctionName(aws.Scope{}, s)
	if err != nil {
		return ""
	}
	name, _, _ := strings.Cut(rest, ":")
	return name
}
