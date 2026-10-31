package gosdk_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	"github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
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
