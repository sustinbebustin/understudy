package events

import (
	"context"
	"errors"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// defaultBus is the name of the one bus each account and region has.
const defaultBus = "default"

// busARN is arn:aws:events:<region>:<account>:event-bus/<name>.
func busARN(scope aws.Scope, name string) aws.ARN {
	return aws.ARN{
		Partition: scope.Partition.String(),
		Service:   "events",
		Region:    scope.Region.String(),
		Account:   scope.Account.String(),
		Resource:  "event-bus/" + name,
	}
}

// busRef is the bus an EventBusName names, read for a caller in a scope.
// It is syntactic: whether the bus exists is a question for the state.
//
//sumtype:decl
type busRef interface {
	isBusRef()
	// busARN is the ARN of the bus, for a caller in scope.
	busARN(scope aws.Scope) aws.ARN
}

// ownBus is a bus of the caller's scope: the default bus for no name, the
// empty name or the name default, else the bus a name or an ARN in the
// caller's scope names.
type ownBus struct{ name string }

// foreignBus is, by ARN, a bus of another served account or region.
type foreignBus struct {
	scope aws.Scope
	name  string
}

// missingBus is an ARN no bus can have: another service's, one that is not
// event-bus/, or one in a scope understudy does not serve. name is the bus
// name AWS's not-found text quotes; arn is the ARN the caller sent.
type missingBus struct {
	name string
	arn  aws.ARN
}

func (ownBus) isBusRef()     {}
func (foreignBus) isBusRef() {}
func (missingBus) isBusRef() {}

func (o ownBus) busARN(scope aws.Scope) aws.ARN { return busARN(scope, o.name) }
func (f foreignBus) busARN(aws.Scope) aws.ARN   { return f.arn() }
func (m missingBus) busARN(aws.Scope) aws.ARN   { return m.arn }

// arn is the ARN of the foreign bus.
func (f foreignBus) arn() aws.ARN { return busARN(f.scope, f.name) }

// parseBus reads an EventBusName, a name or an ARN, for a caller in scope;
// nil and empty name the caller's default bus. An ARN names its own account
// and region, not the caller's.
func parseBus(scope aws.Scope, name *string) busRef {
	if name == nil || *name == "" {
		return ownBus{name: defaultBus}
	}
	arn, err := aws.ParseARN(*name)
	if err != nil {
		return ownBus{name: *name}
	}
	bus, isBus := strings.CutPrefix(arn.Resource, "event-bus/")
	if arn.Service != "events" || !isBus {
		return missingBus{name: *name, arn: arn}
	}
	at, serr := aws.ScopeOf(arn)
	switch {
	case serr != nil:
		return missingBus{name: bus, arn: arn}
	case at == scope:
		return ownBus{name: bus}
	}
	return foreignBus{scope: at, name: bus}
}

// namedBus names the bus a bus call addresses, so fault rules scoped by
// resource match it before the handler runs. A missingBus names none.
func namedBus[In any](name func(*In) *string) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		switch b := parseBus(c.Scope(), name(in)).(type) {
		case ownBus:
			return []aws.ARN{busARN(c.Scope(), b.name)}
		case foreignBus:
			return []aws.ARN{b.arn()}
		case missingBus:
		}
		return nil
	})
}

// busNamePart is the bus name an EventBusName gives, whoever's bus it is:
// the default bus for nil and empty, the name after event-bus/ in an
// EventBridge ARN, and anything else as sent.
func busNamePart(name *string) string {
	if name == nil || *name == "" {
		return defaultBus
	}
	if arn, err := aws.ParseARN(*name); err == nil && arn.Service == "events" {
		if bus, ok := strings.CutPrefix(arn.Resource, "event-bus/"); ok {
			return bus
		}
	}
	return *name
}

// ownBusName is the name of the caller's bus a rule or target call's
// EventBusName names. Another served account's or region's bus is
// NotImplemented, and an ARN no bus can have does not exist. Whether an own
// bus exists is State.requireBus's question, asked under the cell lock.
func ownBusName(scope aws.Scope, name *string) (string, *awserr.Error) {
	return admitOwnBus(parseBus(scope, name))
}

// admitOwnBus is ownBusName on a parsed bus. A nil bus, which parseBus
// never returns, is refused as an internal failure rather than admitted.
func admitOwnBus(bus busRef) (string, *awserr.Error) {
	switch b := bus.(type) {
	case ownBus:
		return b.name, nil
	case foreignBus:
		return "", errForeignBus(b.arn())
	case missingBus:
		return "", errBusNotFound(b.name)
	}
	return "", awserr.Internalf(errNilBus)
}

// errNilBus is the cause of resolving a busRef that is nil, a bug in
// whatever built it.
var errNilBus = errors.New("events: the event bus reference is nil, so no bus was checked; parseBus builds every busRef")

// readBus runs fn on scope's state when the scope holds bus
// (State.requireBus); a scope without a state cell holds only its default
// bus, which then has no rules for fn to read.
func (s *Service) readBus(scope aws.Scope, bus string, fn func(st *State)) *awserr.Error {
	empty := newState(scope)
	err := empty.requireBus(bus)
	s.read(scope, func(st *State) {
		if err = st.requireBus(bus); err == nil {
			fn(st)
		}
	})
	return err
}

// lookupBus returns the bus name of scope as State.bus does; a scope
// without a state cell has only its default bus, created at at.
func (s *Service) lookupBus(scope aws.Scope, name string, at clock.Instant) (Bus, bool) {
	empty := newState(scope)
	b, ok := empty.bus(name, at)
	s.read(scope, func(st *State) { b, ok = st.bus(name, at) })
	return b, ok
}

// ruleRef locates a rule: its scope, its bus's name and its name. Rule
// names are unique per bus (eventbridge-buses.md 10.2.3).
type ruleRef struct {
	scope aws.Scope
	bus   string
	name  string
}

// arn returns arn:aws:events:<region>:<account>:rule/<name> for a rule of
// the default bus and rule/<bus>/<name> for a rule of a custom bus
// (eventbridge-buses.md 5.2).
func (r ruleRef) arn() aws.ARN {
	resource := "rule/" + r.name
	if r.bus != defaultBus {
		resource = "rule/" + r.bus + "/" + r.name
	}
	return aws.ARN{
		Partition: r.scope.Partition.String(),
		Service:   "events",
		Region:    r.scope.Region.String(),
		Account:   r.scope.Account.String(),
		Resource:  resource,
	}
}

// parseRuleARN reads a rule ARN back into its ruleRef: rule/<name> on the
// default bus, or rule/<bus>/<name> on a custom bus. arn never writes the
// default bus's name into an ARN, so rule/default/<name> is no rule's.
func parseRuleARN(s string) (ruleRef, bool) {
	arn, err := aws.ParseARN(s)
	if err != nil || arn.Service != "events" {
		return ruleRef{}, false
	}
	rest, ok := strings.CutPrefix(arn.Resource, "rule/")
	if !ok {
		return ruleRef{}, false
	}
	bus, name, onCustom := strings.Cut(rest, "/")
	if !onCustom {
		bus, name = defaultBus, rest
	}
	if bus == "" || name == "" || strings.Contains(name, "/") || onCustom && bus == defaultBus {
		return ruleRef{}, false
	}
	scope, err := aws.ScopeOf(arn)
	if err != nil {
		return ruleRef{}, false
	}
	return ruleRef{scope: scope, bus: bus, name: name}, true
}

// kmsNote is how a KmsKeyIdentifier is checked and echoed (checkBusKey):
// as EBB-05 recorded, but for an alias that does not exist, which it did
// not record (eventbridge-buses.md 3.1).
const kmsNote = "a KmsKeyIdentifier must name a key or alias understudy's KMS holds and, under iam.enforce=enforce, a key whose policy allows " +
	"events.amazonaws.com kms:GenerateDataKey; a key id is echoed as its key ARN and an alias name as its alias ARN; " +
	"an alias KMS does not find is refused as a missing key is, which is inferred"

// bindBuses binds the event bus operations with the coverage notes of
// eventbridge-buses.md 10.1.
func (s *Service) bindBuses(b eventbridgeapi.Binder) {
	b.CreateEventBus(s.createEventBus,
		registry.Partial{Note: "custom buses; EventSourceName (partner buses) answers NotImplemented; " +
			"KmsKeyIdentifier, DeadLetterConfig and LogConfig are stored and echoed, never applied: events are not encrypted, dead-lettered or logged; " + kmsNote},
		kernel.WithCheck(checkCreateEventBus),
		namedBus(func(in *eventbridgeapi.CreateEventBusInput) *string { return &in.Name }))
	b.DescribeEventBus(s.describeEventBus, registry.Partial{Note: noteForeignBus},
		namedBus(func(in *eventbridgeapi.DescribeEventBusInput) *string { return in.Name }))
	b.ListEventBuses(s.listEventBuses, registry.Full{})
	b.UpdateEventBus(s.updateEventBus,
		registry.StoredOnly{Note: "Description, KmsKeyIdentifier, DeadLetterConfig and LogConfig are stored and echoed; events are not encrypted, dead-lettered or logged; " + kmsNote},
		namedBus(func(in *eventbridgeapi.UpdateEventBusInput) *string { return in.Name }))
	b.DeleteEventBus(s.deleteEventBus, registry.Full{},
		namedBus(func(in *eventbridgeapi.DeleteEventBusInput) *string { return &in.Name }))
}

// maxBuses is how many buses an account holds per region, the default bus
// among them (eventbridge-buses.md 7, Custom Event Bus - Classic).
const maxBuses = 100

// checkCreateEventBus runs CreateEventBus's stateless checks before fault
// rules: a partner bus is not implemented (eventbridge-buses.md 10.7); the
// default bus always exists; and a custom bus name cannot start with aws.,
// checked before it is checked for a '/' (eventbridge-buses.md 2.2, EBB-02).
func checkCreateEventBus(in *eventbridgeapi.CreateEventBusInput) *awserr.Error {
	switch {
	case in.EventSourceName != nil:
		return errPartnerBus()
	case in.Name == defaultBus:
		return errBusExists(defaultBus)
	case strings.HasPrefix(in.Name, reservedSourcePrefix):
		return errAWSBusName()
	case strings.Contains(in.Name, "/"):
		return errSlashInBusName()
	}
	return nil
}

// createEventBus creates a custom bus and echoes each optional member the
// request carried (eventbridge-buses.md 3.1). It stores its settings as
// sent and applies none, but for a KmsKeyIdentifier, which it checks and
// stores as AWS does (checkBusKey) before it checks the name is free; that
// order is unrecorded.
func (s *Service) createEventBus(ctx context.Context, c *kernel.Call, in *eventbridgeapi.CreateEventBusInput) (*eventbridgeapi.CreateEventBusOutput, *awserr.Error) {
	scope := c.Scope()
	key, err := s.busKey(ctx, c, in.Name, in.KmsKeyIdentifier)
	if err != nil {
		return nil, err
	}
	_, err = kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if _, ok := st.Buses[in.Name]; ok {
			return struct{}{}, errBusExists(in.Name)
		}
		if st.countsTowardQuota() >= maxBuses {
			return struct{}{}, errTooManyBuses()
		}
		tags := tagMap(in.Tags)
		if len(tags) > maxTags {
			return struct{}{}, errTooManyTags(len(tags))
		}
		st.setBus(Bus{
			Name:             in.Name,
			Description:      nonEmpty(in.Description),
			KMSKeyIdentifier: nonEmpty(&key),
			DeadLetter:       deadLetterOf(in.DeadLetterConfig),
			Log:              logOf(in.LogConfig),
			Tags:             tags,
			Created:          c.Admitted(),
			Modified:         c.Admitted(),
		}, c.Admitted())
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	echoedKey := copyOf(in.KmsKeyIdentifier)
	if key != "" {
		echoedKey = &key
	}
	return &eventbridgeapi.CreateEventBusOutput{
		EventBusArn:      new(busARN(scope, in.Name).String()),
		Description:      copyOf(in.Description),
		KmsKeyIdentifier: echoedKey,
		DeadLetterConfig: deadLetterOf(in.DeadLetterConfig).api(),
		LogConfig:        logOf(in.LogConfig).api(),
	}, nil
}

// describeEventBus describes a bus of the caller's scope by name or ARN
// (eventbridge-buses.md 3.2); another scope's bus is not one understudy
// manages for the caller.
func (s *Service) describeEventBus(_ context.Context, c *kernel.Call, in *eventbridgeapi.DescribeEventBusInput) (*eventbridgeapi.DescribeEventBusOutput, *awserr.Error) {
	var name string
	switch b := parseBus(c.Scope(), in.Name).(type) {
	case ownBus:
		name = b.name
	case foreignBus:
		return nil, errForeignBus(b.arn())
	case missingBus:
		return nil, errBusNotFound(b.name)
	default:
		return nil, awserr.Internalf(errNilBus)
	}
	bus, ok := s.lookupBus(c.Scope(), name, c.Admitted())
	if !ok {
		return nil, errBusNotFound(name)
	}
	return &eventbridgeapi.DescribeEventBusOutput{
		Name:             new(bus.Name),
		Arn:              new(busARN(c.Scope(), bus.Name).String()),
		Description:      copyOf(bus.Description),
		KmsKeyIdentifier: copyOf(bus.KMSKeyIdentifier),
		DeadLetterConfig: bus.DeadLetter.api(),
		Policy:           bus.policyText(),
		LogConfig:        bus.Log.api(),
		CreationTime:     new(bus.Created.Wire()),
		LastModifiedTime: new(bus.Modified.Wire()),
	}, nil
}

// listEventBuses lists every bus of the caller's scope, the default bus
// included, by name (eventbridge-buses.md 3.3, events.list-buses.order).
func (s *Service) listEventBuses(_ context.Context, c *kernel.Call, in *eventbridgeapi.ListEventBusesInput) (*eventbridgeapi.ListEventBusesOutput, *awserr.Error) {
	empty := newState(c.Scope())
	all := empty.buses(c.Admitted())
	s.read(c.Scope(), func(st *State) { all = st.buses(c.Admitted()) })
	var listed []eventbridgeapi.EventBus
	for i := range all {
		b := &all[i]
		if in.NamePrefix != nil && !strings.HasPrefix(b.Name, *in.NamePrefix) {
			continue
		}
		listed = append(listed, eventbridgeapi.EventBus{
			Name:             new(b.Name),
			Arn:              new(busARN(c.Scope(), b.Name).String()),
			Description:      copyOf(b.Description),
			Policy:           b.policyText(),
			CreationTime:     new(b.Created.Wire()),
			LastModifiedTime: new(b.Modified.Wire()),
		})
	}
	out, next, err := page(listingBuses, listed, func(b eventbridgeapi.EventBus) string { return *b.Name }, in.NextToken, in.Limit)
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.ListEventBusesOutput{EventBuses: out, NextToken: next}, nil
}

// updateEventBus replaces each setting the request sends and keeps each it
// omits (events.update-bus.omitted-members); an empty Description clears
// it, as Terraform's update relies on (eventbridge-buses.md 3.4). An empty
// KmsKeyIdentifier clears the key (EBB-05), and no Name updates the default
// bus (EBB-04). No Name, or
// default, materializes the default bus (eventbridge-buses.md 10.2.2). A
// KmsKeyIdentifier is checked as CreateEventBus checks one (checkBusKey),
// before the bus is looked up; that order is unrecorded.
func (s *Service) updateEventBus(ctx context.Context, c *kernel.Call, in *eventbridgeapi.UpdateEventBusInput) (*eventbridgeapi.UpdateEventBusOutput, *awserr.Error) {
	name := defaultBus
	if in.Name != nil && *in.Name != "" {
		name = *in.Name
	}
	key, err := s.busKey(ctx, c, name, in.KmsKeyIdentifier)
	if err != nil {
		return nil, err
	}
	bus, err := kernel.Write(c, s.cells, c.Scope(), func(st *State, _ clock.Instant, _ *kernel.Tx) (Bus, *awserr.Error) {
		bus, ok := st.bus(name, c.Admitted())
		if !ok {
			return Bus{}, errBusNotFound(name)
		}
		if in.Description != nil {
			bus.Description = nonEmpty(in.Description)
		}
		if in.KmsKeyIdentifier != nil {
			bus.KMSKeyIdentifier = nonEmpty(&key)
		}
		if in.DeadLetterConfig != nil {
			bus.DeadLetter = deadLetterOf(in.DeadLetterConfig)
		}
		if in.LogConfig != nil {
			bus.Log = logOf(in.LogConfig)
		}
		bus.Modified = c.Admitted()
		st.setBus(bus, c.Admitted())
		return bus, nil
	})
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.UpdateEventBusOutput{
		Arn:              new(busARN(c.Scope(), bus.Name).String()),
		Name:             new(bus.Name),
		Description:      copyOf(bus.Description),
		KmsKeyIdentifier: copyOf(bus.KMSKeyIdentifier),
		DeadLetterConfig: bus.DeadLetter.api(),
		LogConfig:        bus.Log.api(),
	}, nil
}

// deleteEventBus deletes a custom bus with its tags and policy. The default
// bus cannot be deleted, nor a name that starts with aws. (EBB-02), deleting
// a bus that does not exist succeeds, and a bus with rules is refused
// (eventbridge-buses.md 3.5, events.delete-bus.with-rules).
func (s *Service) deleteEventBus(_ context.Context, c *kernel.Call, in *eventbridgeapi.DeleteEventBusInput) (*eventbridgeapi.DeleteEventBusOutput, *awserr.Error) {
	switch {
	case in.Name == defaultBus:
		return nil, errDeleteDefaultBus()
	case strings.HasPrefix(in.Name, reservedSourcePrefix):
		return nil, errAWSBusName()
	}
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if _, ok := st.Buses[in.Name]; !ok {
			tx.Unchanged()
			return struct{}{}, nil
		}
		if len(st.Rules[in.Name]) > 0 {
			return struct{}{}, errBusHasRules()
		}
		delete(st.Buses, in.Name)
		st.used(c.Admitted())
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.DeleteEventBusOutput{}, nil
}

// busKey is the KmsKeyIdentifier key of the caller's bus as the bus stores
// it (checkBusKey): "" for none, and for "", which UpdateEventBus reads as
// clearing the key without asking KMS (EBB-05 05-clear).
func (s *Service) busKey(ctx context.Context, c *kernel.Call, bus string, key *string) (string, *awserr.Error) {
	if key == nil || *key == "" {
		return "", nil
	}
	return s.checkBusKey(ctx, c, busARN(c.Scope(), bus), *key)
}

// policyText is the bus's Policy member, nil when it has no policy.
func (b *Bus) policyText() *string {
	if b.Policy == nil {
		return nil
	}
	return new(b.Policy.text())
}

// nonEmpty returns a copy of *p, or nil when p is nil or empty.
func nonEmpty(p *string) *string {
	if p == nil || *p == "" {
		return nil
	}
	return new(*p)
}

// deadLetterOf is a request's DeadLetterConfig as stored, nil when absent.
func deadLetterOf(d *eventbridgeapi.DeadLetterConfig) *DeadLetterConfig {
	if d == nil {
		return nil
	}
	return &DeadLetterConfig{ARN: copyOf(d.Arn)}
}

// api is the stored DeadLetterConfig as sent, nil when absent.
func (d *DeadLetterConfig) api() *eventbridgeapi.DeadLetterConfig {
	if d == nil {
		return nil
	}
	return &eventbridgeapi.DeadLetterConfig{Arn: copyOf(d.ARN)}
}

// logOf is a request's LogConfig as stored, NONE and OFF included; nil when
// absent.
func logOf(l *eventbridgeapi.LogConfig) *LogConfig {
	if l == nil {
		return nil
	}
	return &LogConfig{IncludeDetail: copyOf(l.IncludeDetail), Level: copyOf(l.Level)}
}

// api is the stored LogConfig as sent, nil when absent.
func (l *LogConfig) api() *eventbridgeapi.LogConfig {
	if l == nil {
		return nil
	}
	return &eventbridgeapi.LogConfig{IncludeDetail: copyOf(l.IncludeDetail), Level: copyOf(l.Level)}
}

// busSwitches are the bus behaviors chosen without AWS-recorded evidence,
// and those EBB has since recorded (eventbridge-buses.md 10.5). The
// handlers implement each Value; a value is a constant, not an option. A
// switch EBB recorded is settled by its step in
// testdata/parity/events/EBB/scenario.json when the replayer can replay
// it.
func busSwitches() []fidelity.Switch[string] {
	sw := func(name, value string, label evidence.Label, anchor, record string, settles fidelity.Settles, ops ...string) fidelity.Switch[string] {
		return fidelity.Switch[string]{
			Name:    name,
			Value:   value,
			Label:   label,
			Ref:     "docs/research/eventbridge-buses.md#" + anchor,
			Record:  record,
			Ops:     ops,
			Settles: settles,
		}
	}
	ebb := func(step string) fidelity.Settles {
		return fidelity.SettledBy{Step: fidelity.StepRef{Scenario: "events/EBB", Step: step}}
	}
	undelivered := func(steps string) fidelity.Settles {
		return fidelity.NoScenario{Reason: "recorded in events/EBB (" + steps + ", 2026-11-03); the replayer does not observe deliveries"}
	}
	return []fidelity.Switch[string]{
		// DeleteEventBus refuses a bus that still has rules (deleteEventBus).
		sw("events.delete-bus.with-rules", "rejected", evidence.AWSRecorded, "35-deleteeventbus", "EBB-01", ebb("01-delete-with-rule"),
			"eventbridge.DeleteEventBus"),
		// UpdateEventBus keeps a member it omits (updateEventBus).
		sw("events.update-bus.omitted-members", "kept", evidence.AWSRecorded, "34-updateeventbus", "EBB-04", ebb("04-update-name-only"),
			"eventbridge.UpdateEventBus"),
		// Removing a bus policy's last statement removes the policy, so
		// DescribeEventBus answers no Policy (removed).
		sw("events.bus-policy.remove-last-statement", "policy-removed", evidence.AWSRecorded, "43-removepermission", "EBB-06",
			fidelity.NoScenario{Reason: "recorded in events/EBB (06-remove-last, 06-describe-after-last, 2026-11-03); DescribeEventBus answers the bus's times, which a replay does not reproduce"},
			"eventbridge.RemovePermission"),
		// RemoveAllPermissions on a bus without a policy succeeds (removed).
		sw("events.bus-policy.remove-all-without-policy", "succeed", evidence.AWSRecorded, "43-removepermission", "EBB-06", ebb("06-remove-all-empty"),
			"eventbridge.RemovePermission"),
		// ListEventBuses sorts by name, the default bus among the rest
		// (State.buses).
		sw("events.list-buses.order", "by-name", evidence.AWSRecorded, "33-listeventbuses", "EBB-07",
			fidelity.NoScenario{Reason: "recorded in events/EBB (07-list-all, 2026-11-03); ListEventBuses answers the buses' times, which a replay does not reproduce"},
			"eventbridge.ListEventBuses"),
		// Until a write stores it, the default bus was created and last
		// modified at the scope's first use (State.bus).
		sw("events.default-bus.creation-time", "first-use", evidence.AWSRecorded, "36-the-default-bus", "EBB-07",
			fidelity.NoScenario{Reason: "recorded in events/EBB (04-update-no-name, 07-describe-default, 2026-11-03); a replay does not reproduce the recorded times"},
			"eventbridge.DescribeEventBus", "eventbridge.ListEventBuses"),
		// An event forwarded to a bus of the rule's own account and region
		// keeps its id, as it does across accounts (fanOut.forward).
		sw("events.bus-target.same-account-event-id", "kept", evidence.AWSRecorded, "62-an-event-bus-as-a-target", "EBB-08", undelivered("08-put-h1"),
			"eventbridge.PutEvents"),
		// PutTargets refuses another account's bus target without a RoleArn,
		// by the newer user-guide rule, unless it replaces one with the same
		// Id and Arn whose role it keeps (withBusRole).
		sw("events.bus-target.cross-account-role", "required", evidence.AWSDoc, "63-cross-account", "EBB-09",
			fidelity.NoScenario{Reason: "unrecorded: EBB-09 needs a second account, which the recording account lacks"},
			"eventbridge.PutTargets"),
		// A forwarded event keeps its time and resources (fanOut.forward).
		sw("events.bus-target.forwarded-time-resources", "kept", evidence.AWSRecorded, "62-an-event-bus-as-a-target", "EBB-08", undelivered("08-put-h1"),
			"eventbridge.PutEvents"),
		// PutTargets accepts an event bus that does not exist, and a
		// delivery to it is dropped as PutEvents drops one (targetOf). The
		// acceptance is recorded; the drop is not.
		sw("events.bus-target.missing-bus", "dropped", evidence.AWSRecorded, "62-an-event-bus-as-a-target", "EBB-08", ebb("08-missing-target-bus"),
			"eventbridge.PutTargets"),
	}
}
