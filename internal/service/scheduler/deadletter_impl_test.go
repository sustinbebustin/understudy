package scheduler_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// RetryPolicy and DeadLetterConfig read back as sent; a policy that sends
// one member reads the other's default (tf schedule.go:L360-L371, tf-acc
// schedule_test.go:L219-L220; scheduler.md 4.2, 7.1). UNVERIFIED (SCH-01):
// AWS's fill of a partial policy is the provider's, read as AWS's.
func TestRetryPolicyAndDeadLetterEcho(t *testing.T) {
	run(t, func(h *harness) {
		in := schedule("s", "rate(1 hour)", h.queueTarget("q", nil))
		in.Target.RetryPolicy = &schedulerapi.RetryPolicy{MaximumEventAgeInSeconds: new(int32(60))}
		in.Target.DeadLetterConfig = &schedulerapi.DeadLetterConfig{Arn: new(h.queueArn("dlq"))}
		h.create(in)
		got := h.get("", "s").Target
		if r := got.RetryPolicy; r == nil || r.MaximumEventAgeInSeconds == nil || *r.MaximumEventAgeInSeconds != 60 ||
			r.MaximumRetryAttempts == nil || *r.MaximumRetryAttempts != 185 {
			h.Errorf("RetryPolicy = %+v, want 60 and the default 185", r)
		}
		if d := got.DeadLetterConfig; d == nil || d.Arn == nil || *d.Arn != h.queueArn("dlq") {
			h.Errorf("DeadLetterConfig = %+v, want %s", d, h.queueArn("dlq"))
		}
	})
}

// A schedule's dead-letter queue survives a snapshot: the imported
// schedule reads back the same and still dead-letters to it (ADR 0009).
func TestDeadLetterQueueSurvivesASnapshot(t *testing.T) {
	run(t, func(h *harness) {
		h.answerQueues(func(queue string, _ int) *awserr.Error {
			if queue == "gone" {
				return sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
			}
			return nil
		})
		h.create(h.deadLetterTo("dl", "gone", 0))
		before := fmt.Sprintf("%+v", deref(h.get("", "dl")))
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		h.svc.Rearm(h.Clock.Now())
		if after := fmt.Sprintf("%+v", deref(h.get("", "dl"))); after != before {
			h.Errorf("after import:\n%s\nbefore:\n%s", after, before)
		}
		h.advance(15 * time.Minute)
		if n := len(h.sentTo("dlq")); n != 1 {
			h.Errorf("dead letters after the import = %d, want 1", n)
		}
	})
}

// The fire hands the schedule's dead-letter queue to the delivery: a
// missing target queue leaves one dead letter whose body is the payload
// with its context attributes substituted, whose EXECUTION_ID is the one
// substituted, and whose TARGET_ARN is the universal sqs:sendMessage form
// (aws-doc configuring-schedule-dlq; scheduler.md 7.2, 14.5.3).
// UNVERIFIED (SCH-08): the substituted body and the universal TARGET_ARN
// for a templated target are inferred.
func TestFireDeadLettersThePayload(t *testing.T) {
	run(t, func(h *harness) {
		h.answerQueues(func(queue string, _ int) *awserr.Error {
			if queue == "gone" {
				return sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
			}
			return nil
		})
		in := h.deadLetterTo("dl", "gone", 185)
		in.Target.Input = new("run <aws.scheduler.attempt-number> <aws.scheduler.execution-id>")
		h.create(in)
		h.advance(15 * time.Minute)
		dead := h.sentTo("dlq")
		if len(dead) != 1 {
			h.Fatalf("dead letters = %d, want 1", len(dead))
		}
		id, _ := attr(dead[0], "EXECUTION_ID")
		if body := dead[0].sqs.MessageBody; body != "run 1 "+id || !executionID.MatchString(id) {
			h.Errorf("body = %q, EXECUTION_ID %q; want run 1 and that id", body, id)
		}
		if v, _ := attr(dead[0], "TARGET_ARN"); v != "arn:aws:scheduler:::aws-sdk:sqs:sendMessage" {
			h.Errorf("TARGET_ARN = %q, want arn:aws:scheduler:::aws-sdk:sqs:sendMessage", v)
		}
		if v, _ := attr(dead[0], "ERROR_MESSAGE"); !strings.Contains(v, "does not exist") {
			h.Errorf("ERROR_MESSAGE = %q, want the queue's message", v)
		}
	})
}
