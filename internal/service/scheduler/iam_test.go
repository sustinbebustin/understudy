package scheduler_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/scheduler"
)

// question is one authorization question the fake Authorizer was asked.
type question struct {
	op        string
	principal identity.Principal
	mode      kernel.PrincipalMode // nil for a call's own questions; set by kernel.AuthorizeAs
	q         kernel.AuthzQuery
}

// caller reports whether the question is about the client's own call:
// the root caller, not a role session or an AuthorizeAs question.
func (q question) caller() bool {
	_, root := q.principal.(identity.Root)
	return root && q.mode == nil
}

// gate is a fake Authorizer standing in for iam.enforce=enforce: it
// records every question and denies those deny matches (spec "Enforce
// matrix": tests use a fake Authorizer until 05b).
type gate struct {
	mu    sync.Mutex
	deny  func(q question, action, resource string) bool
	asked []question
}

func (g *gate) Authorize(_ context.Context, c *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	qu := question{op: c.Op().ID.Name, principal: c.Principal(), mode: c.Mode(), q: q}
	g.asked = append(g.asked, qu)
	for _, p := range q.Pairs {
		if g.deny != nil && g.deny(qu, p.Action.String(), resourceOf(p.Resource)) {
			return kernel.Denied{Message: "User is not authorized to perform: " + p.Action.String()}
		}
	}
	return kernel.Allowed{}
}

func resourceOf(r kernel.ResourceRef) string {
	if a, ok := r.(kernel.ResourceARN); ok {
		return a.ARN.String()
	}
	return "*"
}

// callerPairs lists "action resource" for every pair the caller was asked
// about during op.
func (g *gate) callerPairs(op string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, a := range g.asked {
		if a.op != op || !a.caller() {
			continue
		}
		for _, p := range a.q.Pairs {
			if s := p.Action.String() + " " + resourceOf(p.Resource); !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	slices.Sort(out)
	return out
}

// callerQuestion returns the caller's question about action during op.
func (g *gate) callerQuestion(op, action string) (kernel.AuthzQuery, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, a := range g.asked {
		if a.op != op || !a.caller() {
			continue
		}
		for _, p := range a.q.Pairs {
			if p.Action.String() == action {
				return a.q, true
			}
		}
	}
	return kernel.AuthzQuery{}, false
}

func (g *gate) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked = nil
}

func withGate(g *gate) harnessOptions { return harnessOptions{authorizer: g} }

// A caller denied at step 8 gets Scheduler's denial identity:
// AccessDeniedException at the status of scheduler.denial.status (spec
// conflict 9; iam-enforcement-surfaces.md 12.3; scheduler.md 2.4; aws-doc
// https://docs.aws.amazon.com/scheduler/latest/APIReference/CommonErrors.html).
func TestDeniedGetSchedule(t *testing.T) {
	g := &gate{deny: func(q question, _, _ string) bool { return q.caller() }}
	runWith(t, withGate(g), func(h *harness) {
		_, err := call(h, schedulerapi.OpGetSchedule, &schedulerapi.GetScheduleInput{Name: "s"})
		wantError(h, err, "AccessDeniedException", scheduler.DenialStatus().Value, "")
	})
}

// Each operation asks about its own action only, on the schedule ARN, the
// group ARN or * for the two lists, never the whole generated action list;
// CreateSchedule and UpdateSchedule add iam:PassRole on the role,
// CreateScheduleGroup with tags adds scheduler:TagResource (scheduler.md
// 2.2, 9, 14.11; contract section 1; spec "Contract" IAM).
func TestOperationAuthorization(t *testing.T) {
	g := &gate{}
	runWith(t, withGate(g), func(h *harness) {
		sch, grp, role := h.scheduleArn("default", "s"), h.groupArn("g"), h.roleArn()
		steps := []struct {
			op   string
			do   func()
			want []string
		}{
			{"CreateScheduleGroup", func() {
				must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g"})
			}, []string{"scheduler:CreateScheduleGroup " + grp}},
			{
				"CreateSchedule", func() { h.create(schedule("s", "rate(1 hour)", h.queueTarget("q", nil))) },
				[]string{"iam:PassRole " + role, "scheduler:CreateSchedule " + sch},
			},
			{
				"UpdateSchedule", func() {
					must(h, schedulerapi.OpUpdateSchedule, update(schedule("s", "rate(2 hours)", h.queueTarget("q", nil))))
				},
				[]string{"iam:PassRole " + role, "scheduler:UpdateSchedule " + sch},
			},
			{"GetSchedule", func() { h.get("", "s") }, []string{"scheduler:GetSchedule " + sch}},
			{"ListSchedules", func() { must(h, schedulerapi.OpListSchedules, &schedulerapi.ListSchedulesInput{}) }, []string{"scheduler:ListSchedules *"}},
			{"GetScheduleGroup", func() {
				must(h, schedulerapi.OpGetScheduleGroup, &schedulerapi.GetScheduleGroupInput{Name: "g"})
			}, []string{"scheduler:GetScheduleGroup " + grp}},
			{"ListScheduleGroups", func() { must(h, schedulerapi.OpListScheduleGroups, &schedulerapi.ListScheduleGroupsInput{}) }, []string{"scheduler:ListScheduleGroups *"}},
			{"TagResource", func() {
				must(h, schedulerapi.OpTagResource, &schedulerapi.TagResourceInput{ResourceArn: grp, Tags: []schedulerapi.Tag{{Key: "k", Value: "v"}}})
			}, []string{"scheduler:TagResource " + grp}},
			{"ListTagsForResource", func() {
				must(h, schedulerapi.OpListTagsForResource, &schedulerapi.ListTagsForResourceInput{ResourceArn: grp})
			}, []string{"scheduler:ListTagsForResource " + grp}},
			{"UntagResource", func() {
				must(h, schedulerapi.OpUntagResource, &schedulerapi.UntagResourceInput{ResourceArn: grp, TagKeys: []string{"k"}})
			}, []string{"scheduler:UntagResource " + grp}},
			{
				"DeleteSchedule", func() { must(h, schedulerapi.OpDeleteSchedule, &schedulerapi.DeleteScheduleInput{Name: "s"}) },
				[]string{"scheduler:DeleteSchedule " + sch},
			},
			{
				"DeleteScheduleGroup", func() { must(h, schedulerapi.OpDeleteScheduleGroup, &schedulerapi.DeleteScheduleGroupInput{Name: "g"}) },
				[]string{"scheduler:DeleteScheduleGroup " + grp},
			},
		}
		for _, s := range steps {
			g.reset()
			s.do()
			want := slices.Sorted(slices.Values(s.want))
			if got := g.callerPairs(s.op); !slices.Equal(got, want) {
				h.Errorf("%s asked %v, want %v", s.op, got, want)
			}
		}
		g.reset()
		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "tagged", Tags: []schedulerapi.Tag{{Key: "k", Value: "v"}}})
		if got, want := g.callerPairs("CreateScheduleGroup"), []string{"scheduler:CreateScheduleGroup " + h.groupArn("tagged"), "scheduler:TagResource " + h.groupArn("tagged")}; !slices.Equal(got, want) {
			h.Errorf("tagged CreateScheduleGroup asked %v, want %v", got, want)
		}
	})
}

// iam:PassRole carries iam:PassedToService = scheduler.amazonaws.com
// (spec conflict 8; scheduler.md 9, 14.11; iam-enforcement-surfaces.md
// 10.2), and a denied PassRole refuses the create with Scheduler's denial
// before anything is stored (spec "Enforce matrix").
func TestPassRole(t *testing.T) {
	g := &gate{deny: func(q question, action, _ string) bool { return q.caller() && action == "iam:PassRole" }}
	runWith(t, withGate(g), func(h *harness) {
		_, err := call(h, schedulerapi.OpCreateSchedule, schedule("s", "rate(1 hour)", h.queueTarget("q", nil)))
		wantError(h, err, "AccessDeniedException", scheduler.DenialStatus().Value, "")
		q, ok := g.callerQuestion("CreateSchedule", "iam:PassRole")
		if !ok || !slices.Equal(q.Keys["iam:PassedToService"], []string{"scheduler.amazonaws.com"}) {
			h.Errorf("PassRole question = %+v (asked %v), want iam:PassedToService=scheduler.amazonaws.com", q, ok)
		}
		wantError(h, errOf(call(h, schedulerapi.OpGetSchedule, &schedulerapi.GetScheduleInput{Name: "s"})), "ResourceNotFoundException", 404, "")
	})
}

// DeleteScheduleGroup of a group holding schedules asks once for
// scheduler:DeleteSchedule on arn:...:schedule/<group>/*, the resource
// AWS's denial named (SCH-10 10-03-group-delete-denied, recorded
// 2026-11-03; actions.json; scheduler.md 9); denied, nothing is deleted.
func TestDeleteScheduleGroupAuthorizesEachMember(t *testing.T) {
	g := &gate{}
	runWith(t, withGate(g), func(h *harness) {
		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g"})
		for _, name := range []string{"a", "b"} {
			in := schedule(name, "rate(1 hour)", h.queueTarget("q", nil))
			in.GroupName = new("g")
			h.create(in)
		}
		g.mu.Lock()
		g.deny = func(q question, action, _ string) bool { return q.caller() && action == "scheduler:DeleteSchedule" }
		g.mu.Unlock()
		g.reset()
		_, err := call(h, schedulerapi.OpDeleteScheduleGroup, &schedulerapi.DeleteScheduleGroupInput{Name: "g"})
		wantError(h, err, "AccessDeniedException", scheduler.DenialStatus().Value, "")
		h.get("g", "a")
		h.get("g", "b")
		must(h, schedulerapi.OpGetScheduleGroup, &schedulerapi.GetScheduleGroupInput{Name: "g"})

		g.mu.Lock()
		g.deny = nil
		g.mu.Unlock()
		g.reset()
		must(h, schedulerapi.OpDeleteScheduleGroup, &schedulerapi.DeleteScheduleGroupInput{Name: "g"})
		got := g.callerPairs("DeleteScheduleGroup")
		want := []string{"scheduler:DeleteSchedule " + h.scheduleArn("g", "*"), "scheduler:DeleteScheduleGroup " + h.groupArn("g")}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			h.Errorf("DeleteScheduleGroup asked %v, want %v", got, want)
		}
	})
}

// KmsKeyArn at create and update: the caller's kms:DescribeKey,
// kms:GenerateDataKey and kms:Decrypt on the key ARN (aws-doc
// https://docs.aws.amazon.com/scheduler/latest/UserGuide/encryption-rest.html;
// scheduler.md 4.1, 9, 14.11; spec "Enforce matrix" KMS rows); one denied
// refuses the write with Scheduler's denial and stores nothing; no KMS
// question without a key.
func TestKmsKeyCallerAuthorization(t *testing.T) {
	kms := []string{"kms:Decrypt", "kms:DescribeKey", "kms:GenerateDataKey"}
	g := &gate{}
	runWith(t, withGate(g), func(h *harness) {
		h.create(schedule("plain", "rate(1 hour)", h.queueTarget("q", nil)))
		for _, p := range g.callerPairs("CreateSchedule") {
			if p[:4] == "kms:" {
				h.Errorf("a create without KmsKeyArn asked %s", p)
			}
		}
		for _, op := range []string{"CreateSchedule", "UpdateSchedule"} {
			g.reset()
			in := schedule("keyed", "rate(1 hour)", h.queueTarget("q", nil))
			in.KmsKeyArn = new(h.keyArn())
			if op == "CreateSchedule" {
				h.create(in)
			} else {
				must(h, schedulerapi.OpUpdateSchedule, update(in))
			}
			got := g.callerPairs(op)
			for _, a := range kms {
				if !slices.Contains(got, a+" "+h.keyArn()) {
					h.Errorf("%s with a key asked %v, missing %s on the key", op, got, a)
				}
			}
		}
		for _, denied := range kms {
			g.mu.Lock()
			g.deny = func(q question, action, _ string) bool { return q.caller() && action == denied }
			g.mu.Unlock()
			in := schedule("denied", "rate(1 hour)", h.queueTarget("q", nil))
			in.KmsKeyArn = new(h.keyArn())
			_, err := call(h, schedulerapi.OpCreateSchedule, in)
			wantError(h, err, "AccessDeniedException", scheduler.DenialStatus().Value, "")
			wantError(h, errOf(call(h, schedulerapi.OpGetSchedule, &schedulerapi.GetScheduleInput{Name: "denied"})), "ResourceNotFoundException", 404, "")
		}
	})
}

// KmsKeyArn at invocation: the role session's kms:Decrypt is evaluated
// before the target call (aws-doc encryption-rest; scheduler.md 9, 14.11);
// denied, the target is never called and the invocation is dead-lettered
// (spec "Enforce matrix" KMS row; scheduler.md 14.13.6).
func TestKmsDecryptDeniedAtInvocation(t *testing.T) {
	g := &gate{deny: func(q question, action, _ string) bool { return !q.caller() && action == "kms:Decrypt" }}
	runWith(t, withGate(g), func(h *harness) {
		in := h.deadLetterTo("keyed", "target", 0)
		in.KmsKeyArn = new(h.keyArn())
		h.create(in)
		h.advanceBy(30*time.Minute, time.Minute)
		if n := len(h.sentTo("target")); n != 0 {
			h.Errorf("the target received %d messages although the role may not decrypt", n)
		}
		if n := len(h.sentTo("dlq")); n != 1 {
			h.Errorf("dead letters = %d, want 1", n)
		}
		asked := false
		g.mu.Lock()
		for _, a := range g.asked {
			for _, p := range a.q.Pairs {
				if !a.caller() && p.Action.String() == "kms:Decrypt" && resourceOf(p.Resource) == h.keyArn() {
					asked = true
				}
			}
		}
		g.mu.Unlock()
		if !asked {
			h.Error("kms:Decrypt on the key was never asked for the role session")
		}
	})
}

// The role session's own target permission: denied sqs:SendMessage on the
// target queue is the target's refusal, never a delivery, and the
// invocation goes to the dead-letter queue (spec "Enforce matrix";
// scheduler.md 9, 14.11, 14.13.6).
func TestRoleSessionTargetDenied(t *testing.T) {
	g := &gate{}
	runWith(t, withGate(g), func(h *harness) {
		target := h.queueArn("target")
		g.mu.Lock()
		g.deny = func(q question, action, resource string) bool {
			_, role := q.principal.(identity.AssumedRole)
			return role && action == "sqs:SendMessage" && resource == target
		}
		g.mu.Unlock()
		h.create(h.deadLetterTo("denied", "target", 0))
		h.advanceBy(30*time.Minute, time.Minute)
		if n := len(h.sentTo("target")); n != 0 {
			h.Errorf("the target received %d messages although the role may not send", n)
		}
		dead := h.sentTo("dlq")
		if len(dead) != 1 {
			h.Fatalf("dead letters = %d, want 1", len(dead))
		}
		if _, present := attr(dead[0], "EXHAUSTED_RETRY_CONDITION"); present {
			h.Error("EXHAUSTED_RETRY_CONDITION present for an access denial, a permanent error")
		}
		wantSchedulerCall(h, dead[0])
	})
}
