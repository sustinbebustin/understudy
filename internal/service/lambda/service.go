package lambda

import (
	"maps"
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TimerSettle ends a provisioning state under TransitionsClock: a
// function version's Creating or Updating, an event source mapping's
// transient state. Its id is the version's qualified ARN or the mapping's
// ARN.
const TimerSettle kernel.TimerKind = "lambda.settle"

// TimerImage starts making a version's image present. It fires at once:
// the timer only moves the pull out of the call that asked for it, and
// Rearm restarts the pulls a snapshot load interrupted. Its id is the
// version's qualified ARN.
const TimerImage kernel.TimerKind = "lambda.image"

// settleDelay is how long a provisioning state lasts under
// TransitionsClock (ARCHITECTURE.md 14.7).
const settleDelay = 10 * time.Millisecond

// Service is AWS Lambda: one state cell per account and region.
type Service struct {
	k     *kernel.Kernel
	ids   *ids.Source
	blobs *blob.Store
	cells *store.Partitioned[aws.Scope, State]
	opts  Options
	// settle and image are TimerSettle and TimerImage as registered.
	settle kernel.HandledKind
	image  kernel.HandledKind
	// overlayDir returns the directory of a function's active hot-reload
	// overlay, false when none: the Execution's overlays, which
	// NewExecution sets before any call is served; nil without one.
	overlayDir func(fnKey) (string, bool)
	// codeChanged follows a committed call that changed which code the
	// functions of a scope store, so the Execution binds the directories
	// stored as code and ends the bindings no version names; set by
	// NewExecution, nil without one.
	codeChanged func(aws.Scope)
}

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// New binds the Lambda control plane and registers its timers. Invoke and
// the function URL data plane stay unbound, answering 501, until
// NewExecution and Execution.BindFunctionURLs bind them; every operation
// this package does not implement stays unbound.
func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{
		k:     d.Kernel,
		ids:   d.IDs,
		blobs: d.Blob,
		cells: store.NewPartitioned(d.Kernel.Gate(), stateCodec(), newState),
		opts:  o,
	}
	s.settle = d.Kernel.Timers().Handle(TimerSettle, s.onSettle)
	s.image = d.Kernel.Timers().Handle(TimerImage, s.onImage)
	b := lambdaapi.NewBinder(d.Binder)
	s.bindFunctions(b)
	s.bindCodeLinks(b)
	s.bindVersions(b)
	s.bindAliases(b)
	s.bindPermissions(b)
	s.bindConcurrency(b)
	s.bindTags(b)
	s.bindURLConfigs(b)
	s.bindEventInvokeConfigs(b)
	s.bindMappings(b)
	s.bindLayers(b)
	declareAccess(d.Binder)
	declareAddressed(d.Binder)
	return s
}

// declareAccess states what the bound operations whose generated list
// holds more than one lambda action touch and use besides their primary
// action (CreateFunction and UpdateFunctionConfiguration, Permissions):
// the role passed as the execution role, on which step 8 asks and the
// access record keeps iam:PassRole with iam:PassedToService
// lambda.amazonaws.com; lambda:TagResource for tags; and
// lambda:PassCapacityProvider for a capacity provider, which the call then
// touches so the action lands on the provider's ARN. The recorder pairs
// every lambda action of a call with every lambda resource it touched, so
// the provider also gets the primary action and the function
// lambda:PassCapacityProvider; and lambda:GetLayerVersion on each layer
// version a configuration names, which the call touches likewise
// (lambda-destinations-layers.md 7.4). It runs after the bind sites, whose
// bindings it extends.
func declareAccess(b *kernel.Binder) {
	// GetFunction declares no secondary: lambda:ListTags only decides
	// whether the response carries the function's tags.
	kernel.Declare(b, lambdaapi.OpGetFunction, kernel.WithoutSecondaryActions())
	kernel.Declare(b, lambdaapi.OpCreateEventSourceMapping,
		kernel.WithActions(func(_ *kernel.Call, in *lambdaapi.CreateEventSourceMappingInput) []registry.IAMAction {
			if len(in.Tags) == 0 {
				return nil
			}
			return []registry.IAMAction{lambdaAction("TagResource")}
		}))
	kernel.Declare(b, lambdaapi.OpCreateFunction,
		kernel.WithResources(func(_ *kernel.Call, in *lambdaapi.CreateFunctionInput) []aws.ARN {
			return slices.Concat(passedRole(&in.Role), capacityProvider(in.CapacityProviderConfig), layerVersions(in.Layers))
		}),
		kernel.WithActions(func(_ *kernel.Call, in *lambdaapi.CreateFunctionInput) []registry.IAMAction {
			out := append(providerActions(in.CapacityProviderConfig), layerActions(in.Layers)...)
			if len(in.Tags) > 0 {
				out = append(out, lambdaAction("TagResource"))
			}
			return append(out, passRoleAction())
		}),
		kernel.WithConditionKeys(func(*kernel.Call, *lambdaapi.CreateFunctionInput) kernel.ConditionKeys { return passedToLambda() }))
	kernel.Declare(b, lambdaapi.OpUpdateFunctionConfiguration,
		kernel.WithResources(func(_ *kernel.Call, in *lambdaapi.UpdateFunctionConfigurationInput) []aws.ARN {
			return slices.Concat(passedRole(in.Role), capacityProvider(in.CapacityProviderConfig), layerVersions(in.Layers))
		}),
		kernel.WithActions(func(_ *kernel.Call, in *lambdaapi.UpdateFunctionConfigurationInput) []registry.IAMAction {
			return append(providerActions(in.CapacityProviderConfig), append(layerActions(in.Layers), passRoleAction())...)
		}),
		kernel.WithConditionKeys(func(_ *kernel.Call, in *lambdaapi.UpdateFunctionConfigurationInput) kernel.ConditionKeys {
			if in.Role == nil {
				return nil
			}
			return passedToLambda()
		}))
}

// passRoleAction is iam:PassRole, which CreateFunction and
// UpdateFunctionConfiguration ask about at step 8 on the role they pass
// and on nothing when they pass none, since the kernel pairs an action of
// another service only with the resources of that service the call
// touched (iam-enforcement-surfaces.md 12.6). AWS answers its denial
// before the role's trust is checked (testdata/parity/lambda/IAM-ENF/26-no-passrole.json).
func passRoleAction() registry.IAMAction { return registry.IAMAction{Prefix: "iam", Name: "PassRole"} }

// passedToLambda is the condition key a role passed to Lambda carries
// (iam-enforcement-surfaces.md 7.4, 10.2).
func passedToLambda() kernel.ConditionKeys {
	return kernel.ConditionKeys{"iam:PassedToService": {lambdaService}}
}

// layerActions is lambda:GetLayerVersion for a configuration that names
// layers.
func layerActions(layers []string) []registry.IAMAction {
	if len(layers) == 0 {
		return nil
	}
	return []registry.IAMAction{lambdaAction("GetLayerVersion")}
}

// layerVersions are the layer version ARNs a configuration names, which
// lambda:GetLayerVersion acts on; the generated check has matched each
// against the model's pattern.
func layerVersions(layers []string) []aws.ARN {
	var out []aws.ARN
	for _, l := range layers {
		if a, err := aws.ParseARN(l); err == nil && a.Service == "lambda" {
			out = append(out, a)
		}
	}
	return out
}

// providerActions is lambda:PassCapacityProvider for a configuration that
// names a capacity provider.
func providerActions(provider *lambdaapi.CapacityProviderConfig) []registry.IAMAction {
	if provider == nil {
		return nil
	}
	return []registry.IAMAction{lambdaAction("PassCapacityProvider")}
}

// capacityProvider is the capacity provider a configuration names, which
// lambda:PassCapacityProvider acts on; validation has checked the ARN's
// form.
func capacityProvider(provider *lambdaapi.CapacityProviderConfig) []aws.ARN {
	if provider == nil {
		return nil
	}
	a, err := aws.ParseARN(provider.LambdaManagedInstancesCapacityProviderConfig.CapacityProviderArn)
	if err != nil || a.Service != "lambda" {
		return nil
	}
	return []aws.ARN{a}
}

// lambdaAction is the lambda action name.
func lambdaAction(name string) registry.IAMAction {
	return registry.IAMAction{Prefix: "lambda", Name: name}
}

// Switches lists the service's fidelity switches: the state transitions, the asynchronous queue's, destinations' and layers'
// choices, the event source mapping filter engine's choices, Lambda's
// denial body, how a mapping meets a role refused the queue
// (iam-enforcement-surfaces.md 8.6, 12.8) and the stream poller's LATEST
// anchor, retry delay and partial-resend budget. The transitions govern
// the calls that create or change a function or mapping and the reads that
// report its state.
func (s *Service) Switches() []fidelity.Entry {
	denial := authz.RecordedSwitch("lambda.denial.body", `AccessDeniedException 403, {"Message":"<grammar of 4.1>"}`, "3.2", "IAM-ENF-11",
		authz.SessionUnreplayed, s.k.Bindings().AuthorizedOps(lambdaapi.Service()))
	// checkQueue and checkStream answer esmRoleDeniedText; a poll or a
	// stream read the role is refused is retried after a backoff, invoking
	// nothing and changing no field. The queue's sentence is recorded, the
	// streams' follow it.
	roleDenied := authz.RecordedSwitch("lambda.esm.create-denied-text", esmCreateDeniedValue(), "8.6", "IAM-ENF-24", authz.SessionUnreplayed,
		[]string{"lambda.CreateEventSourceMapping", "lambda.UpdateEventSourceMapping"})
	permissionLoss := authz.EnforcementSwitch("lambda.esm.permission-loss", "stay-enabled", evidence.Inferred, "8.6", "IAM-ENF-25",
		[]string{"lambda.CreateEventSourceMapping"})
	// Invoke authorizes a delivery before it finds the function: AWS
	// dead-letters an EventBridge delivery to a deleted function with
	// NO_PERMISSIONS (iam-enforcement-surfaces.md 8.4, IAM-ENF-22); other
	// delivering services follow by extrapolation (8.2).
	missingDelivery := authz.RecordedSwitch("lambda.enforce.missing-resource.delivery", "deny-first", "8.4", "IAM-ENF-22", authz.DeliveryUnreplayed,
		[]string{lambdaapi.OpInvoke.Descriptor().ID.String()})
	entries := []fidelity.Entry{denial, roleDenied, permissionLoss, missingDelivery, fidelity.Switch[string]{
		Name:  "lambda.stateTransitions",
		Value: s.opts.StateTransitions.String(),
		Label: evidence.AWSDoc,
		Ref:   "docs/ARCHITECTURE.md#9-the-clock",
		Ops: []string{
			"lambda.CreateFunction", "lambda.UpdateFunctionCode", "lambda.UpdateFunctionConfiguration", "lambda.PublishVersion",
			"lambda.GetFunction", "lambda.GetFunctionConfiguration",
			"lambda.CreateEventSourceMapping", "lambda.UpdateEventSourceMapping", "lambda.DeleteEventSourceMapping",
			"lambda.GetEventSourceMapping",
		},
		Settles: fidelity.NoScenario{Reason: "a deliberate default (ARCHITECTURE.md 9), which a recording of AWS's transient states does not change"},
	}.Entry()}
	for _, sw := range append(asyncSwitches(), layerSwitches()...) {
		entries = append(entries, sw.Entry())
	}
	entries = append(entries, esm.Switches()...)
	return append(entries, esm.StreamSwitches(s.opts.Streams)...)
}

// Name returns "lambda".
func (*Service) Name() ref.ServiceID { return "lambda" }

// SchemaVersion returns the version of State.
func (*Service) SchemaVersion() int { return stateVersion }

// OldestSchemaVersion returns the oldest version Import reads
// (snapshot.OlderReader).
func (*Service) OldestSchemaVersion() int { return stateVersionOldest }

// Export serializes every non-empty scope to one section.
func (s *Service) Export() ([]store.Section, error) {
	return s.cells.Export(store.ScopeKeys(), stateVersion)
}

// Import loads sections written by Export of this schema version or of
// version 4 up to the one before it, which read as this version without
// what was added since, or of version 3, which it migrates first
// (migrateUnkindedScaling).
func (s *Service) Import(ss []store.Section) error {
	out := make([]store.Section, 0, len(ss))
	for _, sec := range ss {
		switch {
		case sec.Version > stateVersionUnkindedScaling && sec.Version < stateVersion:
			sec.Version = stateVersion
		case sec.Version == stateVersionUnkindedScaling:
			data, err := migrateUnkindedScaling(sec.Data)
			if err != nil {
				return &store.SectionError{Path: sec.Path, Defect: store.UndecodableState, Err: err}
			}
			sec.Version, sec.Data = stateVersion, data
		}
		out = append(out, sec)
	}
	return s.cells.Import(store.ScopeKeys(), stateVersion, out)
}

// Reset drops the scopes rs covers. It releases no blob reference: the
// admin reset releases those the dropped scopes' sections listed.
func (s *Service) Reset(rs kernel.ResetScope) {
	s.cells.Reset(func(k aws.Scope) bool { return !rs.Covers(k) })
}

// Rearm arms the timers the state implies, which are never saved: the
// settle timer of every version and mapping in a provisioning state under
// TransitionsClock, and the image timer of every version waiting for its
// image, whose pull a load interrupted.
func (s *Service) Rearm(now clock.Instant) {
	kernel.RearmEach(s.k.Timers().Scheduler(), s.cells, func(scope aws.Scope, st *State) []clock.Armed {
		return s.armed(scope, st, now)
	})
}

// Resources lists the functions and event source mappings of scope.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *State) []kernel.Resource {
		var out []kernel.Resource
		for _, name := range slices.Sorted(maps.Keys(st.Functions)) {
			if a, err := aws.ParseARN(functionARN(scope, name)); err == nil {
				out = append(out, kernel.Resource{ARN: a, Summary: string(renderStatus(st.Functions[name].Latest.Status).State)})
			}
		}
		for _, uuid := range slices.Sorted(maps.Keys(st.Mappings)) {
			if a, err := aws.ParseARN(mappingARN(scope, uuid)); err == nil {
				out = append(out, kernel.Resource{ARN: a, Summary: string(st.Mappings[uuid].state())})
			}
		}
		return out
	})
}

// Scopes returns the accounts and regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }

// armed lists the timers st implies.
func (s *Service) armed(scope aws.Scope, st *State, now clock.Instant) []clock.Armed {
	var out []clock.Armed
	for _, name := range slices.Sorted(maps.Keys(st.Functions)) {
		versions := st.Functions[name].all()
		for i := range versions {
			if kind, due, ok := s.provisioning(&versions[i], now); ok {
				out = append(out, clock.Armed{Key: kind.Key(versionID(scope, name, versions[i].Number)), Due: due})
			}
		}
	}
	for _, uuid := range slices.Sorted(maps.Keys(st.Mappings)) {
		if t := st.Mappings[uuid].Transition; t != nil {
			out = append(out, clock.Armed{Key: s.settle.Key(mappingARN(scope, uuid)), Due: t.Settles})
		}
	}
	return out
}

// provisioning is the timer v's status waits on: the settle timer of
// Creating and Updating, the image timer, due now, of a version waiting
// for its image. false when v waits on nothing.
func (s *Service) provisioning(v *Version, now clock.Instant) (kernel.HandledKind, clock.Instant, bool) {
	switch st := v.Status.(type) {
	case Creating:
		return s.settle, st.Settles, true
	case PullingImage:
		return s.image, now, true
	case Active:
		switch u := st.Update.(type) {
		case Updating:
			return s.settle, u.Settles, true
		case UpdatingImage:
			return s.image, now, true
		case Successful, UpdateFailed:
		}
	case Failed:
	}
	return kernel.HandledKind{}, clock.Instant{}, false
}

// armProvisioning arms the timer v's status waits on, if any.
func (s *Service) armProvisioning(tx *kernel.Tx, scope aws.Scope, name string, v *Version, now clock.Instant) {
	if kind, due, ok := s.provisioning(v, now); ok {
		tx.Arm(kind, versionID(scope, name, v.Number), due)
	}
}

// disarmProvisioning removes the timers of a deleted version.
func (s *Service) disarmProvisioning(tx *kernel.Tx, scope aws.Scope, name, number string) {
	tx.Disarm(s.settle, versionID(scope, name, number))
	tx.Disarm(s.image, versionID(scope, name, number))
}

// versionID is a version's timer id: its qualified ARN.
func versionID(scope aws.Scope, name, number string) string {
	return functionARN(scope, name) + ":" + number
}

// clockTransitions reports the lambda.stateTransitions: clock switch.
func (s *Service) clockTransitions() bool { return s.opts.StateTransitions == TransitionsClock }

// write runs fn on the state of scope through kernel.Write.
func write[R any](s *Service, c *kernel.Call, scope aws.Scope, fn func(st *State, now clock.Instant, tx *kernel.Tx) (R, *awserr.Error)) (R, *awserr.Error) {
	return kernel.Write(c, s.cells, scope, fn)
}

// touch records the resources a call addressed, by the ARNs this service
// minted for them.
func touch(c *kernel.Call, arns ...string) {
	for _, s := range arns {
		if a, err := aws.ParseARN(s); err == nil {
			c.Touch(a)
		}
	}
}

// full, stored and partial are the coverage of the operations this
// package binds.
func full() registry.Full { return registry.Full{} }

func stored(note string) registry.StoredOnly { return registry.StoredOnly{Note: note} }

func partial(note string) registry.Partial { return registry.Partial{Note: note} }
