package events

import (
	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/ruler"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TimerSchedule fires when a scheduled rule is due. The key's ID is the
// rule's ARN.
const TimerSchedule kernel.TimerKind = "events.schedule"

// Service is Amazon EventBridge: one state cell per account and region,
// holding its buses and their rules and targets.
type Service struct {
	k     *kernel.Kernel
	ids   *ids.Source
	cells *store.Partitioned[aws.Scope, State]
	// schedule is TimerSchedule as registered with the kernel.
	schedule kernel.HandledKind
	// profile is the Event Ruler profile event patterns compile with.
	profile ruler.EventBridge
	// patterns holds the rules' event patterns compiled with profile.
	patterns *patternCache
}

// declareAccess states what CreateEventBus, PutRule and PutTargets touch
// and use besides their primary action: each IAM role the input passes
// (RoleArn), on which the access record keeps iam:PassRole, and
// events:TagResource for a bus or rule created with tags (CreateEventBus
// and PutRule, Permissions). It runs after the bind sites, whose bindings
// it extends.
func declareAccess(b *kernel.Binder) {
	kernel.Declare(b, eventbridgeapi.OpCreateEventBus,
		kernel.WithActions(func(_ *kernel.Call, in *eventbridgeapi.CreateEventBusInput) []registry.IAMAction {
			return tagActions(in.Tags)
		}))
	kernel.Declare(b, eventbridgeapi.OpPutRule,
		kernel.WithResources(func(_ *kernel.Call, in *eventbridgeapi.PutRuleInput) []aws.ARN { return passedRoles(in.RoleArn) }),
		kernel.WithActions(func(_ *kernel.Call, in *eventbridgeapi.PutRuleInput) []registry.IAMAction { return tagActions(in.Tags) }))
	kernel.Declare(b, eventbridgeapi.OpPutTargets,
		kernel.WithResources(func(_ *kernel.Call, in *eventbridgeapi.PutTargetsInput) []aws.ARN {
			roles := make([]*string, 0, len(in.Targets))
			for i := range in.Targets {
				roles = append(roles, in.Targets[i].RoleArn)
			}
			return passedRoles(roles...)
		}))
	// The bus an EventBusName names is its ARN, known from the name: step 8
	// asks events:PutEvents about each entry's bus (iam-evaluation.md 2.1).
	// The bus operations name theirs through namedBus.
	kernel.Declare(b, eventbridgeapi.OpPutEvents,
		kernel.WithResources(func(c *kernel.Call, in *eventbridgeapi.PutEventsInput) []aws.ARN {
			out := make([]aws.ARN, 0, len(in.Entries))
			for i := range in.Entries {
				out = append(out, parseBus(c.Scope(), in.Entries[i].EventBusName).busARN(c.Scope()))
			}
			return out
		}))
}

// tagActions is events:TagResource for a create call that tags what it
// creates, and nothing for one without tags.
func tagActions(tags []eventbridgeapi.Tag) []registry.IAMAction {
	if len(tags) == 0 {
		return nil
	}
	return []registry.IAMAction{{Prefix: "events", Name: "TagResource"}}
}

// passedRoles are the IAM roles among roles; a value that is not an IAM
// ARN passes none, and the handler answers it.
func passedRoles(roles ...*string) []aws.ARN {
	var out []aws.ARN
	for _, r := range roles {
		if r == nil {
			continue
		}
		if a, err := aws.ParseARN(*r); err == nil && a.Service == "iam" {
			out = append(out, a)
		}
	}
	return out
}

// New binds the EventBridge operations understudy implements and registers
// the schedule timer. Every other operation stays unbound and answers 501.
func New(d kernel.ServiceDeps, _ Options) *Service {
	c, err := codec()
	if err != nil {
		c = store.Codec{
			Marshal:   func(any) ([]byte, error) { return nil, err },
			Unmarshal: func([]byte, any) error { return err },
		}
	}
	s := &Service{
		k:     d.Kernel,
		ids:   d.IDs,
		cells: store.NewPartitioned(d.Kernel.Gate(), c, newState),
	}
	s.patterns = newPatternCache(s.profile)
	s.schedule = d.Kernel.Timers().Handle(TimerSchedule, s.onSchedule)
	b := eventbridgeapi.NewBinder(d.Binder)
	s.bindBuses(b)
	s.bindPolicy(b)
	s.bindRules(b)
	s.bindTags(b)
	s.bindTargets(b)
	s.bindEvents(b)
	declareAccess(d.Binder)
	return s
}

// rateRestart is when a rate schedule's period starts: AWS documents that
// a rate rule starts when it is created; whether a later PutRule or
// EnableRule restarts it is unrecorded (secrets-logs-events-alarms.md 4.2).
func rateRestart() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "events.schedule.restart",
		Value: "on-schedule-change-or-enable",
		Label: evidence.Inferred,
		Ref:   "docs/research/secrets-logs-events-alarms.md#42-schedule-expressions-rate-and-cron",
		Ops:   []string{"eventbridge.PutRule", "eventbridge.EnableRule"},
		Settles: fidelity.NoScenario{
			Reason: "events/R14 records the EnableRule half only, as arrival times (02-enable.time, 02-arrivals-after-enable.jsonl) a scenario does not replay; a PutRule schedule change is unrecorded",
		},
	}
}

// unmatchedInputPath is what a target whose InputPath selects nothing in
// an event receives: no AWS source says; understudy delivers nothing.
func unmatchedInputPath() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    "events.input-path.unmatched",
		Value:   "not-delivered",
		Label:   evidence.Inferred,
		Ref:     "docs/research/secrets-logs-events-alarms.md#45-targets",
		Ops:     []string{"eventbridge.PutEvents"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: no recording delivers to a target whose InputPath selects nothing"},
	}
}

// detailIntegerRange is how PutEvents fails an entry whose Detail holds an
// integer outside the signed 64-bit range API_PutEvents documents: no AWS
// source records the code or text, so understudy uses the recorded
// MalformedDetail failure for a bad Detail.
func detailIntegerRange() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    "events.put-events.detail-integer-range",
		Value:   codeMalformedDetail,
		Label:   evidence.Inferred,
		Ref:     "https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutEvents.html",
		Ops:     []string{"eventbridge.PutEvents"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: no recording sends a Detail integer outside the signed 64-bit range"},
	}
}

// Switches lists the behaviors chosen without AWS-recorded evidence: the
// service's own, the status of a denial (iam-enforcement-surfaces.md 12.8)
// and those of its Event Ruler profile, with the label, operations and
// settling step rulerSwitches states for each.
func (s *Service) Switches() []fidelity.Entry {
	profile := ruler.Switches(s.profile)
	table := rulerSwitches()
	buses := busSwitches()
	out := make([]fidelity.Entry, 0, 4+len(buses)+len(profile))
	out = append(out, rateRestart().Entry(), unmatchedInputPath().Entry(), detailIntegerRange().Entry(),
		authz.DenialSwitch("events.denial.status", "AccessDeniedException 400", evidence.Inferred, "3.2", "IAM-ENF-08", s.k.Bindings().AuthorizedOps(eventbridgeapi.Service())))
	for _, sw := range buses {
		out = append(out, sw.Entry())
	}
	for _, sw := range profile {
		m := table[sw.Name]
		out = append(out, fidelity.Switch[string]{
			Name:    sw.Name,
			Value:   sw.Value,
			Label:   m.label,
			Ref:     sw.Ref,
			Record:  sw.Record,
			Ops:     m.ops,
			Settles: m.settles,
		}.Entry())
	}
	return out
}

// rulerSwitch is what EventBridge states about one of Ruler's EventBridge
// profile switches: what backs its value, the operations it governs and
// what settles it.
type rulerSwitch struct {
	label   evidence.Label
	ops     []string
	settles fidelity.Settles
}

// rulerSwitches holds the rulerSwitch of every EventBridge profile switch
// by name. A name it lacks gets no label, operations or settles, which the
// coverage checks refuse. PutRule and TestEventPattern compile a pattern,
// so the switches that accept or refuse one govern them; PutEvents and
// TestEventPattern match it, so the switches that decide a match govern
// those. The events/R14 steps a switch names are its settles claims in
// testdata/parity/events/R14/scenario.json.
func rulerSwitches() map[string]rulerSwitch {
	compile := []string{"eventbridge.PutRule", "eventbridge.TestEventPattern"}
	match := []string{"eventbridge.PutEvents", "eventbridge.TestEventPattern"}
	r14 := func(step string) fidelity.Settles {
		return fidelity.SettledBy{Step: fidelity.StepRef{Scenario: "events/R14", Step: step}}
	}
	unreplayed := func(steps string) fidelity.Settles {
		return fidelity.NoScenario{Reason: "recorded in events/R14 (" + steps + "); the events/R14 scenario does not replay those steps yet"}
	}
	return map[string]rulerSwitch{
		"events.pattern.exists-true-empty-value": {
			evidence.Inferred, match,
			fidelity.NoScenario{Reason: "unrecorded for EventBridge: matching-engines.md H2 was recorded for SNS and Lambda only"},
		},
		"events.pattern.empty-input": {evidence.Inferred, match, r14("H1-01")},
		"events.pattern.bare-number": {
			evidence.Inferred, match,
			fidelity.NoScenario{Reason: "events/R14's H8 steps do not decide it: both values answer false for a string"},
		},
		"events.pattern.malformed-or":            {evidence.Inferred, compile, unreplayed("H10-01 to H10-06")},
		"events.pattern.exactly-operator":        {evidence.Inferred, compile, r14("H14-01")},
		"events.pattern.or-shared-values":        {evidence.Inferred, match, r14("H14-02")},
		"events.pattern.wildcard-complexity-cap": {evidence.Inferred, compile, unreplayed("H11-01 to H11-03")},
		"events.pattern.or-combination-counting": {evidence.Inferred, compile, unreplayed("H12-01-10x10x10, H12-02-7x11x13, H12-03-nested")},
	}
}
