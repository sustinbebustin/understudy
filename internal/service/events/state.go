package events

import (
	"maps"
	"regexp"
	"slices"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/schedexpr"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of State in snapshots.
const stateVersion = 1

// State is the EventBridge state of one account and region: the rules of
// its default bus, by name. Records are values, replaced on change and
// never mutated in place, so a value read out of a cell never aliases it.
type State struct {
	Rules map[string]Rule `json:"rules"`
	// TargetSeq is the last Target.Seq handed out in the scope.
	TargetSeq uint64 `json:"targetSeq,omitzero"`
}

func newState(aws.Scope) State { return State{Rules: map[string]Rule{}} }

// IsZero reports a scope without rules, which snapshots skip.
func (s *State) IsZero() bool { return len(s.Rules) == 0 }

// Rule is one rule of the default bus, as PutRule last set it.
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
	Rule   string
	Reason string
}

func (e *StoredRuleError) Error() string {
	return "events: the stored rule " + strconv.Quote(e.Rule) + " " + e.Reason +
		"; the snapshot was not loaded. Fix or remove the rule in the snapshot, or recreate it with PutRule"
}

// check refuses decoded state PutRule and PutTargets could not have stored:
// a rule under a key that is not a rule name or not its own name, with no
// trigger, a schedule the decoder left unset, an unknown state, a next fire
// on a rule that has no schedule or is disabled, or more targets than a
// rule may have; a target with no input form, an id another target of its
// rule has, or a creation sequence number that is zero, past TargetSeq or
// shared with another target. A missing or null rules object is a scope
// without rules.
func (s *State) check() error {
	if s.Rules == nil {
		s.Rules = map[string]Rule{}
	}
	// seqs maps each target sequence number seen to its rule's name.
	seqs := map[uint64]string{}
	for _, name := range slices.Sorted(maps.Keys(s.Rules)) {
		rule := s.Rules[name]
		if reason, ok := rule.problem(name); ok {
			return &StoredRuleError{Rule: name, Reason: reason}
		}
		ids := map[string]bool{}
		for _, t := range rule.Targets {
			if reason, ok := s.targetProblem(&t, seqs); ok {
				return &StoredRuleError{Rule: name, Reason: "has the target " + strconv.Quote(t.ID) + " " + reason}
			}
			if ids[t.ID] {
				return &StoredRuleError{Rule: name, Reason: "has two targets with the id " + strconv.Quote(t.ID)}
			}
			ids[t.ID] = true
			seqs[t.Seq] = name
		}
	}
	return nil
}

// ruleName is the RuleName pattern and length PutRule accepts.
var ruleName = regexp.MustCompile(`^[.\-_A-Za-z0-9]{1,64}$`)

// problem is why PutRule could not have stored the decoded rule r under the
// key key, when it could not.
func (r *Rule) problem(key string) (string, bool) {
	switch {
	case !ruleName.MatchString(key):
		return "is stored under a key that is not a rule name (1 to 64 of letters, digits, '.', '-' and '_')", true
	case r.Name != key:
		return "is named " + strconv.Quote(r.Name) + " but stored under another name", true
	case len(r.Targets) > maxTargets:
		return "has " + strconv.Itoa(len(r.Targets)) + " targets; a rule has at most " + strconv.Itoa(maxTargets), true
	case r.Trigger == nil:
		return "has neither an event pattern nor a schedule expression", true
	}
	sched, scheduled := r.Trigger.schedule()
	switch {
	case scheduled && sched.parsed == nil:
		return "has a trigger with no schedule expression", true
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
// checked before it, by rule name.
func (s *State) targetProblem(t *Target, seqs map[uint64]string) (string, bool) {
	seq := strconv.FormatUint(t.Seq, 10)
	if t.Input == nil {
		return "with no input form", true
	}
	if t.Seq == 0 || t.Seq > s.TargetSeq {
		return "with the sequence number " + seq + ", which is not between 1 and the snapshot's targetSeq " + strconv.FormatUint(s.TargetSeq, 10), true
	}
	if other, dup := seqs[t.Seq]; dup {
		return "with the sequence number " + seq + ", which a target of the rule " + strconv.Quote(other) + " also has", true
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
// tagged-union convention. Decoding refuses state PutRule could not have
// stored (State.check). The tables are static, so an error is a bug in
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
