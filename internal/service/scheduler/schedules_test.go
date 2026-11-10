package scheduler_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/schedexpr"
	"github.com/sustinbebustin/understudy/internal/service/scheduler"
)

// The basic Terraform configuration reads back with the defaults AWS
// fills and without the members it was never sent: ARN
// schedule/default/<name>, ActionAfterCompletion NONE, RetryPolicy
// 86400/185, GroupName default, the sent time zone and State, window OFF
// with no maximum, and no Description, dates, KmsKeyArn, Input,
// DeadLetterConfig or parameter blocks (tf-acc schedule_test.go:L179-L233,
// assertions L199-L223; scheduler.md 4.2, 11.3, 14.10.1).
func TestGetScheduleReadShape(t *testing.T) {
	run(t, func(h *harness) {
		arn := h.create(schedule("basic", "rate(1 hour)", h.queueTarget("q", nil)))
		if want := h.scheduleArn("default", "basic"); arn != want {
			h.Errorf("ScheduleArn = %s, want %s (tf-acc L199)", arn, want)
		}
		got := h.get("", "basic")
		switch {
		case val(got.Arn) != arn || val(got.Name) != "basic" || val(got.GroupName) != "default":
			h.Errorf("Arn, Name, GroupName = %s, %s, %s; want %s, basic, default (tf-acc L199, L205)", val(got.Arn), val(got.Name), val(got.GroupName), arn)
		case got.ActionAfterCompletion == nil || *got.ActionAfterCompletion != schedulerapi.ActionAfterCompletionNone:
			h.Errorf("ActionAfterCompletion = %v, want NONE although never sent (tf-acc L200)", got.ActionAfterCompletion)
		case val(got.ScheduleExpression) != "rate(1 hour)" || val(got.ScheduleExpressionTimezone) != "UTC":
			h.Errorf("expression, zone = %s, %s; want rate(1 hour), UTC as sent (tf-acc L210)", val(got.ScheduleExpression), val(got.ScheduleExpressionTimezone))
		case got.State == nil || *got.State != schedulerapi.ScheduleStateEnabled:
			h.Errorf("State = %v, want ENABLED as sent", got.State)
		case got.StartDate != nil || got.EndDate != nil || got.KmsKeyArn != nil || (got.Description != nil && *got.Description != ""):
			h.Errorf("StartDate %v EndDate %v KmsKeyArn %v Description %v; want absent (tf-acc L201-L211)", got.StartDate, got.EndDate, got.KmsKeyArn, got.Description)
		case got.FlexibleTimeWindow == nil || got.FlexibleTimeWindow.Mode != schedulerapi.FlexibleTimeWindowModeOff ||
			(got.FlexibleTimeWindow.MaximumWindowInMinutes != nil && *got.FlexibleTimeWindow.MaximumWindowInMinutes != 0):
			h.Errorf("FlexibleTimeWindow = %+v, want OFF with no maximum (tf-acc L203)", got.FlexibleTimeWindow)
		case got.CreationDate == nil || got.LastModificationDate == nil:
			h.Errorf("CreationDate %v, LastModificationDate %v; want both (model GetScheduleOutput)", got.CreationDate, got.LastModificationDate)
		}
		tg := got.Target
		if tg == nil {
			h.Fatal("no Target")
		}
		if tg.Arn != h.queueArn("q") || tg.RoleArn != h.roleArn() {
			h.Errorf("Target = %s as %s, want %s as %s", tg.Arn, tg.RoleArn, h.queueArn("q"), h.roleArn())
		}
		if tg.RetryPolicy == nil || val32(tg.RetryPolicy.MaximumEventAgeInSeconds) != 86400 || val32(tg.RetryPolicy.MaximumRetryAttempts) != 185 {
			h.Errorf("RetryPolicy = %+v, want 86400/185 although never sent (tf-acc L219-L220)", tg.RetryPolicy)
		}
		if tg.Input != nil || tg.DeadLetterConfig != nil || tg.EventBridgeParameters != nil || tg.SqsParameters != nil ||
			tg.KinesisParameters != nil || tg.EcsParameters != nil || tg.SageMakerPipelineParameters != nil {
			h.Errorf("Target = %+v, want no Input, DeadLetterConfig or parameter blocks (tf-acc L214-L223)", tg)
		}
	})
}

func val32(p *int32) int32 {
	if p == nil {
		return -1
	}
	return *p
}

// fullSchedule sets every optional member Terraform's acceptance tests
// echo: a group, Europe/Paris, second-precision dates, description,
// FLEXIBLE 10, retry 60/1, a dead-letter queue, Input, DELETE and a KMS key.
func (h *harness) fullSchedule(name, group string) *schedulerapi.CreateScheduleInput {
	in := schedule(name, "cron(0 8 ? * MON-FRI *)", schedulerapi.Target{
		Arn: h.queueArn("q"), RoleArn: h.roleArn(), Input: new(`{ "spaced" :  "json" }`),
		RetryPolicy:      &schedulerapi.RetryPolicy{MaximumEventAgeInSeconds: new(int32(60)), MaximumRetryAttempts: new(int32(1))},
		DeadLetterConfig: &schedulerapi.DeadLetterConfig{Arn: new(h.queueArn("dlq"))},
	})
	in.GroupName = new(group)
	in.ScheduleExpressionTimezone = new("Europe/Paris")
	in.StartDate = new(time.Date(2099, 1, 1, 1, 0, 0, 0, time.UTC))
	in.EndDate = new(time.Date(2100, 1, 1, 1, 2, 3, 0, time.UTC))
	in.Description = new("weekday mornings")
	in.FlexibleTimeWindow = schedulerapi.FlexibleTimeWindow{Mode: schedulerapi.FlexibleTimeWindowModeFlexible, MaximumWindowInMinutes: new(int32(10))}
	in.ActionAfterCompletion = new(schedulerapi.ActionAfterCompletionDelete)
	in.KmsKeyArn = new(h.keyArn())
	return in
}

// Members are echoed exactly as sent: time zone, dates to the second,
// window 10, retry 60/1, the dead-letter ARN, Input byte for byte,
// DELETE, KMS key and group (tf-acc schedule_test.go:L365-L419,
// L421-L478, L480-L510, L676-L730, L1174-L1233, L1292-L1349,
// L1774-L1778; scheduler.md 11.3, 14.10.1).
func TestGetScheduleEchoesEveryMember(t *testing.T) {
	run(t, func(h *harness) {
		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g"})
		in := h.fullSchedule("full", "g")
		if arn := h.create(in); arn != h.scheduleArn("g", "full") {
			h.Errorf("ScheduleArn = %s, want %s", arn, h.scheduleArn("g", "full"))
		}
		got := h.get("g", "full")
		switch {
		case val(got.GroupName) != "g" || val(got.ScheduleExpressionTimezone) != "Europe/Paris" || val(got.Description) != "weekday mornings":
			h.Errorf("group, zone, description = %s, %s, %s", val(got.GroupName), val(got.ScheduleExpressionTimezone), val(got.Description))
		case got.StartDate == nil || !got.StartDate.Equal(*in.StartDate) || got.EndDate == nil || !got.EndDate.Equal(*in.EndDate):
			h.Errorf("dates = %v, %v; want %v, %v", got.StartDate, got.EndDate, *in.StartDate, *in.EndDate)
		case got.FlexibleTimeWindow == nil || got.FlexibleTimeWindow.Mode != schedulerapi.FlexibleTimeWindowModeFlexible || val32(got.FlexibleTimeWindow.MaximumWindowInMinutes) != 10:
			h.Errorf("FlexibleTimeWindow = %+v, want FLEXIBLE 10", got.FlexibleTimeWindow)
		case got.ActionAfterCompletion == nil || *got.ActionAfterCompletion != schedulerapi.ActionAfterCompletionDelete:
			h.Errorf("ActionAfterCompletion = %v, want DELETE", got.ActionAfterCompletion)
		case val(got.KmsKeyArn) != h.keyArn():
			h.Errorf("KmsKeyArn = %s, want %s", val(got.KmsKeyArn), h.keyArn())
		}
		tg := got.Target
		if tg == nil {
			h.Fatal("no Target")
		}
		if val(tg.Input) != *in.Target.Input {
			h.Errorf("Input = %q, want %q byte for byte", val(tg.Input), *in.Target.Input)
		}
		if tg.RetryPolicy == nil || val32(tg.RetryPolicy.MaximumEventAgeInSeconds) != 60 || val32(tg.RetryPolicy.MaximumRetryAttempts) != 1 {
			h.Errorf("RetryPolicy = %+v, want 60/1", tg.RetryPolicy)
		}
		if tg.DeadLetterConfig == nil || val(tg.DeadLetterConfig.Arn) != h.queueArn("dlq") {
			h.Errorf("DeadLetterConfig = %+v, want %s", tg.DeadLetterConfig, h.queueArn("dlq"))
		}
	})
}

// UpdateSchedule replaces the schedule: members left out of the request
// take their defaults (aws-doc
// https://docs.aws.amazon.com/scheduler/latest/APIReference/API_UpdateSchedule.html;
// tf-acc L409, L467, L556, L776, L932, L1338, L288-L302; scheduler.md 4.3).
func TestUpdateScheduleReplacesEveryMember(t *testing.T) {
	run(t, func(h *harness) {
		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g"})
		h.create(h.fullSchedule("s", "g"))
		basic := schedule("s", "rate(1 hour)", h.queueTarget("q", nil))
		basic.GroupName = new("g")
		if out := must(h, schedulerapi.OpUpdateSchedule, update(basic)); out.ScheduleArn != h.scheduleArn("g", "s") {
			h.Errorf("UpdateSchedule ScheduleArn = %s, want %s", out.ScheduleArn, h.scheduleArn("g", "s"))
		}
		got := h.get("g", "s")
		if got.StartDate != nil || got.EndDate != nil || got.KmsKeyArn != nil || (got.Description != nil && *got.Description != "") {
			h.Errorf("dropped StartDate %v EndDate %v KmsKeyArn %v Description %v; want absent", got.StartDate, got.EndDate, got.KmsKeyArn, got.Description)
		}
		if got.FlexibleTimeWindow == nil || got.FlexibleTimeWindow.Mode != schedulerapi.FlexibleTimeWindowModeOff ||
			(got.FlexibleTimeWindow.MaximumWindowInMinutes != nil && *got.FlexibleTimeWindow.MaximumWindowInMinutes != 0) {
			h.Errorf("FlexibleTimeWindow = %+v, want OFF with no maximum", got.FlexibleTimeWindow)
		}
		if got.ActionAfterCompletion == nil || *got.ActionAfterCompletion != schedulerapi.ActionAfterCompletionNone {
			h.Errorf("ActionAfterCompletion = %v, want NONE again", got.ActionAfterCompletion)
		}
		if val(got.ScheduleExpressionTimezone) != "UTC" || val(got.ScheduleExpression) != "rate(1 hour)" {
			h.Errorf("zone, expression = %s, %s; want UTC, rate(1 hour)", val(got.ScheduleExpressionTimezone), val(got.ScheduleExpression))
		}
		tg := got.Target
		if tg == nil || tg.Input != nil || tg.DeadLetterConfig != nil || tg.RetryPolicy == nil ||
			val32(tg.RetryPolicy.MaximumEventAgeInSeconds) != 86400 || val32(tg.RetryPolicy.MaximumRetryAttempts) != 185 {
			h.Errorf("Target = %+v, want no Input or DeadLetterConfig and RetryPolicy 86400/185", tg)
		}
	})
}

// A deleted schedule is gone: GetSchedule, UpdateSchedule and a second
// DeleteSchedule answer ResourceNotFoundException 404 (aws-doc
// https://docs.aws.amazon.com/scheduler/latest/APIReference/API_DeleteSchedule.html;
// model; scheduler.md 2.4, 4.4), each "Schedule <name> does not exist."
// without the group (SCH-02 02-get-missing, 02-update-missing,
// 02-delete-again, recorded 2026-11-03).
func TestDeleteSchedule(t *testing.T) {
	run(t, func(h *harness) {
		const missing = "Schedule s does not exist."
		h.create(schedule("s", "rate(1 hour)", h.queueTarget("q", nil)))
		must(h, schedulerapi.OpDeleteSchedule, &schedulerapi.DeleteScheduleInput{Name: "s"})
		wantError(h, errOf(call(h, schedulerapi.OpGetSchedule, &schedulerapi.GetScheduleInput{Name: "s"})), "ResourceNotFoundException", 404, missing)
		wantError(h, errOf(call(h, schedulerapi.OpUpdateSchedule, update(schedule("s", "rate(1 hour)", h.queueTarget("q", nil))))), "ResourceNotFoundException", 404, missing)
		wantError(h, errOf(call(h, schedulerapi.OpDeleteSchedule, &schedulerapi.DeleteScheduleInput{Name: "s"})), "ResourceNotFoundException", 404, missing)
	})
}

// ListSchedules always returns a Schedules list (G6: localstack@8b9a79f
// tests/aws/services/scheduler/test_scheduler.py:L13-L20, validated
// 2024-06-11); NamePrefix, ScheduleGroup and State filter, entries carry
// Target {Arn}, and following NextToken with MaxResults 1 yields every
// match once (scheduler.md 4.5; order and page size are UNVERIFIED,
// SCH-01, so the union is compared).
func TestListSchedules(t *testing.T) {
	run(t, func(h *harness) {
		empty := must(h, schedulerapi.OpListSchedules, &schedulerapi.ListSchedulesInput{})
		if empty.Schedules == nil || len(empty.Schedules) != 0 {
			h.Errorf("ListSchedules with none = %#v, want an empty list", empty.Schedules)
		}
		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g"})
		for _, name := range []string{"a-1", "a-2", "a-3", "b-1"} {
			h.create(schedule(name, "rate(1 hour)", h.queueTarget("q", nil)))
		}
		inG := schedule("a-g", "rate(1 hour)", h.queueTarget("q", nil))
		inG.GroupName = new("g")
		h.create(inG)
		off := schedule("a-off", "rate(1 hour)", h.queueTarget("q", nil))
		off.State = new(schedulerapi.ScheduleStateDisabled)
		h.create(off)

		list := func(in schedulerapi.ListSchedulesInput) []string {
			var names []string
			for {
				out := must(h, schedulerapi.OpListSchedules, &in)
				for _, s := range out.Schedules {
					names = append(names, val(s.GroupName)+"/"+val(s.Name))
					if s.Target == nil || s.Target.Arn != h.queueArn("q") || val(s.Arn) != h.scheduleArn(val(s.GroupName), val(s.Name)) {
						h.Errorf("summary %+v, want its ARN and Target {Arn}", s)
					}
				}
				if out.NextToken == nil {
					break
				}
				in.NextToken = out.NextToken
			}
			slices.Sort(names)
			return names
		}
		if got := list(schedulerapi.ListSchedulesInput{NamePrefix: new("a-"), MaxResults: new(int32(1))}); !slices.Equal(got, []string{"default/a-1", "default/a-2", "default/a-3", "default/a-off", "g/a-g"}) {
			h.Errorf("prefix a- across pages = %v", got)
		}
		if got := list(schedulerapi.ListSchedulesInput{GroupName: new("g")}); !slices.Equal(got, []string{"g/a-g"}) {
			h.Errorf("ScheduleGroup g = %v, want g/a-g", got)
		}
		if got := list(schedulerapi.ListSchedulesInput{State: new(schedulerapi.ScheduleStateDisabled)}); !slices.Equal(got, []string{"default/a-off"}) {
			h.Errorf("State DISABLED = %v, want default/a-off", got)
		}
		wantError(h, errOf(call(h, schedulerapi.OpListSchedules, &schedulerapi.ListSchedulesInput{GroupName: new("nope")})), "ResourceNotFoundException", 404, "")
	})
}

// g3Requests are the 17 AWS-validated invalid expressions; the requests
// send FLEXIBLE 4 with a role and a function that do not exist, so the
// expression is checked first (G3: localstack@8b9a79f
// tests/aws/services/scheduler/test_scheduler.snapshot.json:L31-L302 and
// test_scheduler.py:L68-L110, validated 2025-01-26; scheduler.md 2.4, 4.7).
var g3Requests = []string{
	"cron(0 1 * * * *)", "cron(7 20 * * NOT *)", "cron(INVALID)", "cron(0 dummy ? * MON-FRI *)", "cron(71 8 1 * ? *)", "cron()",
	"rate(10 seconds)", "rate(10 years)", "rate()", "rate(10)", "rate(10 minutess)", "rate(foo minutes)", "rate(-10 minutes)",
	"rate( 10 minutes )", " rate(10 minutes)",
	"at(2021-12-31T23:59:59Z)", "at(2021-12-31)",
}

// g3Input is the G3 request: FLEXIBLE 4, no GroupName, a role and a
// function that do not exist.
func (h *harness) g3Input(name, expr string) *schedulerapi.CreateScheduleInput {
	return &schedulerapi.CreateScheduleInput{
		Name: name, ScheduleExpression: expr,
		Target:             schedulerapi.Target{Arn: h.functionArn("does-not-exist"), RoleArn: fmt.Sprintf("arn:aws:iam::%s:role/role-name", h.Scope.Account)},
		FlexibleTimeWindow: schedulerapi.FlexibleTimeWindow{Mode: schedulerapi.FlexibleTimeWindowModeFlexible, MaximumWindowInMinutes: new(int32(4))},
	}
}

func TestCreateScheduleRecordedRefusals(t *testing.T) {
	run(t, func(h *harness) {
		for i, expr := range g3Requests {
			_, err := call(h, schedulerapi.OpCreateSchedule, h.g3Input(fmt.Sprintf("bad-%d", i), expr))
			wantError(h, err, "ValidationException", 400, "Invalid Schedule Expression "+expr+".")
		}
	})
}

// Accepted with a ScheduleArn: the six recorded crons (G5, snapshot
// L315-L387, validated 2026-01-17) and a past at() with seconds (G4,
// snapshot L303-L314 and test_scheduler.py:L146-L203, validated
// 2025-02-02), each to a Lambda function that was never created: the
// target is not checked to exist (scheduler.md 4.1, 5.1, 5.3).
func TestCreateScheduleRecordedAcceptances(t *testing.T) {
	run(t, func(h *harness) {
		for i, expr := range []string{
			"cron(0-15/1 8-8 ? * 2,3,4,5,6,7 *)", "cron(30-30 13-13 ? * 2,4,6 *)", "cron(1-31/30 1-1 ? * 1,7 *)",
			"cron(0 18 ? * MON-FRI *)", "cron(*/15 * * * ? *)", "cron(0-59/10 0-23/2 ? * * *)",
			"at(1999-12-31T23:59:59)",
		} {
			in := h.g3Input(fmt.Sprintf("ok-%d", i), expr)
			in.Target.RoleArn = h.roleArn()
			out := must(h, schedulerapi.OpCreateSchedule, in)
			if out.ScheduleArn != h.scheduleArn("default", in.Name) {
				h.Errorf("CreateSchedule(%s) ScheduleArn = %s", expr, out.ScheduleArn)
			}
		}
	})
}

// A cron that can never fire again is refused with the recorded text,
// null for the absent dates of a DISABLED schedule (SCH-03 03-e24,
// recorded 2026-11-03; scheduler.md 2.4, 14.3.6).
func TestCreateScheduleRefusesACronThatNeverFires(t *testing.T) {
	run(t, func(h *harness) {
		in := schedule("never", "cron(30 0 1 1 ? 1999)", h.queueTarget("q", nil))
		in.State = new(schedulerapi.ScheduleStateDisabled)
		_, err := call(h, schedulerapi.OpCreateSchedule, in)
		wantError(h, err, "ValidationException", 400,
			"The schedule expression cron(30 0 1 1 ? 1999), with StartDate null and EndDate null, is invalid because it will never schedule an invocation.")
	})
}

// scheduler.rate.unit-agreement: the census's rate(5 minute) and
// rate(1 minutes) are accepted under any, and refused with the Scheduler
// text under singular-iff-one (scheduler.md 5.2, 12).
func TestRateUnitAgreementSwitch(t *testing.T) {
	census := []string{"rate(5 minute)", "rate(1 minutes)"}
	for _, o := range []scheduler.Options{{}, {RateUnits: schedexpr.SingularIffOne}} {
		t.Run(scheduler.UnitAgreement(o).Value.String(), func(t *testing.T) {
			runWith(t, harnessOptions{scheduler: o}, func(h *harness) {
				h.create(schedule("existing", "rate(1 hour)", h.queueTarget("q", nil)))
				for i, expr := range census {
					created := errOf(call(h, schedulerapi.OpCreateSchedule, schedule(fmt.Sprintf("r-%d", i), expr, h.queueTarget("q", nil))))
					updated := errOf(call(h, schedulerapi.OpUpdateSchedule, update(schedule("existing", expr, h.queueTarget("q", nil)))))
					switch scheduler.UnitAgreement(o).Value {
					case schedexpr.AnyUnitAgreement:
						if created != nil || updated != nil {
							h.Errorf("%s under any: create %v, update %v; want both accepted", expr, created, updated)
						}
					case schedexpr.SingularIffOne:
						wantError(h, created, "ValidationException", 400, "Invalid Schedule Expression "+expr+".")
						wantError(h, updated, "ValidationException", 400, "Invalid Schedule Expression "+expr+".")
					}
				}
			})
		})
	}
}

// Expression forms SCH-03 recorded (2026-11-03; scheduler.md 5.1, 5.2):
// rate units are case-insensitive (rate(1 MINUTE)) and agree with no
// value; at() takes its seconds or leaves them out (at(2030-01-01T00:00));
// a fraction, single-digit fields and a space for the T are refused.
func TestSchedulerExpressionForms(t *testing.T) {
	run(t, func(h *harness) {
		for i, expr := range []string{
			"rate(1 MINUTE)", "rate(1 minutes)", "rate(5 minute)", "rate(1 hours)", "rate(2 hour)", "rate(1 day)", "rate(2 days)",
			"rate(100000 days)", "at(2030-01-01T00:00)", "at(2030-01-01T00:00:00)",
		} {
			h.create(schedule(fmt.Sprintf("ok-%d", i), expr, h.queueTarget("q", nil)))
		}
		for _, expr := range []string{"at(2030-01-01T00:00:00.000)", "at(2030-1-1T00:00:00)", "at(2030-01-01 00:00:00)", "rate(0 minutes)"} {
			wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, schedule("bad", expr, h.queueTarget("q", nil)))),
				"ValidationException", 400, "Invalid Schedule Expression "+expr+".")
		}
	})
}

// An at() without seconds fires at the minute's start.
func TestAtWithoutSecondsFiresAtTheMinute(t *testing.T) {
	run(t, func(h *harness) {
		at := h.now().Add(10 * time.Minute).Truncate(time.Minute)
		h.create(schedule("m", "at("+at.Format("2006-01-02T15:04")+")", h.queueTarget("m", new("x"))))
		h.advance(15 * time.Minute)
		if got := h.sentTo("m"); len(got) != 1 || !got[0].at.Wire().Equal(at) {
			h.Errorf("sends = %+v, want one at %v", got, at)
		}
	})
}

// GetSchedule reads ScheduleExpressionTimezone back as UTC when none was
// sent, after a create and after an update that leaves it out (SCH-01
// 01-get-min, SCH-05 05-get, recorded 2026-11-03; scheduler.md 4.2).
func TestGetScheduleDefaultsAnOmittedZoneToUTC(t *testing.T) {
	run(t, func(h *harness) {
		in := schedule("z", "rate(1 hour)", h.queueTarget("q", nil))
		in.ScheduleExpressionTimezone = nil
		h.create(in)
		if got := val(h.get("", "z").ScheduleExpressionTimezone); got != "UTC" {
			h.Errorf("zone after create = %s, want UTC", got)
		}
		paris := schedule("p", "rate(1 hour)", h.queueTarget("q", nil))
		paris.ScheduleExpressionTimezone = new("Europe/Paris")
		h.create(paris)
		paris.ScheduleExpressionTimezone = nil
		must(h, schedulerapi.OpUpdateSchedule, update(paris))
		if got := val(h.get("", "p").ScheduleExpressionTimezone); got != "UTC" {
			h.Errorf("zone after an update without one = %s, want UTC", got)
		}
	})
}

// Universal (aws-sdk) targets and the templated targets understudy does
// not invoke answer 501 NotImplemented at create and update, never
// accepted and silently not invoked (CLAUDE.md invariant 3;
// scheduler.md 14.1, 14.13.1). ARNs per the templated-target list
// (aws-doc https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-targets-templated.html)
// and the universal form (aws-doc managing-targets-universal).
func TestUnsupportedTargetsAnswerNotImplemented(t *testing.T) {
	run(t, func(h *harness) {
		acct, region := h.Scope.Account, h.Scope.Region
		h.create(schedule("u", "rate(1 hour)", h.queueTarget("q", nil)))
		for _, target := range []string{
			"arn:aws:scheduler:::aws-sdk:sqs:sendMessage",
			fmt.Sprintf("arn:aws:ecs:%s:%s:cluster/c", region, acct),
			fmt.Sprintf("arn:aws:kinesis:%s:%s:stream/s", region, acct),
			fmt.Sprintf("arn:aws:firehose:%s:%s:deliverystream/d", region, acct),
			fmt.Sprintf("arn:aws:codebuild:%s:%s:project/p", region, acct),
			fmt.Sprintf("arn:aws:codepipeline:%s:%s:p", region, acct),
			fmt.Sprintf("arn:aws:inspector:%s:%s:target/0-abc/template/0-def", region, acct),
			fmt.Sprintf("arn:aws:sagemaker:%s:%s:pipeline/p", region, acct),
		} {
			tg := schedulerapi.Target{Arn: target, RoleArn: h.roleArn(), Input: new(`{}`)}
			if strings.HasPrefix(target, "arn:aws:kinesis:") {
				tg.KinesisParameters = &schedulerapi.KinesisParameters{PartitionKey: "k"}
			}
			_, err := call(h, schedulerapi.OpCreateSchedule, schedule("n", "rate(1 hour)", tg))
			wantError(h, err, "NotImplemented", 501, "")
			_, err = call(h, schedulerapi.OpUpdateSchedule, update(schedule("u", "rate(1 hour)", tg)))
			wantError(h, err, "NotImplemented", 501, "")
		}
		wantError(h, errOf(call(h, schedulerapi.OpGetSchedule, &schedulerapi.GetScheduleInput{Name: "n"})), "ResourceNotFoundException", 404, "")
	})
}

// SNS topics and Step Functions state machines are templated targets
// understudy invokes, so their ARNs are accepted, as are
// Lambda (qualified too), SQS and event bus ARNs (aws-doc
// managing-targets-templated; scheduler.md 6.1, 14.4.1).
func TestInvokedTargetsAreAccepted(t *testing.T) {
	run(t, func(h *harness) {
		for i, tg := range []schedulerapi.Target{
			{Arn: h.functionArn("f"), RoleArn: h.roleArn()},
			{Arn: h.functionArn("f") + ":live", RoleArn: h.roleArn()},
			{Arn: h.queueArn("q"), RoleArn: h.roleArn()},
			{Arn: h.topicArn("ticks"), RoleArn: h.roleArn()},
			{Arn: h.machineArn("flow"), RoleArn: h.roleArn(), Input: new(`{}`)},
			{Arn: h.busArn("default"), RoleArn: h.roleArn(), EventBridgeParameters: &schedulerapi.EventBridgeParameters{Source: "my.app", DetailType: "tick"}},
		} {
			h.create(schedule(fmt.Sprintf("t-%d", i), "rate(1 hour)", tg))
		}
	})
}
