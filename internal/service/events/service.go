package events

import (
	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
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
// holding the rules and targets of its default bus.
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

// declareAccess states what PutRule and PutTargets touch and use besides
// their primary action: each IAM role the input passes (RoleArn), on which
// the access record keeps iam:PassRole, and events:TagResource for a rule
// created with tags (PutRule, Permissions). It runs after the bind sites,
// whose bindings it extends.
func declareAccess(b *kernel.Binder) {
	kernel.Declare(b, eventbridgeapi.OpPutRule,
		kernel.WithResources(func(_ *kernel.Call, in *eventbridgeapi.PutRuleInput) []aws.ARN { return passedRoles(in.RoleArn) }),
		kernel.WithActions(func(_ *kernel.Call, in *eventbridgeapi.PutRuleInput) []registry.IAMAction {
			if len(in.Tags) == 0 {
				return nil
			}
			return []registry.IAMAction{{Prefix: "events", Name: "TagResource"}}
		}))
	kernel.Declare(b, eventbridgeapi.OpPutTargets,
		kernel.WithResources(func(_ *kernel.Call, in *eventbridgeapi.PutTargetsInput) []aws.ARN {
			roles := make([]*string, 0, len(in.Targets))
			for i := range in.Targets {
				roles = append(roles, in.Targets[i].RoleArn)
			}
			return passedRoles(roles...)
		}))
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
		Ref:   "docs/research/phase1/secrets-logs-events-alarms.md#42-schedule-expressions-rate-and-cron",
	}
}

// unmatchedInputPath is what a target whose InputPath selects nothing in
// an event receives: no AWS source says; understudy delivers nothing.
func unmatchedInputPath() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "events.input-path.unmatched",
		Value: "not-delivered",
		Label: evidence.Inferred,
		Ref:   "docs/research/phase1/secrets-logs-events-alarms.md#45-targets",
	}
}

// detailIntegerRange is how PutEvents fails an entry whose Detail holds an
// integer outside the signed 64-bit range API_PutEvents documents: no AWS
// source records the code or text, so understudy uses the recorded
// MalformedDetail failure for a bad Detail.
func detailIntegerRange() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "events.put-events.detail-integer-range",
		Value: codeMalformedDetail,
		Label: evidence.Inferred,
		Ref:   "https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutEvents.html",
	}
}

// Switches lists the behaviors chosen without AWS-recorded evidence: the
// service's own and those of its Event Ruler profile.
func (s *Service) Switches() []fidelity.Entry {
	profile := ruler.Switches(s.profile)
	out := make([]fidelity.Entry, 0, 3+len(profile))
	out = append(out, rateRestart().Entry(), unmatchedInputPath().Entry(), detailIntegerRange().Entry())
	for _, sw := range profile {
		out = append(out, fidelity.Switch[string]{
			Name:   sw.Name,
			Value:  sw.Value,
			Label:  evidence.Inferred,
			Ref:    sw.Ref,
			Record: sw.Record,
		}.Entry())
	}
	return out
}
