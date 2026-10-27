package lambda

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/ruler"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

// The SQS defaults and limits of an event source mapping
// (lambda-events.md 1.2).
const (
	defaultBatchSize  = 10
	maxFIFOBatchSize  = 10
	sqsBatchWindowCap = 10
	// maxFilters is the default quota of filters per mapping; AWS raises
	// it to 10 on request, which understudy does not model.
	maxFilters = 5
)

// maxMappingsPage is the most mappings one ListEventSourceMappings page
// holds.
const maxMappingsPage = 100

// userInitiated is the StateTransitionReason every mapping reports.
const userInitiated = "USER_INITIATED"

// Mapping is an event source mapping.
type Mapping struct {
	UUID string `json:"uuid"`
	// Source is the source's ARN: the queue's for SQS.
	Source string `json:"source"`
	// SourceKind is the kind of source Source names, never nil once
	// createMapping has stored the mapping.
	SourceKind esm.EventSourceKind `json:"sourceKind"`
	// Function is the function's name; Qualifier its version or alias, ""
	// when the mapping names the function unqualified.
	Function  string `json:"function"`
	Qualifier string `json:"qualifier,omitempty"`
	BatchSize int32  `json:"batchSize"`
	Window    int32  `json:"window"`
	// Filters are the filter patterns as sent; nil when FilterCriteria is
	// unset, which the responses then leave out.
	Filters []string `json:"filters,omitempty"`
	// ReportFailures is FunctionResponseTypes [ReportBatchItemFailures].
	ReportFailures bool              `json:"reportFailures,omitzero"`
	KMSKeyArn      string            `json:"kmsKeyArn,omitempty"`
	Metrics        []string          `json:"metrics,omitempty"`
	Enabled        bool              `json:"enabled"`
	Transition     *Transition       `json:"transition,omitempty"`
	Modified       clock.Instant     `json:"modified"`
	Tags           map[string]string `json:"tags,omitempty"`
	// Processed is set once a stream mapping's batch succeeded, which
	// turns its LastProcessingResult from "No records processed" to "OK"
	// (recorded LS-K:L3066, LS-D:L184).
	Processed bool `json:"processed,omitzero"`
}

// Transition is a mapping's transient state under TransitionsClock, until
// Settles; nil means the mapping is settled in Enabled or Disabled.
type Transition struct {
	State   MappingState  `json:"state"`
	Settles clock.Instant `json:"settles"`
}

// MappingState is an event source mapping's State, which the model types
// as a plain string (lambda-events.md 1.5).
type MappingState string

// The documented mapping states.
const (
	MappingCreating  MappingState = "Creating"
	MappingEnabling  MappingState = "Enabling"
	MappingEnabled   MappingState = "Enabled"
	MappingDisabling MappingState = "Disabling"
	MappingDisabled  MappingState = "Disabled"
	MappingUpdating  MappingState = "Updating"
	MappingDeleting  MappingState = "Deleting"
)

// state is the State a mapping reports.
func (m Mapping) state() MappingState {
	switch {
	case m.Transition != nil:
		return m.Transition.State
	case m.Enabled:
		return MappingEnabled
	}
	return MappingDisabled
}

func (s *Service) bindMappings(b lambdaapi.Binder) {
	const note = "SQS queues and DynamoDB streams; serve polls the source and invokes the function with each batch; " +
		"Kinesis streams, ParallelizationFactor above 1, BisectBatchOnFunctionError, TumblingWindowInSeconds above 0, a valid FilterCriteria on a stream and an S3 on-failure destination answer 501; " +
		"a stream is read as soon as it receives a record rather than at AWS's polling rate (4 per second for DynamoDB, once per second for Kinesis), which is used as the delay before a retry; " +
		"UNVERIFIED (lambda-stream-sources.md 3.3, 3.4, 3.7, 4.2, 5.1, 5.3, 5.5, 5.6): the text refusing MaximumRecordAgeInSeconds 0 to 59, an explicit BatchSize above 10 without a window being accepted on a stream, " +
		"members of the other source type being ignored, destination existence checked only at delivery, a retried batch keeping its records, the 6 MB limit counted over the serialized event, " +
		"record age measured from the record's arrival in the stream, RetryAttemptsExhausted reported when the last retry fails after the records have also passed their age, one on-failure record per expired record, " +
		"an itemIdentifier outside the batch counting as complete failure, batchItemFailures keys matched case-sensitively, records lost to stream retention dropped without an on-failure record, " +
		"an on-failure record the execution role may not send dropped with a journal entry, LastProcessingResult unchanged by a failure, and on-failure messages without attributes; " +
		"MetricsConfig is stored, no metrics are emitted; " +
		"under iam.enforce=enforce an SQS mapping's execution role must be allowed sqs:GetQueueAttributes on the queue, where AWS is said to check sqs:ReceiveMessage, " +
		"and a stream mapping's dynamodb:DescribeStream on the stream, and at LATEST dynamodb:GetShardIterator and dynamodb:GetRecords (lambda.esm.create-denied-text); " +
		"a role lacking only dynamodb:ListStreams, or at TRIM_HORIZON only dynamodb:GetShardIterator or dynamodb:GetRecords, is accepted, where whether AWS refuses it is UNVERIFIED (iam-enforcement-surfaces.md 8.6); " +
		"a role refused its polls or stream reads leaves the mapping Enabled and invoking nothing (lambda.esm.permission-loss)"
	b.CreateEventSourceMapping(s.createMapping, partial(note), kernel.WithValidator(validateCreateMapping))
	b.GetEventSourceMapping(s.getMapping, full())
	b.ListEventSourceMappings(s.listMappings, full())
	b.UpdateEventSourceMapping(s.updateMapping, partial(note))
	b.DeleteEventSourceMapping(s.deleteMapping, full())
}

// mappingSettings are the members Create and Update share.
type mappingSettings struct {
	batchSize, window *int32
	filters           *lambdaapi.FilterCriteria
	scaling           *lambdaapi.ScalingConfig
	pollers           *lambdaapi.ProvisionedPollerConfig
	destinations      *lambdaapi.DestinationConfig
	responseTypes     []lambdaapi.FunctionResponseType
	kmsKeyArn         *string
	metrics           *lambdaapi.EventSourceMappingMetricsConfig
}

// apply sets what u carries on m, then checks the result in the order AWS
// reports (lambda-events.md 1.3): destinations, filters, batching.
func (u mappingSettings) apply(m *Mapping) *awserr.Error {
	if d := u.destinations; d != nil {
		if success, failure := destinations(d); success != "" || failure != "" {
			return errUnsupportedDestination()
		}
	}
	if u.filters != nil {
		filters, err := filterPatterns(u.filters)
		if err != nil {
			return err
		}
		m.Filters = filters
	}
	if err := u.applyBatching(m); err != nil {
		return err
	}
	if err := u.applyScaling(m); err != nil {
		return err
	}
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

// filterPatterns checks the filter count against the default quota and
// each filter pattern with Lambda's rules. An empty FilterCriteria yields
// no patterns, which clears the mapping's filters.
func filterPatterns(fc *lambdaapi.FilterCriteria) ([]string, *awserr.Error) {
	if len(fc.Filters) > maxFilters {
		return nil, errTooManyFilters(len(fc.Filters))
	}
	var out []string
	for _, f := range fc.Filters {
		if f.Pattern == nil {
			return nil, errInvalidFilter()
		}
		if _, err := ruler.Compile([]byte(*f.Pattern), ruler.LambdaESM{}); err != nil {
			return nil, errInvalidFilter()
		}
		out = append(out, *f.Pattern)
	}
	return out, nil
}

// applyBatching sets the batch size and window and checks them together:
// a batch above 10 needs a window, and a FIFO queue's batch is at most 10.
func (u mappingSettings) applyBatching(m *Mapping) *awserr.Error {
	set(&m.BatchSize, u.batchSize)
	set(&m.Window, u.window)
	if m.BatchSize > sqsBatchWindowCap && m.Window == 0 {
		return errBatchWindow()
	}
	if strings.HasSuffix(m.Source, ".fifo") && m.BatchSize > maxFIFOBatchSize {
		return errFIFOBatchSize(m.BatchSize)
	}
	return nil
}

// applyScaling sets an SQS mapping's ScalingConfig or
// ProvisionedPollerConfig, never both; an empty ScalingConfig removes the
// bound.
func (u mappingSettings) applyScaling(m *Mapping) *awserr.Error {
	kind := esm.SQS{Scaling: esm.Unbounded{}}
	if k, ok := m.SourceKind.(esm.SQS); ok && k.Scaling != nil {
		kind = k
	}
	switch {
	case u.scaling != nil && u.pollers != nil:
		return errPollerConfigs()
	case u.scaling != nil && u.scaling.MaximumConcurrency != nil:
		kind.Scaling = esm.MaximumConcurrency{N: *u.scaling.MaximumConcurrency}
	case u.scaling != nil:
		kind.Scaling = esm.Unbounded{}
	case u.pollers != nil:
		kind.Scaling = esm.ProvisionedPollers{Minimum: u.pollers.MinimumPollers, Maximum: u.pollers.MaximumPollers, Group: deref(u.pollers.PollerGroupName)}
	}
	m.SourceKind = kind
	return nil
}

// mappingTarget is the function a mapping invokes, read before the cell
// lock so the queue can be checked against it.
type mappingTarget struct {
	ref     functionRef
	role    string
	timeout int32
}

// mappingFunction resolves a mapping's FunctionName: 400 "Function does
// not exist" when the function, version or alias is missing
// (lambda-events.md 1.3).
func (s *Service) mappingFunction(c *kernel.Call, name string) (mappingTarget, *awserr.Error) {
	fn, err := parseFunctionRef(c, name, nil)
	if err != nil {
		return mappingTarget{}, err
	}
	return view(s, fn.scope, func(st *State) (mappingTarget, *awserr.Error) {
		_, v, err := st.lookup(fn)
		if err != nil {
			return mappingTarget{}, errMappingFunctionMissing()
		}
		return mappingTarget{ref: fn, role: v.Config.Role, timeout: v.Config.Timeout}, nil
	})
}

// checkQueue reads the queue's visibility timeout as the function's
// execution role and refuses a missing queue or one whose visibility
// timeout is shorter than the function's timeout (lambda-events.md 1.3).
// The queue's URL is how understudy addresses it, which needs no
// permission in AWS, so Lambda reads it as itself: the role needs only
// sqs:ReceiveMessage, sqs:DeleteMessage and sqs:GetQueueAttributes
// (iam-enforcement-surfaces.md 8.1). A role that may not read the queue is
// refused with errESMRoleDenied.
func (s *Service) checkQueue(ctx context.Context, c *kernel.Call, queue aws.ARN, target mappingTarget) *awserr.Error {
	scope, serr := aws.ScopeOf(queue)
	if serr != nil || queue.Region != c.Scope().Region.String() {
		return errQueueMissing()
	}
	role, perr := aws.ParseARN(target.role)
	if perr != nil {
		return errQueueMissing()
	}
	platform := kernel.Target{Scope: scope, As: kernel.AsPlatform{Name: lambdaService}, Via: ref.LambdaESM}
	url, err := kernel.Invoke(ctx, s.k, c, sqsapi.OpGetQueueUrl, platform, &sqsapi.GetQueueUrlInput{QueueName: queue.Resource, QueueOwnerAWSAccountId: new(queue.Account)})
	if err != nil {
		return fromSQS(err)
	}
	t := kernel.Target{Scope: scope, As: kernel.AsExecutionRole{Role: role}, Via: ref.LambdaESM}
	attrs, err := kernel.Invoke(ctx, s.k, c, sqsapi.OpGetQueueAttributes, t, &sqsapi.GetQueueAttributesInput{
		QueueUrl: deref(url.QueueUrl), AttributeNames: []sqsapi.QueueAttributeName{sqsapi.QueueAttributeNameVisibilityTimeout},
	})
	if err != nil {
		if _, denied := err.Origin.(awserr.Denial); denied {
			return errESMRoleDenied("ReceiveMessage", "SQS")
		}
		return fromSQS(err)
	}
	visibility, perr := strconv.Atoi(attrs.Attributes[sqsapi.QueueAttributeNameVisibilityTimeout])
	if perr != nil {
		return awserr.Internalf(perr)
	}
	if visibility < int(target.timeout) {
		return errVisibilityTimeout(visibility, int(target.timeout))
	}
	return nil
}

// fromSQS maps an SQS error onto Lambda's model: a missing queue in its
// recorded words, any other SQS answer (an injected fault or throttle
// included) wrapped with its code and message, as the Lambda client must
// never see an SQS error shape. An emulator bug or an aborted request
// stays as it is.
func fromSQS(err *awserr.Error) *awserr.Error {
	if _, internal := err.Origin.(awserr.Internal); internal || err.ID == awserr.RequestAbortedException() {
		return err
	}
	if err.ID == sqsapi.IdentityQueueDoesNotExist() {
		return errQueueMissing()
	}
	code := err.ID.QueryCode
	if code == "" {
		code = err.ID.JSONType
	}
	return errSQSFailure(code, err.Message)
}

// eventSource reads EventSourceArn and the kind of source it names: an
// SQS queue's ARN, or a DynamoDB stream's, whose kind createStreamMapping
// completes with its StartingPosition. Another service's source is
// unimplemented; none is "Unrecognized event source.".
func eventSource(arn *string) (aws.ARN, esm.EventSourceKind, *awserr.Error) {
	if arn == nil {
		return aws.ARN{}, nil, errUnrecognizedEventSource()
	}
	a, err := aws.ParseARN(*arn)
	if err != nil {
		return aws.ARN{}, nil, errUnrecognizedEventSource()
	}
	if isDynamoDBStream(a) {
		return a, esm.DynamoDBStream{}, nil
	}
	kind, ok := esm.SourceKindOf(a)
	if !ok {
		return aws.ARN{}, nil, errEventSourceUnsupported(*arn)
	}
	return a, kind, nil
}

func (s *Service) createMapping(ctx context.Context, c *kernel.Call, in *lambdaapi.CreateEventSourceMappingInput) (*lambdaapi.EventSourceMappingConfiguration, *awserr.Error) {
	queue, kind, err := eventSource(in.EventSourceArn)
	if err != nil {
		return nil, err
	}
	switch kind.(type) {
	case esm.DynamoDBStream:
		return s.createStreamMapping(ctx, c, in, queue)
	case esm.SQS:
	}
	m := Mapping{Source: queue.String(), SourceKind: kind, BatchSize: defaultBatchSize, Enabled: in.Enabled == nil || *in.Enabled, Tags: maps.Clone(in.Tags)}
	settings := mappingSettings{
		batchSize: in.BatchSize, window: in.MaximumBatchingWindowInSeconds, filters: in.FilterCriteria, scaling: in.ScalingConfig,
		pollers: in.ProvisionedPollerConfig, destinations: in.DestinationConfig, responseTypes: in.FunctionResponseTypes,
		kmsKeyArn: in.KMSKeyArn, metrics: in.MetricsConfig,
	}
	if err := settings.apply(&m); err != nil {
		return nil, err
	}
	target, err := s.mappingFunction(c, in.FunctionName)
	if err != nil {
		return nil, err
	}
	if err := s.checkQueue(ctx, c, queue, target); err != nil {
		return nil, err
	}
	m.UUID, m.Function, m.Qualifier = string(s.ids.ESMID()), target.ref.name, target.ref.qualifier
	out, err := write(s, c, target.ref.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (lambdaapi.EventSourceMappingConfiguration, *awserr.Error) {
		for _, uuid := range slices.Sorted(maps.Keys(st.Mappings)) {
			if other := st.Mappings[uuid]; other.Source == m.Source && other.Function == m.Function && other.Qualifier == m.Qualifier {
				return lambdaapi.EventSourceMappingConfiguration{}, errMappingExists(m.Source, m.Function, uuid)
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

func (s *Service) getMapping(_ context.Context, c *kernel.Call, in *lambdaapi.GetEventSourceMappingInput) (*lambdaapi.EventSourceMappingConfiguration, *awserr.Error) {
	scope := c.Scope()
	return view(s, scope, func(st *State) (*lambdaapi.EventSourceMappingConfiguration, *awserr.Error) {
		m, ok := st.Mappings[in.UUID]
		if !ok {
			return nil, errResourceNotFound()
		}
		return new(renderMapping(scope, m)), nil
	})
}

// listMappings filters by queue and function without checking that
// either exists, as AWS does (lambda-events.md 1.1), and leaves out the
// FilterCriteria of a mapping with a KMSKeyArn.
func (s *Service) listMappings(_ context.Context, c *kernel.Call, in *lambdaapi.ListEventSourceMappingsInput) (*lambdaapi.ListEventSourceMappingsOutput, *awserr.Error) {
	scope := c.Scope()
	var fn *functionRef
	if in.FunctionName != nil {
		named, err := parseFunctionRef(c, *in.FunctionName, nil)
		if err != nil {
			return nil, err
		}
		fn = &named
	}
	rows, _ := view(s, scope, func(st *State) ([]lambdaapi.EventSourceMappingConfiguration, *awserr.Error) {
		var out []lambdaapi.EventSourceMappingConfiguration
		for _, uuid := range slices.Sorted(maps.Keys(st.Mappings)) {
			m := st.Mappings[uuid]
			if (in.EventSourceArn == nil || m.Source == *in.EventSourceArn) && (fn == nil || (m.Function == fn.name && m.Qualifier == fn.qualifier)) {
				row := renderMapping(scope, m)
				// Filters encrypted with a customer key read as null in a
				// listing; Get and Delete show them (lambda-events.md 1.2).
				if m.KMSKeyArn != "" {
					row.FilterCriteria = nil
				}
				out = append(out, row)
			}
		}
		return out, nil
	})
	p, err := page(listingEventSourceMappings, rows, func(m lambdaapi.EventSourceMappingConfiguration) string { return *m.UUID }, in.MaxItems, maxMappingsPage, in.Marker)
	if err != nil {
		return nil, err
	}
	return &lambdaapi.ListEventSourceMappingsOutput{EventSourceMappings: append([]lambdaapi.EventSourceMappingConfiguration{}, p.Items...), NextMarker: p.Next}, nil
}

// retarget resolves the function an update moves the mapping uuid to and
// checks the mapping's queue against it; nil when name is nil.
func (s *Service) retarget(ctx context.Context, c *kernel.Call, uuid string, name *string) (*mappingTarget, *awserr.Error) {
	source, err := view(s, c.Scope(), func(st *State) (string, *awserr.Error) {
		m, ok := st.Mappings[uuid]
		if !ok {
			return "", errResourceNotFound()
		}
		return m.Source, nil
	})
	if err != nil || name == nil {
		return nil, err
	}
	t, err := s.mappingFunction(c, *name)
	if err != nil {
		return nil, err
	}
	queue, perr := aws.ParseARN(source)
	if perr != nil {
		return nil, awserr.Internalf(perr)
	}
	if err := s.checkQueue(ctx, c, queue, t); err != nil {
		return nil, err
	}
	return &t, nil
}

// toggle sets the mapping's Enabled and returns the transition an update
// starts: Enabling or Disabling when Enabled changes, Updating otherwise.
func toggle(m *Mapping, enabled *bool) MappingState {
	switch {
	case enabled == nil || *enabled == m.Enabled:
		return MappingUpdating
	case *enabled:
		m.Enabled = true
		return MappingEnabling
	}
	m.Enabled = false
	return MappingDisabling
}

func (s *Service) updateMapping(ctx context.Context, c *kernel.Call, in *lambdaapi.UpdateEventSourceMappingInput) (*lambdaapi.EventSourceMappingConfiguration, *awserr.Error) {
	scope := c.Scope()
	current, err := view(s, scope, func(st *State) (Mapping, *awserr.Error) {
		return st.Mappings[in.UUID], nil
	})
	if err != nil {
		return nil, err
	}
	if _, stream := current.SourceKind.(esm.DynamoDBStream); stream {
		return s.updateStreamMapping(ctx, c, in, current)
	}
	target, err := s.retarget(ctx, c, in.UUID, in.FunctionName)
	if err != nil {
		return nil, err
	}
	settings := mappingSettings{
		batchSize: in.BatchSize, window: in.MaximumBatchingWindowInSeconds, filters: in.FilterCriteria, scaling: in.ScalingConfig,
		pollers: in.ProvisionedPollerConfig, destinations: in.DestinationConfig, responseTypes: in.FunctionResponseTypes,
		kmsKeyArn: in.KMSKeyArn, metrics: in.MetricsConfig,
	}
	out, err := write(s, c, scope, func(st *State, now clock.Instant, tx *kernel.Tx) (lambdaapi.EventSourceMappingConfiguration, *awserr.Error) {
		m, ok := st.Mappings[in.UUID]
		if !ok {
			return lambdaapi.EventSourceMappingConfiguration{}, errResourceNotFound()
		}
		if m.Transition != nil {
			return lambdaapi.EventSourceMappingConfiguration{}, errMappingInUse(m.UUID, m.Transition.State)
		}
		m.Filters, m.Metrics = slices.Clone(m.Filters), slices.Clone(m.Metrics)
		if err := settings.apply(&m); err != nil {
			return lambdaapi.EventSourceMappingConfiguration{}, err
		}
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

// deleteMapping answers with the mapping in Deleting. With final
// transitions it is gone when the call returns; under TransitionsClock
// once the settle timer fires.
func (s *Service) deleteMapping(_ context.Context, c *kernel.Call, in *lambdaapi.DeleteEventSourceMappingInput) (*lambdaapi.EventSourceMappingConfiguration, *awserr.Error) {
	scope := c.Scope()
	out, err := write(s, c, scope, func(st *State, now clock.Instant, tx *kernel.Tx) (lambdaapi.EventSourceMappingConfiguration, *awserr.Error) {
		m, ok := st.Mappings[in.UUID]
		if !ok {
			return lambdaapi.EventSourceMappingConfiguration{}, errResourceNotFound()
		}
		if m.Transition != nil {
			return lambdaapi.EventSourceMappingConfiguration{}, errMappingInUse(m.UUID, m.Transition.State)
		}
		s.transition(tx, scope, &m, MappingDeleting, now)
		if m.Transition == nil {
			st.dropMapping(m.UUID)
			m.Transition = &Transition{State: MappingDeleting, Settles: now}
		} else {
			st.Mappings[m.UUID] = m
		}
		return renderMapping(scope, m), nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, mappingARN(scope, in.UUID))
	return &out, nil
}

// renderMapping reports a mapping as AWS does for SQS (lambda-events.md
// 1.4): FunctionResponseTypes and MaximumBatchingWindowInSeconds always,
// FilterCriteria and the scaling configurations only when set.
func renderMapping(scope aws.Scope, m Mapping) lambdaapi.EventSourceMappingConfiguration {
	fn := functionARN(scope, m.Function)
	if m.Qualifier != "" {
		fn += ":" + m.Qualifier
	}
	out := lambdaapi.EventSourceMappingConfiguration{
		UUID:                           new(m.UUID),
		BatchSize:                      new(m.BatchSize),
		MaximumBatchingWindowInSeconds: new(m.Window),
		EventSourceArn:                 new(m.Source),
		FunctionArn:                    new(fn),
		LastModified:                   new(m.Modified.Wire()),
		State:                          new(string(m.state())),
		StateTransitionReason:          new(userInitiated),
		FunctionResponseTypes:          []lambdaapi.FunctionResponseType{},
		EventSourceMappingArn:          new(mappingARN(scope, m.UUID)),
	}
	if m.ReportFailures {
		out.FunctionResponseTypes = append(out.FunctionResponseTypes, lambdaapi.FunctionResponseTypeReportBatchItemFailures)
	}
	if m.Filters != nil {
		out.FilterCriteria = &lambdaapi.FilterCriteria{Filters: []lambdaapi.Filter{}}
		for _, p := range m.Filters {
			out.FilterCriteria.Filters = append(out.FilterCriteria.Filters, lambdaapi.Filter{Pattern: new(p)})
		}
	}
	if m.KMSKeyArn != "" {
		out.KMSKeyArn = new(m.KMSKeyArn)
	}
	if m.Metrics != nil {
		out.MetricsConfig = &lambdaapi.EventSourceMappingMetricsConfig{}
		for _, metric := range m.Metrics {
			out.MetricsConfig.Metrics = append(out.MetricsConfig.Metrics, lambdaapi.EventSourceMappingMetric(metric))
		}
	}
	switch kind := m.SourceKind.(type) {
	case esm.SQS:
		renderScaling(&out, kind.Scaling)
	case esm.DynamoDBStream:
		renderStreamMapping(&out, &m, kind)
	}
	return out
}

// renderScaling reports an SQS mapping's ScalingConfig or
// ProvisionedPollerConfig, only when set.
func renderScaling(out *lambdaapi.EventSourceMappingConfiguration, s esm.Scaling) {
	switch sc := s.(type) {
	case esm.MaximumConcurrency:
		out.ScalingConfig = &lambdaapi.ScalingConfig{MaximumConcurrency: new(sc.N)}
	case esm.ProvisionedPollers:
		out.ProvisionedPollerConfig = &lambdaapi.ProvisionedPollerConfig{MinimumPollers: sc.Minimum, MaximumPollers: sc.Maximum}
		if sc.Group != "" {
			out.ProvisionedPollerConfig.PollerGroupName = new(sc.Group)
		}
	case esm.Unbounded, nil:
	}
}
