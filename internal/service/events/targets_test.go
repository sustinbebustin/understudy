package events_test

import (
	"bytes"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/events"
)

// The aws_cloudwatch_event_rule and aws_cloudwatch_event_target sequences
// of tf-compute.md 4: every read returns what was written, so a second plan
// is empty, and the delete path ends in not-found.
func TestTerraformRuleAndTargetSequence(t *testing.T) {
	run(t, func(h *harness) {
		const name = "nightly"
		arn := must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{
			Name: name, EventBusName: new("default"), Description: new("nightly batch"),
			ScheduleExpression: new("cron(0 2 * * ? *)"), State: new(eventbridgeapi.RuleStateEnabled),
			Tags: []eventbridgeapi.Tag{{Key: "env", Value: "test"}},
		}).RuleArn
		if arn == nil || *arn != ruleARN+name {
			t.Fatalf("RuleArn = %v", arn)
		}
		describe := func() *eventbridgeapi.DescribeRuleOutput {
			t.Helper()
			return must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: name, EventBusName: new("default")})
		}
		want := &eventbridgeapi.DescribeRuleOutput{
			Name: new(name), Arn: new(ruleARN + name), ScheduleExpression: new("cron(0 2 * * ? *)"),
			State: new(eventbridgeapi.RuleStateEnabled), Description: new("nightly batch"),
			EventBusName: new("default"), CreatedBy: new(account),
		}
		if got := describe(); !reflect.DeepEqual(got, want) {
			t.Errorf("DescribeRule = %+v\nwant %+v", got, want)
		}
		tags := must(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: *arn}).Tags
		if !reflect.DeepEqual(tags, []eventbridgeapi.Tag{{Key: "env", Value: "test"}}) {
			t.Errorf("tags = %+v", tags)
		}

		target := eventbridgeapi.Target{
			Id: "worker", Arn: "arn:aws:sqs:us-east-1:000000000000:jobs.fifo",
			InputTransformer: &eventbridgeapi.InputTransformer{
				InputPathsMap: map[string]string{"time": "$.time"},
				InputTemplate: `{"at": <time>}`,
			},
			RetryPolicy:      &eventbridgeapi.RetryPolicy{MaximumRetryAttempts: new(int32(3)), MaximumEventAgeInSeconds: new(int32(600))},
			DeadLetterConfig: &eventbridgeapi.DeadLetterConfig{Arn: new("arn:aws:sqs:us-east-1:000000000000:dlq")},
			SqsParameters:    &eventbridgeapi.SqsParameters{MessageGroupId: new("g")},
		}
		out := must(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: name, EventBusName: new("default"), Targets: []eventbridgeapi.Target{target}})
		if out.FailedEntryCount != 0 || out.FailedEntries == nil || len(out.FailedEntries) != 0 {
			t.Fatalf("PutTargets = %+v", out)
		}
		listed := must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: name, EventBusName: new("default"), Limit: new(int32(100))})
		if listed.NextToken != nil || !reflect.DeepEqual(listed.Targets, []eventbridgeapi.Target{target}) {
			t.Errorf("ListTargetsByRule = %+v\nwant %+v", listed.Targets, target)
		}

		// Update: PutRule again without Tags keeps tags and targets.
		must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{
			Name: name, EventBusName: new("default"), Description: new("changed"), ScheduleExpression: new("cron(0 2 * * ? *)"),
		})
		want.Description = new("changed")
		if got := describe(); !reflect.DeepEqual(got, want) {
			t.Errorf("DescribeRule after update = %+v\nwant %+v", got, want)
		}
		must(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{ResourceARN: *arn, Tags: []eventbridgeapi.Tag{{Key: "team", Value: "a"}}})
		must(h, eventbridgeapi.OpUntagResource, &eventbridgeapi.UntagResourceInput{ResourceARN: *arn, TagKeys: []string{"env"}})
		tags = must(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: *arn}).Tags
		if !reflect.DeepEqual(tags, []eventbridgeapi.Tag{{Key: "team", Value: "a"}}) {
			t.Errorf("tags after update = %+v", tags)
		}
		if n := len(must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: name}).Targets); n != 1 {
			t.Errorf("%d targets after the rule update, want 1", n)
		}

		// Delete: the rule cannot go while it has targets.
		_, err := call(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: name, EventBusName: new("default")})
		wantError(t, err, "ValidationException", "Rule can't be deleted since it has targets.")
		removed := must(h, eventbridgeapi.OpRemoveTargets, &eventbridgeapi.RemoveTargetsInput{Rule: name, EventBusName: new("default"), Ids: []string{"worker"}})
		if removed.FailedEntryCount != 0 {
			t.Errorf("RemoveTargets = %+v", removed)
		}
		must(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: name, EventBusName: new("default")})
		_, err = call(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: name})
		wantError(t, err, "ResourceNotFoundException", "Rule nightly does not exist on EventBus default.")
		_, err = call(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: name})
		wantError(t, err, "ResourceNotFoundException", "Rule nightly does not exist on EventBus default.")
		_, err = call(h, eventbridgeapi.OpRemoveTargets, &eventbridgeapi.RemoveTargetsInput{Rule: name, Ids: []string{"worker"}})
		wantError(t, err, "ResourceNotFoundException", "Rule nightly does not exist on EventBus default.")
		_, err = call(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: name})
		wantError(t, err, "ResourceNotFoundException", "Rule nightly does not exist on EventBus default.")
	})
}

// An event-pattern rule echoes its pattern text and has no schedule; a
// rule with only a schedule has no pattern.
func TestDescribeRuleEchoesThePattern(t *testing.T) {
	run(t, func(h *harness) {
		const pattern = `{"detail-type":["order"],"source":["shop"]}`
		h.patternRule("p", pattern)
		got := must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "p"})
		if got.EventPattern == nil || *got.EventPattern != pattern || got.ScheduleExpression != nil || got.Description != nil || got.RoleArn != nil {
			t.Errorf("DescribeRule = %+v", got)
		}
	})
}

// PutTargets refuses the whole request for a sixth target, two input forms,
// an undefined placeholder and a FIFO queue without a message group.
func TestPutTargetsRequestErrors(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("r", `{"source": ["s"]}`)
		put := func(targets ...eventbridgeapi.Target) *eventbridgeapi.PutTargetsOutput {
			t.Helper()
			out, err := call(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: "r", Targets: targets})
			if err != nil {
				t.Fatalf("PutTargets = %s %s", err.ID.JSONType, err.Message)
			}
			return out
		}
		put(
			eventbridgeapi.Target{Id: "1", Arn: queueARN}, eventbridgeapi.Target{Id: "2", Arn: queueARN},
			eventbridgeapi.Target{Id: "3", Arn: queueARN}, eventbridgeapi.Target{Id: "4", Arn: queueARN},
			eventbridgeapi.Target{Id: "5", Arn: queueARN},
		)
		put(eventbridgeapi.Target{Id: "1", Arn: "arn:aws:sqs:us-east-1:000000000000:replaced"})
		_, err := call(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: "r", Targets: []eventbridgeapi.Target{{Id: "6", Arn: queueARN}}})
		wantError(t, err, "LimitExceededException", "The requested resource exceeds the maximum number allowed.")
		listed := must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "r"}).Targets
		if len(listed) != 5 || listed[0].Id != "1" || listed[0].Arn != "arn:aws:sqs:us-east-1:000000000000:replaced" {
			t.Errorf("targets = %+v, want five with 1 replaced in place", listed)
		}
		page := must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "r", Limit: new(int32(2))})
		if len(page.Targets) != 2 || page.NextToken == nil {
			t.Fatalf("first page = %+v", page)
		}
		rest := must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "r", NextToken: page.NextToken})
		if len(rest.Targets) != 3 || rest.Targets[0].Id != "3" || rest.NextToken != nil {
			t.Errorf("second page = %+v", rest)
		}

		h.patternRule("other", `{"source": ["s"]}`)
		for _, tt := range []struct {
			target  eventbridgeapi.Target
			id, msg string
		}{
			{eventbridgeapi.Target{Id: "t", Arn: queueARN, Input: new(`{}`), InputPath: new("$.detail")}, "ValidationException", "Only one of Input, InputPath, or InputTransformer must be provided for target t."},
			{eventbridgeapi.Target{Id: "t", Arn: queueARN, InputTransformer: &eventbridgeapi.InputTransformer{
				InputPathsMap: map[string]string{"a": "$.detail"}, InputTemplate: `"<a> <notdefinedkey>"`,
			}}, "ValidationException", "InputTemplate for target t contains invalid placeholder notdefinedkey."},
			{eventbridgeapi.Target{Id: "t", Arn: "arn:aws:sqs:us-east-1:000000000000:q.fifo"}, "ValidationException", "Parameter(s) SqsParameters must be specified for target: t."},
			{eventbridgeapi.Target{Id: "t", Arn: "arn:aws:sqs:us-east-1:000000000000:q.fifo", SqsParameters: &eventbridgeapi.SqsParameters{MessageGroupId: new("")}}, "ValidationException", "Parameter(s) SqsParameters must be specified for target: t."},
			{eventbridgeapi.Target{Id: "t", Arn: queueARN, Input: new(`not json`)}, "ValidationException", "Parameter Input is not valid."},
			{eventbridgeapi.Target{Id: "t", Arn: queueARN, InputPath: new(`$..detail`)}, "ValidationException", "Parameter InputPath is not valid."},
			{eventbridgeapi.Target{Id: "t", Arn: queueARN, DeadLetterConfig: &eventbridgeapi.DeadLetterConfig{Arn: new("arn:aws:sns:us-east-1:000000000000:t")}}, "ValidationException", "Parameter DeadLetterConfig.Arn is not valid."},
		} {
			_, err := call(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: "other", Targets: []eventbridgeapi.Target{tt.target}})
			wantError(t, err, tt.id, tt.msg)
		}
		_, err = call(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: "missing", Targets: []eventbridgeapi.Target{{Id: "t", Arn: queueARN}}})
		wantError(t, err, "ResourceNotFoundException", "Rule missing does not exist on EventBus default.")
	})
}

// A target understudy cannot deliver to fails its entry and is not stored;
// the other entries are.
func TestPutTargetsRefusesUnsupportedTargetsPerEntry(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("r", `{"source": ["s"]}`)
		out := must(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: "r", Targets: []eventbridgeapi.Target{
			{Id: "stream", Arn: "arn:aws:kinesis:us-east-1:000000000000:stream/s"},
			{Id: "q", Arn: queueARN},
			{Id: "http", Arn: "arn:aws:lambda:us-east-1:000000000000:function:f", HttpParameters: &eventbridgeapi.HttpParameters{}},
			{
				Id: "ecs", Arn: queueARN, EcsParameters: &eventbridgeapi.EcsParameters{TaskDefinitionArn: "arn:aws:ecs:us-east-1:000000000000:task-definition/t:1"},
				KinesisParameters: &eventbridgeapi.KinesisParameters{PartitionKeyPath: "$.id"},
			},
		}})
		if out.FailedEntryCount != 3 || len(out.FailedEntries) != 3 {
			t.Fatalf("PutTargets = %+v", out)
		}
		for i, want := range []struct{ id, message string }{
			{"stream", "understudy delivers EventBridge targets to Lambda functions, SQS queues and SNS topics in an account and region it serves, and " +
				"arn:aws:kinesis:us-east-1:000000000000:stream/s is none of them; the target was not added. Run 'understudy coverage --service eventbridge' for what is supported."},
			{"http", "understudy does not implement HttpParameters on EventBridge targets; the target was not added. " +
				"Run 'understudy coverage --service eventbridge' for what is supported."},
			{"ecs", "understudy does not implement KinesisParameters, EcsParameters on EventBridge targets; the target was not added. " +
				"Run 'understudy coverage --service eventbridge' for what is supported."},
		} {
			f := out.FailedEntries[i]
			if *f.TargetId != want.id || *f.ErrorCode != "NotImplemented" || *f.ErrorMessage != want.message {
				t.Errorf("failed entry %d = %s %s %q, want %s NotImplemented %q", i, *f.TargetId, *f.ErrorCode, *f.ErrorMessage, want.id, want.message)
			}
		}
		listed := must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "r"}).Targets
		if len(listed) != 1 || listed[0].Id != "q" {
			t.Errorf("targets = %+v", listed)
		}
		names := must(h, eventbridgeapi.OpListRuleNamesByTarget, &eventbridgeapi.ListRuleNamesByTargetInput{TargetArn: queueARN}).RuleNames
		if len(names) != 1 || names[0] != "r" {
			t.Errorf("ListRuleNamesByTarget = %v", names)
		}
	})
}

// A target's RetryPolicy and DeadLetterConfig become the delivery's policy
// and dead-letter queue; a FIFO queue target's group travels with it.
func TestTargetRetryPolicyAndDeadLetterReachTheDelivery(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("r", `{"source": ["s"]}`)
		h.target("r", eventbridgeapi.Target{
			Id: "f", Arn: "arn:aws:sqs:us-east-1:000000000000:jobs.fifo",
			SqsParameters:    &eventbridgeapi.SqsParameters{MessageGroupId: new("g")},
			RetryPolicy:      &eventbridgeapi.RetryPolicy{MaximumRetryAttempts: new(int32(2))},
			DeadLetterConfig: &eventbridgeapi.DeadLetterConfig{Arn: new("arn:aws:sqs:us-east-1:000000000000:dlq")},
		})
		h.putEvents(entry("s", "t", `{}`))
		got := h.sink.take()
		if len(got) != 1 {
			t.Fatalf("%d deliveries", len(got))
		}
		req := got[0].Request
		q, ok := req.Target.(delivery.SQSQueue)
		if !ok || q.GroupID != "g" || q.DedupID != "" {
			t.Errorf("target = %+v", req.Target)
		}
		if p := req.Policy; p != (delivery.EventBridgeTarget{MaxAge: 24 * time.Hour, MaxAttempts: 2}) {
			t.Errorf("policy = %+v", p)
		}
		if req.DLQ == nil || req.DLQ.String() != "arn:aws:sqs:us-east-1:000000000000:dlq" {
			t.Errorf("DLQ = %v", req.DLQ)
		}
	})
}

// A stored DeadLetterConfig Arn that is not an SQS queue, which only an
// edited snapshot holds, still delivers the event, with no dead-letter
// queue, and logs an error naming the rule, the target and the Arn.
func TestUnusableStoredDeadLetterQueueIsLogged(t *testing.T) {
	const (
		dlq    = "arn:aws:sqs:us-east-1:000000000000:dlq"
		edited = "arn:aws:sns:us-east-1:000000000000:dlq"
	)
	run(t, func(h *harness) {
		h.patternRule("r", `{"source": ["s"]}`)
		h.target("r", eventbridgeapi.Target{Id: "q", Arn: queueARN, DeadLetterConfig: &eventbridgeapi.DeadLetterConfig{Arn: new(dlq)}})
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		for i := range sections {
			sections[i].Data = bytes.ReplaceAll(sections[i].Data, []byte(dlq), []byte(edited))
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		h.putEvents(entry("s", "t", `{}`))
		got := h.sink.take()
		if len(got) != 1 || got[0].Request.DLQ != nil {
			t.Fatalf("deliveries = %+v, want one with no DLQ", got)
		}
		var logged bool
		for _, r := range h.logs.records(t) {
			if r["level"] == "ERROR" && r["rule"] == ruleARN+"r" && r["target"] == queueARN && r["dead_letter_queue"] == edited {
				logged = true
			}
		}
		if !logged {
			t.Errorf("no error log names the rule, target and dead-letter queue; logs = %v", h.logs.records(t))
		}
	})
}

// A snapshot holding a rule PutRule or PutTargets could not have stored
// fails to load and leaves the state as it was. Each case edits the
// snapshot of an enabled rate(5 minutes) rule with targets q (seq 1) and q2
// (seq 2, the scope's targetSeq).
func TestImportRefusesRulesPutRuleWouldRefuse(t *testing.T) {
	trigger := regexp.MustCompile(`"trigger":\{[^}]*\}`)
	state := regexp.MustCompile(`"state":"ENABLED"`)
	input := regexp.MustCompile(`"input":\{[^}]*\}`)
	ruleError := func(err error) bool {
		re, ok := errors.AsType[*events.StoredRuleError](err)
		return ok && re.Rule == "r"
	}
	ruleSaying := func(rule, reason string) func(error) bool {
		return func(err error) bool {
			re, ok := errors.AsType[*events.StoredRuleError](err)
			return ok && re.Rule == rule && strings.Contains(re.Reason, reason)
		}
	}
	for _, tt := range []struct {
		name        string
		edit        *regexp.Regexp
		replacement string
		want        func(error) bool
	}{
		{"unparseable schedule", trigger, `"trigger":{"kind":"schedule","schedule":"rate(0 minutes)"}`, func(err error) bool {
			se, ok := errors.AsType[*events.StoredScheduleError](err)
			return ok && se.Expression == "rate(0 minutes)"
		}},
		{"no trigger", trigger, `"trigger":null`, ruleError},
		{"schedule trigger without its schedule", trigger, `"trigger":{"kind":"schedule"}`, ruleError},
		{"pattern rule with a next fire", trigger, `"trigger":{"kind":"pattern","pattern":"{\"source\":[\"s\"]}"}`, ruleError},
		{"disabled rule with a next fire", state, `"state":"DISABLED"`, ruleError},
		{"unknown state", state, `"state":"PAUSED"`, ruleError},
		{"input null", input, `"input":null`, ruleError},
		{"target sequence number zero", regexp.MustCompile(`"seq":1\}`), `"seq":0}`, ruleError},
		{"target sequence number past targetSeq", regexp.MustCompile(`"seq":2\}`), `"seq":3}`, ruleError},
		{"duplicate target sequence number", regexp.MustCompile(`"seq":2\}`), `"seq":1}`, ruleError},
		{"duplicate target id", regexp.MustCompile(`"id":"q2"`), `"id":"q"`, ruleSaying("r", "two targets with the id")},
		{"rule named other than its key", regexp.MustCompile(`"name":"r"`), `"name":"other"`, ruleSaying("r", "stored under another name")},
		{"key that is not a rule name", regexp.MustCompile(`"r":\{"name":"r"`), `"a/b":{"name":"a/b"`, ruleSaying("a/b", "not a rule name")},
		{"more targets than a rule may have", regexp.MustCompile(`"targets":\[(\{[^]]*\})\]`), `"targets":[$1,$1,$1,$1,$1,$1]`, ruleSaying("r", "at most 5")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			run(t, func(h *harness) {
				h.schedule("r", "rate(5 minutes)")
				h.target("r", eventbridgeapi.Target{Id: "q2", Arn: queueARN})
				sections, err := h.svc.Export()
				if err != nil {
					h.Fatal(err)
				}
				edited := slices.Clone(sections)
				for i := range edited {
					if !tt.edit.Match(edited[i].Data) {
						h.Fatalf("section %s has no match for %s: %s", edited[i].Path, tt.edit, edited[i].Data)
					}
					edited[i].Data = tt.edit.ReplaceAll(edited[i].Data, []byte(tt.replacement))
				}
				err = h.svc.Import(edited)
				if !tt.want(err) {
					h.Fatalf("Import = %v", err)
				}
				rule := must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r"})
				if rule.ScheduleExpression == nil || *rule.ScheduleExpression != "rate(5 minutes)" || *rule.State != eventbridgeapi.RuleStateEnabled {
					h.Errorf("rule after the refused import = %+v, want it unchanged", rule)
				}
			})
		})
	}
}

// A section whose rules object is null or missing loads as a scope without
// rules, which later writes can use.
func TestImportReadsNullRulesAsNone(t *testing.T) {
	run(t, func(h *harness) {
		h.schedule("r", "rate(5 minutes)")
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		edited := slices.Clone(sections)
		for i := range edited {
			edited[i].Data = []byte(`{"rules":null}`)
		}
		if err := h.svc.Import(edited); err != nil {
			h.Fatalf("Import = %v", err)
		}
		h.schedule("after", "rate(5 minutes)")
		if n := len(must(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{}).Rules); n != 1 {
			h.Errorf("ListRules after the import and one PutRule = %d rules, want 1", n)
		}
	})
}

// A snapshot round trip keeps rules, targets with every input form and the
// schedule, which Rearm restores after the scheduler is cleared.
func TestSnapshotRoundTrip(t *testing.T) {
	run(t, func(h *harness) {
		h.schedule("timer", "rate(10 minutes)")
		h.patternRule("p", `{"source": ["s"]}`)
		targets := []eventbridgeapi.Target{
			{Id: "a", Arn: queueARN, Input: new(`{"k": 1}`)},
			{Id: "b", Arn: queueARN, InputPath: new("$.detail")},
			{Id: "c", Arn: queueARN, InputTransformer: &eventbridgeapi.InputTransformer{InputPathsMap: map[string]string{"s": "$.source"}, InputTemplate: `"<s>"`}},
		}
		must(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: "p", Targets: targets})
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		if n := len(must(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{}).Rules); n != 0 {
			t.Fatalf("%d rules after reset", n)
		}
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		h.svc.Rearm(h.Clock.Now())
		listed := must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "p"}).Targets
		if !reflect.DeepEqual(listed, targets) {
			t.Errorf("targets after import = %+v", listed)
		}
		h.advance(10 * time.Minute)
		if n := len(h.sink.take()); n != 1 {
			t.Errorf("%d scheduled deliveries after import, want 1", n)
		}
	})
}
