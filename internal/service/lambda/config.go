package lambda

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// Config is a version's configuration: what CreateFunction and
// UpdateFunctionConfiguration set, with AWS's defaults filled in
// (lambda-api.md 4.5), so a read echoes exactly what Terraform wrote.
type Config struct {
	// Runtime and Handler are empty for an image function.
	Runtime          string `json:"runtime,omitempty"`
	Handler          string `json:"handler,omitempty"`
	Role             string `json:"role"`
	Description      string `json:"description"`
	Timeout          int32  `json:"timeout"`
	MemorySize       int32  `json:"memorySize"`
	Architecture     string `json:"architecture"`
	EphemeralStorage int32  `json:"ephemeralStorage"`
	// Environment is nil when the function has no variables; an empty map
	// is never stored, so a cleared environment reads as none.
	Environment      map[string]string `json:"environment,omitempty"`
	DeadLetterTarget string            `json:"deadLetterTarget,omitempty"`
	KMSKeyArn        string            `json:"kmsKeyArn,omitempty"`
	TracingMode      string            `json:"tracingMode"`
	// Vpc is nil when the function is not attached to a VPC.
	Vpc         *Vpc         `json:"vpc,omitempty"`
	FileSystems []FileSystem `json:"fileSystems,omitempty"`
	// Image holds the image settings that override the Dockerfile; nil
	// when none are set.
	Image     *ImageSettings `json:"image,omitempty"`
	SnapStart string         `json:"snapStart"`
	Logging   Logging        `json:"logging"`
}

// Vpc is a VPC attachment, stored and echoed.
type Vpc struct {
	SubnetIDs        []string `json:"subnetIds,omitempty"`
	SecurityGroupIDs []string `json:"securityGroupIds,omitempty"`
	IPv6DualStack    *bool    `json:"ipv6DualStack,omitempty"`
}

// FileSystem is an EFS mount, stored and echoed.
type FileSystem struct {
	ARN       string `json:"arn"`
	MountPath string `json:"mountPath"`
}

// ImageSettings override an image's ENTRYPOINT, CMD and WORKDIR.
type ImageSettings struct {
	EntryPoint       []string `json:"entryPoint,omitempty"`
	Command          []string `json:"command,omitempty"`
	WorkingDirectory *string  `json:"workingDirectory,omitempty"`
}

// Logging is the function's logging configuration. Group is empty for the
// default group, /aws/lambda/<function name>; the levels are empty unless
// set.
type Logging struct {
	Format           string `json:"format"`
	ApplicationLevel string `json:"applicationLevel,omitempty"`
	SystemLevel      string `json:"systemLevel,omitempty"`
	Group            string `json:"group,omitempty"`
}

// The defaults of an unset member (lambda-api.md 4.5).
const (
	defaultTimeout          = 3
	defaultMemorySize       = 128
	defaultEphemeralStorage = 512
)

// newConfig is the configuration CreateFunction stores.
func newConfig(in *lambdaapi.CreateFunctionInput) Config {
	c := Config{
		Role:             in.Role,
		Description:      deref(in.Description),
		Timeout:          defaultTimeout,
		MemorySize:       defaultMemorySize,
		Architecture:     string(lambdaapi.ArchitectureX8664),
		EphemeralStorage: defaultEphemeralStorage,
		TracingMode:      string(lambdaapi.TracingModePassThrough),
		SnapStart:        string(lambdaapi.SnapStartApplyOnNone),
		Logging:          Logging{Format: string(lambdaapi.LogFormatText)},
	}
	if in.Runtime != nil {
		c.Runtime = string(*in.Runtime)
	}
	c.Handler = deref(in.Handler)
	if len(in.Architectures) == 1 {
		c.Architecture = string(in.Architectures[0])
	}
	c.apply(configUpdate{
		Timeout: in.Timeout, MemorySize: in.MemorySize, Environment: in.Environment, DeadLetter: in.DeadLetterConfig,
		KMSKeyArn: in.KMSKeyArn, Tracing: in.TracingConfig, Vpc: in.VpcConfig, FileSystems: in.FileSystemConfigs,
		Image: in.ImageConfig, EphemeralStorage: in.EphemeralStorage, SnapStart: in.SnapStart, Logging: in.LoggingConfig,
	})
	return c
}

// configUpdate is the members CreateFunction and
// UpdateFunctionConfiguration share; nil leaves a setting as it is.
type configUpdate struct {
	Timeout          *int32
	MemorySize       *int32
	Environment      *lambdaapi.Environment
	DeadLetter       *lambdaapi.DeadLetterConfig
	KMSKeyArn        *string
	Tracing          *lambdaapi.TracingConfig
	Vpc              *lambdaapi.VpcConfig
	FileSystems      []lambdaapi.FileSystemConfig
	Image            *lambdaapi.ImageConfig
	EphemeralStorage *lambdaapi.EphemeralStorage
	SnapStart        *lambdaapi.SnapStart
	Logging          *lambdaapi.LoggingConfig
}

// apply sets what u carries. Each structure replaces the setting whole, as
// AWS does: Environment.Variables {} clears the environment, an empty
// DeadLetterConfig.TargetArn the queue, an empty ImageConfig the overrides
// and empty VPC lists the attachment (lambda-api.md 6.2).
func (c *Config) apply(u configUpdate) {
	set(&c.Timeout, u.Timeout)
	set(&c.MemorySize, u.MemorySize)
	set(&c.KMSKeyArn, u.KMSKeyArn)
	if u.Environment != nil {
		c.Environment = nil
		if len(u.Environment.Variables) > 0 {
			c.Environment = maps.Clone(u.Environment.Variables)
		}
	}
	if u.DeadLetter != nil {
		c.DeadLetterTarget = deref(u.DeadLetter.TargetArn)
	}
	if u.Tracing != nil && u.Tracing.Mode != nil {
		c.TracingMode = string(*u.Tracing.Mode)
	}
	if u.Vpc != nil {
		c.Vpc = vpcOf(u.Vpc)
	}
	if u.FileSystems != nil {
		c.FileSystems = nil
		for _, fs := range u.FileSystems {
			c.FileSystems = append(c.FileSystems, FileSystem{ARN: fs.Arn, MountPath: fs.LocalMountPath})
		}
	}
	if u.Image != nil {
		c.Image = imageSettingsOf(u.Image)
	}
	if u.EphemeralStorage != nil {
		c.EphemeralStorage = u.EphemeralStorage.Size
	}
	if u.SnapStart != nil && u.SnapStart.ApplyOn != nil {
		c.SnapStart = string(*u.SnapStart.ApplyOn)
	}
	if u.Logging != nil {
		c.Logging = loggingOf(u.Logging)
	}
}

// update applies an UpdateFunctionConfiguration: the members it carries
// replace the stored ones, the others stay.
func (c *Config) update(in *lambdaapi.UpdateFunctionConfigurationInput) {
	set(&c.Role, in.Role)
	set(&c.Handler, in.Handler)
	set(&c.Description, in.Description)
	if in.Runtime != nil {
		c.Runtime = string(*in.Runtime)
	}
	c.apply(configUpdate{
		Timeout: in.Timeout, MemorySize: in.MemorySize, Environment: in.Environment, DeadLetter: in.DeadLetterConfig,
		KMSKeyArn: in.KMSKeyArn, Tracing: in.TracingConfig, Vpc: in.VpcConfig, FileSystems: in.FileSystemConfigs,
		Image: in.ImageConfig, EphemeralStorage: in.EphemeralStorage, SnapStart: in.SnapStart, Logging: in.LoggingConfig,
	})
}

// set stores *v in dst when v is set.
func set[T any](dst, v *T) {
	if v != nil {
		*dst = *v
	}
}

// vpcOf is the attachment a VpcConfig asks for; nil for empty lists.
func vpcOf(v *lambdaapi.VpcConfig) *Vpc {
	if len(v.SubnetIds) == 0 && len(v.SecurityGroupIds) == 0 {
		return nil
	}
	return &Vpc{SubnetIDs: slices.Clone(v.SubnetIds), SecurityGroupIDs: slices.Clone(v.SecurityGroupIds), IPv6DualStack: v.Ipv6AllowedForDualStack}
}

// imageSettingsOf is the overrides an ImageConfig asks for; nil for {}.
func imageSettingsOf(i *lambdaapi.ImageConfig) *ImageSettings {
	if len(i.EntryPoint) == 0 && len(i.Command) == 0 && i.WorkingDirectory == nil {
		return nil
	}
	return &ImageSettings{EntryPoint: slices.Clone(i.EntryPoint), Command: slices.Clone(i.Command), WorkingDirectory: i.WorkingDirectory}
}

// loggingOf is the logging configuration a LoggingConfig asks for, with
// the Text format by default.
func loggingOf(l *lambdaapi.LoggingConfig) Logging {
	out := Logging{Format: string(lambdaapi.LogFormatText), Group: deref(l.LogGroup)}
	if l.LogFormat != nil {
		out.Format = string(*l.LogFormat)
	}
	if l.ApplicationLogLevel != nil {
		out.ApplicationLevel = string(*l.ApplicationLogLevel)
	}
	if l.SystemLogLevel != nil {
		out.SystemLevel = string(*l.SystemLogLevel)
	}
	return out
}

// clone returns a copy that shares no map or slice with c.
func (c *Config) clone() Config {
	out := *c
	out.Environment = maps.Clone(c.Environment)
	out.FileSystems = slices.Clone(c.FileSystems)
	if c.Vpc != nil {
		v := *c.Vpc
		v.SubnetIDs, v.SecurityGroupIDs = slices.Clone(v.SubnetIDs), slices.Clone(v.SecurityGroupIDs)
		out.Vpc = &v
	}
	if c.Image != nil {
		i := *c.Image
		i.EntryPoint, i.Command = slices.Clone(i.EntryPoint), slices.Clone(i.Command)
		out.Image = &i
	}
	return out
}

// listing selects the members a FunctionConfiguration carries: a listing
// row (ListFunctions, ListVersionsByFunction) omits the state members and
// RuntimeVersionConfig (lambda-api.md 5.1).
type listing bool

// renderConfig renders version v of f as AWS reports it, under arn: the
// unqualified ARN for $LATEST addressed without a qualifier, else the ARN
// as qualified (lambda-api.md 3).
func renderConfig(scope aws.Scope, f *Function, v *Version, arn string, rows listing) lambdaapi.FunctionConfiguration {
	c := v.Config
	out := lambdaapi.FunctionConfiguration{
		FunctionName:     new(f.Name),
		FunctionArn:      new(arn),
		Role:             new(c.Role),
		Description:      new(c.Description),
		Timeout:          new(c.Timeout),
		MemorySize:       new(c.MemorySize),
		LastModified:     new(lastModified(v.Modified)),
		Version:          new(v.Number),
		RevisionId:       new(v.RevisionID),
		Architectures:    []lambdaapi.Architecture{lambdaapi.Architecture(c.Architecture)},
		EphemeralStorage: &lambdaapi.EphemeralStorage{Size: c.EphemeralStorage},
		TracingConfig:    &lambdaapi.TracingConfigResponse{Mode: new(lambdaapi.TracingMode(c.TracingMode))},
		SnapStart:        &lambdaapi.SnapStartResponse{ApplyOn: new(lambdaapi.SnapStartApplyOn(c.SnapStart)), OptimizationStatus: new(lambdaapi.SnapStartOptimizationStatusOff)},
		LoggingConfig:    renderLogging(f.Name, c.Logging),
	}
	switch code := v.Code.(type) {
	case ZipCode:
		out.PackageType = new(lambdaapi.PackageTypeZip)
		out.CodeSha256, out.CodeSize = new(code.Sha256), code.Size
		out.Runtime, out.Handler = new(lambdaapi.Runtime(c.Runtime)), new(c.Handler)
		if !rows {
			out.RuntimeVersionConfig = &lambdaapi.RuntimeVersionConfig{RuntimeVersionArn: new(runtimeVersionARN(scope, c.Runtime))}
		}
	case ImageCode:
		out.PackageType = new(lambdaapi.PackageTypeImage)
		out.CodeSha256 = new(imageSha256(code))
		if c.Image != nil {
			out.ImageConfigResponse = &lambdaapi.ImageConfigResponse{ImageConfig: &lambdaapi.ImageConfig{
				EntryPoint: slices.Clone(c.Image.EntryPoint), Command: slices.Clone(c.Image.Command), WorkingDirectory: c.Image.WorkingDirectory,
			}}
		}
	}
	if len(c.Environment) > 0 {
		out.Environment = &lambdaapi.EnvironmentResponse{Variables: maps.Clone(c.Environment)}
	}
	if c.DeadLetterTarget != "" {
		out.DeadLetterConfig = &lambdaapi.DeadLetterConfig{TargetArn: new(c.DeadLetterTarget)}
	}
	if c.KMSKeyArn != "" {
		out.KMSKeyArn = new(c.KMSKeyArn)
	}
	if c.Vpc != nil {
		out.VpcConfig = &lambdaapi.VpcConfigResponse{SubnetIds: slices.Clone(c.Vpc.SubnetIDs), SecurityGroupIds: slices.Clone(c.Vpc.SecurityGroupIDs), Ipv6AllowedForDualStack: c.Vpc.IPv6DualStack}
	}
	for _, fs := range c.FileSystems {
		out.FileSystemConfigs = append(out.FileSystemConfigs, lambdaapi.FileSystemConfig{Arn: fs.ARN, LocalMountPath: fs.MountPath})
	}
	if !rows {
		sv := renderStatus(v.Status)
		out.State = new(sv.State)
		if sv.Reason != "" {
			out.StateReason, out.StateReasonCode = new(sv.Reason), new(sv.Code)
		}
		if sv.Update != "" {
			out.LastUpdateStatus = new(sv.Update)
		}
		if sv.UpdateReason != "" {
			out.LastUpdateStatusReason, out.LastUpdateStatusReasonCode = new(sv.UpdateReason), new(sv.UpdateCode)
		}
	}
	return out
}

// renderLogging fills the defaults AWS reports: the group
// /aws/lambda/<name>, and INFO levels for the JSON format.
func renderLogging(name string, l Logging) *lambdaapi.LoggingConfig {
	out := &lambdaapi.LoggingConfig{LogFormat: new(lambdaapi.LogFormat(l.Format)), LogGroup: new(logGroup(name, l))}
	if l.ApplicationLevel != "" {
		out.ApplicationLogLevel = new(lambdaapi.ApplicationLogLevel(l.ApplicationLevel))
	}
	if l.SystemLevel != "" {
		out.SystemLogLevel = new(lambdaapi.SystemLogLevel(l.SystemLevel))
	}
	if l.Format == string(lambdaapi.LogFormatJson) {
		if out.ApplicationLogLevel == nil {
			out.ApplicationLogLevel = new(lambdaapi.ApplicationLogLevelInfo)
		}
		if out.SystemLogLevel == nil {
			out.SystemLogLevel = new(lambdaapi.SystemLogLevelInfo)
		}
	}
	return out
}

// statusView is a Status as FunctionConfiguration reports it; empty
// strings are absent members.
type statusView struct {
	State        lambdaapi.State
	Reason       string
	Code         lambdaapi.StateReasonCode
	Update       lambdaapi.LastUpdateStatus
	UpdateReason string
	UpdateCode   lambdaapi.LastUpdateStatusReasonCode
}

// beingCreated is the reason AWS gives both while a function is created
// and while an update is applied (lambda-api.md 5.1).
const beingCreated = "The function is being created."

// updateCreating is the LastUpdateStatusReasonCode recorded during an
// update, which the model's enum lacks (lambda-api.md 5.1).
const updateCreating lambdaapi.LastUpdateStatusReasonCode = "Creating"

func renderStatus(s Status) statusView {
	switch s := s.(type) {
	case Creating, PullingImage:
		return statusView{State: lambdaapi.StatePending, Reason: beingCreated, Code: lambdaapi.StateReasonCodeCreating}
	case Active:
		v := statusView{State: lambdaapi.StateActive}
		switch u := s.Update.(type) {
		case Successful:
			v.Update = lambdaapi.LastUpdateStatusSuccessful
		case Updating, UpdatingImage:
			v.Update, v.UpdateReason, v.UpdateCode = lambdaapi.LastUpdateStatusInProgress, beingCreated, updateCreating
		case UpdateFailed:
			v.Update, v.UpdateReason, v.UpdateCode = lambdaapi.LastUpdateStatusFailed, u.Reason, lambdaapi.LastUpdateStatusReasonCode(u.ReasonCode)
		}
		return v
	case Failed:
		return statusView{
			State: lambdaapi.StateFailed, Reason: s.Reason, Code: lambdaapi.StateReasonCode(s.ReasonCode),
			Update: lambdaapi.LastUpdateStatusFailed, UpdateReason: s.Reason, UpdateCode: lambdaapi.LastUpdateStatusReasonCode(s.ReasonCode),
		}
	}
	return statusView{}
}

// lastModified renders LastModified as Lambda does:
// 2023-11-15T00:37:46.592+0000.
func lastModified(at clock.Instant) string {
	return at.Wire().UTC().Format("2006-01-02T15:04:05.000-0700")
}

// runtimeVersionARN is the RuntimeVersionArn of runtime in scope's region:
// a fixed version per runtime, since understudy pins one image per runtime.
func runtimeVersionARN(scope aws.Scope, runtime string) string {
	h := sha256.Sum256([]byte(runtime))
	return "arn:" + scope.Partition.String() + ":lambda:" + scope.Region.String() + "::runtime:" + hex.EncodeToString(h[:])
}

// imageSha256 is an image function's CodeSha256: the hex digest of the
// image present locally, or of its URI while none is.
func imageSha256(code ImageCode) string {
	if digest, ok := cutDigest(code.Digest); ok {
		return digest
	}
	h := sha256.Sum256([]byte(code.URI))
	return hex.EncodeToString(h[:])
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// The asynchronous invocation configurations are stored per (function,
// qualifier) and echoed; the async queue that applies them belongs to the
// invoke path.

func (s *Service) bindEventInvokeConfigs(b lambdaapi.Binder) {
	const note = "stored and echoed; asynchronous invocations do not apply them in this build"
	b.PutFunctionEventInvokeConfig(s.putEventInvokeConfig, stored(note))
	b.UpdateFunctionEventInvokeConfig(s.updateEventInvokeConfig, stored(note))
	b.GetFunctionEventInvokeConfig(s.getEventInvokeConfig, full())
	b.DeleteFunctionEventInvokeConfig(s.deleteEventInvokeConfig, full())
	b.ListFunctionEventInvokeConfigs(s.listEventInvokeConfigs, full())
}

// eventInvokeKey is the key a configuration is stored under: its
// qualifier, with the unqualified function's under $LATEST.
func (r functionRef) eventInvokeKey() string {
	if r.qualifier == "" {
		return latest
	}
	return r.qualifier
}

// eventInvokeInput is what Put and Update carry.
type eventInvokeInput struct {
	retries, age *int32
	dest         *lambdaapi.DestinationConfig
}

func (s *Service) putEventInvokeConfig(_ context.Context, c *kernel.Call, in *lambdaapi.PutFunctionEventInvokeConfigInput) (*lambdaapi.FunctionEventInvokeConfig, *awserr.Error) {
	return s.storeEventInvoke(c, in.FunctionName, in.Qualifier, eventInvokeInput{in.MaximumRetryAttempts, in.MaximumEventAgeInSeconds, in.DestinationConfig}, true)
}

func (s *Service) updateEventInvokeConfig(_ context.Context, c *kernel.Call, in *lambdaapi.UpdateFunctionEventInvokeConfigInput) (*lambdaapi.FunctionEventInvokeConfig, *awserr.Error) {
	return s.storeEventInvoke(c, in.FunctionName, in.Qualifier, eventInvokeInput{in.MaximumRetryAttempts, in.MaximumEventAgeInSeconds, in.DestinationConfig}, false)
}

// eventInvokeNotFound is the answer for a configuration that is not
// stored, under the qualified ARN; Get and Delete give it for a missing
// function or qualifier too (lambda-api.md 7.7).
func (r functionRef) eventInvokeNotFound() *awserr.Error {
	return errEventInvokeNotFound(r.arn() + ":" + r.eventInvokeKey())
}

// storeEventInvoke stores a configuration: replaced whole by Put, merged
// by Update into one that must exist (lambda-api.md 7.7).
func (s *Service) storeEventInvoke(c *kernel.Call, name string, qualifier *string, in eventInvokeInput, replace bool) (*lambdaapi.FunctionEventInvokeConfig, *awserr.Error) {
	ref, err := parseFunctionRef(c, name, qualifier)
	if err != nil {
		return nil, err
	}
	if in.retries == nil && in.age == nil && in.dest == nil {
		return nil, errEventInvokeNoSettings()
	}
	out, err := write(s, c, ref.scope, func(st *State, now clock.Instant, _ *kernel.Tx) (lambdaapi.FunctionEventInvokeConfig, *awserr.Error) {
		f, _, err := st.lookup(ref)
		if err != nil {
			return lambdaapi.FunctionEventInvokeConfig{}, errEventInvokeFunctionMissing()
		}
		cfg, stored := f.EventInvoke[ref.eventInvokeKey()]
		switch {
		case replace:
			cfg = EventInvokeConfig{}
		case !stored:
			return lambdaapi.FunctionEventInvokeConfig{}, ref.eventInvokeNotFound()
		}
		if in.dest != nil {
			if err := st.checkDestinations(ref, in.dest); err != nil {
				return lambdaapi.FunctionEventInvokeConfig{}, err
			}
		}
		cfg = cfg.with(in, replace)
		cfg.Modified = now
		f = f.clone()
		if f.EventInvoke == nil {
			f.EventInvoke = map[string]EventInvokeConfig{}
		}
		f.EventInvoke[ref.eventInvokeKey()] = cfg
		st.Functions[ref.name] = f
		return renderEventInvoke(ref, cfg), nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, ref.qualifiedARN())
	return &out, nil
}

// with returns cfg with the settings in sets; with replace, the settings
// in leaves out are removed.
func (cfg EventInvokeConfig) with(in eventInvokeInput, replace bool) EventInvokeConfig {
	if in.retries != nil || replace {
		cfg.MaximumRetryAttempts = in.retries
	}
	if in.age != nil || replace {
		cfg.MaximumEventAgeInSeconds = in.age
	}
	switch {
	case in.dest != nil:
		cfg.OnSuccess, cfg.OnFailure = destinations(in.dest)
	case replace:
		cfg.OnSuccess, cfg.OnFailure = "", ""
	}
	return cfg
}

// destinations are the ARNs of both sides of a DestinationConfig; empty
// for a side without one.
func destinations(d *lambdaapi.DestinationConfig) (success, failure string) {
	if d.OnSuccess != nil {
		success = deref(d.OnSuccess.Destination)
	}
	if d.OnFailure != nil {
		failure = deref(d.OnFailure.Destination)
	}
	return success, failure
}

// checkDestinations refuses a destination that is the function itself, a
// function that does not exist, or a service Lambda does not deliver to
// (S3 is an on-failure destination only).
func (st *State) checkDestinations(ref functionRef, d *lambdaapi.DestinationConfig) *awserr.Error {
	success, failure := destinations(d)
	for _, side := range []struct {
		arn       string
		onFailure bool
	}{{success, false}, {failure, true}} {
		if side.arn == "" {
			continue
		}
		a, err := aws.ParseARN(side.arn)
		switch {
		case err != nil:
			return errDestinationConfig(success, failure)
		case a.Service == "lambda":
			target, ok := strings.CutPrefix(a.Resource, "function:")
			if !ok {
				return errDestinationConfig(success, failure)
			}
			if name, _, _ := strings.Cut(target, ":"); name == ref.name {
				return errDestinationSelf()
			} else if _, ok := st.Functions[name]; !ok {
				return errDestinationMissing(side.arn)
			}
		case a.Service == "sqs", a.Service == "sns", a.Service == "events", a.Service == "s3" && side.onFailure:
		default:
			return errDestinationConfig(success, failure)
		}
	}
	return nil
}

// renderEventInvoke reports a configuration: FunctionArn qualified, with
// :$LATEST for the unqualified function, unset limits left out and both
// destination sides always present (tf-remaining.md 4.2).
func renderEventInvoke(ref functionRef, cfg EventInvokeConfig) lambdaapi.FunctionEventInvokeConfig {
	out := lambdaapi.FunctionEventInvokeConfig{
		LastModified:             new(cfg.Modified.Wire()),
		FunctionArn:              new(ref.arn() + ":" + ref.eventInvokeKey()),
		MaximumRetryAttempts:     cfg.MaximumRetryAttempts,
		MaximumEventAgeInSeconds: cfg.MaximumEventAgeInSeconds,
		DestinationConfig:        &lambdaapi.DestinationConfig{OnSuccess: &lambdaapi.OnSuccess{}, OnFailure: &lambdaapi.OnFailure{}},
	}
	if cfg.OnSuccess != "" {
		out.DestinationConfig.OnSuccess.Destination = new(cfg.OnSuccess)
	}
	if cfg.OnFailure != "" {
		out.DestinationConfig.OnFailure.Destination = new(cfg.OnFailure)
	}
	return out
}

func (s *Service) getEventInvokeConfig(_ context.Context, c *kernel.Call, in *lambdaapi.GetFunctionEventInvokeConfigInput) (*lambdaapi.FunctionEventInvokeConfig, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, in.Qualifier)
	if err != nil {
		return nil, err
	}
	return view(s, ref.scope, func(st *State) (*lambdaapi.FunctionEventInvokeConfig, *awserr.Error) {
		cfg, ok := st.eventInvoke(ref)
		if !ok {
			return nil, ref.eventInvokeNotFound()
		}
		return new(renderEventInvoke(ref, cfg)), nil
	})
}

// eventInvoke finds the configuration ref names; false when the function,
// its qualifier or the configuration is missing, which Get and Delete
// answer alike.
func (st *State) eventInvoke(ref functionRef) (EventInvokeConfig, bool) {
	f, _, err := st.lookup(ref)
	if err != nil {
		return EventInvokeConfig{}, false
	}
	cfg, ok := f.EventInvoke[ref.eventInvokeKey()]
	return cfg, ok
}

func (s *Service) deleteEventInvokeConfig(_ context.Context, c *kernel.Call, in *lambdaapi.DeleteFunctionEventInvokeConfigInput) (*lambdaapi.DeleteFunctionEventInvokeConfigOutput, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, in.Qualifier)
	if err != nil {
		return nil, err
	}
	_, err = write(s, c, ref.scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if _, ok := st.eventInvoke(ref); !ok {
			return struct{}{}, ref.eventInvokeNotFound()
		}
		f := st.Functions[ref.name].clone()
		delete(f.EventInvoke, ref.eventInvokeKey())
		st.Functions[ref.name] = f
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, ref.qualifiedARN())
	return &lambdaapi.DeleteFunctionEventInvokeConfigOutput{}, nil
}

func (s *Service) listEventInvokeConfigs(_ context.Context, c *kernel.Call, in *lambdaapi.ListFunctionEventInvokeConfigsInput) (*lambdaapi.ListFunctionEventInvokeConfigsOutput, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, nil)
	if err != nil {
		return nil, err
	}
	configs, err := view(s, ref.scope, func(st *State) ([]lambdaapi.FunctionEventInvokeConfig, *awserr.Error) {
		f, ok := st.Functions[ref.name]
		if !ok {
			return nil, errEventInvokeFunctionMissing()
		}
		var out []lambdaapi.FunctionEventInvokeConfig
		for _, q := range slices.Sorted(maps.Keys(f.EventInvoke)) {
			out = append(out, renderEventInvoke(functionRef{scope: ref.scope, name: ref.name, qualifier: q}, f.EventInvoke[q]))
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	p, err := page(configs, func(cfg lambdaapi.FunctionEventInvokeConfig) string { return deref(cfg.FunctionArn) }, in.MaxItems, listPageCap, in.Marker)
	if err != nil {
		return nil, err
	}
	return &lambdaapi.ListFunctionEventInvokeConfigsOutput{FunctionEventInvokeConfigs: append([]lambdaapi.FunctionEventInvokeConfig{}, p.items...), NextMarker: p.next}, nil
}
