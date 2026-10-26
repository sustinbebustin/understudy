package events

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/events/envelope"
)

// maxTargets is how many targets a rule may have
// (secrets-logs-events-alarms.md 4.5).
const maxTargets = 5

// bindTargets binds the target operations.
func (s *Service) bindTargets(b eventbridgeapi.Binder) {
	b.PutTargets(s.putTargets,
		registry.Partial{Note: "Lambda function, SQS queue and SNS topic targets in the rule's region, and event bus targets in any served account and region; " +
			"RoleArn, RetryPolicy and DeadLetterConfig are stored, and the retry policy and dead-letter queue apply to deliveries; " +
			"a target with HttpParameters, KinesisParameters, EcsParameters, BatchParameters, RunCommandParameters, RedshiftDataParameters, SageMakerPipelineParameters or AppSyncParameters fails its entry with NotImplemented"},
		namedRule(func(in *eventbridgeapi.PutTargetsInput) *string { return in.EventBusName },
			func(in *eventbridgeapi.PutTargetsInput) string { return in.Rule }))
	b.RemoveTargets(s.removeTargets, registry.Full{},
		namedRule(func(in *eventbridgeapi.RemoveTargetsInput) *string { return in.EventBusName },
			func(in *eventbridgeapi.RemoveTargetsInput) string { return in.Rule }))
	b.ListTargetsByRule(s.listTargetsByRule, registry.Full{},
		namedRule(func(in *eventbridgeapi.ListTargetsByRuleInput) *string { return in.EventBusName },
			func(in *eventbridgeapi.ListTargetsByRuleInput) string { return in.Rule }))
}

// putTargets adds targets to a rule of one of the caller's buses or
// replaces those with the same Id. A request-level problem (inputs,
// placeholders, a FIFO queue's message group, the dead-letter queue, the
// target limit) fails the whole request; a target understudy cannot deliver
// to, or with a member it does not model, fails its entry. Like the rule,
// the bus is looked up after the targets are checked.
func (s *Service) putTargets(_ context.Context, c *kernel.Call, in *eventbridgeapi.PutTargetsInput) (*eventbridgeapi.PutTargetsOutput, *awserr.Error) {
	bus, err := ownBusName(c.Scope(), in.EventBusName)
	if err != nil {
		return nil, err
	}
	scope := c.Scope()
	parsed := make([]Target, len(in.Targets))
	for i := range in.Targets {
		t, err := parseTarget(scope, &in.Targets[i])
		if err != nil {
			return nil, err
		}
		parsed[i] = t
	}
	failed := []eventbridgeapi.PutTargetsResultEntry{}
	accepted := make([]Target, 0, len(parsed))
	for i := range in.Targets {
		if message, refused := refusal(scope, &in.Targets[i]); refused {
			failed = append(failed, eventbridgeapi.PutTargetsResultEntry{
				TargetId: new(in.Targets[i].Id), ErrorCode: new(codeNotImplemented), ErrorMessage: new(message),
			})
			continue
		}
		accepted = append(accepted, parsed[i])
	}
	r := ruleRef{scope: scope, bus: bus, name: in.Rule}
	_, err = kernel.Write(c, s.cells, r.scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if err := st.requireBus(r.bus); err != nil {
			return struct{}{}, err
		}
		rule, ok := st.rule(r.bus, r.name)
		if !ok {
			return struct{}{}, errRuleNotFound(r.name, r.bus)
		}
		targets, seq, err := merged(r.scope, rule.Targets, accepted, st.TargetSeq)
		if err != nil {
			return struct{}{}, err
		}
		rule.Targets = targets
		st.setRule(r.bus, rule)
		st.TargetSeq = seq
		st.used(c.Admitted())
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.PutTargetsOutput{FailedEntryCount: int32(len(failed)), FailedEntries: failed}, nil //nolint:gosec // at most 100 targets, the model's limit
}

// merged is current, a rule's targets in scope, with accepted put: each
// replaces the target with its Id, keeping its Seq, or is added with the
// next number after seq. It returns the targets and the scope's last Seq,
// or the refusal of a cross-account bus target without a role
// (withCrossAccountRole) or of more than maxTargets.
func merged(scope aws.Scope, current, accepted []Target, seq uint64) ([]Target, uint64, *awserr.Error) {
	targets := slices.Clone(current)
	for _, t := range accepted {
		i := slices.IndexFunc(targets, func(o Target) bool { return o.ID == t.ID })
		var replaced *Target
		if i >= 0 {
			replaced = &targets[i]
		}
		t, err := withCrossAccountRole(scope, t, replaced)
		if err != nil {
			return nil, 0, err
		}
		if replaced != nil {
			t.Seq = replaced.Seq
			targets[i] = t
			continue
		}
		seq++
		t.Seq = seq
		targets = append(targets, t)
	}
	if len(targets) > maxTargets {
		return nil, 0, errTooManyTargets()
	}
	return targets, seq, nil
}

// parseTarget checks a PutTargets entry for a rule in scope and returns it
// as stored: at most one input form, each well formed, a message group for a
// FIFO queue, no input form for another account's event bus, and a
// dead-letter queue deadLetterQueue accepts. Whether such a bus target has
// a RoleArn depends on the target it replaces (withCrossAccountRole).
func parseTarget(scope aws.Scope, t *eventbridgeapi.Target) (Target, *awserr.Error) {
	input, err := parseInput(t)
	if err != nil {
		return Target{}, err
	}
	out := Target{ID: t.Id, ARN: t.Arn, RoleARN: copyOf(t.RoleArn), Input: input}
	if p := t.SqsParameters; p != nil {
		out.SQS = &SQSParameters{MessageGroupID: copyOf(p.MessageGroupId)}
	}
	dt, _ := targetOf(scope, t.Arn)
	switch dt := dt.(type) {
	case queueTarget:
		if dt.fifo() && (out.SQS == nil || out.SQS.MessageGroupID == nil || *out.SQS.MessageGroupID == "") {
			return Target{}, errFIFONeedsGroup(t.Id)
		}
	case busTarget:
		if err := checkCrossAccountBus(scope, dt, t); err != nil {
			return Target{}, err
		}
	}
	if d := t.DeadLetterConfig; d != nil {
		if d.Arn != nil {
			if _, ok := deadLetterQueue(scope, *d.Arn); !ok {
				return Target{}, errInvalidParameter("DeadLetterConfig.Arn")
			}
		}
		out.DeadLetter = &DeadLetterConfig{ARN: copyOf(d.Arn)}
	}
	if p := t.RetryPolicy; p != nil {
		out.Retry = &RetryPolicy{MaximumRetryAttempts: copyOf(p.MaximumRetryAttempts), MaximumEventAgeInSeconds: copyOf(p.MaximumEventAgeInSeconds)}
	}
	return out, nil
}

// checkCrossAccountBus checks a PutTargets entry t whose ARN is the event
// bus dt, for a rule in scope: another account's bus takes no input form
// (API_PutTargets; the refusal's text is unrecorded, eventbridge-buses.md
// 6.3, EBB-09).
func checkCrossAccountBus(scope aws.Scope, dt busTarget, t *eventbridgeapi.Target) *awserr.Error {
	if dt.crossAccount(scope) && (t.Input != nil || t.InputPath != nil || t.InputTransformer != nil) {
		return errCrossAccountInput()
	}
	return nil
}

// withCrossAccountRole is t, a target of a rule in scope, as stored in
// place of replaced, the rule's target with t's Id, if any. Another
// account's bus needs a RoleArn (events.bus-target.cross-account-role:
// "EventBridge now requires all new cross account event bus targets to add
// IAM roles", eb-cross-account.html; the refusal's text is unrecorded,
// eventbridge-buses.md 6.3, EBB-09), and a call without one keeps the role
// of the target it replaces when the Arn is the same ("a PutTargets call
// without a role on the same target (same Id and Arn) will not remove the
// role", API_PutTargets). The role is stored, not assumed.
func withCrossAccountRole(scope aws.Scope, t Target, replaced *Target) (Target, *awserr.Error) {
	dt, _ := targetOf(scope, t.ARN)
	if bus, isBus := dt.(busTarget); !isBus || !bus.crossAccount(scope) || t.RoleARN != nil {
		return t, nil
	}
	if replaced == nil || replaced.ARN != t.ARN || replaced.RoleARN == nil {
		return Target{}, errCrossAccountRole(t.ID)
	}
	t.RoleARN = replaced.RoleARN
	return t, nil
}

// refusal is why understudy does not add a PutTargets entry, which fails
// the entry with NotImplemented: its ARN is not a target understudy
// delivers to from a rule in scope, it sets a member understudy does not
// model, or it sets an input form on an event bus of the rule's account,
// where AWS's handling is unrecorded (eventbridge-buses.md 6.2, EBB-08).
// Storing it would make deliveries fail, or the member be ignored,
// silently.
func refusal(scope aws.Scope, t *eventbridgeapi.Target) (string, bool) {
	dt, ok := targetOf(scope, t.Arn)
	if !ok {
		return msgUnsupportedTarget(scope, t.Arn), true
	}
	if _, isBus := dt.(busTarget); isBus && (t.Input != nil || t.InputPath != nil || t.InputTransformer != nil) {
		return msgBusTargetInput, true
	}
	var unmodeled []string
	for _, m := range []struct {
		name string
		set  bool
	}{
		{"KinesisParameters", t.KinesisParameters != nil},
		{"RunCommandParameters", t.RunCommandParameters != nil},
		{"EcsParameters", t.EcsParameters != nil},
		{"BatchParameters", t.BatchParameters != nil},
		{"HttpParameters", t.HttpParameters != nil},
		{"RedshiftDataParameters", t.RedshiftDataParameters != nil},
		{"SageMakerPipelineParameters", t.SageMakerPipelineParameters != nil},
		{"AppSyncParameters", t.AppSyncParameters != nil},
	} {
		if m.set {
			unmodeled = append(unmodeled, m.name)
		}
	}
	if len(unmodeled) > 0 {
		return msgUnsupportedParameters(unmodeled), true
	}
	return "", false
}

// parseInput reads a target's input form: at most one of Input (JSON),
// InputPath (a supported path) and InputTransformer (compiled the way
// deliveries will compile it); none means the matched event.
func parseInput(t *eventbridgeapi.Target) (Input, *awserr.Error) {
	var forms []Input
	if t.Input != nil {
		if _, ok := envelope.ParseJSON(*t.Input); !ok {
			return nil, errInvalidParameter("Input")
		}
		forms = append(forms, ConstantInput{JSON: *t.Input})
	}
	if t.InputPath != nil {
		if _, err := envelope.ParsePath(*t.InputPath); err != nil {
			return nil, errInvalidParameter("InputPath")
		}
		forms = append(forms, PathInput{Path: *t.InputPath})
	}
	if tr := t.InputTransformer; tr != nil {
		_, err := envelope.NewTransformer(tr.InputPathsMap, tr.InputTemplate)
		if pe, ok := errors.AsType[*envelope.PlaceholderError](err); ok {
			return nil, errInvalidPlaceholder(t.Id, pe.Name)
		}
		if err != nil {
			return nil, errInvalidParameter("InputPathsMap")
		}
		forms = append(forms, TransformedInput{Variables: maps.Clone(tr.InputPathsMap), Template: tr.InputTemplate})
	}
	switch len(forms) {
	case 0:
		return MatchedEvent{}, nil
	case 1:
		return forms[0], nil
	}
	return nil, errInputsExclusive(t.Id)
}

// removeTargets removes the targets with the given Ids from a rule of one
// of the caller's buses. An Id the rule has no target for is ignored.
func (s *Service) removeTargets(_ context.Context, c *kernel.Call, in *eventbridgeapi.RemoveTargetsInput) (*eventbridgeapi.RemoveTargetsOutput, *awserr.Error) {
	bus, err := ownBusName(c.Scope(), in.EventBusName)
	if err != nil {
		return nil, err
	}
	r := ruleRef{scope: c.Scope(), bus: bus, name: in.Rule}
	_, err = kernel.Write(c, s.cells, r.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if err := st.requireBus(r.bus); err != nil {
			return struct{}{}, err
		}
		rule, ok := st.rule(r.bus, r.name)
		if !ok {
			return struct{}{}, errRuleNotFound(r.name, r.bus)
		}
		kept := slices.DeleteFunc(slices.Clone(rule.Targets), func(t Target) bool { return slices.Contains(in.Ids, t.ID) })
		if len(kept) == len(rule.Targets) {
			tx.Unchanged()
			return struct{}{}, nil
		}
		rule.Targets = kept
		st.setRule(r.bus, rule)
		st.used(c.Admitted())
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.RemoveTargetsOutput{FailedEntries: []eventbridgeapi.RemoveTargetsResultEntry{}}, nil
}

// listTargetsByRule lists the targets of a rule of one of the caller's
// buses in creation order, each as PutTargets sent it.
func (s *Service) listTargetsByRule(_ context.Context, c *kernel.Call, in *eventbridgeapi.ListTargetsByRuleInput) (*eventbridgeapi.ListTargetsByRuleOutput, *awserr.Error) {
	bus, err := ownBusName(c.Scope(), in.EventBusName)
	if err != nil {
		return nil, err
	}
	r := ruleRef{scope: c.Scope(), bus: bus, name: in.Rule}
	var rule Rule
	var ok bool
	if err := s.readBus(r.scope, r.bus, func(st *State) { rule, ok = st.rule(r.bus, r.name) }); err != nil {
		return nil, err
	}
	if !ok {
		return nil, errRuleNotFound(r.name, r.bus)
	}
	listed := make([]eventbridgeapi.Target, len(rule.Targets))
	for i := range rule.Targets {
		listed[i] = rule.Targets[i].api()
	}
	out, next, err := page(listed, in.NextToken, in.Limit)
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.ListTargetsByRuleOutput{Targets: out, NextToken: next}, nil
}

// api returns the target as PutTargets sent it.
func (t *Target) api() eventbridgeapi.Target {
	out := eventbridgeapi.Target{Id: t.ID, Arn: t.ARN, RoleArn: copyOf(t.RoleARN)}
	switch in := t.Input.(type) {
	case MatchedEvent:
	case ConstantInput:
		out.Input = new(in.JSON)
	case PathInput:
		out.InputPath = new(in.Path)
	case TransformedInput:
		out.InputTransformer = &eventbridgeapi.InputTransformer{InputPathsMap: maps.Clone(in.Variables), InputTemplate: in.Template}
	}
	if t.SQS != nil {
		out.SqsParameters = &eventbridgeapi.SqsParameters{MessageGroupId: copyOf(t.SQS.MessageGroupID)}
	}
	if t.DeadLetter != nil {
		out.DeadLetterConfig = &eventbridgeapi.DeadLetterConfig{Arn: copyOf(t.DeadLetter.ARN)}
	}
	if t.Retry != nil {
		out.RetryPolicy = &eventbridgeapi.RetryPolicy{MaximumRetryAttempts: copyOf(t.Retry.MaximumRetryAttempts), MaximumEventAgeInSeconds: copyOf(t.Retry.MaximumEventAgeInSeconds)}
	}
	return out
}
