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
// time, a 16-hex execution id and attempt 1 (aws-doc
// managing-schedule-context-attributes, configuring-schedule-dlq;
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
	want := regexp.MustCompile("^" + regexp.QuoteMeta(aws.ToString(created.ScheduleArn)+"|"+start.Format("2006-01-02T15:04:05Z")+"|") + `[0-9a-f]{16}\|1$`)
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

// A universal (aws-sdk) target answers 501 NotImplemented and nothing is
// stored (CLAUDE.md invariant 3; spec "Scope").
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

// atOf is at(yyyy-mm-ddThh:mm:ss) naming t in UTC (scheduler.md 5.1).
func atOf(t time.Time) string { return "at(" + t.UTC().Format("2006-01-02T15:04:05") + ")" }
