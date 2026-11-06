package scheduler_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// roleTrustText is the captured refusal of an execution role Scheduler may
// not assume (scheduler.md 1 item 10, 2.4).
const roleTrustText = "The execution role you provide must allow AWS EventBridge Scheduler to assume the role."

// scheduler.amazonaws.com assuming the role, denied, answers the captured
// ValidationException at create and update and stores nothing; the group
// lookup comes first, so a missing group answers its own text (scheduler.md
// 14.9.3, UNVERIFIED SCH-04).
func TestRoleTrustRefusesTheWrite(t *testing.T) {
	g := &gate{deny: func(q question, action, _ string) bool {
		_, svc := q.principal.(identity.ServicePrincipal)
		return svc && action == "sts:AssumeRole"
	}}
	runWith(t, withGate(g), func(h *harness) {
		in := schedule("s", "rate(1 hour)", h.queueTarget("q", nil))
		_, err := call(h, schedulerapi.OpCreateSchedule, in)
		wantError(h, err, "ValidationException", 400, roleTrustText)
		wantError(h, errOf(call(h, schedulerapi.OpGetSchedule, &schedulerapi.GetScheduleInput{Name: "s"})), "ResourceNotFoundException", 404, "")

		in.GroupName = new("missing")
		_, err = call(h, schedulerapi.OpCreateSchedule, in)
		wantError(h, err, "ResourceNotFoundException", 404, "Schedule group missing does not exist.")

		g.mu.Lock()
		deny := g.deny
		g.deny = nil
		g.mu.Unlock()
		h.create(schedule("s", "rate(1 hour)", h.queueTarget("q", nil)))
		g.mu.Lock()
		g.deny = deny
		g.mu.Unlock()
		_, uerr := call(h, schedulerapi.OpUpdateSchedule, update(schedule("s", "rate(2 hours)", h.queueTarget("q", nil))))
		wantError(h, uerr, "ValidationException", 400, roleTrustText)
		if got := h.get("", "s").ScheduleExpression; got == nil || *got != "rate(1 hour)" {
			h.Errorf("a refused update changed the expression to %v", got)
		}
	})
}

// The trust question is scheduler.amazonaws.com's sts:AssumeRole on the
// role with aws:SourceArn the schedule group and aws:SourceAccount its
// account (aws-doc cross-service-confused-deputy-prevention; scheduler.md
// 9).
func TestRoleTrustAsksAsSchedulerFromTheGroup(t *testing.T) {
	g := &gate{}
	runWith(t, withGate(g), func(h *harness) {
		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g"})
		in := schedule("s", "rate(1 hour)", h.queueTarget("q", nil))
		in.GroupName = new("g")
		h.create(in)
		g.mu.Lock()
		defer g.mu.Unlock()
		for _, a := range g.asked {
			sp, ok := a.principal.(identity.ServicePrincipal)
			if !ok || a.op != "CreateSchedule" {
				continue
			}
			if sp.Name != "scheduler.amazonaws.com" || len(a.q.Pairs) != 1 || a.q.Pairs[0].Action.String() != "sts:AssumeRole" ||
				resourceOf(a.q.Pairs[0].Resource) != h.roleArn() {
				h.Errorf("trust question = %+v as %+v, want scheduler.amazonaws.com sts:AssumeRole on the role", a.q, sp)
			}
			group, err := aws.ParseARN(h.groupArn("g"))
			if err != nil {
				h.Fatal(err)
			}
			want := kernel.AsService{Name: "scheduler.amazonaws.com", Source: group, SourceAccount: h.Scope.Account}
			if a.mode != kernel.PrincipalMode(want) {
				h.Errorf("trust question asked in mode %#v, want %#v", a.mode, want)
			}
			return
		}
		h.Error("no trust question was asked at create")
	})
}
