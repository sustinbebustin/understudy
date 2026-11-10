package lambda

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// The control plane of a DynamoDB or Kinesis stream mapping
// (lambda-stream-sources.md 3, 12.4).
// What the stream poller does not build answers 501 rather than being
// stored without effect: ParallelizationFactor above 1,
// BisectBatchOnFunctionError, TumblingWindowInSeconds above 0, a
// FilterCriteria pattern and an S3 on-failure destination. The SQS rule
// that a BatchSize above 10 needs a batching window does not apply to a
// stream (aws-recorded for DynamoDB,
// lambda-stream-sources.md 3.3: ESMS-02 03).

// defaultStreamBatchSize is a stream mapping's BatchSize when Create sets
// none (lambda-stream-sources.md 3.5, recorded LS-D:L246-L271).
const defaultStreamBatchSize = 100

// streamTransitionReason is the StateTransitionReason a stream mapping
// reports (recorded LS-D:L173-L197; lambda-stream-sources.md 3.5).
const streamTransitionReason = "User action"

// The MaximumRecordAgeInSeconds range besides -1 (smithy member doc,
// lambda-stream-sources.md 2.2).
const (
	minRecordAge = 60
	maxRecordAge = 604800
)

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
// row 1 of lambda-stream-sources.md 3.3 (recorded, LS-API:L12333-L12366;
// LS-API:L18250-L18260 for Kinesis). AT_TIMESTAMP answers row 3 on a
// DynamoDB stream (recorded), and 501 on a Kinesis stream, where AWS
// accepts it (12.1 item 2). A value outside the enum never gets here: the
// validator answers row 2.
func streamStart(p *lambdaapi.EventSourcePosition, stream aws.ARN, kind esm.EventSourceKind) (esm.StreamStart, *awserr.Error) {
	if p == nil {
		return nil, user(lambdaapi.ErrInvalidParameterValueException("1 validation error detected: Value null at 'startingPosition' failed to satisfy constraint: Member must not be null."))
	}
	start, ok := esm.ParseStreamStart(string(*p))
	if ok {
		return start, nil
	}
	if _, kinesis := kind.(esm.KinesisStream); kinesis {
		return nil, errKinesisAtTimestampUnsupported()
	}
	return nil, user(lambdaapi.ErrInvalidParameterValueException("Unsupported starting position for arn type: " + stream.String()))
}

// streamNoun names a stream kind in a 501's text: "a DynamoDB stream" or
// "a Kinesis stream".
func streamNoun(kind esm.EventSourceKind) string {
	switch kind.(type) {
	case esm.KinesisStream:
		return "a Kinesis stream"
	case esm.DynamoDBStream, esm.SQS:
	}
	return "a DynamoDB stream"
}

// errStreamSettingUnbuilt answers a setting understudy does not build on a
// mapping of the stream kind noun names (streamNoun).
func errStreamSettingUnbuilt(setting, noun string) *awserr.Error {
	return lambdaapi.ErrNotImplemented("understudy does not implement " + setting + " on " + noun + " event source mapping. " +
		"Run 'understudy coverage --service lambda' for what is supported.")
}

// withStream is the stream kind of kind's type with start and settings.
// An SQS kind, which has neither, is returned as it is.
func withStream(kind esm.EventSourceKind, start esm.StreamStart, settings esm.StreamSettings) esm.EventSourceKind {
	switch kind.(type) {
	case esm.DynamoDBStream:
		return esm.DynamoDBStream{Position: start, Settings: settings}
	case esm.KinesisStream:
		return esm.KinesisStream{Position: start, Settings: settings}
	case esm.SQS:
	}
	return kind
}

// errRecordAge refuses a MaximumRecordAgeInSeconds of 0 to 59, which the
// model's range admits and the member documentation forbids (inferred
// text, ESMS-01).
func errRecordAge() *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("MaximumRecordAgeInSeconds must be -1 or between 60 and 604800."))
}

// streamSettings are the members of a stream mapping Create and Update
// share. Members of the SQS type (ScalingConfig, ProvisionedPollerConfig,
// Queues) are not among them: a stream mapping ignores them and does not
// echo them (lambda-stream-sources.md 12.4 item 9; inferred, ESMS-03).
type streamSettings struct {
	batchSize, window, recordAge, retries, parallelization, tumbling *int32
	bisect                                                           *bool
	filters                                                          *lambdaapi.FilterCriteria
	destinations                                                     *lambdaapi.DestinationConfig
	responseTypes                                                    []lambdaapi.FunctionResponseType
	kmsKeyArn                                                        *string
	metrics                                                          *lambdaapi.EventSourceMappingMetricsConfig
}

func createStreamSettings(in *lambdaapi.CreateEventSourceMappingInput) streamSettings {
	return streamSettings{
		batchSize: in.BatchSize, window: in.MaximumBatchingWindowInSeconds, recordAge: in.MaximumRecordAgeInSeconds, retries: in.MaximumRetryAttempts,
		parallelization: in.ParallelizationFactor, tumbling: in.TumblingWindowInSeconds, bisect: in.BisectBatchOnFunctionError,
		filters: in.FilterCriteria, destinations: in.DestinationConfig, responseTypes: in.FunctionResponseTypes,
		kmsKeyArn: in.KMSKeyArn, metrics: in.MetricsConfig,
	}
}

func updateStreamSettings(in *lambdaapi.UpdateEventSourceMappingInput) streamSettings {
	return streamSettings{
		batchSize: in.BatchSize, window: in.MaximumBatchingWindowInSeconds, recordAge: in.MaximumRecordAgeInSeconds, retries: in.MaximumRetryAttempts,
		parallelization: in.ParallelizationFactor, tumbling: in.TumblingWindowInSeconds, bisect: in.BisectBatchOnFunctionError,
		filters: in.FilterCriteria, destinations: in.DestinationConfig, responseTypes: in.FunctionResponseTypes,
		kmsKeyArn: in.KMSKeyArn, metrics: in.MetricsConfig,
	}
}

// apply sets what u carries on m and its stream settings, checking in
// this order, which decides the refusal a caller sees: destinations,
// filters, then the
// settings understudy does not build and the record-age rule. noun names
// the stream kind in a 501 (streamNoun).
func (u streamSettings) apply(m *Mapping, settings *esm.StreamSettings, noun string) *awserr.Error {
	if err := u.applyDestinations(settings, noun); err != nil {
		return err
	}
	if u.filters != nil {
		patterns, err := filterPatterns(u.filters)
		if err != nil {
			return err
		}
		if len(patterns) > 0 {
			return errStreamSettingUnbuilt("a FilterCriteria pattern", noun)
		}
	}
	if err := u.checkUnbuilt(noun); err != nil {
		return err
	}
	u.applyLimits(settings)
	set(&m.BatchSize, u.batchSize)
	set(&m.Window, u.window)
	if u.responseTypes != nil {
		m.ReportFailures = slices.Contains(u.responseTypes, lambdaapi.FunctionResponseTypeReportBatchItemFailures)
	}
	set(&m.KMSKeyArn, u.kmsKeyArn)
	if u.metrics != nil {
		m.Metrics = nil
		for _, metric := range u.metrics.Metrics {
			m.Metrics = append(m.Metrics, string(metric))
		}
	}
	return nil
}

// applyDestinations sets the on-failure destination u carries; an
// OnSuccess destination answers the recorded SQS text, inferred to apply
// (12.4 item 9).
func (u streamSettings) applyDestinations(settings *esm.StreamSettings, noun string) *awserr.Error {
	d := u.destinations
	if d == nil {
		return nil
	}
	if d.OnSuccess != nil && deref(d.OnSuccess.Destination) != "" {
		return errUnsupportedDestination()
	}
	if d.OnFailure != nil {
		dest, err := streamOnFailure(deref(d.OnFailure.Destination), noun)
		if err != nil {
			return err
		}
		settings.OnFailure = dest
	}
	return nil
}

// checkUnbuilt refuses the settings the poller does not build, and a
// MaximumRecordAgeInSeconds of 0 to 59.
func (u streamSettings) checkUnbuilt(noun string) *awserr.Error {
	switch {
	case u.parallelization != nil && *u.parallelization != 1:
		return errStreamSettingUnbuilt("ParallelizationFactor above 1", noun)
	case u.bisect != nil && *u.bisect:
		return errStreamSettingUnbuilt("BisectBatchOnFunctionError", noun)
	case u.tumbling != nil && *u.tumbling != 0:
		return errStreamSettingUnbuilt("TumblingWindowInSeconds above 0", noun)
	case u.recordAge != nil && *u.recordAge >= 0 && *u.recordAge < minRecordAge:
		return errRecordAge()
	}
	return nil
}

// refuseOnSQS refuses an SQS mapping that carries a member of the stream
// type, in AWS's words (aws-recorded on Create, one member at a time,
// lambda-stream-sources.md 3.4: ESMS-03 02-06). Update is inferred to
// refuse them alike, and the order when several are set is unrecorded.
func (u streamSettings) refuseOnSQS() *awserr.Error {
	switch {
	case u.parallelization != nil:
		return errUnsupportedMember("ParallelizationFactor")
	case u.bisect != nil:
		return errUnsupportedMember("BisectBatchOnFunctionError")
	case u.retries != nil:
		return errUnsupportedMember("MaximumRetryAttempts")
	case u.recordAge != nil:
		return errUnsupportedMember("MaximumRecordAgeInSeconds")
	case u.tumbling != nil:
		return errUnsupportedMember("TumblingWindowInSeconds")
	}
	return nil
}

// applyLimits sets the record age and retry limit u carries on settings;
// -1 lifts either. The model's ranges (-1 to 604800, -1 to 10000) are
// checked before a handler runs, and the record-age floor by checkUnbuilt.
func (u streamSettings) applyLimits(settings *esm.StreamSettings) {
	if u.recordAge != nil {
		settings.RecordAge = esm.AnyAge{}
		if *u.recordAge != -1 {
			settings.RecordAge = esm.MaxAge{Seconds: *u.recordAge}
		}
	}
	if u.retries != nil {
		settings.Retries = esm.Unlimited{}
		if *u.retries != -1 {
			settings.Retries = esm.Attempts{N: *u.retries}
		}
	}
}

// streamOnFailure reads an on-failure destination: "" is none, which also
// clears one on Update (aws-doc "supply an empty string",
// lambda-stream-sources.md 3.7); an SQS queue or SNS topic is kept; an S3
// bucket answers 501; a FIFO queue and anything else are refused with
// recorded texts. Whether the destination exists is checked only at
// delivery (inferred, 3.7).
func streamOnFailure(dest, noun string) (esm.OnFailure, *awserr.Error) {
	if dest == "" {
		return esm.NoDestination{}, nil
	}
	a, err := aws.ParseARN(dest)
	if err != nil {
		return nil, errUnsupportedDestination()
	}
	switch a.Service {
	case "sqs":
		if strings.HasSuffix(a.Resource, ".fifo") {
			return nil, errFIFOStreamDestination()
		}
		return esm.QueueDestination{ARN: a}, nil
	case "sns":
		return esm.TopicDestination{ARN: a}, nil
	case "s3":
		return nil, errStreamSettingUnbuilt("an S3 on-failure destination", noun)
	}
	return nil, errUnsupportedDestination()
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

// errStreamMissing is row 4 of lambda-stream-sources.md 3.3 (recorded,
// LS-D:L4478-L4494).
func errStreamMissing(stream aws.ARN) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Stream not found: " + stream.String()))
}

// newStreamSource is the source that reads stream, a mapping of kind's
// source, on behalf of parent through target, as role: a dynamoSource or a
// kinesisSource.
func newStreamSource(k *kernel.Kernel, parent *kernel.Call, kind esm.EventSourceKind, target kernel.Target, stream, role aws.ARN) esm.StreamSource {
	switch kind.(type) {
	case esm.KinesisStream:
		return kinesisSource{k: k, parent: parent, target: target, stream: stream, role: role}
	case esm.DynamoDBStream, esm.SQS:
	}
	return dynamoSource{k: k, parent: parent, target: target, stream: stream}
}

// streamSource is the stream of kind as the control plane reads it, as
// the function's execution role; false when the stream is not one this
// emulator can address from c's region.
func (s *Service) streamSource(c *kernel.Call, kind esm.EventSourceKind, stream aws.ARN, target mappingTarget) (esm.StreamSource, bool) {
	role, perr := aws.ParseARN(target.role)
	if perr != nil || stream.Region != c.Scope().Region.String() {
		return nil, false
	}
	t, ok := streamTarget(stream, role)
	if !ok {
		return nil, false
	}
	return newStreamSource(s.k, c, kind, t, stream, role), true
}

// checkStream describes the stream of kind as the function's execution
// role, through DescribeStream for a DynamoDB stream and
// DescribeStreamSummary for a Kinesis stream, and
// refuses one that does not exist with row 4 of 3.3; it returns the source
// it described, for a caller that reads on.
func (s *Service) checkStream(ctx context.Context, c *kernel.Call, kind esm.EventSourceKind, stream aws.ARN, target mappingTarget) (esm.StreamSource, *awserr.Error) {
	src, ok := s.streamSource(c, kind, stream, target)
	if !ok {
		return nil, errStreamMissing(stream)
	}
	if _, kinesis := kind.(esm.KinesisStream); kinesis {
		_, err := src.Retention(ctx)
		return src, fromStreamRead("DescribeStreamSummary", stream, err)
	}
	_, err := src.Shards(ctx)
	return src, fromStreamRead("DescribeStream", stream, err)
}

// fromStreamRead maps a failed read of stream by the control plane onto
// Lambda's model: a stream that is gone is row 4 of 3.3; a read
// iam.enforce refused the execution role is switch
// lambda.esm.create-denied-text's sentence naming the refused operation on
// DynamoDB or Kinesis (iam-enforcement-surfaces.md 8.6); an emulator bug or an
// aborted request stays as it is; any other answer of op is wrapped with
// its code and message, as the Lambda client must never see a Streams or
// Kinesis error shape (the Kinesis form mirrors the DynamoDB one,
// understudy's).
func fromStreamRead(op string, stream aws.ARN, err error) *awserr.Error {
	if err == nil {
		return nil
	}
	service, code := "DynamoDB", "DynamoDB Streams Error Code"
	if stream.Service == "kinesis" {
		service, code = "Kinesis", "Kinesis Error Code"
	}
	if _, gone := errors.AsType[*esm.GoneError](err); gone {
		return errStreamMissing(stream)
	}
	if denied, ok := errors.AsType[*deniedReadError](err); ok {
		return errESMRoleDenied(denied.Op, service)
	}
	boundary, ok := errors.AsType[*awserr.BoundaryError](err)
	if !ok {
		return awserr.Internalf(err)
	}
	e := boundary.Err
	if _, internal := e.Origin.(awserr.Internal); internal || e.ID == awserr.RequestAbortedException() {
		return e
	}
	return user(lambdaapi.ErrInvalidParameterValueException("Error occurred while " + op + ". " + code + ": " + e.ID.JSONType + ". Error Message: " + e.Message))
}

// anchorLatest resolves LATEST for every shard of src: the position just
// after the shard's newest record, so the records written after it are
// read and none before (lambda.esm-stream.latest-anchor, inferred,
// ESMS-17).
func anchorLatest(ctx context.Context, src esm.StreamSource) ([]esm.Checkpoint, error) {
	shards, err := src.Shards(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]esm.Checkpoint, 0, len(shards))
	for _, shard := range shards {
		tip, err := shardTip(ctx, src, shard)
		if err != nil {
			return nil, err
		}
		out = append(out, esm.Checkpoint{Shard: shard, Next: tip})
	}
	return out, nil
}

// shardTip reads shard from TRIM_HORIZON to its end and returns the
// position after its newest record, TRIM_HORIZON when it holds none. A
// LATEST iterator would not do: it names no record, so the reader would
// skip what arrives between this read and its next.
func shardTip(ctx context.Context, src esm.StreamSource, shard string) (shardlog.Position, error) {
	var pos shardlog.Position = shardlog.TrimHorizon{}
	for {
		page, err := src.Read(ctx, shard, pos, getRecordsLimit)
		if err != nil {
			return nil, err
		}
		if len(page.Records) == 0 {
			return pos, nil
		}
		pos = page.Next
		if page.Closed {
			return pos, nil
		}
	}
}

// errStreamMappingExists is the recorded duplicate text for a stream
// mapping (lambda-stream-sources.md 3.3 row 5, LS-D:L557-L568).
func errStreamMappingExists(stream, function, uuid string) *awserr.Error {
	return user(lambdaapi.ErrResourceConflictException(`The event source arn (" ` + stream + ` ") and function (" ` + function +
		` ") provided mapping already exists. Please update or delete the existing mapping with UUID ` + uuid))
}

// createStreamMapping creates a mapping on a DynamoDB or Kinesis stream,
// kind telling which, checking in this order, which decides the refusal a
// caller sees: StartingPosition, the settings, the function, the stream
// as the execution role, then a duplicate. LATEST is resolved before
// Create answers under the create-instant anchor.
func (s *Service) createStreamMapping(ctx context.Context, c *kernel.Call, in *lambdaapi.CreateEventSourceMappingInput, stream aws.ARN, kind esm.EventSourceKind) (*lambdaapi.EventSourceMappingConfiguration, *awserr.Error) {
	start, err := streamStart(in.StartingPosition, stream, kind)
	if err != nil {
		return nil, err
	}
	settings := esm.DefaultStreamSettings()
	m := Mapping{Source: stream.String(), BatchSize: defaultStreamBatchSize, Enabled: in.Enabled == nil || *in.Enabled, Tags: maps.Clone(in.Tags)}
	if err := createStreamSettings(in).apply(&m, &settings, streamNoun(kind)); err != nil {
		return nil, err
	}
	m.SourceKind = withStream(kind, start, settings)
	target, err := s.mappingFunction(c, in.FunctionName)
	if err != nil {
		return nil, err
	}
	src, err := s.checkStream(ctx, c, kind, stream, target)
	if err != nil {
		return nil, err
	}
	var anchors []esm.Checkpoint
	if _, latest := start.(esm.Latest); latest && s.opts.Streams.LatestAnchor == esm.AnchorCreateInstant {
		cps, aerr := anchorLatest(ctx, src)
		if err := fromStreamRead("GetRecords", stream, aerr); err != nil {
			return nil, err
		}
		anchors = cps
	}
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
		for _, cp := range anchors {
			st.setCheckpoint(m.UUID, cp.Shard, cp.Next)
		}
		return renderMapping(target.ref.scope, m), nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, mappingARN(target.ref.scope, m.UUID), target.ref.qualifiedARN())
	return &out, nil
}

// updateStreamMapping updates the stream mapping m: its settings as Create
// checks them, its function (checked against the stream as the new
// function's execution role), and Enabled. A DestinationConfig with an
// empty on-failure Destination clears it; one left out keeps it
// (lambda-stream-sources.md 12.4 item 8).
func (s *Service) updateStreamMapping(ctx context.Context, c *kernel.Call, in *lambdaapi.UpdateEventSourceMappingInput, m Mapping) (*lambdaapi.EventSourceMappingConfiguration, *awserr.Error) {
	settings := updateStreamSettings(in)
	if _, set, ok := esm.StreamOf(m.SourceKind); ok {
		if err := settings.apply(&m, &set, streamNoun(m.SourceKind)); err != nil {
			return nil, err
		}
	}
	target, err := s.retargetStream(ctx, c, m.SourceKind, m.Source, in.FunctionName)
	if err != nil {
		return nil, err
	}
	scope := c.Scope()
	out, err := write(s, c, scope, func(st *State, now clock.Instant, tx *kernel.Tx) (lambdaapi.EventSourceMappingConfiguration, *awserr.Error) {
		m, ok := st.Mappings[in.UUID]
		if !ok {
			return lambdaapi.EventSourceMappingConfiguration{}, errResourceNotFound()
		}
		if m.Transition != nil {
			return lambdaapi.EventSourceMappingConfiguration{}, errMappingInUse(m.UUID, m.Transition.State)
		}
		start, set, ok := esm.StreamOf(m.SourceKind)
		if !ok {
			return lambdaapi.EventSourceMappingConfiguration{}, awserr.Internalf(errors.New("mapping " + m.UUID + " stopped being a stream mapping during its update"))
		}
		m.Metrics = slices.Clone(m.Metrics)
		if err := settings.apply(&m, &set, streamNoun(m.SourceKind)); err != nil {
			return lambdaapi.EventSourceMappingConfiguration{}, err
		}
		m.SourceKind = withStream(m.SourceKind, start, set)
		if target != nil {
			m.Function, m.Qualifier = target.ref.name, target.ref.qualifier
		}
		state := toggle(&m, in.Enabled)
		m.Modified = now
		s.transition(tx, scope, &m, state, now)
		st.Mappings[m.UUID] = m
		return renderMapping(scope, m), nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, mappingARN(scope, in.UUID))
	return &out, nil
}

// retargetStream resolves the function an update moves a mapping of
// source, a stream of kind, to and checks the stream as that function's
// execution role; nil when name is nil.
func (s *Service) retargetStream(ctx context.Context, c *kernel.Call, kind esm.EventSourceKind, source string, name *string) (*mappingTarget, *awserr.Error) {
	if name == nil {
		return nil, nil
	}
	t, err := s.mappingFunction(c, *name)
	if err != nil {
		return nil, err
	}
	stream, perr := aws.ParseARN(source)
	if perr != nil {
		return nil, awserr.Internalf(perr)
	}
	if _, err := s.checkStream(ctx, c, kind, stream, t); err != nil {
		return nil, err
	}
	return &t, nil
}

// renderStreamMapping completes out with the members a stream mapping
// starting at start with settings reports (lambda-stream-sources.md 3.5,
// recorded LS-D:L246-L271 and LS-K:L5-L29).
func renderStreamMapping(out *lambdaapi.EventSourceMappingConfiguration, m *Mapping, start esm.StreamStart, settings esm.StreamSettings) {
	out.StartingPosition = new(lambdaapi.EventSourcePosition(esm.StreamStartName(start)))
	out.StateTransitionReason = new(streamTransitionReason)
	out.BisectBatchOnFunctionError = new(false)
	out.ParallelizationFactor = new(int32(1))
	out.TumblingWindowInSeconds = new(int32(0))
	out.MaximumRecordAgeInSeconds = new(int32(-1))
	if age, ok := settings.RecordAge.(esm.MaxAge); ok {
		out.MaximumRecordAgeInSeconds = new(age.Seconds)
	}
	out.MaximumRetryAttempts = new(int32(-1))
	if r, ok := settings.Retries.(esm.Attempts); ok {
		out.MaximumRetryAttempts = new(r.N)
	}
	out.DestinationConfig = &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{}}
	switch d := settings.OnFailure.(type) {
	case esm.QueueDestination:
		out.DestinationConfig.OnFailure.Destination = new(d.ARN.String())
	case esm.TopicDestination:
		out.DestinationConfig.OnFailure.Destination = new(d.ARN.String())
	case esm.NoDestination, nil:
	}
	result := "No records processed"
	if m.Processed {
		result = "OK"
	}
	out.LastProcessingResult = new(result)
}
