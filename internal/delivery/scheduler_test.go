package delivery_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/delivery"
)

func scheduleARN() aws.ARN {
	return aws.ARN{Partition: "aws", Service: "scheduler", Region: "us-east-1", Account: "000000000000", Resource: "schedule/default/s"}
}

// Payload replaces the four context attributes wherever they appear in the
// template: the schedule ARN, the scheduled time as
// 2022-03-22T18:59:43Z, the execution id and the attempt number counting
// from 1 (aws-doc
// https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-schedule-context-attributes.html,
// whose examples are these values; scheduler.md 6.3, 14.4.2).
func TestSchedulerPayloadContextAttributes(t *testing.T) {
	inv := delivery.SchedulerInvocation{
		ScheduleARN: scheduleARN(),
		TargetARN:   "arn:aws:sqs:us-east-1:000000000000:q",
		Scheduled:   time.Date(2022, 3, 22, 18, 59, 43, 0, time.UTC),
		ExecutionID: "ad06616e51cdf74a",
		Template:    `{"arn":"<aws.scheduler.schedule-arn>","at":"<aws.scheduler.scheduled-time>","id":"<aws.scheduler.execution-id>","n":<aws.scheduler.attempt-number>,"again":"<aws.scheduler.attempt-number>"}`,
	}
	for _, attempt := range []int{1, 2, 185} {
		n := strconv.Itoa(attempt)
		want := `{"arn":"arn:aws:scheduler:us-east-1:000000000000:schedule/default/s","at":"2022-03-22T18:59:43Z","id":"ad06616e51cdf74a","n":` + n + `,"again":"` + n + `"}`
		if got := inv.Payload(attempt); got != want {
			t.Errorf("Payload(%d) =\n%s\nwant\n%s", attempt, got, want)
		}
	}

	// The scheduled time is rendered in UTC whatever zone the instant
	// carries ("2022-03-22T18:59:43Z", aws-doc as above).
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	inv.Scheduled = time.Date(2022, 3, 22, 19, 59, 43, 0, paris)
	inv.Template = "<aws.scheduler.scheduled-time>"
	if got := inv.Payload(1); got != "2022-03-22T18:59:43Z" {
		t.Errorf("Payload of a Paris instant = %q, want 2022-03-22T18:59:43Z", got)
	}

	// Text Input is not JSON and is substituted the same way: the guide's
	// templated SQS examples put the attributes in plain text (aws-doc
	// managing-targets-templated; scheduler.md 6.3).
	inv.Template = "hello <aws.scheduler.attempt-number>"
	if got := inv.Payload(1); got != "hello 1" {
		t.Errorf("Payload(1) of %q = %q, want hello 1", inv.Template, got)
	}
}

// A template without context attributes is delivered unchanged, byte for
// byte, on every attempt: Input is echoed and passed as given (scheduler.md
// 4.2, 6.4, 11.3).
func TestPropertySchedulerPayloadWithoutAttributesIsVerbatim(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		tmpl := rapid.String().Filter(func(s string) bool { return !strings.Contains(s, "<aws.scheduler.") }).Draw(t, "template")
		attempt := rapid.IntRange(1, 186).Draw(t, "attempt")
		inv := delivery.SchedulerInvocation{
			ScheduleARN: scheduleARN(),
			Scheduled:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			ExecutionID: "0123456789abcdef",
			Template:    tmpl,
		}
		if got := inv.Payload(attempt); got != tmpl {
			t.Fatalf("Payload(%d) of %q = %q, want it unchanged", attempt, tmpl, got)
		}
	})
}

// The switch values of scheduler.retry.permanent-errors (contract section
// 3; scheduler.md 14.12).
func TestPermanentErrorsStrings(t *testing.T) {
	if delivery.PermanentNotRetried.String() != "not-retried" || delivery.PermanentRetried.String() != "retried" {
		t.Errorf("PermanentErrors strings = %q, %q; want not-retried, retried", delivery.PermanentNotRetried, delivery.PermanentRetried)
	}
}
