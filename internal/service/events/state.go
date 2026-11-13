package events

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/schedexpr"
	"github.com/sustinbebustin/understudy/internal/service/events/envelope"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of State in snapshots. Version 2 holds
// buses and keys rules by bus; a version 1 section is refused, not migrated
// (ADR 0009).
const stateVersion = 2

// State is the EventBridge state of one account and region: its buses and
// their rules. Records are values, replaced on change and never mutated in
// place, so a value read out of a cell never aliases it.
type State struct {
	// Buses are the scope's buses by name: each custom bus, and the default
	// bus once a write materializes it (eventbridge-buses.md 10.2.2).
	Buses map[string]Bus `json:"buses,omitempty"`
	// Rules are the scope's rules by bus name, then rule name. A bus without
	// rules has no entry.
	Rules map[string]map[string]Rule `json:"rules"`
	// TargetSeq is the last Target.Seq handed out in the scope, across all
	// its buses.
	TargetSeq uint64 `json:"targetSeq,omitzero"`
	// FirstUse is the admission instant of the first call that changed the
	// scope's state (events.default-bus.creation-time); zero until then.
	FirstUse clock.Instant `json:"firstUse,omitzero"`
}

func newState(aws.Scope) State {
	return State{Buses: map[string]Bus{}, Rules: map[string]map[string]Rule{}}
}

// IsZero reports a scope no call has changed, which snapshots skip. A scope
// left with only FirstUse is kept: it is the creation time of a default bus
// no write has stored (State.bus), which a snapshot must not move.
func (s *State) IsZero() bool {
	if !s.FirstUse.IsZero() || len(s.Buses) > 0 {
		return false
	}
	for _, rules := range s.Rules {
		if len(rules) > 0 {
			return false
		}
	}
	return true
}

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*State)(nil)

// used records at as the scope's first use unless an earlier call changed
// the state. Every write that changes the state calls it.
func (s *State) used(at clock.Instant) {
	if s.FirstUse.IsZero() {
		s.FirstUse = at
	}
}

// rule returns the rule name of bus.
func (s *State) rule(bus, name string) (Rule, bool) {
	r, ok := s.Rules[bus][name]
	return r, ok
}

// setRule stores r on bus under its name.
func (s *State) setRule(bus string, r Rule) {
	rules, ok := s.Rules[bus]
	if !ok {
		rules = map[string]Rule{}
		s.Rules[bus] = rules
	}
	rules[r.Name] = r
}

// deleteRule removes the rule name of bus, and the bus's entry in Rules
// with its last rule.
func (s *State) deleteRule(bus, name string) {
	delete(s.Rules[bus], name)
	if len(s.Rules[bus]) == 0 {
		delete(s.Rules, bus)
	}
}

// bus returns the bus name: its stored record, or for a default bus no
// write has materialized, the record one would store, created and modified
// at the scope's first use, or at when nothing has changed the scope
// (eventbridge-buses.md 10.2.2, events.default-bus.creation-time). ok is
// false for a custom bus the scope does not hold.
func (s *State) bus(name string, at clock.Instant) (Bus, bool) {
	if b, ok := s.Buses[name]; ok {
		return b, true
	}
	if name != defaultBus {
		return Bus{}, false
	}
	first := s.FirstUse
	if first.IsZero() {
		first = at
	}
	return Bus{Name: defaultBus, Created: first, Modified: first}, true
}

// requireBus refuses a bus the scope does not hold: it holds its default
// bus always and a custom bus from CreateEventBus to DeleteEventBus. A rule
// or target call on a missing bus gets the bus operations' not-found text;
// AWS's answer to those calls is unrecorded (eventbridge-buses.md 5.4,
// EBB-03).
func (s *State) requireBus(name string) *awserr.Error {
	if _, ok := s.Buses[name]; ok || name == defaultBus {
		return nil
	}
	return errBusNotFound(name)
}

// setBus stores b, which materializes the default bus, as a write admitted
// at at.
func (s *State) setBus(b Bus, at clock.Instant) {
	s.Buses[b.Name] = b
	s.used(at)
}

// buses returns every bus of the scope, the default bus included whether or
// not a write materialized it, sorted by name (events.list-buses.order).
func (s *State) buses(at clock.Instant) []Bus {
	out := make([]Bus, 0, len(s.Buses)+1)
	if _, ok := s.Buses[defaultBus]; !ok {
		def, _ := s.bus(defaultBus, at)
		out = append(out, def)
	}
	for _, b := range s.Buses {
		out = append(out, b)
	}
	slices.SortFunc(out, func(a, b Bus) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// countsTowardQuota is how many buses the scope holds against the bus
// quota: every custom bus and the default bus (eventbridge-buses.md 7).
func (s *State) countsTowardQuota() int {
	if _, ok := s.Buses[defaultBus]; ok {
		return len(s.Buses)
	}
	return len(s.Buses) + 1
}

// Bus is one event bus of a scope: a custom bus as CreateEventBus and
// UpdateEventBus last set it, or the default bus once a write materializes
// it (eventbridge-buses.md 10.2.1, 10.2.2). Its ARN derives from the scope
// and Name.
type Bus struct {
	Name string `json:"name"`
	// Description is nil when absent or empty.
	Description *string `json:"description,omitempty"`
	// KMSKeyIdentifier is stored as busKeyIdentifier gives it and never
	// applied; nil when absent or empty.
	KMSKeyIdentifier *string `json:"kmsKeyIdentifier,omitempty"`
	// DeadLetter is stored as sent and never applied.
	DeadLetter *DeadLetterConfig `json:"deadLetter,omitempty"`
	// Log is stored as sent, NONE and OFF included, and never applied.
	Log      *LogConfig        `json:"log,omitempty"`
	Policy   *Policy           `json:"policy,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
	Created  clock.Instant     `json:"createdAt"`
	Modified clock.Instant     `json:"modifiedAt"`
}

// LogConfig is a bus's LogConfig, members as sent.
type LogConfig struct {
	IncludeDetail *eventbridgeapi.IncludeDetail `json:"includeDetail,omitempty"`
	Level         *eventbridgeapi.Level         `json:"level,omitempty"`
}

// Policy is a bus's resource policy (eventbridge-buses.md 10.2.5).
type Policy struct {
	// Statements are in insertion order; a non-empty Sid is unique.
	Statements []PolicyStatement `json:"statements"`
	// Document is the text PutPermission's Policy member stored, returned
	// verbatim until a statement call changes Statements; nil otherwise.
	Document *string `json:"document,omitempty"`
}

// policyVersion is the Version of a policy document understudy writes.
const policyVersion = "2012-10-17"

// text is the policy as DescribeEventBus and ListEventBuses return it: the
// document PutPermission's Policy member stored, verbatim, or else a
// document of the statements in order, compact and without an Id
// (eventbridge-buses.md 4.2).
func (p *Policy) text() string {
	if p.Document != nil {
		return *p.Document
	}
	var b strings.Builder
	b.WriteString(`{"Version":"` + policyVersion + `","Statement":[`)
	for i, st := range p.Statements {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(st.JSON)
	}
	b.WriteString("]}")
	return b.String()
}

// PolicyStatement is one statement of a bus policy, kept as its JSON text
// with its Sid.
type PolicyStatement struct {
	Sid  string `json:"sid,omitempty"`
	JSON string `json:"json"`
}

// Rule is one rule of a bus, as PutRule last set it.
type Rule struct {
	Name        string                   `json:"name"`
	Trigger     Trigger                  `json:"trigger"`
	State       eventbridgeapi.RuleState `json:"state"`
	Description *string                  `json:"description,omitempty"`
	RoleARN     *string                  `json:"roleArn,omitempty"`
	// NextFire is when the schedule fires next; zero when the rule has no
	// schedule, is disabled or its cron expression never fires again.
	NextFire clock.Instant     `json:"nextFireAt,omitzero"`
	Tags     map[string]string `json:"tags,omitempty"`
	// Targets are in creation order; a replaced target keeps its place.
	Targets []Target `json:"targets,omitempty"`
}

// enabled reports whether the rule matches events and fires its schedule:
// ENABLED, or ENABLED_WITH_ALL_CLOUDTRAIL_MANAGEMENT_EVENTS, which differs
// only for CloudTrail events understudy never produces.
func (r *Rule) enabled() bool { return r.State != eventbridgeapi.RuleStateDisabled }

// Trigger is what makes a rule send events: an event pattern, a schedule
// or both. A rule always has one.
//
//sumtype:decl
type Trigger interface {
	// pattern is the event pattern, when the rule has one.
	pattern() (string, bool)
	// schedule is the schedule, when the rule has one.
	schedule() (Schedule, bool)
}

// PatternTrigger is a rule with an event pattern only.
type PatternTrigger struct {
	Pattern string `json:"pattern"`
}

// ScheduleTrigger is a rule with a schedule expression only.
type ScheduleTrigger struct {
	Schedule Schedule `json:"schedule"`
}

// PatternAndSchedule is a rule with both an event pattern and a schedule
// expression: it sends matching events and fires on its schedule.
type PatternAndSchedule struct {
	Pattern  string   `json:"pattern"`
	Schedule Schedule `json:"schedule"`
}

func (t PatternTrigger) pattern() (string, bool)     { return t.Pattern, true }
func (ScheduleTrigger) pattern() (string, bool)      { return "", false }
func (t PatternAndSchedule) pattern() (string, bool) { return t.Pattern, true }

func (PatternTrigger) schedule() (Schedule, bool)       { return Schedule{}, false }
func (t ScheduleTrigger) schedule() (Schedule, bool)    { return t.Schedule, true }
func (t PatternAndSchedule) schedule() (Schedule, bool) { return t.Schedule, true }

// newTrigger is the trigger of a PutRule request's EventPattern and
// ScheduleExpression; the schedule must parse.
func newTrigger(pattern, expr *string) (Trigger, *awserr.Error) {
	if expr == nil {
		if pattern == nil {
			return nil, errNoPatternOrSchedule()
		}
		return PatternTrigger{Pattern: *pattern}, nil
	}
	sched, err := parseSchedule(*expr)
	if err != nil {
		return nil, eventbridgeapi.ErrValidationException(err.Message())
	}
	if pattern == nil {
		return ScheduleTrigger{Schedule: sched}, nil
	}
	return PatternAndSchedule{Pattern: *pattern, Schedule: sched}, nil
}

// Schedule is a parsed schedule expression. It is stored as the
// expression's text and parsed again on load, so a stored schedule always
// parses: a snapshot with one that does not fails to load.
type Schedule struct {
	text   string
	parsed schedexpr.Schedule
}

func parseSchedule(expr string) (Schedule, *schedexpr.Error) {
	parsed, err := schedexpr.Parse(expr)
	if err != nil {
		return Schedule{}, err
	}
	return Schedule{text: expr, parsed: parsed}, nil
}

// String returns the expression as PutRule sent it.
func (s Schedule) String() string { return s.text }

// MarshalText writes the expression.
func (s Schedule) MarshalText() ([]byte, error) { return []byte(s.text), nil }

// UnmarshalText parses a stored expression.
func (s *Schedule) UnmarshalText(b []byte) error {
	parsed, err := parseSchedule(string(b))
	if err != nil {
		return &StoredScheduleError{Expression: string(b), Detail: err.Detail()}
	}
	*s = parsed
	return nil
}

// first is the first fire of the schedule started at start: start itself
// for a rate, as AWS fires a rate rule at its creation and its enable
// (events/R14 02), and the first fire strictly after start for a cron.
func (s Schedule) first(start clock.Instant) clock.Instant {
	if s.rate() {
		return start
	}
	return s.next(start)
}

// rate reports whether s is a rate expression, whose fires are counted
// from when it starts rather than named by the expression.
func (s Schedule) rate() bool {
	_, ok := s.parsed.(schedexpr.Rate)
	return ok
}

// next is the first fire strictly after after; zero when the schedule never
// fires again.
func (s Schedule) next(after clock.Instant) clock.Instant {
	next, ok := schedexpr.Next(s.parsed, after.Wire())
	if !ok {
		return clock.Instant{}
	}
	return clock.FromWire(next)
}

// StoredScheduleError is a snapshot's schedule expression that does not
// parse. PutRule refuses such an expression, so the snapshot was edited or
// written by a build with a different schedule grammar.
type StoredScheduleError struct {
	Expression string
	// Detail says where parsing stopped.
	Detail string
}

func (e *StoredScheduleError) Error() string {
	return "events: the stored schedule expression " + strconv.Quote(e.Expression) + " does not parse (" + e.Detail +
		"); the snapshot was not loaded. Fix or remove the rule's schedule in the snapshot, or recreate the rule with PutRule"
}

// StoredRuleError is a snapshot's rule that PutRule could not have stored.
type StoredRuleError struct {
	Rule string
	// Bus is the name of the bus the rule is stored under.
	Bus    string
	Reason string
}

func (e *StoredRuleError) Error() string {
	return "events: the stored rule " + strconv.Quote(e.Rule) + " on the event bus " + strconv.Quote(e.Bus) + " " + e.Reason +
		"; the snapshot was not loaded. Fix or remove the rule in the snapshot, or recreate it with PutRule"
}

// StoredBusError is a snapshot's event bus that CreateEventBus, TagResource
// or PutPermission could not have stored.
type StoredBusError struct {
	Bus    string
	Reason string
}

func (e *StoredBusError) Error() string {
	return "events: the stored event bus " + strconv.Quote(e.Bus) + " " + e.Reason +
		"; the snapshot was not loaded. Fix or remove the bus in the snapshot, or recreate it with CreateEventBus"
}

// check refuses decoded state CreateEventBus, PutPermission, PutRule and
// PutTargets could not have stored: a bus under a key that is not a bus
// name or not its own name, with more tags than a bus may have, or with a
// policy statement that is not a JSON object or a Sid another statement
// has; a rule under a bus the state does not hold, under a key that is not
// a rule name or not its own name, with no trigger, a schedule the decoder
// left unset, a schedule on a bus other than the default, an unknown state,
// a next fire on a rule that has no schedule or is disabled, or more
// targets or tags than a rule may have; a target with no input form, an id
// another target of its rule has, or a creation sequence number that is
// zero, past TargetSeq or shared with another target of the scope. A
// missing or null rules object is a scope without rules.
func (s *State) check() error {
	if s.Buses == nil {
		s.Buses = map[string]Bus{}
	}
	if s.Rules == nil {
		s.Rules = map[string]map[string]Rule{}
	}
	for _, name := range slices.Sorted(maps.Keys(s.Buses)) {
		bus := s.Buses[name]
		if reason, ok := bus.problem(name); ok {
			return &StoredBusError{Bus: name, Reason: reason}
		}
	}
	// seqs maps each target sequence number seen to its rule.
	seqs := map[uint64]storedRule{}
	for _, bus := range slices.Sorted(maps.Keys(s.Rules)) {
		rules := s.Rules[bus]
		if len(rules) == 0 {
			delete(s.Rules, bus)
			continue
		}
		_, held := s.Buses[bus]
		for _, name := range slices.Sorted(maps.Keys(rules)) {
			at := storedRule{bus: bus, name: name}
			if bus != defaultBus && !held {
				return at.error("is stored under a bus the snapshot does not hold")
			}
			if err := s.checkRule(at, rules[name], seqs); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkRule is check on the rule at, adding its targets' sequence numbers
// to seqs.
func (s *State) checkRule(at storedRule, rule Rule, seqs map[uint64]storedRule) *StoredRuleError {
	if reason, ok := rule.problem(at.bus, at.name); ok {
		return at.error(reason)
	}
	ids := map[string]bool{}
	for _, t := range rule.Targets {
		if reason, ok := s.targetProblem(&t, seqs); ok {
			return at.error("has the target " + strconv.Quote(t.ID) + " " + reason)
		}
		if ids[t.ID] {
			return at.error("has two targets with the id " + strconv.Quote(t.ID))
		}
		ids[t.ID] = true
		seqs[t.Seq] = at
	}
	return nil
}

// storedRule is where check found a rule: its bus and name.
type storedRule struct{ bus, name string }

func (r storedRule) error(reason string) *StoredRuleError {
	return &StoredRuleError{Rule: r.name, Bus: r.bus, Reason: reason}
}

// busName is the Name pattern and length CreateEventBus accepts, which
// also admits the name default of a materialized default bus: the model's
// EventBusName less the '/' AWS refuses (eventbridge-buses.md 2.2).
var busName = regexp.MustCompile(`^[.\-_A-Za-z0-9]{1,256}$`)

// problem is why no call could have stored the decoded bus b under the key
// key, when none could.
func (b *Bus) problem(key string) (string, bool) {
	switch {
	case !busName.MatchString(key):
		return "is stored under a key that is not an event bus name (1 to 256 of letters, digits, '.', '-' and '_')", true
	case b.Name != key:
		return "is named " + strconv.Quote(b.Name) + " but stored under another name", true
	case len(b.Tags) > maxTags:
		return "has " + strconv.Itoa(len(b.Tags)) + " tags; a bus has at most " + strconv.Itoa(maxTags), true
	case b.Policy != nil:
		return b.Policy.problem()
	}
	return "", false
}

// problem is why PutPermission could not have stored the decoded policy p,
// when it could not: a statement that is not a JSON object, or two
// statements with one non-empty Sid (eventbridge-buses.md 10.2.5).
func (p *Policy) problem() (string, bool) {
	sids := map[string]bool{}
	for i, st := range p.Statements {
		v, ok := envelope.ParseJSON(st.JSON)
		if _, object := v.(document.Map); !ok || !object {
			return "has a policy statement (number " + strconv.Itoa(i+1) + ", Sid " + strconv.Quote(st.Sid) + ") that is not a JSON object", true
		}
		if st.Sid == "" {
			continue
		}
		if sids[st.Sid] {
			return "has two policy statements with the Sid " + strconv.Quote(st.Sid), true
		}
		sids[st.Sid] = true
	}
	return "", false
}

// ruleName is the RuleName pattern and length PutRule accepts.
var ruleName = regexp.MustCompile(`^[.\-_A-Za-z0-9]{1,64}$`)

// problem is why PutRule could not have stored the decoded rule r on bus
// under the key key, when it could not.
func (r *Rule) problem(bus, key string) (string, bool) {
	switch {
	case !ruleName.MatchString(key):
		return "is stored under a key that is not a rule name (1 to 64 of letters, digits, '.', '-' and '_')", true
	case r.Name != key:
		return "is named " + strconv.Quote(r.Name) + " but stored under another name", true
	case len(r.Targets) > maxTargets:
		return "has " + strconv.Itoa(len(r.Targets)) + " targets; a rule has at most " + strconv.Itoa(maxTargets), true
	case len(r.Tags) > maxTags:
		return "has " + strconv.Itoa(len(r.Tags)) + " tags; a rule has at most " + strconv.Itoa(maxTags), true
	case r.Trigger == nil:
		return "has neither an event pattern nor a schedule expression", true
	}
	sched, scheduled := r.Trigger.schedule()
	switch {
	case scheduled && sched.parsed == nil:
		return "has a trigger with no schedule expression", true
	case scheduled && bus != defaultBus:
		return "has a schedule expression, which only a rule on the default event bus can have", true
	case !r.State.Known():
		return "has the state " + strconv.Quote(string(r.State)) + ", which is not ENABLED, DISABLED or ENABLED_WITH_ALL_CLOUDTRAIL_MANAGEMENT_EVENTS", true
	case !r.NextFire.IsZero() && !scheduled:
		return "has a next fire time but no schedule expression", true
	case !r.NextFire.IsZero() && !r.enabled():
		return "has a next fire time but is DISABLED", true
	}
	return "", false
}

// targetProblem is why PutTargets could not have stored the decoded target
// t, when it could not; seqs holds the sequence numbers of the targets
// checked before it, by rule.
func (s *State) targetProblem(t *Target, seqs map[uint64]storedRule) (string, bool) {
	seq := strconv.FormatUint(t.Seq, 10)
	if t.Input == nil {
		return "with no input form", true
	}
	if t.Seq == 0 || t.Seq > s.TargetSeq {
		return "with the sequence number " + seq + ", which is not between 1 and the snapshot's targetSeq " + strconv.FormatUint(s.TargetSeq, 10), true
	}
	if other, dup := seqs[t.Seq]; dup {
		return "with the sequence number " + seq + ", which a target of the rule " + strconv.Quote(other.name) +
			" on the event bus " + strconv.Quote(other.bus) + " also has", true
	}
	return "", false
}

// Target is one target of a rule, stored as PutTargets sent it so
// ListTargetsByRule echoes it exactly.
type Target struct {
	ID  string `json:"id"`
	ARN string `json:"arn"`
	// Seq orders targets by creation across the scope's rules: fan-out
	// follows it.
	Seq     uint64  `json:"seq"`
	Input   Input   `json:"input"`
	RoleARN *string `json:"roleArn,omitempty"`
	// SQS is SqsParameters; MessageGroupID is its MessageGroupId.
	SQS        *SQSParameters    `json:"sqs,omitempty"`
	DeadLetter *DeadLetterConfig `json:"deadLetter,omitempty"`
	Retry      *RetryPolicy      `json:"retry,omitempty"`
}

// SQSParameters is a target's SqsParameters.
type SQSParameters struct {
	MessageGroupID *string `json:"messageGroupId,omitempty"`
}

// DeadLetterConfig is a target's DeadLetterConfig.
type DeadLetterConfig struct {
	ARN *string `json:"arn,omitempty"`
}

// RetryPolicy is a target's RetryPolicy, members as sent.
type RetryPolicy struct {
	MaximumRetryAttempts     *int32 `json:"maximumRetryAttempts,omitempty"`
	MaximumEventAgeInSeconds *int32 `json:"maximumEventAgeInSeconds,omitempty"`
}

// Input is how a target's payload is built from a matched event: at most
// one of Input, InputPath and InputTransformer, which PutTargets enforces.
//
//sumtype:decl
type Input interface{ isInput() }

// MatchedEvent delivers the whole event envelope.
type MatchedEvent struct{}

// ConstantInput delivers Input, a JSON text, in place of the event.
type ConstantInput struct {
	JSON string `json:"json"`
}

// PathInput delivers what InputPath selects in the event.
type PathInput struct {
	Path string `json:"path"`
}

// TransformedInput delivers InputTransformer's template with its
// variables (InputPathsMap) filled from the event.
type TransformedInput struct {
	Variables map[string]string `json:"variables,omitempty"`
	Template  string            `json:"template"`
}

func (MatchedEvent) isInput()     {}
func (ConstantInput) isInput()    {}
func (PathInput) isInput()        {}
func (TransformedInput) isInput() {}

// codec returns the state codec, with the Input and Trigger unions in the
// tagged-union convention. Decoding refuses state PutRule and PutTargets
// could not have stored (State.check). The tables are static, so an error is a bug in
// them.
func codec() (store.Codec, error) {
	inputs, err := jsonx.NewUnion("events.Input",
		jsonx.Variant[Input, MatchedEvent]("matched-event"),
		jsonx.Variant[Input, ConstantInput]("constant"),
		jsonx.Variant[Input, PathInput]("path"),
		jsonx.Variant[Input, TransformedInput]("transformer"),
	)
	if err != nil {
		return store.Codec{}, err
	}
	triggers, err := jsonx.NewUnion("events.Trigger",
		jsonx.Variant[Trigger, PatternTrigger]("pattern"),
		jsonx.Variant[Trigger, ScheduleTrigger]("schedule"),
		jsonx.Variant[Trigger, PatternAndSchedule]("pattern-and-schedule"),
	)
	if err != nil {
		return store.Codec{}, err
	}
	c := jsonx.NewCodec(inputs, triggers)
	unmarshal := func(data []byte, v any) error {
		if err := c.Unmarshal(data, v); err != nil {
			return err
		}
		if st, ok := v.(*State); ok {
			return st.check()
		}
		return nil
	}
	return store.Codec{Marshal: c.Marshal, Unmarshal: unmarshal}, nil
}
