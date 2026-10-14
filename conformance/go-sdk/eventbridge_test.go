package gosdk_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Research: docs/research/secrets-logs-events-alarms.md section 4,
// matching-engines.md, error-matrix.md 5.12.

// putRule creates or replaces a rule on the default bus and, when the test
// ends, removes its targets and deletes it.
func putRule(t *testing.T, in *eventbridge.PutRuleInput) string {
	t.Helper()
	client := eventbridge.NewFromConfig(awsCfg)
	out, err := client.PutRule(t.Context(), in)
	ok(t, err, "PutRule "+aws.ToString(in.Name))
	t.Cleanup(func() {
		ctx := context.Background()
		targets, err := client.ListTargetsByRule(ctx, &eventbridge.ListTargetsByRuleInput{Rule: in.Name})
		if err == nil && len(targets.Targets) > 0 {
			var ids []string
			for _, tg := range targets.Targets {
				ids = append(ids, aws.ToString(tg.Id))
			}
			_, _ = client.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{Rule: in.Name, Ids: ids})
		}
		_, _ = client.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: in.Name})
	})
	return aws.ToString(out.RuleArn)
}

// putTargets adds targets to rule and fails the test on any failed entry.
func putTargets(t *testing.T, rule string, targets ...types.Target) {
	t.Helper()
	out, err := eventbridge.NewFromConfig(awsCfg).PutTargets(t.Context(), &eventbridge.PutTargetsInput{Rule: aws.String(rule), Targets: targets})
	ok(t, err, "PutTargets")
	if out.FailedEntryCount != 0 {
		t.Fatalf("PutTargets FailedEntries = %+v", out.FailedEntries)
	}
}

// ebEvent is the envelope EventBridge delivers to targets.
type ebEvent struct {
	Version    string          `json:"version"`
	ID         string          `json:"id"`
	DetailType string          `json:"detail-type"`
	Source     string          `json:"source"`
	Account    string          `json:"account"`
	Time       string          `json:"time"`
	Region     string          `json:"region"`
	Resources  []string        `json:"resources"`
	Detail     json.RawMessage `json:"detail"`
}

func TestEventBridgeRules(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := eventbridge.NewFromConfig(awsCfg)
	acct := account(t)
	ruleName := name("rule")
	pattern := `{"source":["conformance.app"],"detail-type":["Order Placed"]}`
	ruleArn := putRule(t, &eventbridge.PutRuleInput{Name: aws.String(ruleName), EventPattern: aws.String(pattern), Tags: []types.Tag{{Key: aws.String("team"), Value: aws.String("platform")}}})

	t.Run("PutRule and DescribeRule", func(t *testing.T) {
		if want := "arn:aws:events:" + region() + ":" + acct + ":rule/" + ruleName; ruleArn != want {
			t.Errorf("RuleArn = %q, want %q", ruleArn, want)
		}
		out, err := client.DescribeRule(ctx, &eventbridge.DescribeRuleInput{Name: aws.String(ruleName)})
		ok(t, err, "DescribeRule")
		if aws.ToString(out.Arn) != ruleArn || aws.ToString(out.EventBusName) != "default" || out.State != types.RuleStateEnabled ||
			aws.ToString(out.CreatedBy) != acct || out.Description != nil || out.ScheduleExpression != nil {
			t.Errorf("DescribeRule = Arn %q, EventBusName %q, State %q, CreatedBy %q, Description %v, ScheduleExpression %v",
				aws.ToString(out.Arn), aws.ToString(out.EventBusName), out.State, aws.ToString(out.CreatedBy), out.Description, out.ScheduleExpression)
		}
		wantJSONEqual(t, "DescribeRule EventPattern", aws.ToString(out.EventPattern), pattern)
	})

	t.Run("PutRule replaces", func(t *testing.T) {
		other := name("replace")
		putRule(t, &eventbridge.PutRuleInput{Name: aws.String(other), EventPattern: aws.String(pattern), Description: aws.String("first")})
		_, err := client.PutRule(ctx, &eventbridge.PutRuleInput{Name: aws.String(other), EventPattern: aws.String(pattern), State: types.RuleStateDisabled})
		ok(t, err, "PutRule again")
		out, err := client.DescribeRule(ctx, &eventbridge.DescribeRuleInput{Name: aws.String(other)})
		ok(t, err, "DescribeRule")
		if out.State != types.RuleStateDisabled || out.Description != nil {
			t.Errorf("replaced rule = State %q, Description %v; want DISABLED and the omitted description gone", out.State, out.Description)
		}
	})

	t.Run("ListRules", func(t *testing.T) {
		out, err := client.ListRules(ctx, &eventbridge.ListRulesInput{NamePrefix: aws.String(ruleName)})
		ok(t, err, "ListRules")
		if len(out.Rules) != 1 || aws.ToString(out.Rules[0].Arn) != ruleArn || aws.ToString(out.Rules[0].EventBusName) != "default" || out.Rules[0].State != types.RuleStateEnabled {
			t.Errorf("ListRules = %+v, want just %s", out.Rules, ruleName)
		}
	})

	t.Run("errors", func(t *testing.T) {
		missing := name("missing")
		_, err := client.DescribeRule(ctx, &eventbridge.DescribeRuleInput{Name: aws.String(missing)})
		errAs[*types.ResourceNotFoundException](t, err, "DescribeRule missing")
		wantMessage(t, err, "DescribeRule missing", "Rule "+missing+" does not exist on EventBus default.")
		_, err = client.PutRule(ctx, &eventbridge.PutRuleInput{Name: aws.String(name("bad")), EventPattern: aws.String(`{"source":"not-an-array"}`)})
		errAs[*types.InvalidEventPatternException](t, err, "PutRule with a non-array leaf")
		wantMessageContains(t, err, "PutRule with a non-array leaf", "Event pattern is not valid. Reason: ")
		_, err = client.PutRule(ctx, &eventbridge.PutRuleInput{Name: aws.String(name("bad")), EventPattern: aws.String(`{"source":[]}`)})
		errAs[*types.InvalidEventPatternException](t, err, "PutRule with an empty array")
		_, err = client.PutRule(ctx, &eventbridge.PutRuleInput{Name: aws.String("bad name!"), EventPattern: aws.String(pattern)})
		apiErr(t, err, "PutRule with an invalid name", "ValidationException")
		_, err = client.PutRule(ctx, &eventbridge.PutRuleInput{Name: aws.String(name("none"))})
		// A rule needs EventPattern or ScheduleExpression; the research
		// records no error code for the omission, so only the failure is
		// asserted.
		if err == nil {
			t.Errorf("PutRule without a pattern or a schedule succeeded")
		}
	})

	t.Run("schedule expressions", func(t *testing.T) {
		for _, expr := range []string{
			"rate(1 minute)", "rate(5 minutes)", "rate(2 hours)", "rate(1 day)", "cron(0 10 * * ? *)", "cron(0/15 * * * ? *)",
			"cron(0 18 ? * MON-FRI *)", "cron(15 10 ? * 6L 2002-2005)", "cron(0 2 ? * SAT#3 *)", "cron(0/5 5 ? JAN 1-5 2022)",
		} {
			n := name("sched")
			putRule(t, &eventbridge.PutRuleInput{Name: aws.String(n), ScheduleExpression: aws.String(expr), State: types.RuleStateDisabled})
			out, err := client.DescribeRule(ctx, &eventbridge.DescribeRuleInput{Name: aws.String(n)})
			ok(t, err, "DescribeRule "+expr)
			if aws.ToString(out.ScheduleExpression) != expr || out.EventPattern != nil {
				t.Errorf("DescribeRule of %q = ScheduleExpression %q, EventPattern %v", expr, aws.ToString(out.ScheduleExpression), out.EventPattern)
			}
		}
		for _, expr := range []string{
			"rate(10 seconds)", "rate(1 minutes)", "rate(10 minute)", "rate(10 MINUTES)", "rate(0 minutes)", "rate( 10 minutes )",
			"cron(0 1 * * * *)", "cron(71 8 1 * ? *)", "cron(INVALID)",
		} {
			_, err := client.PutRule(ctx, &eventbridge.PutRuleInput{Name: aws.String(name("badsched")), ScheduleExpression: aws.String(expr)})
			apiErr(t, err, "PutRule ScheduleExpression "+expr, "ValidationException")
			wantMessage(t, err, "PutRule ScheduleExpression "+expr, "Parameter ScheduleExpression is not valid.")
		}
	})

	t.Run("tags", func(t *testing.T) {
		out, err := client.ListTagsForResource(ctx, &eventbridge.ListTagsForResourceInput{ResourceARN: aws.String(ruleArn)})
		ok(t, err, "ListTagsForResource")
		if len(out.Tags) != 1 || aws.ToString(out.Tags[0].Key) != "team" {
			t.Errorf("tags = %+v, want team=platform from PutRule", out.Tags)
		}
		_, err = client.TagResource(ctx, &eventbridge.TagResourceInput{ResourceARN: aws.String(ruleArn), Tags: []types.Tag{{Key: aws.String("env"), Value: aws.String("test")}}})
		ok(t, err, "TagResource")
		_, err = client.UntagResource(ctx, &eventbridge.UntagResourceInput{ResourceARN: aws.String(ruleArn), TagKeys: []string{"team"}})
		ok(t, err, "UntagResource")
		out, err = client.ListTagsForResource(ctx, &eventbridge.ListTagsForResourceInput{ResourceARN: aws.String(ruleArn)})
		ok(t, err, "ListTagsForResource")
		if len(out.Tags) != 1 || aws.ToString(out.Tags[0].Key) != "env" {
			t.Errorf("tags = %+v, want only env=test", out.Tags)
		}
	})

	t.Run("targets", func(t *testing.T) {
		queueURL := createQueue(t, name("eb-target"), nil)
		qArn := queueArn(t, queueURL)
		putTargets(t, ruleName, types.Target{Id: aws.String("q"), Arn: aws.String(qArn), Input: aws.String(`{"fixed":true}`)})
		list, err := client.ListTargetsByRule(ctx, &eventbridge.ListTargetsByRuleInput{Rule: aws.String(ruleName)})
		ok(t, err, "ListTargetsByRule")
		if len(list.Targets) != 1 || aws.ToString(list.Targets[0].Arn) != qArn || aws.ToString(list.Targets[0].Input) != `{"fixed":true}` {
			t.Errorf("ListTargetsByRule = %+v, want the queue target with its Input", list.Targets)
		}
		_, err = client.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String(ruleName)})
		apiErr(t, err, "DeleteRule with targets", "ValidationException")
		wantMessage(t, err, "DeleteRule with targets", "Rule can't be deleted since it has targets.")

		_, err = client.PutTargets(ctx, &eventbridge.PutTargetsInput{Rule: aws.String(ruleName), Targets: []types.Target{
			{Id: aws.String("both"), Arn: aws.String(qArn), Input: aws.String("{}"), InputPath: aws.String("$.detail")},
		}})
		apiErr(t, err, "PutTargets with Input and InputPath", "ValidationException")
		wantMessage(t, err, "PutTargets with Input and InputPath", "Only one of Input, InputPath, or InputTransformer must be provided for target both.")
		_, err = client.PutTargets(ctx, &eventbridge.PutTargetsInput{Rule: aws.String(ruleName), Targets: []types.Target{{Id: aws.String("!@#$@!#$"), Arn: aws.String(qArn)}}})
		apiErr(t, err, "PutTargets with an invalid Id", "ValidationException")
		wantMessage(t, err, "PutTargets with an invalid Id", "1 validation error detected: Value '!@#$@!#$' at 'targets.1.member.id' failed to satisfy constraint: Member must satisfy regular expression pattern: [\\.\\-_A-Za-z0-9]+")
		_, err = client.PutTargets(ctx, &eventbridge.PutTargetsInput{Rule: aws.String(ruleName), Targets: []types.Target{{
			Id: aws.String("tx"), Arn: aws.String(qArn),
			InputTransformer: &types.InputTransformer{InputTemplate: aws.String(`"<notdefinedkey>"`)},
		}}})
		apiErr(t, err, "PutTargets with an undefined placeholder", "ValidationException")
		wantMessage(t, err, "PutTargets with an undefined placeholder", "InputTemplate for target tx contains invalid placeholder notdefinedkey.")

		var five []types.Target
		for i := range 4 {
			five = append(five, types.Target{Id: aws.String("extra" + strconv.Itoa(i)), Arn: aws.String(qArn)})
		}
		putTargets(t, ruleName, five...)
		_, err = client.PutTargets(ctx, &eventbridge.PutTargetsInput{Rule: aws.String(ruleName), Targets: []types.Target{{Id: aws.String("sixth"), Arn: aws.String(qArn)}}})
		errAs[*types.LimitExceededException](t, err, "PutTargets beyond 5 per rule")
		wantMessage(t, err, "PutTargets beyond 5 per rule", "The requested resource exceeds the maximum number allowed.")

		removed, err := client.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{Rule: aws.String(ruleName), Ids: []string{"q", "extra0", "extra1", "extra2", "extra3"}})
		ok(t, err, "RemoveTargets")
		if removed.FailedEntryCount != 0 {
			t.Errorf("RemoveTargets FailedEntries = %+v", removed.FailedEntries)
		}
		_, err = client.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String(ruleName)})
		ok(t, err, "DeleteRule without targets")
		_, err = client.DescribeRule(ctx, &eventbridge.DescribeRuleInput{Name: aws.String(ruleName)})
		errAs[*types.ResourceNotFoundException](t, err, "DescribeRule after DeleteRule")
	})
}

func TestEventBridgePutEvents(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := eventbridge.NewFromConfig(awsCfg)
	acct := account(t)
	source := "conformance." + name("app")
	ruleName := name("orders")
	putRule(t, &eventbridge.PutRuleInput{Name: aws.String(ruleName), EventPattern: aws.String(`{"source":["` + source + `"],"detail":{"amount":[{"numeric":[">",100]}]}}`)})
	whole := createQueue(t, name("eb-whole"), nil)
	path := createQueue(t, name("eb-path"), nil)
	transformed := createQueue(t, name("eb-tx"), nil)
	topicArn := createTopic(t, name("eb-topic"), nil)
	viaTopic, _ := subscribeQueue(t, topicArn, name("eb-via-topic"), nil, map[string]string{"RawMessageDelivery": "true"})
	putTargets(t, ruleName,
		types.Target{Id: aws.String("whole"), Arn: aws.String(queueArn(t, whole))},
		types.Target{Id: aws.String("path"), Arn: aws.String(queueArn(t, path)), InputPath: aws.String("$.detail.customer")},
		types.Target{Id: aws.String("tx"), Arn: aws.String(queueArn(t, transformed)), InputTransformer: &types.InputTransformer{
			InputPathsMap: map[string]string{"amount": "$.detail.amount", "nothing": "$.detail.absent"},
			InputTemplate: aws.String(`{"amount": <amount>, "rule": "<aws.events.rule-name>", "nothing": "<nothing>"}`),
		}},
		types.Target{Id: aws.String("topic"), Arn: aws.String(topicArn)},
	)

	out, err := client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []types.PutEventsRequestEntry{
		{Source: aws.String(source), DetailType: aws.String("Order Placed"), Detail: aws.String(`{"amount":150,"customer":{"id":"c1"}}`), Resources: []string{"arn:aws:s3:::example"}},
		{Source: aws.String(source), DetailType: aws.String("Order Placed"), Detail: aws.String(`{"amount":50,"customer":{"id":"c2"}}`)},
		{DetailType: aws.String("Order Placed"), Detail: aws.String(`{}`)},
		{Source: aws.String(source), DetailType: aws.String("Order Placed"), Detail: aws.String(`[1,2]`)},
	}})
	ok(t, err, "PutEvents")

	t.Run("response", func(t *testing.T) {
		if out.FailedEntryCount != 2 || len(out.Entries) != 4 {
			t.Fatalf("PutEvents = FailedEntryCount %d, %d entries; want 2 of 4", out.FailedEntryCount, len(out.Entries))
		}
		if aws.ToString(out.Entries[0].EventId) == "" || aws.ToString(out.Entries[1].EventId) == "" {
			t.Errorf("successful entries lack EventId: %+v", out.Entries[:2])
		}
		if e := out.Entries[2]; aws.ToString(e.ErrorCode) != "InvalidArgument" || aws.ToString(e.ErrorMessage) != "Parameter Source is not valid. Reason: Source is a required argument." {
			t.Errorf("entry without Source = %q %q", aws.ToString(e.ErrorCode), aws.ToString(e.ErrorMessage))
		}
		if e := out.Entries[3]; aws.ToString(e.ErrorCode) != "MalformedDetail" || aws.ToString(e.ErrorMessage) != "Detail is malformed." {
			t.Errorf("entry with an array Detail = %q %q", aws.ToString(e.ErrorCode), aws.ToString(e.ErrorMessage))
		}
	})

	t.Run("whole event to SQS", func(t *testing.T) {
		msgs := receive(t, whole, 1, 20*time.Second)
		if len(msgs) != 1 {
			t.Fatalf("whole-event queue received %d messages, want 1 (the 50 amount does not match)", len(msgs))
		}
		var ev ebEvent
		ok(t, json.Unmarshal([]byte(aws.ToString(msgs[0].Body)), &ev), "decoding the event")
		if ev.Version != "0" || ev.ID != aws.ToString(out.Entries[0].EventId) || ev.Source != source || ev.DetailType != "Order Placed" ||
			ev.Account != acct || ev.Region != region() || len(ev.Resources) != 1 || ev.Resources[0] != "arn:aws:s3:::example" {
			t.Errorf("delivered event = %+v; want id %s and the envelope fields", ev, aws.ToString(out.Entries[0].EventId))
		}
		if _, err := time.Parse(time.RFC3339, ev.Time); err != nil {
			t.Errorf("event time %q is not RFC 3339: %v", ev.Time, err)
		}
		wantJSONEqual(t, "detail", string(ev.Detail), `{"amount":150,"customer":{"id":"c1"}}`)
		if more := receive(t, whole, 1, 3*time.Second); len(more) != 0 {
			t.Errorf("a non-matching event was delivered: %s", aws.ToString(more[0].Body))
		}
	})

	t.Run("InputPath", func(t *testing.T) {
		msgs := receive(t, path, 1, 20*time.Second)
		if len(msgs) != 1 {
			t.Fatalf("InputPath queue received %d messages", len(msgs))
		}
		wantJSONEqual(t, "InputPath body", aws.ToString(msgs[0].Body), `{"id":"c1"}`)
	})

	t.Run("InputTransformer", func(t *testing.T) {
		msgs := receive(t, transformed, 1, 20*time.Second)
		if len(msgs) != 1 {
			t.Fatalf("InputTransformer queue received %d messages", len(msgs))
		}
		wantJSONEqual(t, "InputTransformer body", aws.ToString(msgs[0].Body), `{"amount":150,"rule":"`+ruleName+`","nothing":""}`)
	})

	t.Run("SNS target", func(t *testing.T) {
		msgs := receive(t, viaTopic, 1, 20*time.Second)
		if len(msgs) != 1 {
			t.Fatalf("queue behind the SNS target received %d messages", len(msgs))
		}
		var ev ebEvent
		ok(t, json.Unmarshal([]byte(aws.ToString(msgs[0].Body)), &ev), "decoding the event")
		if ev.ID != aws.ToString(out.Entries[0].EventId) {
			t.Errorf("event through SNS = %+v", ev)
		}
	})

	t.Run("validation", func(t *testing.T) {
		var eleven []types.PutEventsRequestEntry
		for range 11 {
			eleven = append(eleven, types.PutEventsRequestEntry{Source: aws.String(source), DetailType: aws.String("x"), Detail: aws.String("{}")})
		}
		_, err := client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: eleven})
		apiErr(t, err, "PutEvents with 11 entries", "ValidationException")
		wantMessageContains(t, err, "PutEvents with 11 entries", "at 'entries' failed to satisfy constraint: Member must have length less than or equal to 10")
		none, err := client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []types.PutEventsRequestEntry{
			{Source: aws.String(source), DetailType: aws.String("x"), Detail: aws.String("{}"), EventBusName: aws.String(name("no-such-bus"))},
		}})
		ok(t, err, "PutEvents to a bus that does not exist (200, dropped)")
		if none.FailedEntryCount != 0 || aws.ToString(none.Entries[0].EventId) == "" {
			t.Errorf("PutEvents to a missing bus = %+v, want an EventId and no failure", none.Entries)
		}
	})
}

func TestEventBridgeBusAndPatterns(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := eventbridge.NewFromConfig(awsCfg)
	acct := account(t)

	t.Run("default bus", func(t *testing.T) {
		out, err := client.DescribeEventBus(ctx, &eventbridge.DescribeEventBusInput{})
		ok(t, err, "DescribeEventBus")
		if aws.ToString(out.Name) != "default" || aws.ToString(out.Arn) != "arn:aws:events:"+region()+":"+acct+":event-bus/default" {
			t.Errorf("DescribeEventBus = Name %q, Arn %q", aws.ToString(out.Name), aws.ToString(out.Arn))
		}
		list, err := client.ListEventBuses(ctx, &eventbridge.ListEventBusesInput{})
		ok(t, err, "ListEventBuses")
		found := false
		for _, b := range list.EventBuses {
			found = found || aws.ToString(b.Name) == "default"
		}
		if !found {
			t.Errorf("ListEventBuses = %+v, want the default bus", list.EventBuses)
		}
		missing := name("bus")
		_, err = client.DescribeEventBus(ctx, &eventbridge.DescribeEventBusInput{Name: aws.String(missing)})
		errAs[*types.ResourceNotFoundException](t, err, "DescribeEventBus of a missing bus")
		wantMessage(t, err, "DescribeEventBus of a missing bus", "Event bus "+missing+" does not exist.")
	})

	t.Run("EnableRule, DisableRule and ListRuleNamesByTarget", func(t *testing.T) {
		ruleName := name("toggle")
		putRule(t, &eventbridge.PutRuleInput{Name: aws.String(ruleName), EventPattern: aws.String(`{"source":["x"]}`)})
		_, err := client.DisableRule(ctx, &eventbridge.DisableRuleInput{Name: aws.String(ruleName)})
		ok(t, err, "DisableRule")
		if out, err := client.DescribeRule(ctx, &eventbridge.DescribeRuleInput{Name: aws.String(ruleName)}); err != nil || out.State != types.RuleStateDisabled {
			t.Errorf("after DisableRule: State %v, err %v; want DISABLED", out, err)
		}
		_, err = client.EnableRule(ctx, &eventbridge.EnableRuleInput{Name: aws.String(ruleName)})
		ok(t, err, "EnableRule")
		if out, err := client.DescribeRule(ctx, &eventbridge.DescribeRuleInput{Name: aws.String(ruleName)}); err != nil || out.State != types.RuleStateEnabled {
			t.Errorf("after EnableRule: State %v, err %v; want ENABLED", out, err)
		}
		_, err = client.DisableRule(ctx, &eventbridge.DisableRuleInput{Name: aws.String(name("missing"))})
		errAs[*types.ResourceNotFoundException](t, err, "DisableRule of a missing rule")
		queueURL := createQueue(t, name("eb-by-target"), nil)
		qArn := queueArn(t, queueURL)
		putTargets(t, ruleName, types.Target{Id: aws.String("q"), Arn: aws.String(qArn)})
		names, err := client.ListRuleNamesByTarget(ctx, &eventbridge.ListRuleNamesByTargetInput{TargetArn: aws.String(qArn)})
		ok(t, err, "ListRuleNamesByTarget")
		if len(names.RuleNames) != 1 || names.RuleNames[0] != ruleName {
			t.Errorf("ListRuleNamesByTarget = %v, want [%s]", names.RuleNames, ruleName)
		}
	})

	t.Run("TestEventPattern", func(t *testing.T) {
		event := `{"version":"0","id":"6a7e8feb-b491-4cf7-a9f1-bf3703467718","detail-type":"EC2 Instance State-change Notification","source":"aws.ec2",` +
			`"account":"111122223333","time":"2017-12-22T18:43:48Z","region":"us-west-1","resources":["arn:aws:ec2:us-west-1:123456789012:instance/i-1234567890abcdef0"],` +
			`"detail":{"instance-id":"i-1234567890abcdef0","state":"terminated","count":300,"ip":"10.0.0.7","tags":["a","b"],"name":"EventBridge","file":"dir/img.png"}}`
		cases := []struct {
			pattern string
			want    bool
		}{
			{`{"source":["aws.ec2"]}`, true},
			{`{"source":["aws.s3"]}`, false},
			{`{"detail":{"state":["running","terminated"]}}`, true},
			{`{"detail":{"count":[{"numeric":[">",100,"<=",300]}]}}`, true},
			{`{"detail":{"count":[{"numeric":["<",100]}]}}`, false},
			{`{"detail":{"state":[{"prefix":"term"}]}}`, true},
			{`{"detail":{"file":[{"suffix":".png"}]}}`, true},
			{`{"detail":{"state":[{"anything-but":"terminated"}]}}`, false},
			{`{"detail":{"name":[{"equals-ignore-case":"eventbridge"}]}}`, true},
			{`{"detail":{"missing":[{"exists":false}]}}`, true},
			{`{"detail":{"state":[{"exists":false}]}}`, false},
			{`{"detail":{"ip":[{"cidr":"10.0.0.0/24"}]}}`, true},
			{`{"detail":{"file":[{"wildcard":"dir/*.png"}]}}`, true},
			{`{"detail":{"tags":["b","z"]}}`, true},
			{`{"Source":["aws.ec2"]}`, false},
			{`{"$or":[{"source":["aws.s3"]},{"detail":{"state":["terminated"]}}]}`, true},
		}
		for _, c := range cases {
			out, err := client.TestEventPattern(ctx, &eventbridge.TestEventPatternInput{Event: aws.String(event), EventPattern: aws.String(c.pattern)})
			if err != nil {
				t.Errorf("TestEventPattern %s: %v", c.pattern, err)
				continue
			}
			if out.Result != c.want {
				t.Errorf("TestEventPattern %s = %v, want %v", c.pattern, out.Result, c.want)
			}
		}
		_, err := client.TestEventPattern(ctx, &eventbridge.TestEventPatternInput{Event: aws.String(event), EventPattern: aws.String(`{"detail":{"state":[{"EXISTS":true}]}}`)})
		errAs[*types.InvalidEventPatternException](t, err, "TestEventPattern with an upper-case operator")
		wantMessageContains(t, err, "TestEventPattern with an upper-case operator", "Unrecognized match type EXISTS")
		_, err = client.TestEventPattern(ctx, &eventbridge.TestEventPatternInput{Event: aws.String(`[1]`), EventPattern: aws.String(`{"source":["x"]}`)})
		apiErr(t, err, "TestEventPattern with a non-object event", "ValidationException")
		wantMessage(t, err, "TestEventPattern with a non-object event", "Parameter Event is not valid.")
	})
}

// TestEventBridgeLambdaTarget delivers a PutEvents event to a Lambda
// function, which forwards it to a queue.
func TestEventBridgeLambdaTarget(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	forward := createQueue(t, name("eb-lambda-out"), nil)
	fn := deployFunction(t, name("eb-fn"), "handler.forward", map[string]string{"FORWARD_QUEUE_URL": forward})
	source := "conformance." + name("lambda")
	ruleName := name("to-lambda")
	ruleArn := putRule(t, &eventbridge.PutRuleInput{Name: aws.String(ruleName), EventPattern: aws.String(`{"source":["` + source + `"]}`)})
	_, err := lambda.NewFromConfig(awsCfg).AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName: fn.FunctionName, StatementId: aws.String("eventbridge"), Action: aws.String("lambda:InvokeFunction"),
		Principal: aws.String("events.amazonaws.com"), SourceArn: aws.String(ruleArn),
	})
	ok(t, err, "AddPermission for EventBridge")
	putTargets(t, ruleName, types.Target{Id: aws.String("fn"), Arn: fn.FunctionArn})
	out, err := eventbridge.NewFromConfig(awsCfg).PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []types.PutEventsRequestEntry{
		{Source: aws.String(source), DetailType: aws.String("Ping"), Detail: aws.String(`{"n":1}`)},
	}})
	ok(t, err, "PutEvents")
	msgs := receive(t, forward, 1, 90*time.Second)
	if len(msgs) != 1 {
		t.Fatalf("the function forwarded %d events, want 1", len(msgs))
	}
	var ev ebEvent
	ok(t, json.Unmarshal([]byte(aws.ToString(msgs[0].Body)), &ev), "decoding the forwarded event")
	if ev.ID != aws.ToString(out.Entries[0].EventId) || ev.DetailType != "Ping" {
		t.Errorf("event the function received = %+v", ev)
	}
}

// TestEventBridgeSchedule waits for a rate(1 minute) rule to fire into a
// queue; the first fire is one interval after the rule is created.
func TestEventBridgeSchedule(t *testing.T) {
	t.Parallel()
	queueURL := createQueue(t, name("eb-sched"), nil)
	ruleName := name("every-minute")
	created := time.Now()
	ruleArn := putRule(t, &eventbridge.PutRuleInput{Name: aws.String(ruleName), ScheduleExpression: aws.String("rate(1 minute)")})
	putTargets(t, ruleName, types.Target{Id: aws.String("q"), Arn: aws.String(queueArn(t, queueURL))})
	var msgs []sqstypes.Message
	deadline := time.Now().Add(150 * time.Second)
	for len(msgs) == 0 && time.Now().Before(deadline) {
		msgs = receive(t, queueURL, 1, 10*time.Second)
	}
	if len(msgs) == 0 {
		t.Fatalf("a rate(1 minute) rule did not fire within 150 s")
	}
	if elapsed := time.Since(created); elapsed < 55*time.Second {
		t.Errorf("first scheduled event arrived %s after PutRule; a rate rule first fires one interval after creation", elapsed)
	}
	var ev ebEvent
	ok(t, json.Unmarshal([]byte(aws.ToString(msgs[0].Body)), &ev), "decoding the scheduled event")
	if ev.Version != "0" || ev.DetailType != "Scheduled Event" || ev.Source != "aws.events" || ev.Region != region() ||
		len(ev.Resources) != 1 || ev.Resources[0] != ruleArn || strings.TrimSpace(string(ev.Detail)) != "{}" {
		t.Errorf("scheduled event = %+v (detail %s); want Scheduled Event from aws.events naming %s", ev, ev.Detail, ruleArn)
	}
	if !strings.HasSuffix(ev.Time, "Z") || strings.Contains(ev.Time, ".") {
		t.Errorf("scheduled event time %q, want whole-second UTC RFC 3339", ev.Time)
	}
}
