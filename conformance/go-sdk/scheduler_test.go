package gosdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	"github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Research: docs/research/scheduler.md.

const schedulerTrustPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"scheduler.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

// The tracer bullet: a rate(1 hour) schedule with an SQS target reads back
// ActionAfterCompletion NONE, RetryPolicy 86400/185 and GroupName default
// although none was sent (tf-acc schedule_test.go:L199-L223; scheduler.md
// 4.2), sends its Input with <aws.scheduler.attempt-number> substituted
// within the hour (aws-doc managing-schedule-context-attributes;
// scheduler.md 6.3), and is gone after its delete (scheduler.md 4.4).
func TestSchedulerRateToSQS(t *testing.T) {
	ctx := t.Context()
	client := scheduler.NewFromConfig(awsCfg)
	queueURL := createQueue(t, name(t, "sch-q"), nil)
	role := *createRole(t, name(t, "sch-role"), schedulerTrustPolicy).Arn
	scheduleName := name(t, "sch")

	created, err := client.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name:               aws.String(scheduleName),
		ScheduleExpression: aws.String("rate(1 hour)"),
		FlexibleTimeWindow: &types.FlexibleTimeWindow{Mode: types.FlexibleTimeWindowModeOff},
		Target: &types.Target{
			Arn: aws.String(queueArn(t, queueURL)), RoleArn: aws.String(role),
			Input: aws.String("hello <aws.scheduler.attempt-number>"),
		},
	})
	ok(t, err, "CreateSchedule")
	t.Cleanup(func() {
		_, _ = client.DeleteSchedule(context.Background(), &scheduler.DeleteScheduleInput{Name: aws.String(scheduleName)})
	})
	if want := "arn:aws:scheduler:" + region() + ":" + account(t) + ":schedule/default/" + scheduleName; aws.ToString(created.ScheduleArn) != want {
		t.Errorf("ScheduleArn = %s, want %s", aws.ToString(created.ScheduleArn), want)
	}

	got, err := client.GetSchedule(ctx, &scheduler.GetScheduleInput{Name: aws.String(scheduleName)})
	ok(t, err, "GetSchedule")
	if aws.ToString(got.GroupName) != "default" || got.ActionAfterCompletion != types.ActionAfterCompletionNone ||
		got.Target == nil || got.Target.RetryPolicy == nil ||
		aws.ToInt32(got.Target.RetryPolicy.MaximumEventAgeInSeconds) != 86400 || aws.ToInt32(got.Target.RetryPolicy.MaximumRetryAttempts) != 185 {
		t.Errorf("GetSchedule = %+v, want group default, NONE and retry 86400/185", got)
	}

	advance(t, 3600)
	received, err := sqs.NewFromConfig(awsCfg).ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10})
	ok(t, err, "ReceiveMessage")
	if len(received.Messages) == 0 {
		t.Fatal("the queue received nothing within the hour")
	}
	for _, m := range received.Messages {
		if aws.ToString(m.Body) != "hello 1" {
			t.Errorf("body = %q, want hello 1", aws.ToString(m.Body))
		}
	}

	_, err = client.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String(scheduleName)})
	ok(t, err, "DeleteSchedule")
	_, err = client.GetSchedule(ctx, &scheduler.GetScheduleInput{Name: aws.String(scheduleName)})
	errAs[*types.ResourceNotFoundException](t, err, "GetSchedule after delete")
}

// schedulerTarget is a fresh queue's URL and a target naming it with a
// role Scheduler may assume.
func schedulerTarget(t *testing.T) (string, *types.Target) {
	t.Helper()
	queueURL := createQueue(t, name(t, "sch-q"), nil)
	role := *createRole(t, name(t, "sch-role"), schedulerTrustPolicy).Arn
	return queueURL, &types.Target{Arn: aws.String(queueArn(t, queueURL)), RoleArn: aws.String(role)}
}

// createScheduleT creates a schedule and deletes it when the test ends.
func createScheduleT(t *testing.T, in *scheduler.CreateScheduleInput) *scheduler.CreateScheduleOutput {
	t.Helper()
	client := scheduler.NewFromConfig(awsCfg)
	out, err := client.CreateSchedule(t.Context(), in)
	ok(t, err, "CreateSchedule "+aws.ToString(in.Name))
	t.Cleanup(func() {
		_, _ = client.DeleteSchedule(context.Background(), &scheduler.DeleteScheduleInput{Name: in.Name})
	})
	return out
}

// emulatorNow is emulator time from GET /clock, to the second.
func emulatorNow(t *testing.T) time.Time {
	t.Helper()
	var c struct {
		Now time.Time `json:"now"`
	}
	if err := json.Unmarshal(admin(t, http.MethodGet, "/clock", nil), &c); err != nil {
		t.Fatalf("GET /clock: %v", err)
	}
	return c.Now.UTC().Truncate(time.Second)
}

// A rate schedule first fires at its StartDate (aws-doc schedule-types;
// scheduler.md 5.2), its body carrying the schedule ARN, the scheduled
// time, an execution id in UUID form and attempt 1 (aws-doc
// managing-schedule-context-attributes; SCH-07, recorded 2026-11-03;
// scheduler.md 6.3, 7.2); the time zone, State and StartDate read back as
// sent (tf-acc schedule_test.go:L365-L419, L1870-L1895; scheduler.md 4.2).
func TestSchedulerStartDateFireCarriesTheContextAttributes(t *testing.T) {
	queueURL, target := schedulerTarget(t)
	target.Input = aws.String("<aws.scheduler.schedule-arn>|<aws.scheduler.scheduled-time>|<aws.scheduler.execution-id>|<aws.scheduler.attempt-number>")
	start := emulatorNow(t).Add(10 * time.Minute)
	scheduleName := name(t, "sch")
	created := createScheduleT(t, &scheduler.CreateScheduleInput{
		Name: aws.String(scheduleName), ScheduleExpression: aws.String("rate(1 hour)"),
		ScheduleExpressionTimezone: aws.String("Europe/Paris"), State: types.ScheduleStateEnabled, StartDate: aws.Time(start),
		FlexibleTimeWindow: &types.FlexibleTimeWindow{Mode: types.FlexibleTimeWindowModeOff}, Target: target,
	})
	got, err := scheduler.NewFromConfig(awsCfg).GetSchedule(t.Context(), &scheduler.GetScheduleInput{Name: aws.String(scheduleName)})
	ok(t, err, "GetSchedule")
	if aws.ToString(got.ScheduleExpressionTimezone) != "Europe/Paris" || got.State != types.ScheduleStateEnabled || got.StartDate == nil || !got.StartDate.Equal(start) {
		t.Errorf("zone, State, StartDate = %s, %s, %v; want Europe/Paris, ENABLED, %v", aws.ToString(got.ScheduleExpressionTimezone), got.State, got.StartDate, start)
	}

	advance(t, 660)
	received, err := sqs.NewFromConfig(awsCfg).ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10})
	ok(t, err, "ReceiveMessage")
	if len(received.Messages) != 1 {
		t.Fatalf("received %d messages, want the StartDate fire", len(received.Messages))
	}
	want := regexp.MustCompile("^" + regexp.QuoteMeta(aws.ToString(created.ScheduleArn)+"|"+start.Format("2006-01-02T15:04:05Z")+"|") + `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\|1$`)
	if body := aws.ToString(received.Messages[0].Body); !want.MatchString(body) {
		t.Errorf("body = %q, want %s", body, want)
	}
}

// UpdateSchedule replaces the schedule: members left out read back their
// defaults (aws-doc UpdateSchedule; tf-acc schedule_test.go:L409, L776,
// L1338; scheduler.md 4.3).
func TestSchedulerUpdateDropsMembersToTheirDefaults(t *testing.T) {
	_, target := schedulerTarget(t)
	full := *target
	full.Input = aws.String("x")
	full.RetryPolicy = &types.RetryPolicy{MaximumEventAgeInSeconds: aws.Int32(60), MaximumRetryAttempts: aws.Int32(1)}
	scheduleName := name(t, "sch")
	off := &types.FlexibleTimeWindow{Mode: types.FlexibleTimeWindowModeOff}
	createScheduleT(t, &scheduler.CreateScheduleInput{
		Name: aws.String(scheduleName), ScheduleExpression: aws.String("rate(1 hour)"), Description: aws.String("before"),
		StartDate: aws.Time(time.Date(2099, 1, 1, 1, 0, 0, 0, time.UTC)), EndDate: aws.Time(time.Date(2100, 1, 1, 1, 2, 3, 0, time.UTC)),
		FlexibleTimeWindow: off, Target: &full,
	})
	client := scheduler.NewFromConfig(awsCfg)
	_, err := client.UpdateSchedule(t.Context(), &scheduler.UpdateScheduleInput{
		Name: aws.String(scheduleName), ScheduleExpression: aws.String("rate(1 hour)"), FlexibleTimeWindow: off, Target: target,
	})
	ok(t, err, "UpdateSchedule")
	got, err := client.GetSchedule(t.Context(), &scheduler.GetScheduleInput{Name: aws.String(scheduleName)})
	ok(t, err, "GetSchedule")
	if aws.ToString(got.Description) != "" || got.StartDate != nil || got.EndDate != nil || got.Target == nil || got.Target.Input != nil {
		t.Errorf("GetSchedule = %+v, want no Description, dates or Input", got)
	}
	if r := got.Target.RetryPolicy; r == nil || aws.ToInt32(r.MaximumEventAgeInSeconds) != 86400 || aws.ToInt32(r.MaximumRetryAttempts) != 185 {
		t.Errorf("RetryPolicy = %+v, want 86400/185", r)
	}
}

// ListSchedules returns a Schedules list (localstack@8b9a79f
// test_scheduler.py:L13-L20, validated 2024-06-11); NamePrefix filters,
// and following NextToken with MaxResults 1 yields every match once
// (scheduler.md 4.5; the order is unrecorded, so the union is compared).
func TestSchedulerListByPrefixAcrossPages(t *testing.T) {
	_, target := schedulerTarget(t)
	prefix := name(t, "ls")
	var want []string
	for i := range 3 {
		n := prefix + "-" + string(rune('0'+i))
		want = append(want, n)
		createScheduleT(t, &scheduler.CreateScheduleInput{
			Name: aws.String(n), ScheduleExpression: aws.String("rate(1 hour)"),
			FlexibleTimeWindow: &types.FlexibleTimeWindow{Mode: types.FlexibleTimeWindowModeOff}, Target: target,
		})
	}
	var seen []string
	pages := scheduler.NewListSchedulesPaginator(scheduler.NewFromConfig(awsCfg), &scheduler.ListSchedulesInput{NamePrefix: aws.String(prefix), MaxResults: aws.Int32(1)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(t.Context())
		ok(t, err, "ListSchedules")
		if page.Schedules == nil {
			t.Error("a page without a Schedules list")
		}
		for _, s := range page.Schedules {
			seen = append(seen, aws.ToString(s.Name))
		}
	}
	slices.Sort(seen)
	if !slices.Equal(seen, want) {
		t.Errorf("listed %v across pages, want %v", seen, want)
	}
}

// rate(10 seconds) is refused with "Invalid Schedule Expression <expr>."
// (G3: localstack@8b9a79f test_scheduler.snapshot.json:L127-L270,
// validated 2025-01-26); a past at() is accepted (G4: snapshot L303-L314,
// validated 2025-02-02; scheduler.md 5.1).
func TestSchedulerRecordedExpressionRefusalAndPastAt(t *testing.T) {
	_, target := schedulerTarget(t)
	off := &types.FlexibleTimeWindow{Mode: types.FlexibleTimeWindowModeOff}
	_, err := scheduler.NewFromConfig(awsCfg).CreateSchedule(t.Context(), &scheduler.CreateScheduleInput{
		Name: aws.String(name(t, "sch")), ScheduleExpression: aws.String("rate(10 seconds)"), FlexibleTimeWindow: off, Target: target,
	})
	errAs[*types.ValidationException](t, err, "CreateSchedule rate(10 seconds)")
	if v := (*types.ValidationException)(nil); errors.As(err, &v) && aws.ToString(v.Message) != "Invalid Schedule Expression rate(10 seconds)." {
		t.Errorf("message = %q", aws.ToString(v.Message))
	}
	scheduleName := name(t, "sch")
	created := createScheduleT(t, &scheduler.CreateScheduleInput{
		Name: aws.String(scheduleName), ScheduleExpression: aws.String("at(2022-12-31T23:59:59)"), FlexibleTimeWindow: off, Target: target,
	})
	if !strings.HasSuffix(aws.ToString(created.ScheduleArn), ":schedule/default/"+scheduleName) {
		t.Errorf("ScheduleArn = %s", aws.ToString(created.ScheduleArn))
	}
}

// A default-bus target puts an event whose source and detail-type are the
// EventBridgeParameters (aws-doc managing-targets-templated; scheduler.md
// 6.4), which a rule on the default bus sends on to an SQS queue.
func TestSchedulerDefaultBusTarget(t *testing.T) {
	source := "conformance." + name(t, "sch")
	ruleName := name(t, "sch-rule")
	putRule(t, &eventbridge.PutRuleInput{Name: aws.String(ruleName), EventPattern: aws.String(`{"source":["` + source + `"]}`)})
	queueURL := createQueue(t, name(t, "sch-bus-q"), nil)
	putTargets(t, ruleName, ebtypes.Target{Id: aws.String("q"), Arn: aws.String(queueArn(t, queueURL))})
	// The queue's policy lets the rule deliver (iam-enforcement-surfaces.md 8.1, 8.4).
	allowQueueDelivery(t, queueURL, "events.amazonaws.com", "arn:aws:events:"+region()+":"+account(t)+":rule/"+ruleName)
	role := *createRole(t, name(t, "sch-role"), schedulerTrustPolicy).Arn
	createScheduleT(t, &scheduler.CreateScheduleInput{
		Name: aws.String(name(t, "sch")), ScheduleExpression: aws.String(atOf(emulatorNow(t).Add(10 * time.Minute))),
		FlexibleTimeWindow: &types.FlexibleTimeWindow{Mode: types.FlexibleTimeWindowModeOff},
		Target: &types.Target{
			Arn: aws.String("arn:aws:events:" + region() + ":" + account(t) + ":event-bus/default"), RoleArn: aws.String(role),
			Input:                 aws.String(`{"attempt":"<aws.scheduler.attempt-number>"}`),
			EventBridgeParameters: &types.EventBridgeParameters{Source: aws.String(source), DetailType: aws.String("tick")},
		},
	})

	advance(t, 660)
	received, err := sqs.NewFromConfig(awsCfg).ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10})
	ok(t, err, "ReceiveMessage")
	if len(received.Messages) != 1 {
		t.Fatalf("received %d events, want the at() invocation's", len(received.Messages))
	}
	var e ebEvent
	ok(t, json.Unmarshal([]byte(aws.ToString(received.Messages[0].Body)), &e), "decoding the event")
	if e.Source != source || e.DetailType != "tick" {
		t.Errorf("event source, detail-type = %q, %q; want %q, tick", e.Source, e.DetailType, source)
	}
}

// A Lambda target is invoked asynchronously with the Input as its event,
// context attributes substituted ("EventBridge Scheduler invokes your
// Lambda Event Function asynchronously", aws-doc with-eventbridge-scheduler;
// aws-doc managing-schedule-context-attributes; scheduler.md 6.3, 6.4); the
// function forwards the event it received to a queue.
func TestSchedulerLambdaTarget(t *testing.T) {
	forward := createQueue(t, name(t, "sch-fn-out"), nil)
	fn := deployFunction(t, name(t, "sch-fn"), "handler.forward", map[string]string{"FORWARD_QUEUE_URL": forward})
	role := *createRole(t, name(t, "sch-role"), schedulerTrustPolicy).Arn
	createScheduleT(t, &scheduler.CreateScheduleInput{
		Name: aws.String(name(t, "sch")), ScheduleExpression: aws.String(atOf(emulatorNow(t).Add(10 * time.Minute))),
		FlexibleTimeWindow: &types.FlexibleTimeWindow{Mode: types.FlexibleTimeWindowModeOff},
		Target:             &types.Target{Arn: fn.FunctionArn, RoleArn: aws.String(role), Input: aws.String(`{"attempt":"<aws.scheduler.attempt-number>"}`)},
	})
	advance(t, 660)
	msgs := receive(t, forward, 1, 90*time.Second)
	if len(msgs) != 1 {
		t.Fatalf("the function forwarded %d events, want 1", len(msgs))
	}
	var event map[string]string
	ok(t, json.Unmarshal([]byte(aws.ToString(msgs[0].Body)), &event), "decoding the forwarded event")
	if event["attempt"] != "1" || len(event) != 1 {
		t.Errorf("the function received %v, want {\"attempt\":\"1\"}", event)
	}
}

// An SQS target's message carries SenderId <role id>:<session name>, the
// execution role's AROA id and a 32-hex session (aws-doc
// configuring-schedule-dlq, the SenderId example; scheduler.md 6.4, 9).
func TestSchedulerSQSSenderIdIsTheRoleSession(t *testing.T) {
	queueURL, target := schedulerTarget(t)
	createScheduleT(t, &scheduler.CreateScheduleInput{
		Name: aws.String(name(t, "sch")), ScheduleExpression: aws.String(atOf(emulatorNow(t).Add(10 * time.Minute))),
		FlexibleTimeWindow: &types.FlexibleTimeWindow{Mode: types.FlexibleTimeWindowModeOff}, Target: target,
	})
	advance(t, 660)
	received, err := sqs.NewFromConfig(awsCfg).ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10,
		MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameSenderId},
	})
	ok(t, err, "ReceiveMessage")
	if len(received.Messages) != 1 {
		t.Fatalf("received %d messages, want the at() invocation's", len(received.Messages))
	}
	if id := received.Messages[0].Attributes["SenderId"]; !regexp.MustCompile(`^AROA[0-9A-Z]+:[0-9a-f]{32}$`).MatchString(id) {
		t.Errorf("SenderId = %q, want <AROA role id>:<32 hex>", id)
	}
}

// A schedule whose target queue was deleted, with MaximumRetryAttempts 0,
// sends its Input to the dead-letter queue with the String attributes the
// dead-letter page lists, ERROR_CODE AWS.SimpleQueueService.NonExistentQueue
// (aws-doc configuring-schedule-dlq; scheduler.md 7.2). RETRY_ATTEMPTS and
// EXHAUSTED_RETRY_CONDITION depend on scheduler.retry.permanent-errors and
// are asserted in Go unit tests; DeadLetterConfig reads back as sent (4.2).
func TestSchedulerDeletedTargetQueueDeadLetters(t *testing.T) {
	ctx := t.Context()
	sqsClient := sqs.NewFromConfig(awsCfg)
	dlqURL, target := schedulerTarget(t)
	dlqArn := aws.ToString(target.Arn)
	goneURL := createQueue(t, name(t, "sch-gone"), nil)
	target.Arn = aws.String(queueArn(t, goneURL))
	_, err := sqsClient.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(goneURL)})
	ok(t, err, "DeleteQueue")
	target.Input = aws.String("Hello, world!")
	target.RetryPolicy = &types.RetryPolicy{MaximumRetryAttempts: aws.Int32(0)}
	target.DeadLetterConfig = &types.DeadLetterConfig{Arn: aws.String(dlqArn)}
	at := emulatorNow(t).Add(10 * time.Minute)
	scheduleName := name(t, "sch")
	created := createScheduleT(t, &scheduler.CreateScheduleInput{
		Name: aws.String(scheduleName), ScheduleExpression: aws.String(atOf(at)),
		FlexibleTimeWindow: &types.FlexibleTimeWindow{Mode: types.FlexibleTimeWindowModeOff}, Target: target,
	})
	got, err := scheduler.NewFromConfig(awsCfg).GetSchedule(ctx, &scheduler.GetScheduleInput{Name: aws.String(scheduleName)})
	ok(t, err, "GetSchedule")
	if got.Target == nil || got.Target.DeadLetterConfig == nil || aws.ToString(got.Target.DeadLetterConfig.Arn) != dlqArn {
		t.Errorf("GetSchedule Target = %+v, want DeadLetterConfig %s", got.Target, dlqArn)
	}

	advance(t, 660)
	received, err := sqsClient.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(dlqURL), MaxNumberOfMessages: 10, MessageAttributeNames: []string{"All"},
	})
	ok(t, err, "ReceiveMessage")
	if len(received.Messages) != 1 {
		t.Fatalf("the dead-letter queue received %d messages, want 1", len(received.Messages))
	}
	m := received.Messages[0]
	// The body is the SendMessage request Scheduler built (SCH-08,
	// recorded 2026-11-03; scheduler.md 7.2).
	parts := strings.Split(aws.ToString(target.Arn), ":") // arn:aws:sqs:<region>:<account>:<name>
	wantBody := `{"MessageBody":"Hello, world!","QueueUrl":"https://sqs.` + parts[3] + `.amazonaws.com/` + parts[4] + `/` + parts[5] + `"}`
	if aws.ToString(m.Body) != wantBody {
		t.Errorf("body = %q, want %s", aws.ToString(m.Body), wantBody)
	}
	for _, n := range []string{"CONTAINS_INVALID_PAYLOAD", "ERROR_CODE", "ERROR_MESSAGE", "EXECUTION_ID", "IS_PAYLOAD_TRUNCATED", "RETRY_ATTEMPTS", "SCHEDULED_TIME", "SCHEDULE_ARN", "TARGET_ARN"} {
		if a, present := m.MessageAttributes[n]; !present || aws.ToString(a.DataType) != "String" {
			t.Errorf("attribute %s = %+v (present %v), want a String", n, a, present)
		}
	}
	str := func(n string) string { return aws.ToString(m.MessageAttributes[n].StringValue) }
	if v := str("ERROR_CODE"); v != "AWS.SimpleQueueService.NonExistentQueue" {
		t.Errorf("ERROR_CODE = %q, want AWS.SimpleQueueService.NonExistentQueue", v)
	}
	if v := str("TARGET_ARN"); v != aws.ToString(target.Arn) {
		t.Errorf("TARGET_ARN = %q, want the target as configured, %s", v, aws.ToString(target.Arn))
	}
	if v := str("SCHEDULE_ARN"); v != aws.ToString(created.ScheduleArn) {
		t.Errorf("SCHEDULE_ARN = %q, want %s", v, aws.ToString(created.ScheduleArn))
	}
	if v, want := str("SCHEDULED_TIME"), at.Format("2006-01-02T15:04:05Z"); v != want {
		t.Errorf("SCHEDULED_TIME = %q, want %s", v, want)
	}
}

// A one-time at() with ActionAfterCompletion DELETE reads DELETE back,
// invokes its target once, and is gone after it has (aws-doc
// managing-schedule-delete; scheduler.md 8).
func TestSchedulerAtWithDeleteIsGoneAfterFiring(t *testing.T) {
	ctx := t.Context()
	client := scheduler.NewFromConfig(awsCfg)
	queueURL, target := schedulerTarget(t)
	target.Input = aws.String("once")
	scheduleName := name(t, "sch")
	createScheduleT(t, &scheduler.CreateScheduleInput{
		Name: aws.String(scheduleName), ScheduleExpression: aws.String(atOf(emulatorNow(t).Add(10 * time.Minute))),
		ActionAfterCompletion: types.ActionAfterCompletionDelete,
		FlexibleTimeWindow:    &types.FlexibleTimeWindow{Mode: types.FlexibleTimeWindowModeOff}, Target: target,
	})
	got, err := client.GetSchedule(ctx, &scheduler.GetScheduleInput{Name: aws.String(scheduleName)})
	ok(t, err, "GetSchedule")
	if got.ActionAfterCompletion != types.ActionAfterCompletionDelete {
		t.Errorf("ActionAfterCompletion = %s, want DELETE", got.ActionAfterCompletion)
	}

	advance(t, 660)
	received, err := sqs.NewFromConfig(awsCfg).ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10})
	ok(t, err, "ReceiveMessage")
	if len(received.Messages) != 1 || aws.ToString(received.Messages[0].Body) != "once" {
		t.Errorf("received %+v, want one message once", received.Messages)
	}
	_, err = client.GetSchedule(ctx, &scheduler.GetScheduleInput{Name: aws.String(scheduleName)})
	errAs[*types.ResourceNotFoundException](t, err, "GetSchedule after the last invocation")
}

// A schedule group created with a tag reads ACTIVE with its ARN (tf-acc
// schedule_group_test.go:L25-L72), lists its tag, and untagged lists
// "Tags": [] (G2: localstack@8b9a79f test_scheduler.snapshot.json:L19-L30);
// deleting it deletes its schedules, after which the group and the
// schedule answer 404 (aws-doc managing-schedule-group-delete; scheduler.md
// 3.2-3.4).
func TestSchedulerGroupWithTagsAndMembers(t *testing.T) {
	ctx := t.Context()
	client := scheduler.NewFromConfig(awsCfg)
	group := name(t, "sch-g")
	created, err := client.CreateScheduleGroup(ctx, &scheduler.CreateScheduleGroupInput{
		Name: aws.String(group), Tags: []types.Tag{{Key: aws.String("TagKey"), Value: aws.String("TagValue")}},
	})
	ok(t, err, "CreateScheduleGroup")
	t.Cleanup(func() {
		_, _ = client.DeleteScheduleGroup(context.Background(), &scheduler.DeleteScheduleGroupInput{Name: aws.String(group)})
	})
	arn := aws.ToString(created.ScheduleGroupArn)
	if want := "arn:aws:scheduler:" + region() + ":" + account(t) + ":schedule-group/" + group; arn != want {
		t.Errorf("ScheduleGroupArn = %s, want %s", arn, want)
	}
	got, err := client.GetScheduleGroup(ctx, &scheduler.GetScheduleGroupInput{Name: aws.String(group)})
	ok(t, err, "GetScheduleGroup")
	if aws.ToString(got.Arn) != arn || aws.ToString(got.Name) != group || got.State != types.ScheduleGroupStateActive {
		t.Errorf("GetScheduleGroup = %+v, want its ARN, name and ACTIVE", got)
	}
	tags, err := client.ListTagsForResource(ctx, &scheduler.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	ok(t, err, "ListTagsForResource")
	if len(tags.Tags) != 1 || aws.ToString(tags.Tags[0].Key) != "TagKey" || aws.ToString(tags.Tags[0].Value) != "TagValue" {
		t.Errorf("tags = %+v, want TagKey=TagValue", tags.Tags)
	}
	_, err = client.UntagResource(ctx, &scheduler.UntagResourceInput{ResourceArn: aws.String(arn), TagKeys: []string{"TagKey"}})
	ok(t, err, "UntagResource")
	tags, err = client.ListTagsForResource(ctx, &scheduler.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	ok(t, err, "ListTagsForResource after untag")
	if len(tags.Tags) != 0 {
		t.Errorf("tags after untag = %+v, want none", tags.Tags)
	}

	_, target := schedulerTarget(t)
	createScheduleT(t, &scheduler.CreateScheduleInput{
		Name: aws.String("member"), GroupName: aws.String(group), ScheduleExpression: aws.String("rate(1 hour)"),
		FlexibleTimeWindow: &types.FlexibleTimeWindow{Mode: types.FlexibleTimeWindowModeOff}, Target: target,
	})
	_, err = client.DeleteScheduleGroup(ctx, &scheduler.DeleteScheduleGroupInput{Name: aws.String(group)})
	ok(t, err, "DeleteScheduleGroup")
	_, err = client.GetScheduleGroup(ctx, &scheduler.GetScheduleGroupInput{Name: aws.String(group)})
	errAs[*types.ResourceNotFoundException](t, err, "GetScheduleGroup after delete")
	_, err = client.GetSchedule(ctx, &scheduler.GetScheduleInput{Name: aws.String("member"), GroupName: aws.String(group)})
	errAs[*types.ResourceNotFoundException](t, err, "GetSchedule of a deleted group's member")
}

// A universal (aws-sdk) target answers 501 NotImplemented and nothing is
// stored (CLAUDE.md invariant 3).
func TestSchedulerUniversalTargetIsNotImplemented(t *testing.T) {
	_, target := schedulerTarget(t)
	target.Arn = aws.String("arn:aws:scheduler:::aws-sdk:sqs:sendMessage")
	target.Input = aws.String(`{}`)
	client := scheduler.NewFromConfig(awsCfg)
	scheduleName := name(t, "sch")
	_, err := client.CreateSchedule(t.Context(), &scheduler.CreateScheduleInput{
		Name: aws.String(scheduleName), ScheduleExpression: aws.String("rate(1 hour)"),
		FlexibleTimeWindow: &types.FlexibleTimeWindow{Mode: types.FlexibleTimeWindowModeOff}, Target: target,
	})
	apiErr(t, err, "CreateSchedule universal target", "NotImplemented")
	wantStatus(t, err, "CreateSchedule universal target", http.StatusNotImplemented)
	_, err = client.GetSchedule(t.Context(), &scheduler.GetScheduleInput{Name: aws.String(scheduleName)})
	errAs[*types.ResourceNotFoundException](t, err, "GetSchedule after the refused create")
}

// A Step Functions target starts one execution of the state machine with
// the Input as its input, context attributes substituted (aws-doc
// managing-targets-templated, managing-schedule-context-attributes;
// scheduler.md 6.3, 6.4).
func TestSchedulerStateMachineTarget(t *testing.T) {
	states := sfn.NewFromConfig(awsCfg)
	machine := createMachine(t, states, name(t, "sch-flow"), `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`, statesRole(t))
	role := *createRole(t, name(t, "sch-role"), schedulerTrustPolicy).Arn
	createScheduleT(t, &scheduler.CreateScheduleInput{
		Name: aws.String(name(t, "sch")), ScheduleExpression: aws.String(atOf(emulatorNow(t).Add(10 * time.Minute))),
		FlexibleTimeWindow: &types.FlexibleTimeWindow{Mode: types.FlexibleTimeWindowModeOff},
		Target: &types.Target{
			Arn: aws.String(machine), RoleArn: aws.String(role),
			Input: aws.String(`{"attempt":"<aws.scheduler.attempt-number>"}`),
		},
	})

	advance(t, 660)
	listed, err := states.ListExecutions(t.Context(), &sfn.ListExecutionsInput{StateMachineArn: aws.String(machine)})
	ok(t, err, "ListExecutions")
	if len(listed.Executions) != 1 {
		t.Fatalf("%d executions, want the at() invocation's", len(listed.Executions))
	}
	if got := aws.ToString(describeExecution(t, states, aws.ToString(listed.Executions[0].ExecutionArn)).Input); !sameJSON(t, got, `{"attempt":"1"}`) {
		t.Errorf("execution input = %s, want {\"attempt\":\"1\"}", got)
	}
}

// atOf is at(yyyy-mm-ddThh:mm:ss) naming t in UTC (scheduler.md 5.1).
func atOf(t time.Time) string { return "at(" + t.UTC().Format("2006-01-02T15:04:05") + ")" }
