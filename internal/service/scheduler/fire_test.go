package scheduler_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/scheduler"
)

// precision is the documented invocation precision: a schedule set for
// 1:00 "will invoke the target API between 1:00:00 and 1:00:59" (aws-doc
// https://docs.aws.amazon.com/scheduler/latest/UserGuide/schedule-types.html;
// scheduler.md 5.6). Nothing is invoked before its scheduled instant
// (scheduler.md 14.13.5).
const precision = time.Minute

// inWindow reports whether at lies in [from, from+width).
func inWindow(at clock.Instant, from time.Time, width time.Duration) bool {
	t := at.Wire()
	return !t.Before(from) && t.Before(from.Add(width))
}

// atExpr is at(yyyy-mm-ddThh:mm:ss) naming t's civil time in UTC
// (scheduler.md 5.1).
func atExpr(t time.Time) string { return "at(" + t.UTC().Format("2006-01-02T15:04:05") + ")" }

// rfc3339 is the <aws.scheduler.scheduled-time> form, 2022-03-22T18:59:43Z
// (aws-doc managing-schedule-context-attributes; scheduler.md 6.3).
func rfc3339(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// wantSchedulerCall fails unless the call came over Scheduler delivery as
// a session of the execution role (scheduler.md 1 item 1, 14.4.4).
func wantSchedulerCall(tb testing.TB, c *targetCall) {
	tb.Helper()
	wantRoleSession(tb, c.op, c.principal)
	if c.origin != schedulerOrigin {
		tb.Errorf("%s origin = %v, want %v", c.op, c.origin, schedulerOrigin)
	}
}

// The tracer bullet: a rate(1 hour) SQS schedule delivers its Input with
// the attempt number substituted, "hello 1", within the hour, as a
// session of the execution role (spec "Tracer bullet"; scheduler.md 5.2,
// 6.3, 9). Whether it also fires at the write instant is SCH-09's and not
// asserted.
func TestTracerRateToSQS(t *testing.T) {
	run(t, func(h *harness) {
		h.create(schedule("tracer", "rate(1 hour)", h.queueTarget("tracer", new("hello <aws.scheduler.attempt-number>"))))
		h.settled()
		h.advance(time.Hour)
		got := h.sentTo("tracer")
		if len(got) == 0 {
			h.Fatal("nothing sent within the hour")
		}
		for _, c := range got {
			if c.sqs.MessageBody != "hello 1" {
				h.Errorf("body = %q, want hello 1", c.sqs.MessageBody)
			}
			wantSchedulerCall(h, c)
		}
		for _, c := range h.targetCalls() {
			wantSchedulerCall(h, c)
		}
	})
}

// A rate schedule with a future StartDate first fires at StartDate ("you
// use the StartDate property to set the first occurrence", aws-doc
// schedule-types; scheduler.md 5.2), not before, then every interval,
// each body carrying the four context attributes substituted: the
// schedule ARN, the scheduled time, a 16-hex execution id and attempt 1
// (aws-doc managing-schedule-context-attributes, configuring-schedule-dlq;
// scheduler.md 6.3, 7.2).
func TestContextAttributesFromStartDate(t *testing.T) {
	run(t, func(h *harness) {
		start := h.now().Add(10 * time.Minute)
		in := schedule("ctx", "rate(1 hour)", h.queueTarget("ctx", new("<aws.scheduler.schedule-arn>|<aws.scheduler.scheduled-time>|<aws.scheduler.execution-id>|<aws.scheduler.attempt-number>")))
		in.StartDate = new(start)
		arn := h.create(in)
		h.advance(9 * time.Minute)
		if got := h.sentTo("ctx"); len(got) != 0 {
			h.Fatalf("sent %d before StartDate, want none", len(got))
		}
		h.advance(2 * time.Minute)
		h.advance(time.Hour)
		got := h.sentTo("ctx")
		if len(got) != 2 {
			h.Fatalf("sent %d, want the StartDate fire and one interval later", len(got))
		}
		for i, c := range got {
			scheduled := start.Add(time.Duration(i) * time.Hour)
			parts := strings.Split(c.sqs.MessageBody, "|")
			if len(parts) != 4 || parts[0] != arn || parts[1] != rfc3339(scheduled) || !executionID.MatchString(parts[2]) || parts[3] != "1" {
				h.Errorf("fire %d body = %q, want %s|%s|<16 hex>|1", i+1, c.sqs.MessageBody, arn, rfc3339(scheduled))
			}
			if !inWindow(c.at, scheduled, precision) {
				h.Errorf("fire %d at %v, want within a minute of %v", i+1, c.at.Wire(), scheduled)
			}
			wantSchedulerCall(h, c)
		}
	})
}

// One clock advance delivers every invocation that came due, in order,
// each at its own instant; nothing at or after EndDate ("before which the
// schedule can invoke its target", model; scheduler.md 5.5, 14.3.8; ADR
// 0006).
func TestAdvanceDeliversEveryDueFireInOrder(t *testing.T) {
	run(t, func(h *harness) {
		start := h.now().Add(10 * time.Minute)
		in := schedule("hourly", "rate(1 hour)", h.queueTarget("hourly", new("<aws.scheduler.scheduled-time>")))
		in.StartDate = new(start)
		in.EndDate = new(start.Add(2*time.Hour + 30*time.Minute))
		h.create(in)
		h.advance(6 * time.Hour)
		got := h.sentTo("hourly")
		if len(got) != 3 {
			h.Fatalf("sent %d, want 3 (StartDate, +1h, +2h; EndDate at +2h30m)", len(got))
		}
		for i, c := range got {
			scheduled := start.Add(time.Duration(i) * time.Hour)
			if c.sqs.MessageBody != rfc3339(scheduled) || !inWindow(c.at, scheduled, precision) {
				h.Errorf("fire %d = %q at %v, want %s within a minute", i+1, c.sqs.MessageBody, c.at.Wire(), rfc3339(scheduled))
			}
		}
	})
}

// at() fires once at its civil time in the schedule's zone:
// at(2000-01-01T05:00:00) in America/Los_Angeles is 13:00 UTC (aws-doc
// schedule-types, "runs once at 1pm UTC-8"; scheduler.md 5.1, 5.4).
func TestAtFiresOnceInItsZone(t *testing.T) {
	run(t, func(h *harness) {
		in := schedule("once", "at(2000-01-01T05:00:00)", h.queueTarget("once", new("x")))
		in.ScheduleExpressionTimezone = new("America/Los_Angeles")
		h.create(in)
		h.advance(12*time.Hour + 59*time.Minute)
		if n := len(h.sentTo("once")); n != 0 {
			h.Fatalf("sent %d before 13:00Z, want none", n)
		}
		h.advance(2 * time.Minute)
		h.advance(48 * time.Hour)
		got := h.sentTo("once")
		if len(got) != 1 || !inWindow(got[0].at, time.Date(2000, 1, 1, 13, 0, 0, 0, time.UTC), precision) {
			h.Fatalf("sent %+v, want exactly one at 13:00Z", got)
		}
	})
}

// cron runs in its zone: cron(0 9 * * ? *) in Europe/Paris fires at
// 08:00 UTC in January (aws-doc schedule-types; scheduler.md 5.4).
func TestCronInItsZone(t *testing.T) {
	run(t, func(h *harness) {
		in := schedule("paris", "cron(0 9 * * ? *)", h.queueTarget("paris", new("x")))
		in.ScheduleExpressionTimezone = new("Europe/Paris")
		h.create(in)
		h.advance(7*time.Hour + 59*time.Minute)
		if n := len(h.sentTo("paris")); n != 0 {
			h.Fatalf("sent %d before 08:00Z, want none", n)
		}
		h.advance(time.Hour)
		h.advance(24 * time.Hour)
		got := h.sentTo("paris")
		if len(got) != 2 {
			h.Fatalf("sent %d, want 2 (1 and 2 January at 08:00Z)", len(got))
		}
		for i, c := range got {
			if want := time.Date(2000, 1, 1+i, 8, 0, 0, 0, time.UTC); !inWindow(c.at, want, precision) {
				h.Errorf("fire %d at %v, want %v", i+1, c.at.Wire(), want)
			}
		}
	})
}

// A DISABLED schedule does not invoke; enabled again by UpdateSchedule it
// does (aws-doc managing-schedule-state and troubleshooting; scheduler.md
// 4.6).
func TestDisabledScheduleDoesNotFire(t *testing.T) {
	run(t, func(h *harness) {
		in := schedule("off", "rate(1 hour)", h.queueTarget("off", new("x")))
		in.State = new(schedulerapi.ScheduleStateDisabled)
		in.StartDate = new(h.now().Add(10 * time.Minute))
		h.create(in)
		h.advance(3 * time.Hour)
		if n := len(h.sentTo("off")); n != 0 {
			h.Fatalf("a DISABLED schedule sent %d, want none", n)
		}
		in.State = new(schedulerapi.ScheduleStateEnabled)
		in.StartDate = nil
		must(h, schedulerapi.OpUpdateSchedule, update(in))
		h.advance(2 * time.Hour)
		if n := len(h.sentTo("off")); n == 0 {
			h.Error("the enabled schedule never fired")
		}
	})
}

// scheduler.input.default: without Input, an SQS target receives the
// switch's payload ({} for empty-json-object) and a Lambda target the
// same event, invoked asynchronously ("EventBridge Scheduler invokes your
// Lambda Event Function asynchronously", aws-doc with-eventbridge-scheduler;
// spec switch table; scheduler.md 6.3, 6.4).
func TestInputDefaultSwitch(t *testing.T) {
	payload := map[string]string{"empty-json-object": "{}"}[scheduler.InputDefault().Value]
	if payload == "" {
		t.Fatalf("scheduler.input.default = %q, which this test does not know", scheduler.InputDefault().Value)
	}
	run(t, func(h *harness) {
		when := atExpr(h.now().Add(10 * time.Minute))
		h.create(schedule("q", when, h.queueTarget("plain", nil)))
		h.create(schedule("f", when, schedulerapi.Target{Arn: h.functionArn("f"), RoleArn: h.roleArn()}))
		h.advance(15 * time.Minute)
		if got := h.sentTo("plain"); len(got) != 1 || got[0].sqs.MessageBody != payload {
			h.Errorf("SQS without Input received %+v, want one message %s", got, payload)
		}
		got := h.callsOf("Invoke")
		if len(got) != 1 {
			h.Fatalf("Lambda invoked %d times, want once", len(got))
		}
		in := got[0].lambda
		if string(in.Payload) != payload || in.InvocationType == nil || *in.InvocationType != lambdaapi.InvocationTypeEvent ||
			(in.FunctionName != h.functionArn("f") && in.FunctionName != "f") {
			h.Errorf("Invoke = %s type %v payload %s, want f, Event, %s", in.FunctionName, in.InvocationType, in.Payload, payload)
		}
		wantSchedulerCall(h, got[0])
	})
}

// scheduler.at.past-due: an at() two minutes past is delivered at the
// create, and on enable when created DISABLED; under never nothing is
// delivered (spec switch table; aws-doc troubleshooting, "the schedule
// may immediately invoke its target"; scheduler.md 4.6, 5.1).
func TestPastDueAtSwitch(t *testing.T) {
	for _, o := range []scheduler.Options{{}, {AtPastDue: scheduler.PastDueNever}} {
		t.Run(scheduler.PastDue(o).Value.String(), func(t *testing.T) {
			runWith(t, harnessOptions{scheduler: o}, func(h *harness) {
				past := atExpr(h.now().Add(-2 * time.Minute))
				h.create(schedule("now", past, h.queueTarget("now", new("x"))))
				h.settled()
				afterCreate := h.sentTo("now")

				off := schedule("later", past, h.queueTarget("later", new("x")))
				off.State = new(schedulerapi.ScheduleStateDisabled)
				h.create(off)
				h.advance(time.Hour)
				if n := len(h.sentTo("later")); n != 0 {
					h.Fatalf("the DISABLED past at() sent %d before enable", n)
				}
				off.State = new(schedulerapi.ScheduleStateEnabled)
				must(h, schedulerapi.OpUpdateSchedule, update(off))
				h.settled()
				afterEnable := h.sentTo("later")
				h.advance(24 * time.Hour)

				switch scheduler.PastDue(o).Value {
				case scheduler.PastDueFireAtOnce:
					if len(afterCreate) != 1 || !inWindow(afterCreate[0].at, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), precision) {
						h.Errorf("at create: %+v, want one delivery at the create instant", afterCreate)
					}
					if len(afterEnable) != 1 {
						h.Errorf("at enable: %d deliveries, want one", len(afterEnable))
					}
					if len(h.sentTo("now")) != 1 || len(h.sentTo("later")) != 1 {
						h.Errorf("a past at() fired %d and %d times, want once each", len(h.sentTo("now")), len(h.sentTo("later")))
					}
				case scheduler.PastDueNever:
					if n := len(h.sentTo("now")) + len(h.sentTo("later")); n != 0 {
						h.Errorf("past at() schedules sent %d under never, want none", n)
					}
				}
			})
		})
	}
}

// flexOffsets runs five FLEXIBLE 5 rate(1 hour) schedules from a common
// StartDate and returns each first invocation's offset after its
// scheduled instant, checking the window bounds, whole seconds, the
// scheduled-time attribute, and that the second invocation is scheduled
// one interval after the first scheduled instant, not after its offset
// instant (spec switch table; aws-doc managing-schedule-flexible-time-windows;
// scheduler.md 5.6, 14.3.7).
func flexOffsets(t *testing.T, o scheduler.Options, seed uint64) []time.Duration {
	t.Helper()
	var offsets []time.Duration
	runWith(t, harnessOptions{scheduler: o, seed: seed}, func(h *harness) {
		start := h.now().Add(10 * time.Minute)
		for i := range 5 {
			in := schedule(fmt.Sprintf("flex-%d", i), "rate(1 hour)", h.queueTarget(fmt.Sprintf("flex-%d", i), new("<aws.scheduler.scheduled-time>")))
			in.StartDate = new(start)
			in.FlexibleTimeWindow = schedulerapi.FlexibleTimeWindow{Mode: schedulerapi.FlexibleTimeWindowModeFlexible, MaximumWindowInMinutes: new(int32(5))}
			h.create(in)
		}
		h.advance(time.Hour + 20*time.Minute)
		for i := range 5 {
			got := h.sentTo(fmt.Sprintf("flex-%d", i))
			if len(got) != 2 {
				h.Fatalf("flex-%d sent %d, want 2", i, len(got))
			}
			for n, c := range got {
				scheduled := start.Add(time.Duration(n) * time.Hour)
				if !inWindow(c.at, scheduled, 5*time.Minute) || c.at.Wire().Sub(scheduled)%time.Second != 0 {
					h.Errorf("flex-%d fire %d at %v, want a whole second in [%v, +5m)", i, n+1, c.at.Wire(), scheduled)
				}
				if c.sqs.MessageBody != rfc3339(scheduled) {
					h.Errorf("flex-%d fire %d scheduled-time = %q, want the scheduled instant %s", i, n+1, c.sqs.MessageBody, rfc3339(scheduled))
				}
			}
			offsets = append(offsets, got[0].at.Wire().Sub(start))
		}
	})
	return offsets
}

// scheduler.flexible-window.offset: seeded-uniform draws whole-second
// offsets that the same seed and calls repeat; window-start uses 0.
func TestFlexibleWindowOffsetSwitch(t *testing.T) {
	for _, o := range []scheduler.Options{{}, {FlexibleOffset: scheduler.OffsetWindowStart}} {
		t.Run(scheduler.FlexOffset(o).Value.String(), func(t *testing.T) {
			first := flexOffsets(t, o, 7)
			switch scheduler.FlexOffset(o).Value {
			case scheduler.OffsetSeededUniform:
				if again := flexOffsets(t, o, 7); !slices.Equal(first, again) {
					t.Errorf("the same seed drew %v, then %v", first, again)
				}
				if slices.Max(first) == 0 {
					t.Errorf("offsets = %v, want draws inside the window, not all 0", first)
				}
			case scheduler.OffsetWindowStart:
				for i, d := range first {
					if d != 0 {
						t.Errorf("flex-%d offset = %v under window-start, want 0", i, d)
					}
				}
			}
		})
	}
}

// deadLetterTo is a schedule firing once, 10 minutes from now, into
// queue, with the dead-letter queue dlq and the given retry attempts.
func (h *harness) deadLetterTo(name, queue string, retries int32) *schedulerapi.CreateScheduleInput {
	in := schedule(name, atExpr(h.now().Add(10*time.Minute)), schedulerapi.Target{
		Arn: h.queueArn(queue), RoleArn: h.roleArn(), Input: new("payload"),
		DeadLetterConfig: &schedulerapi.DeadLetterConfig{Arn: new(h.queueArn("dlq"))},
		RetryPolicy:      &schedulerapi.RetryPolicy{MaximumRetryAttempts: new(retries)},
	})
	return in
}

func attr(c *targetCall, name string) (string, bool) {
	a, ok := c.sqs.MessageAttributes[name]
	if !ok || a.StringValue == nil {
		return "", false
	}
	return *a.StringValue, true
}

// scheduler.retry.permanent-errors: a deleted target queue is a client-side
// error. Under not-retried it dead-letters at once with ERROR_CODE
// AWS.SimpleQueueService.NonExistentQueue, RETRY_ATTEMPTS 0 and no
// EXHAUSTED_RETRY_CONDITION; under retried the retries run until
// MaximumRetryAttempts is exhausted and the condition is present (spec
// switch table; aws-doc configuring-schedule-dlq; scheduler.md 7.1, 7.2).
func TestPermanentErrorsSwitch(t *testing.T) {
	for _, o := range []scheduler.Options{{}, {PermanentErrors: delivery.PermanentRetried}} {
		t.Run(scheduler.PermanentErrors(o).Value.String(), func(t *testing.T) {
			runWith(t, harnessOptions{scheduler: o}, func(h *harness) {
				h.answerQueues(func(queue string, _ int) *awserr.Error {
					if queue == "gone" {
						return sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
					}
					return nil
				})
				arn := h.create(h.deadLetterTo("dl", "gone", 2))
				fire := h.now().Add(10 * time.Minute)
				h.advanceBy(3*time.Hour, time.Minute)
				attempts, dead := h.sentTo("gone"), h.sentTo("dlq")
				if len(dead) != 1 {
					h.Fatalf("dead letters = %d, want 1", len(dead))
				}
				dl := dead[0]
				wantSchedulerCall(h, dl)
				for _, name := range []string{"ERROR_CODE", "ERROR_MESSAGE", "EXECUTION_ID", "IS_PAYLOAD_TRUNCATED", "RETRY_ATTEMPTS", "SCHEDULED_TIME", "SCHEDULE_ARN", "TARGET_ARN"} {
					if a, ok := dl.sqs.MessageAttributes[name]; !ok || a.DataType != "String" {
						h.Errorf("dead-letter attribute %s = %+v, want a String (aws-doc configuring-schedule-dlq)", name, a)
					}
				}
				if v, _ := attr(dl, "ERROR_CODE"); v != "AWS.SimpleQueueService.NonExistentQueue" {
					h.Errorf("ERROR_CODE = %q, want AWS.SimpleQueueService.NonExistentQueue", v)
				}
				if v, _ := attr(dl, "SCHEDULE_ARN"); v != arn {
					h.Errorf("SCHEDULE_ARN = %q, want %s", v, arn)
				}
				if v, _ := attr(dl, "SCHEDULED_TIME"); v != rfc3339(fire) {
					h.Errorf("SCHEDULED_TIME = %q, want %s", v, rfc3339(fire))
				}
				if v, _ := attr(dl, "EXECUTION_ID"); !executionID.MatchString(v) {
					h.Errorf("EXECUTION_ID = %q, want 16 lower-case hex", v)
				}
				exhausted, present := attr(dl, "EXHAUSTED_RETRY_CONDITION")
				switch scheduler.PermanentErrors(o).Value {
				case delivery.PermanentNotRetried:
					if len(attempts) != 1 || !inWindow(dl.at, fire, precision) {
						h.Errorf("%d attempts, dead letter at %v; want 1 attempt and the dead letter at the fire", len(attempts), dl.at.Wire())
					}
					if v, _ := attr(dl, "RETRY_ATTEMPTS"); v != "0" || present {
						h.Errorf("RETRY_ATTEMPTS = %q, EXHAUSTED_RETRY_CONDITION %q (present %v); want 0 and absent", v, exhausted, present)
					}
				case delivery.PermanentRetried:
					if len(attempts) != 3 {
						h.Errorf("%d attempts, want 3 (MaximumRetryAttempts 2)", len(attempts))
					}
					if exhausted != "MaximumRetryAttempts" {
						h.Errorf("EXHAUSTED_RETRY_CONDITION = %q (present %v), want MaximumRetryAttempts", exhausted, present)
					}
				}
			})
		})
	}
}

// A retryable target error (throttling) is retried until the policy runs
// out; each attempt substitutes its own attempt number, and the dead
// letter carries EXHAUSTED_RETRY_CONDITION (aws-doc
// managing-schedule-context-attributes, configuring-schedule-dlq;
// scheduler.md 6.3, 7.1, 7.2).
func TestRetryableErrorsExhaustTheRetryPolicy(t *testing.T) {
	run(t, func(h *harness) {
		h.answerQueues(func(queue string, _ int) *awserr.Error {
			if queue == "busy" {
				return sqsapi.ErrRequestThrottled("Request is throttled.")
			}
			return nil
		})
		in := h.deadLetterTo("busy", "busy", 1)
		in.Target.Input = new("try <aws.scheduler.attempt-number>")
		h.create(in)
		h.advanceBy(2*time.Hour, time.Minute)
		attempts := h.sentTo("busy")
		if len(attempts) != 2 || attempts[0].sqs.MessageBody != "try 1" || attempts[1].sqs.MessageBody != "try 2" {
			h.Fatalf("attempts = %+v, want try 1 and try 2", attempts)
		}
		dead := h.sentTo("dlq")
		if len(dead) != 1 {
			h.Fatalf("dead letters = %d, want 1", len(dead))
		}
		if v, _ := attr(dead[0], "EXHAUSTED_RETRY_CONDITION"); v != "MaximumRetryAttempts" {
			h.Errorf("EXHAUSTED_RETRY_CONDITION = %q, want MaximumRetryAttempts", v)
		}
	})
}

// ActionAfterCompletion DELETE: a one-time schedule is deleted after it
// has invoked its target once; a recurring one after its last invocation
// before EndDate; a recurring one without EndDate is never deleted
// (aws-doc https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-schedule-delete.html;
// scheduler.md 8, 14.6).
func TestActionAfterCompletionDelete(t *testing.T) {
	run(t, func(h *harness) {
		once := schedule("once", atExpr(h.now().Add(10*time.Minute)), h.queueTarget("once", new("x")))
		once.ActionAfterCompletion = new(schedulerapi.ActionAfterCompletionDelete)
		h.create(once)

		bounded := schedule("bounded", "rate(1 hour)", h.queueTarget("bounded", new("x")))
		bounded.StartDate = new(h.now().Add(10 * time.Minute))
		bounded.EndDate = new(h.now().Add(2*time.Hour + 30*time.Minute))
		bounded.ActionAfterCompletion = new(schedulerapi.ActionAfterCompletionDelete)
		h.create(bounded)

		forever := schedule("forever", "rate(1 hour)", h.queueTarget("forever", new("x")))
		forever.ActionAfterCompletion = new(schedulerapi.ActionAfterCompletionDelete)
		h.create(forever)

		h.advance(15 * time.Minute)
		if n := len(h.sentTo("once")); n != 1 {
			h.Fatalf("the one-time schedule sent %d, want 1", n)
		}
		wantError(h, errOf(call(h, schedulerapi.OpGetSchedule, &schedulerapi.GetScheduleInput{Name: "once"})), "ResourceNotFoundException", 404, "")
		h.get("", "bounded")
		h.advance(4 * time.Hour)
		if n := len(h.sentTo("bounded")); n != 3 {
			h.Errorf("the bounded schedule sent %d, want 3", n)
		}
		wantError(h, errOf(call(h, schedulerapi.OpGetSchedule, &schedulerapi.GetScheduleInput{Name: "bounded"})), "ResourceNotFoundException", 404, "")
		h.get("", "forever")
	})
}

// Each templated target is called with its API and the Input (scheduler.md
// 6.4, aws-doc managing-targets-templated): Lambda Invoke (asynchronous),
// SQS SendMessage with SqsParameters.MessageGroupId on a FIFO queue
// (aws-doc API_SqsParameters), SNS Publish to the topic, Step Functions
// StartExecution of the state machine, and EventBridge PutEvents with the
// EventBridgeParameters Source and DetailType to the bus; every call is a
// session of the execution role.
func TestTemplatedTargetsAreInvoked(t *testing.T) {
	run(t, func(h *harness) {
		when := atExpr(h.now().Add(10 * time.Minute))
		input := `{"hello":"world"}`
		h.create(schedule("lambda", when, schedulerapi.Target{Arn: h.functionArn("f"), RoleArn: h.roleArn(), Input: new(input)}))
		h.create(schedule("fifo", when, schedulerapi.Target{
			Arn: h.queueArn("orders.fifo"), RoleArn: h.roleArn(), Input: new(input),
			SqsParameters: &schedulerapi.SqsParameters{MessageGroupId: new("g1")},
		}))
		h.create(schedule("sns", when, schedulerapi.Target{Arn: h.topicArn("t"), RoleArn: h.roleArn(), Input: new(input)}))
		h.create(schedule("sfn", when, schedulerapi.Target{Arn: h.machineArn("m"), RoleArn: h.roleArn(), Input: new(input)}))
		h.create(schedule("bus", when, schedulerapi.Target{
			Arn: h.busArn("default"), RoleArn: h.roleArn(), Input: new(input),
			EventBridgeParameters: &schedulerapi.EventBridgeParameters{Source: "my.app", DetailType: "tick"},
		}))
		h.advance(15 * time.Minute)

		if got := h.callsOf("Invoke"); len(got) != 1 || string(got[0].lambda.Payload) != input ||
			got[0].lambda.InvocationType == nil || *got[0].lambda.InvocationType != lambdaapi.InvocationTypeEvent {
			h.Errorf("Lambda calls = %+v, want one asynchronous Invoke with the Input", got)
		}
		if got := h.sentTo("orders.fifo"); len(got) != 1 || got[0].sqs.MessageBody != input || val(got[0].sqs.MessageGroupId) != "g1" {
			h.Errorf("FIFO sends = %+v, want one with the Input and group g1", got)
		}
		if got := h.callsOf("Publish"); len(got) != 1 || val(got[0].sns.TopicArn) != h.topicArn("t") || got[0].sns.Message != input {
			h.Errorf("SNS calls = %+v, want one Publish of the Input to %s", got, h.topicArn("t"))
		}
		if got := h.callsOf("StartExecution"); len(got) != 1 || got[0].sfn.StateMachineArn != h.machineArn("m") || val(got[0].sfn.Input) != input {
			h.Errorf("Step Functions calls = %+v, want one StartExecution of %s with the Input", got, h.machineArn("m"))
		}
		got := h.callsOf("PutEvents")
		if len(got) != 1 || len(got[0].events.Entries) != 1 {
			h.Fatalf("PutEvents calls = %+v, want one call with one entry", got)
		}
		e := got[0].events.Entries[0]
		if val(e.Source) != "my.app" || val(e.DetailType) != "tick" || (val(e.EventBusName) != h.busArn("default") && val(e.EventBusName) != "default") {
			h.Errorf("entry = %+v, want source my.app, detail-type tick, on the default bus", e)
		}
		for _, c := range h.targetCalls() {
			wantSchedulerCall(h, c)
		}
		// Each invocation is one delivery whose Source is the schedule ARN,
		// over the Scheduler mechanism of its target, as the execution role
		// (contract section 4; scheduler.md 14.4.3, 14.4.4).
		mechanisms := map[string]string{}
		for _, d := range h.sink.all() {
			r := d.Request
			name := r.Source.String()[strings.LastIndexByte(r.Source.String(), '/')+1:]
			mechanisms[name] = r.Mechanism.String()
			if r.Source.String() != h.scheduleArn("default", name) || r.Invocation == nil || r.As == nil {
				h.Errorf("delivery %+v, want Source the schedule ARN with As and Invocation", r)
			}
		}
		want := map[string]string{
			"lambda": "scheduler-to-lambda", "fifo": "scheduler-to-sqs", "sns": "scheduler-to-sns",
			"sfn": "scheduler-to-states", "bus": "scheduler-to-events",
		}
		for name, m := range want {
			if mechanisms[name] != m {
				h.Errorf("schedule %s delivered over %q, want %s", name, mechanisms[name], m)
			}
		}
	})
}

// What /iam/policy builds on: the execution role's target actions are
// recorded for the role, not for the caller (scheduler.md 9, 14.11).
func TestRoleUsageIsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		h.create(schedule("s", atExpr(h.now().Add(10*time.Minute)), h.queueTarget("q", new("x"))))
		h.advance(15 * time.Minute)
		role := access.Role{Account: h.Scope.Account, Name: roleName}
		found := false
		for _, u := range h.Access.Usage(access.Filter{Principal: role, Service: "sqs"}) {
			if u.Action.String() == "sqs:SendMessage" {
				found = true
			}
		}
		if !found {
			h.Error("no sqs:SendMessage recorded for the execution role")
		}
	})
}

// Rearm after a load skips the fires that came due while stopped, with one
// schedule-fires-skipped advisory per schedule, and keeps the rate's phase;
// a past-due at() found by Rearm is skipped too (scheduler.md 14.3.8;
// contract section 1; as internal/service/events/stateful.go).
func TestRearmSkipsMissedFires(t *testing.T) {
	run(t, func(h *harness) {
		start := h.now().Add(10 * time.Minute)
		in := schedule("hourly", "rate(1 hour)", h.queueTarget("hourly", new("x")))
		in.StartDate = new(start)
		arn := h.create(in)
		h.create(schedule("once", atExpr(start), h.queueTarget("once", new("x"))))
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		h.advance(3 * time.Hour) // now = start + 2h50m
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		h.svc.Rearm(h.Clock.Now())
		h.settled()
		h.advance(9 * time.Minute)
		if n := len(h.sentTo("hourly")) + len(h.sentTo("once")); n != 0 {
			h.Fatalf("%d deliveries before the next fire in phase, want none: missed fires were delivered", n)
		}
		h.advance(2 * time.Minute)
		if got := h.sentTo("hourly"); len(got) != 1 || !inWindow(got[0].at, start.Add(3*time.Hour), precision) {
			h.Errorf("deliveries at the next fire in phase = %+v, want one at %v", got, start.Add(3*time.Hour))
		}
		h.advance(24 * time.Hour)
		if n := len(h.sentTo("once")); n != 0 {
			h.Errorf("the past-due at() sent %d after Rearm, want none", n)
		}
		skipped := map[string]int{}
		for _, ev := range h.Journal.Query(journal.Filter{Kind: journal.KindAdvisory}).Events {
			if a, ok := ev.Detail.(journal.Advisory); ok && a.Kind == journal.AdvisoryScheduleFiresSkipped {
				skipped[a.Resource.String()]++
			}
		}
		if skipped[arn] != 1 || skipped[h.scheduleArn("default", "once")] != 1 || len(skipped) != 2 {
			h.Errorf("schedule-fires-skipped advisories = %v, want one for each schedule", skipped)
		}
	})
}

// A snapshot round trip keeps every member, and a reset drops them
// (contract section 1; ADR 0009).
func TestSnapshotRoundTrip(t *testing.T) {
	run(t, func(h *harness) {
		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g", Tags: []schedulerapi.Tag{{Key: "k", Value: "v"}}})
		h.create(h.fullSchedule("full", "g"))
		before := h.get("g", "full")
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		if len(sections) == 0 {
			h.Fatal("Export wrote no section")
		}
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		wantError(h, errOf(call(h, schedulerapi.OpGetSchedule, &schedulerapi.GetScheduleInput{Name: "full", GroupName: new("g")})), "ResourceNotFoundException", 404, "")
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		h.svc.Rearm(h.Clock.Now())
		after := h.get("g", "full")
		if fmt.Sprintf("%+v", deref(after)) != fmt.Sprintf("%+v", deref(before)) {
			h.Errorf("after import:\n%+v\nbefore:\n%+v", deref(after), deref(before))
		}
		tags := must(h, schedulerapi.OpListTagsForResource, &schedulerapi.ListTagsForResourceInput{ResourceArn: h.groupArn("g")}).Tags
		if !slices.Equal(tags, []schedulerapi.Tag{{Key: "k", Value: "v"}}) {
			h.Errorf("tags after import = %+v", tags)
		}
	})
}

// deref renders a read with its nested pointers followed, for comparison.
func deref(o *schedulerapi.GetScheduleOutput) string {
	date := func(t *time.Time) string {
		if t == nil {
			return "<nil>"
		}
		return t.UTC().Format(time.RFC3339Nano)
	}
	state := "<nil>"
	if o.State != nil {
		state = string(*o.State)
	}
	s := fmt.Sprintf("%s %s %s %s %s %s %s %s %s %s %s", val(o.Arn), val(o.GroupName), val(o.Name), val(o.ScheduleExpression),
		val(o.ScheduleExpressionTimezone), val(o.Description), date(o.StartDate), date(o.EndDate), state, date(o.CreationDate), date(o.LastModificationDate))
	if o.Target != nil {
		s += fmt.Sprintf(" target %s %s %s", o.Target.Arn, o.Target.RoleArn, val(o.Target.Input))
		if o.Target.RetryPolicy != nil {
			s += fmt.Sprintf(" retry %d/%d", val32(o.Target.RetryPolicy.MaximumEventAgeInSeconds), val32(o.Target.RetryPolicy.MaximumRetryAttempts))
		}
		if o.Target.DeadLetterConfig != nil {
			s += " dlq " + val(o.Target.DeadLetterConfig.Arn)
		}
	}
	if o.FlexibleTimeWindow != nil {
		s += fmt.Sprintf(" window %s %d", o.FlexibleTimeWindow.Mode, val32(o.FlexibleTimeWindow.MaximumWindowInMinutes))
	}
	if o.ActionAfterCompletion != nil {
		s += " " + string(*o.ActionAfterCompletion)
	}
	return s + " " + val(o.KmsKeyArn)
}
