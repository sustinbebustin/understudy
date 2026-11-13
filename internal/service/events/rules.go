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

// noteForeignBus is the Partial note of a call that takes a bus name or ARN
// and manages only the caller's buses: AWS accepts another account's or
// region's bus by ARN (eventbridge-buses.md 4.4), and understudy answers
// errForeignBus (eventbridge-buses.md 10.1).
const noteForeignBus = "another account's or region's bus answers NotImplemented"

// bindRules binds the rule control plane, on the default and custom buses
// of the caller's scope (eventbridge-buses.md 10.1).
func (s *Service) bindRules(b eventbridgeapi.Binder) {
	b.PutRule(s.putRule,
		registry.Partial{Note: "default and custom buses of the caller's account and region; ScheduleExpression only on the default bus, as AWS; " +
			"RoleArn is stored, not used; a rule on another account's or region's bus answers NotImplemented"},
		kernel.WithCheck(s.checkPutRule),
		namedRule(func(in *eventbridgeapi.PutRuleInput) *string { return in.EventBusName },
			func(in *eventbridgeapi.PutRuleInput) string { return in.Name }))
	b.DescribeRule(s.describeRule, registry.Partial{Note: noteForeignBus},
		namedRule(func(in *eventbridgeapi.DescribeRuleInput) *string { return in.EventBusName },
			func(in *eventbridgeapi.DescribeRuleInput) string { return in.Name }))
	b.ListRules(s.listRules, registry.Partial{Note: noteForeignBus})
	b.DeleteRule(s.deleteRule, registry.Partial{Note: noteForeignBus},
		namedRule(func(in *eventbridgeapi.DeleteRuleInput) *string { return in.EventBusName },
			func(in *eventbridgeapi.DeleteRuleInput) string { return in.Name }))
	b.EnableRule(s.enableRule, registry.Partial{Note: noteForeignBus},
		namedRule(func(in *eventbridgeapi.EnableRuleInput) *string { return in.EventBusName },
			func(in *eventbridgeapi.EnableRuleInput) string { return in.Name }))
	b.DisableRule(s.disableRule, registry.Partial{Note: noteForeignBus},
		namedRule(func(in *eventbridgeapi.DisableRuleInput) *string { return in.EventBusName },
			func(in *eventbridgeapi.DisableRuleInput) string { return in.Name }))
	b.ListRuleNamesByTarget(s.listRuleNamesByTarget, registry.Partial{Note: noteForeignBus})
}

// namedRule names the rule a call addresses on the bus its EventBusName
// names, so fault rules scoped by resource match it before the handler
// runs, a rule of a custom bus under its rule/<bus>/<name> ARN. A
// missingBus names none.
func namedRule[In any](bus func(*In) *string, name func(*In) string) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		switch b := parseBus(c.Scope(), bus(in)).(type) {
		case ownBus:
			return []aws.ARN{ruleRef{scope: c.Scope(), bus: b.name, name: name(in)}.arn()}
		case foreignBus:
			return []aws.ARN{ruleRef{scope: b.scope, bus: b.name, name: name(in)}.arn()}
		case missingBus:
		}
		return nil
	})
}

// checkPutRule runs PutRule's stateless checks before fault rules, as AWS
// validates before anything else: a rule needs an event pattern or a
// schedule, and each must be valid. A ScheduleExpression on any bus but the
// default one is left to putRule, which looks the bus up first
// (eventbridge-buses.md 5.3, EBB-03).
func (s *Service) checkPutRule(in *eventbridgeapi.PutRuleInput) *awserr.Error {
	if scheduleOnCustomBus(in) {
		return nil
	}
	if in.EventPattern != nil {
		if _, err := s.patterns.compile(*in.EventPattern); err != nil {
			return errInvalidPattern(err.Message())
		}
	}
	_, err := newTrigger(in.EventPattern, in.ScheduleExpression)
	return err
}

// scheduleOnCustomBus reports whether in puts a ScheduleExpression on any
// bus but the default one, which AWS refuses (eventbridge-buses.md 5.3).
func scheduleOnCustomBus(in *eventbridgeapi.PutRuleInput) bool {
	return in.ScheduleExpression != nil && busNamePart(in.EventBusName) != defaultBus
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

// maxWildcardRules is how many rules whose event patterns hold a wildcard a
// bus may have, a quota AWS does not raise (matching-engines.md 5.2).
const maxWildcardRules = 30

// usesWildcard reports whether t's event pattern holds a wildcard. A
// stored pattern compiled when its rule was put, so one that does not
// compile now holds none worth counting.
func (s *Service) usesWildcard(t Trigger) bool {
	text, ok := t.pattern()
	if !ok {
		return false
	}
	p, err := s.patterns.compile(text)
	return err == nil && p.UsesWildcard()
}

// wildcardRules counts the rules of bus in st other than name whose event
// patterns hold a wildcard.
func (s *Service) wildcardRules(st *State, bus, name string) int {
	n := 0
	for other, rule := range st.Rules[bus] {
		if other != name && s.usesWildcard(rule.Trigger) {
			n++
		}
	}
	return n
}

// putRule creates or replaces a rule on one of the caller's buses: omitted
// members become empty, while its tags and targets stay
// (secrets-logs-events-alarms.md 4.1). The bus must exist, and the rule
// quotas count per bus (eventbridge-buses.md 7, 10.2.3). A schedule on a
// custom bus is refused once the bus is found, before the expression or a
// pattern beside it is checked, an inferred order (eventbridge-buses.md 5.3,
// EBB-03).
func (s *Service) putRule(_ context.Context, c *kernel.Call, in *eventbridgeapi.PutRuleInput) (*eventbridgeapi.PutRuleOutput, *awserr.Error) {
	bus, err := ownBusName(c.Scope(), in.EventBusName)
	if err != nil {
		return nil, err
	}
	r := ruleRef{scope: c.Scope(), bus: bus, name: in.Name}
	_, err = kernel.Write(c, s.cells, r.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if err := st.requireBus(r.bus); err != nil {
			return struct{}{}, err
		}
		if scheduleOnCustomBus(in) {
			return struct{}{}, errScheduleOnCustomBus()
		}
		trigger, err := newTrigger(in.EventPattern, in.ScheduleExpression)
		if err != nil {
			return struct{}{}, err
		}
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
		if existing, ok := st.rule(r.bus, r.name); ok {
			prev = &existing
			rule.Tags, rule.Targets = existing.Tags, existing.Targets
		} else if len(st.Rules[r.bus]) >= maxRules(r.scope.Region.String()) {
			return struct{}{}, errTooManyRules()
		} else if len(rule.Tags) > maxTags {
			return struct{}{}, errTooManyTags(len(rule.Tags))
		}
		if s.usesWildcard(rule.Trigger) && s.wildcardRules(st, r.bus, r.name) >= maxWildcardRules {
			return struct{}{}, errTooManyRules()
		}
		s.setSchedule(tx, r, prev, &rule, now)
		st.setRule(r.bus, rule)
		st.used(c.Admitted())
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.PutRuleOutput{RuleArn: new(r.arn().String())}, nil
}

// setSchedule sets r's next fire and arms or disarms its timer. A schedule
// that, with the rule's enablement, is unchanged since prev keeps its next
// fire, unless it is a rate that has just gained its first target; any
// other enabled schedule restarts at now, so a rate rule fires at once
// (Schedule.first).
func (s *Service) setSchedule(tx *kernel.Tx, ref ruleRef, prev, r *Rule, now clock.Instant) {
	sched, scheduled := r.Trigger.schedule()
	switch {
	case !scheduled || !r.enabled():
		r.NextFire = clock.Instant{}
	case prev != nil && sameSchedule(prev.Trigger, sched) && !prev.NextFire.IsZero() && !gainsFirstTarget(prev, r, sched):
		r.NextFire = prev.NextFire
	default:
		r.NextFire = sched.first(now)
	}
	s.armSchedule(tx, ref, r.NextFire)
}

// gainsFirstTarget reports whether r is a rate rule that had no target in
// prev and has one now. Its rate restarts then, so the fire at its creation
// reaches the targets PutTargets adds after PutRule, as AWS's fire did a
// second later (events/R14 02); restarting at the target is inferred.
func gainsFirstTarget(prev, r *Rule, sched Schedule) bool {
	return sched.rate() && len(prev.Targets) == 0 && len(r.Targets) > 0
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

// describeRule describes a rule of one of the caller's buses, with
// EventBusName the bus's name however the request named it
// (eventbridge-buses.md 5.2, EBB-07).
func (s *Service) describeRule(_ context.Context, c *kernel.Call, in *eventbridgeapi.DescribeRuleInput) (*eventbridgeapi.DescribeRuleOutput, *awserr.Error) {
	bus, err := ownBusName(c.Scope(), in.EventBusName)
	if err != nil {
		return nil, err
	}
	r := ruleRef{scope: c.Scope(), bus: bus, name: in.Name}
	var rule Rule
	var ok bool
	if err := s.readBus(r.scope, r.bus, func(st *State) { rule, ok = st.rule(r.bus, r.name) }); err != nil {
		return nil, err
	}
	if !ok {
		return nil, errRuleNotFound(r.name, r.bus)
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
// pattern or schedule and the description and role only when set, and the
// name of its bus.
func listedRule(r ruleRef, rule *Rule) eventbridgeapi.Rule {
	out := eventbridgeapi.Rule{
		Name:         new(rule.Name),
		Arn:          new(r.arn().String()),
		State:        new(rule.State),
		Description:  copyOf(rule.Description),
		RoleArn:      copyOf(rule.RoleARN),
		EventBusName: new(r.bus),
	}
	if pattern, ok := rule.Trigger.pattern(); ok {
		out.EventPattern = new(pattern)
	}
	if sched, ok := rule.Trigger.schedule(); ok {
		out.ScheduleExpression = new(sched.String())
	}
	return out
}

// listRules lists the rules of one of the caller's buses by name, those
// starting with NamePrefix when it is set.
func (s *Service) listRules(_ context.Context, c *kernel.Call, in *eventbridgeapi.ListRulesInput) (*eventbridgeapi.ListRulesOutput, *awserr.Error) {
	bus, err := ownBusName(c.Scope(), in.EventBusName)
	if err != nil {
		return nil, err
	}
	var listed []eventbridgeapi.Rule
	err = s.readBus(c.Scope(), bus, func(st *State) {
		rules := st.Rules[bus]
		for _, name := range slices.Sorted(maps.Keys(rules)) {
			if in.NamePrefix == nil || strings.HasPrefix(name, *in.NamePrefix) {
				rule := rules[name]
				listed = append(listed, listedRule(ruleRef{scope: c.Scope(), bus: bus, name: name}, &rule))
			}
		}
	})
	if err != nil {
		return nil, err
	}
	rules, next, err := page(listingRules, listed, func(r eventbridgeapi.Rule) string { return *r.Name }, in.NextToken, in.Limit)
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.ListRulesOutput{Rules: rules, NextToken: next}, nil
}

// deleteRule deletes a rule without targets (Force matters only for rules
// another service manages, which understudy has none of). Deleting a rule
// that does not exist on a bus that does succeeds and changes nothing, as
// on AWS.
func (s *Service) deleteRule(_ context.Context, c *kernel.Call, in *eventbridgeapi.DeleteRuleInput) (*eventbridgeapi.DeleteRuleOutput, *awserr.Error) {
	bus, err := ownBusName(c.Scope(), in.EventBusName)
	if err != nil {
		return nil, err
	}
	r := ruleRef{scope: c.Scope(), bus: bus, name: in.Name}
	_, err = kernel.Write(c, s.cells, r.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if err := st.requireBus(r.bus); err != nil {
			return struct{}{}, err
		}
		rule, ok := st.rule(r.bus, r.name)
		if !ok {
			tx.Unchanged()
			return struct{}{}, nil
		}
		if len(rule.Targets) > 0 {
			return struct{}{}, errRuleHasTargets()
		}
		st.deleteRule(r.bus, r.name)
		st.used(c.Admitted())
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
func (s *Service) setState(c *kernel.Call, busName *string, name string, state eventbridgeapi.RuleState) *awserr.Error {
	bus, err := ownBusName(c.Scope(), busName)
	if err != nil {
		return err
	}
	r := ruleRef{scope: c.Scope(), bus: bus, name: name}
	_, err = kernel.Write(c, s.cells, r.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if err := st.requireBus(r.bus); err != nil {
			return struct{}{}, err
		}
		prev, ok := st.rule(r.bus, r.name)
		if !ok {
			return struct{}{}, errRuleNotFound(r.name, r.bus)
		}
		rule := prev
		rule.State = state
		s.setSchedule(tx, r, &prev, &rule, now)
		st.setRule(r.bus, rule)
		st.used(c.Admitted())
		return struct{}{}, nil
	})
	return err
}

// listRuleNamesByTarget lists, by name, the rules of one of the caller's
// buses with a target whose Arn is TargetArn.
func (s *Service) listRuleNamesByTarget(_ context.Context, c *kernel.Call, in *eventbridgeapi.ListRuleNamesByTargetInput) (*eventbridgeapi.ListRuleNamesByTargetOutput, *awserr.Error) {
	bus, err := ownBusName(c.Scope(), in.EventBusName)
	if err != nil {
		return nil, err
	}
	var names []string
	err = s.readBus(c.Scope(), bus, func(st *State) {
		rules := st.Rules[bus]
		for _, name := range slices.Sorted(maps.Keys(rules)) {
			if slices.ContainsFunc(rules[name].Targets, func(t Target) bool { return t.ARN == in.TargetArn }) {
				names = append(names, name)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	out, next, err := page(listingRuleNamesByTarget, names, func(name string) string { return name }, in.NextToken, in.Limit)
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.ListRuleNamesByTargetOutput{RuleNames: out, NextToken: next}, nil
}

// rule returns a copy of the rule r names.
func (s *Service) rule(r ruleRef) (Rule, bool) {
	var rule Rule
	var ok bool
	s.read(r.scope, func(st *State) { rule, ok = st.rule(r.bus, r.name) })
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
