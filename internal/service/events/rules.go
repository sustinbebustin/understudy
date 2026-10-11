package events

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// bindRules binds the rule control plane.
func (s *Service) bindRules(b eventbridgeapi.Binder) {
	b.PutRule(s.putRule,
		registry.Partial{Note: "the default bus only; RoleArn is stored, not used"},
		kernel.WithCheck(s.checkPutRule),
		namedRule(func(in *eventbridgeapi.PutRuleInput) string { return in.Name }))
	b.DescribeRule(s.describeRule, registry.Full{},
		namedRule(func(in *eventbridgeapi.DescribeRuleInput) string { return in.Name }))
	b.ListRules(s.listRules, registry.Full{})
	b.DeleteRule(s.deleteRule, registry.Full{},
		namedRule(func(in *eventbridgeapi.DeleteRuleInput) string { return in.Name }))
	b.EnableRule(s.enableRule, registry.Full{},
		namedRule(func(in *eventbridgeapi.EnableRuleInput) string { return in.Name }))
	b.DisableRule(s.disableRule, registry.Full{},
		namedRule(func(in *eventbridgeapi.DisableRuleInput) string { return in.Name }))
	b.ListRuleNamesByTarget(s.listRuleNamesByTarget, registry.Full{})
}

// namedRule names the rule a call addresses, so fault rules scoped by
// resource match it before the handler runs.
func namedRule[In any](name func(*In) string) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		return []aws.ARN{ruleRef{scope: c.Scope(), name: name(in)}.arn()}
	})
}

// checkPutRule runs PutRule's stateless checks before fault rules (M7): a
// rule needs an event pattern or a schedule, and each must be valid.
func (s *Service) checkPutRule(in *eventbridgeapi.PutRuleInput) *awserr.Error {
	if in.EventPattern != nil {
		if _, err := s.patterns.compile(*in.EventPattern); err != nil {
			return errInvalidPattern(err.Message())
		}
	}
	_, err := newTrigger(in.EventPattern, in.ScheduleExpression)
	return err
}

// maxRules is how many rules a bus may have in region: 300, or 100 in
// af-south-1 and eu-south-1 (secrets-logs-events-alarms.md 4.8).
func maxRules(region string) int {
	switch region {
	case "af-south-1", "eu-south-1":
		return 100
	}
	return 300
}

// putRule creates or replaces a rule: omitted members become empty, while
// its tags and targets stay (secrets-logs-events-alarms.md 4.1).
func (s *Service) putRule(_ context.Context, c *kernel.Call, in *eventbridgeapi.PutRuleInput) (*eventbridgeapi.PutRuleOutput, *awserr.Error) {
	if err := requireDefaultBus(c.Scope(), in.EventBusName); err != nil {
		return nil, err
	}
	trigger, err := newTrigger(in.EventPattern, in.ScheduleExpression)
	if err != nil {
		return nil, err
	}
	r := ruleRef{scope: c.Scope(), name: in.Name}
	_, err = kernel.Write(c, s.cells, r.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		rule := Rule{
			Name:        in.Name,
			Trigger:     trigger,
			State:       eventbridgeapi.RuleStateEnabled,
			Description: copyOf(in.Description),
			RoleARN:     copyOf(in.RoleArn),
			Tags:        tagMap(in.Tags),
		}
		if in.State != nil {
			rule.State = *in.State
		}
		var prev *Rule
		if existing, ok := st.Rules[r.name]; ok {
			prev = &existing
			rule.Tags, rule.Targets = existing.Tags, existing.Targets
		} else if len(st.Rules) >= maxRules(r.scope.Region.String()) {
			return struct{}{}, errTooManyRules()
		}
		s.setSchedule(tx, r, prev, &rule, now)
		st.Rules[r.name] = rule
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.PutRuleOutput{RuleArn: new(r.arn().String())}, nil
}

// setSchedule sets r's next fire and arms or disarms its timer. A schedule
// that, with the rule's enablement, is unchanged since prev keeps its next
// fire; any other enabled schedule restarts at now, so a rate rule fires
// one interval after it.
func (s *Service) setSchedule(tx *kernel.Tx, ref ruleRef, prev, r *Rule, now clock.Instant) {
	sched, scheduled := r.Trigger.schedule()
	switch {
	case !scheduled || !r.enabled():
		r.NextFire = clock.Instant{}
	case prev != nil && sameSchedule(prev.Trigger, sched) && !prev.NextFire.IsZero():
		r.NextFire = prev.NextFire
	default:
		r.NextFire = sched.next(now)
	}
	s.armSchedule(tx, ref, r.NextFire)
}

// sameSchedule reports whether t has a schedule with sched's expression.
func sameSchedule(t Trigger, sched Schedule) bool {
	prev, ok := t.schedule()
	return ok && prev.String() == sched.String()
}

// armSchedule arms the rule's schedule timer at next, or disarms it when
// next is zero.
func (s *Service) armSchedule(tx *kernel.Tx, ref ruleRef, next clock.Instant) {
	id := ref.arn().String()
	if next.IsZero() {
		tx.Disarm(s.schedule, id)
		return
	}
	tx.Arm(s.schedule, id, next)
}

func (s *Service) describeRule(_ context.Context, c *kernel.Call, in *eventbridgeapi.DescribeRuleInput) (*eventbridgeapi.DescribeRuleOutput, *awserr.Error) {
	if err := requireDefaultBus(c.Scope(), in.EventBusName); err != nil {
		return nil, err
	}
	r := ruleRef{scope: c.Scope(), name: in.Name}
	rule, ok := s.rule(r)
	if !ok {
		return nil, errRuleNotFound(in.Name)
	}
	listed := listedRule(r, &rule)
	return &eventbridgeapi.DescribeRuleOutput{
		Name:               listed.Name,
		Arn:                listed.Arn,
		EventPattern:       listed.EventPattern,
		ScheduleExpression: listed.ScheduleExpression,
		State:              listed.State,
		Description:        listed.Description,
		RoleArn:            listed.RoleArn,
		EventBusName:       listed.EventBusName,
		CreatedBy:          new(r.scope.Account.String()),
	}, nil
}

// listedRule is a rule as ListRules and DescribeRule return it: the
// pattern or schedule and the description and role only when set.
func listedRule(r ruleRef, rule *Rule) eventbridgeapi.Rule {
	out := eventbridgeapi.Rule{
		Name:         new(rule.Name),
		Arn:          new(r.arn().String()),
		State:        new(rule.State),
		Description:  copyOf(rule.Description),
		RoleArn:      copyOf(rule.RoleARN),
		EventBusName: new(defaultBus),
	}
	if pattern, ok := rule.Trigger.pattern(); ok {
		out.EventPattern = new(pattern)
	}
	if sched, ok := rule.Trigger.schedule(); ok {
		out.ScheduleExpression = new(sched.String())
	}
	return out
}

// listRules lists the rules by name, those starting with NamePrefix when
// it is set.
func (s *Service) listRules(_ context.Context, c *kernel.Call, in *eventbridgeapi.ListRulesInput) (*eventbridgeapi.ListRulesOutput, *awserr.Error) {
	if err := requireDefaultBus(c.Scope(), in.EventBusName); err != nil {
		return nil, err
	}
	var listed []eventbridgeapi.Rule
	s.read(c.Scope(), func(st *State) {
		for _, name := range slices.Sorted(maps.Keys(st.Rules)) {
			if in.NamePrefix == nil || strings.HasPrefix(name, *in.NamePrefix) {
				rule := st.Rules[name]
				listed = append(listed, listedRule(ruleRef{scope: c.Scope(), name: name}, &rule))
			}
		}
	})
	rules, next, err := page(listed, in.NextToken, in.Limit)
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.ListRulesOutput{Rules: rules, NextToken: next}, nil
}

// deleteRule deletes a rule without targets (Force matters only for rules
// another service manages, which understudy has none of).
func (s *Service) deleteRule(_ context.Context, c *kernel.Call, in *eventbridgeapi.DeleteRuleInput) (*eventbridgeapi.DeleteRuleOutput, *awserr.Error) {
	if err := requireDefaultBus(c.Scope(), in.EventBusName); err != nil {
		return nil, err
	}
	r := ruleRef{scope: c.Scope(), name: in.Name}
	_, err := kernel.Write(c, s.cells, r.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		rule, ok := st.Rules[r.name]
		if !ok {
			return struct{}{}, errRuleNotFound(r.name)
		}
		if len(rule.Targets) > 0 {
			return struct{}{}, errRuleHasTargets()
		}
		delete(st.Rules, r.name)
		tx.Disarm(s.schedule, r.arn().String())
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.DeleteRuleOutput{}, nil
}

func (s *Service) enableRule(_ context.Context, c *kernel.Call, in *eventbridgeapi.EnableRuleInput) (*eventbridgeapi.EnableRuleOutput, *awserr.Error) {
	if err := s.setState(c, in.EventBusName, in.Name, eventbridgeapi.RuleStateEnabled); err != nil {
		return nil, err
	}
	return &eventbridgeapi.EnableRuleOutput{}, nil
}

func (s *Service) disableRule(_ context.Context, c *kernel.Call, in *eventbridgeapi.DisableRuleInput) (*eventbridgeapi.DisableRuleOutput, *awserr.Error) {
	if err := s.setState(c, in.EventBusName, in.Name, eventbridgeapi.RuleStateDisabled); err != nil {
		return nil, err
	}
	return &eventbridgeapi.DisableRuleOutput{}, nil
}

// setState enables or disables a rule. Disabling stops its schedule;
// enabling a disabled rule restarts it.
func (s *Service) setState(c *kernel.Call, bus *string, name string, state eventbridgeapi.RuleState) *awserr.Error {
	if err := requireDefaultBus(c.Scope(), bus); err != nil {
		return err
	}
	r := ruleRef{scope: c.Scope(), name: name}
	_, err := kernel.Write(c, s.cells, r.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		prev, ok := st.Rules[r.name]
		if !ok {
			return struct{}{}, errRuleNotFound(r.name)
		}
		rule := prev
		rule.State = state
		s.setSchedule(tx, r, &prev, &rule, now)
		st.Rules[r.name] = rule
		return struct{}{}, nil
	})
	return err
}

// listRuleNamesByTarget lists, by name, the rules with a target whose Arn
// is TargetArn.
func (s *Service) listRuleNamesByTarget(_ context.Context, c *kernel.Call, in *eventbridgeapi.ListRuleNamesByTargetInput) (*eventbridgeapi.ListRuleNamesByTargetOutput, *awserr.Error) {
	if err := requireDefaultBus(c.Scope(), in.EventBusName); err != nil {
		return nil, err
	}
	var names []string
	s.read(c.Scope(), func(st *State) {
		for _, name := range slices.Sorted(maps.Keys(st.Rules)) {
			if slices.ContainsFunc(st.Rules[name].Targets, func(t Target) bool { return t.ARN == in.TargetArn }) {
				names = append(names, name)
			}
		}
	})
	out, next, err := page(names, in.NextToken, in.Limit)
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.ListRuleNamesByTargetOutput{RuleNames: out, NextToken: next}, nil
}

// rule returns a copy of the rule r names.
func (s *Service) rule(r ruleRef) (Rule, bool) {
	var rule Rule
	var ok bool
	s.read(r.scope, func(st *State) { rule, ok = st.Rules[r.name] })
	return rule, ok
}

// read runs fn on scope's state, without creating a cell for it; fn does
// not run when the scope has none. Records are immutable values, so what
// fn copies out never aliases the cell.
func (s *Service) read(scope aws.Scope, fn func(st *State)) {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return
	}
	store.Read(cell, func(st *State) struct{} {
		fn(st)
		return struct{}{}
	})
}

// tagMap is a Tags list as a map; nil when empty.
func tagMap(tags []eventbridgeapi.Tag) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[t.Key] = t.Value
	}
	return m
}

// copyOf returns a pointer to a copy of *p, or nil.
func copyOf[T any](p *T) *T {
	if p == nil {
		return nil
	}
	return new(*p)
}
