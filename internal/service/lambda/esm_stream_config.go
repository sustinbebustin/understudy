package lambda

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

// The control plane of a DynamoDB stream mapping (lambda-stream-sources.md
// 3, 12.4). Create takes StartingPosition TRIM_HORIZON, BatchSize and
// Enabled; Update takes BatchSize and Enabled. Every other stream setting
// answers 501 rather than being stored without effect.

// defaultStreamBatchSize is a stream mapping's BatchSize when Create sets
// none (lambda-stream-sources.md 3.5, recorded LS-D:L246-L271).
const defaultStreamBatchSize = 100

// streamTransitionReason is the StateTransitionReason a stream mapping
// reports (recorded LS-D:L173-L197; lambda-stream-sources.md 3.5).
const streamTransitionReason = "User action"

// isDynamoDBStream reports whether a names a DynamoDB stream:
// arn:<partition>:dynamodb:<region>:<account>:table/<table>/stream/<label>
// (dynamodb-streams.md 4.1).
func isDynamoDBStream(a aws.ARN) bool {
	table, ok := strings.CutPrefix(a.Resource, "table/")
	if a.Service != "dynamodb" || !ok {
		return false
	}
	name, label, ok := strings.Cut(table, "/stream/")
	return ok && name != "" && label != ""
}

// streamStart reads a stream mapping's StartingPosition: absent answers
// row 1 of lambda-stream-sources.md 3.3, AT_TIMESTAMP row 3 (both
// recorded, LS-API:L12333-L12366). A value outside the enum never gets
// here: the validator answers row 2.
func streamStart(p *lambdaapi.EventSourcePosition, stream aws.ARN) (esm.StreamStart, *awserr.Error) {
	if p == nil {
		return nil, user(lambdaapi.ErrInvalidParameterValueException("1 validation error detected: Value null at 'startingPosition' failed to satisfy constraint: Member must not be null."))
	}
	start, ok := esm.ParseStreamStart(string(*p))
	if !ok {
		return nil, user(lambdaapi.ErrInvalidParameterValueException("Unsupported starting position for arn type: " + stream.String()))
	}
	return start, nil
}

// errStreamSettingUnbuilt answers a stream mapping setting understudy does
// not build yet.
func errStreamSettingUnbuilt(setting string) *awserr.Error {
	return lambdaapi.ErrNotImplemented("understudy does not implement " + setting + " on a DynamoDB stream event source mapping yet; a stream mapping takes StartingPosition TRIM_HORIZON, BatchSize and Enabled. " +
		"Run 'understudy coverage --service lambda' for what is supported.")
}

// unbuiltStreamSetting is the first setting of in that a stream mapping
// does not build yet, or nil. Members of the SQS type (ScalingConfig,
// ProvisionedPollerConfig, Queues) are ignored and not echoed
// (lambda-stream-sources.md 12.4 item 9; inferred, ESMS-03).
func unbuiltStreamSetting(in *lambdaapi.CreateEventSourceMappingInput, start esm.StreamStart) *awserr.Error {
	if _, latest := start.(esm.Latest); latest {
		return errStreamSettingUnbuilt("StartingPosition LATEST")
	}
	switch {
	case in.DestinationConfig != nil && (in.DestinationConfig.OnSuccess != nil || (in.DestinationConfig.OnFailure != nil && in.DestinationConfig.OnFailure.Destination != nil)):
		return errStreamSettingUnbuilt("DestinationConfig")
	case in.FilterCriteria != nil:
		return errStreamSettingUnbuilt("FilterCriteria")
	case in.ParallelizationFactor != nil && *in.ParallelizationFactor != 1:
		return errStreamSettingUnbuilt("ParallelizationFactor above 1")
	case in.BisectBatchOnFunctionError != nil && *in.BisectBatchOnFunctionError:
		return errStreamSettingUnbuilt("BisectBatchOnFunctionError")
	case in.TumblingWindowInSeconds != nil && *in.TumblingWindowInSeconds != 0:
		return errStreamSettingUnbuilt("TumblingWindowInSeconds")
	case in.MaximumRecordAgeInSeconds != nil && *in.MaximumRecordAgeInSeconds != -1:
		return errStreamSettingUnbuilt("MaximumRecordAgeInSeconds")
	case in.MaximumRetryAttempts != nil && *in.MaximumRetryAttempts != -1:
		return errStreamSettingUnbuilt("MaximumRetryAttempts")
	case in.MaximumBatchingWindowInSeconds != nil && *in.MaximumBatchingWindowInSeconds != 0:
		return errStreamSettingUnbuilt("MaximumBatchingWindowInSeconds")
	case len(in.FunctionResponseTypes) > 0:
		return errStreamSettingUnbuilt("FunctionResponseTypes")
	case in.KMSKeyArn != nil:
		return errStreamSettingUnbuilt("KMSKeyArn")
	case in.MetricsConfig != nil:
		return errStreamSettingUnbuilt("MetricsConfig")
	}
	return nil
}

// streamTarget is how the control plane and the poller call the stream's
// service: in the stream's account and region, as the function's
// execution role (lambda-stream-sources.md 12.4 item 6, 12.5).
func streamTarget(stream, role aws.ARN) (kernel.Target, bool) {
	scope, err := aws.ScopeOf(stream)
	if err != nil {
		return kernel.Target{}, false
	}
	return kernel.Target{Scope: scope, As: kernel.AsExecutionRole{Role: role}, Via: ref.LambdaESM}, true
}

// checkStream describes the stream as the function's execution role and
// refuses one that does not exist with row 4 of lambda-stream-sources.md
// 3.3 (recorded, LS-D:L4478-L4494).
func (s *Service) checkStream(ctx context.Context, c *kernel.Call, stream aws.ARN, target mappingTarget) *awserr.Error {
	missing := user(lambdaapi.ErrInvalidParameterValueException("Stream not found: " + stream.String()))
	role, perr := aws.ParseARN(target.role)
	if perr != nil || stream.Region != c.Scope().Region.String() {
		return missing
	}
	t, ok := streamTarget(stream, role)
	if !ok {
		return missing
	}
	_, err := kernel.Invoke(ctx, s.k, c, dynamodbstreamsapi.OpDescribeStream, t, &dynamodbstreamsapi.DescribeStreamInput{StreamArn: stream.String()})
	switch {
	case err == nil:
		return nil
	case err.ID == dynamodbstreamsapi.IdentityResourceNotFoundException():
		return missing
	}
	if _, internal := err.Origin.(awserr.Internal); internal || err.ID == awserr.RequestAbortedException() {
		return err
	}
	return user(lambdaapi.ErrInvalidParameterValueException("Error occurred while DescribeStream. DynamoDB Streams Error Code: " + err.ID.JSONType + ". Error Message: " + err.Message))
}

// errStreamMappingExists is the recorded duplicate text for a stream
// mapping (lambda-stream-sources.md 3.3 row 5, LS-D:L557-L568).
func errStreamMappingExists(stream, function, uuid string) *awserr.Error {
	return user(lambdaapi.ErrResourceConflictException(`The event source arn (" ` + stream + ` ") and function (" ` + function +
		` ") provided mapping already exists. Please update or delete the existing mapping with UUID ` + uuid))
}

// createStreamMapping creates a mapping on a DynamoDB stream, checking in
// the order of the contract: StartingPosition, the settings not built,
// the function, the stream as the execution role, then a duplicate.
func (s *Service) createStreamMapping(ctx context.Context, c *kernel.Call, in *lambdaapi.CreateEventSourceMappingInput, stream aws.ARN) (*lambdaapi.EventSourceMappingConfiguration, *awserr.Error) {
	start, err := streamStart(in.StartingPosition, stream)
	if err != nil {
		return nil, err
	}
	if err := unbuiltStreamSetting(in, start); err != nil {
		return nil, err
	}
	target, err := s.mappingFunction(c, in.FunctionName)
	if err != nil {
		return nil, err
	}
	if err := s.checkStream(ctx, c, stream, target); err != nil {
		return nil, err
	}
	m := Mapping{
		Source: stream.String(), SourceKind: esm.DynamoDBStream{Position: start, Settings: esm.DefaultStreamSettings()},
		BatchSize: defaultStreamBatchSize, Scaling: Unbounded{}, Enabled: in.Enabled == nil || *in.Enabled, Tags: maps.Clone(in.Tags),
	}
	set(&m.BatchSize, in.BatchSize)
	m.UUID, m.Function, m.Qualifier = string(s.ids.ESMID()), target.ref.name, target.ref.qualifier
	out, err := write(s, c, target.ref.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (lambdaapi.EventSourceMappingConfiguration, *awserr.Error) {
		for _, uuid := range slices.Sorted(maps.Keys(st.Mappings)) {
			if other := st.Mappings[uuid]; other.Source == m.Source && other.Function == m.Function && other.Qualifier == m.Qualifier {
				return lambdaapi.EventSourceMappingConfiguration{}, errStreamMappingExists(m.Source, m.Function, uuid)
			}
		}
		m.Modified = now
		if m.Enabled {
			s.transition(tx, target.ref.scope, &m, MappingCreating, now)
		}
		st.Mappings[m.UUID] = m
		return renderMapping(target.ref.scope, m), nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, mappingARN(target.ref.scope, m.UUID), target.ref.qualifiedARN())
	return &out, nil
}

// checkStreamUpdate refuses an update of a stream mapping that changes
// anything but BatchSize and Enabled, which are all a stream mapping
// builds yet.
func checkStreamUpdate(in *lambdaapi.UpdateEventSourceMappingInput) *awserr.Error {
	switch {
	case in.FunctionName != nil:
		return errStreamSettingUnbuilt("an update of FunctionName")
	case in.DestinationConfig != nil:
		return errStreamSettingUnbuilt("DestinationConfig")
	case in.FilterCriteria != nil:
		return errStreamSettingUnbuilt("FilterCriteria")
	case in.ParallelizationFactor != nil, in.BisectBatchOnFunctionError != nil, in.TumblingWindowInSeconds != nil,
		in.MaximumRecordAgeInSeconds != nil, in.MaximumRetryAttempts != nil, in.MaximumBatchingWindowInSeconds != nil,
		in.FunctionResponseTypes != nil, in.KMSKeyArn != nil, in.MetricsConfig != nil:
		return errStreamSettingUnbuilt("an update of a setting other than BatchSize and Enabled")
	}
	return nil
}

// renderStreamMapping completes out with the members a stream mapping
// reports (lambda-stream-sources.md 3.5, recorded LS-D:L246-L271): its
// settings at their built values and its starting position.
func renderStreamMapping(out *lambdaapi.EventSourceMappingConfiguration, m *Mapping, kind esm.DynamoDBStream) {
	out.StartingPosition = new(lambdaapi.EventSourcePosition(esm.StreamStartName(kind.Position)))
	out.StateTransitionReason = new(streamTransitionReason)
	out.BisectBatchOnFunctionError = new(false)
	out.ParallelizationFactor = new(int32(1))
	out.TumblingWindowInSeconds = new(int32(0))
	out.MaximumRecordAgeInSeconds = new(int32(-1))
	out.MaximumRetryAttempts = new(int32(-1))
	out.DestinationConfig = &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{}}
	result := "No records processed"
	if m.Processed {
		result = "OK"
	}
	out.LastProcessingResult = new(result)
}
