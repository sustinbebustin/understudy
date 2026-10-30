package scheduler_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/journal"
)

// A created group returns its ARN schedule-group/<name> and reads ACTIVE
// at once with Arn, Name and both dates (model; tf-acc
// schedule_group_test.go:L25-L72; aws-doc managing-schedule-group;
// scheduler.md 3.2, 14.7.2). default exists and reads ACTIVE (aws-doc
// managing-schedule-group: "Your AWS account comes with a default
// scheduler group"; scheduler.md 3.1).
func TestScheduleGroupLifecycle(t *testing.T) {
	run(t, func(h *harness) {
		out := must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g"})
		if out.ScheduleGroupArn != h.groupArn("g") {
			h.Errorf("ScheduleGroupArn = %s, want %s", out.ScheduleGroupArn, h.groupArn("g"))
		}
		for _, name := range []string{"g", "default"} {
			got := must(h, schedulerapi.OpGetScheduleGroup, &schedulerapi.GetScheduleGroupInput{Name: name})
			if val(got.Arn) != h.groupArn(name) || val(got.Name) != name || got.State == nil || *got.State != schedulerapi.ScheduleGroupStateActive ||
				got.CreationDate == nil || got.LastModificationDate == nil {
				h.Errorf("GetScheduleGroup(%s) = %+v, want its ARN, name, ACTIVE and both dates", name, got)
			}
		}
		listed := map[string]bool{}
		for _, g := range must(h, schedulerapi.OpListScheduleGroups, &schedulerapi.ListScheduleGroupsInput{NamePrefix: new("g")}).ScheduleGroups {
			listed[val(g.Name)] = true
			if g.State == nil || *g.State != schedulerapi.ScheduleGroupStateActive || val(g.Arn) != h.groupArn(val(g.Name)) {
				h.Errorf("summary %+v, want ACTIVE with its ARN", g)
			}
		}
		if !listed["g"] {
			h.Errorf("ListScheduleGroups(prefix g) = %v, want g", listed)
		}
	})
}

// A missing group answers ResourceNotFoundException "Schedule group <name>
// does not exist." (aws-doc, CLI output on
// https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-schedule-group-delete.html;
// scheduler.md 2.4, 3.3, 14.7.4); CreateSchedule into it answers
// ResourceNotFoundException (model).
func TestMissingScheduleGroup(t *testing.T) {
	run(t, func(h *harness) {
		wantError(h, errOf(call(h, schedulerapi.OpGetScheduleGroup, &schedulerapi.GetScheduleGroupInput{Name: "TestGroup"})),
			"ResourceNotFoundException", 404, "Schedule group TestGroup does not exist.")
		wantError(h, errOf(call(h, schedulerapi.OpDeleteScheduleGroup, &schedulerapi.DeleteScheduleGroupInput{Name: "TestGroup"})),
			"ResourceNotFoundException", 404, "Schedule group TestGroup does not exist.")
		in := schedule("s", "rate(1 hour)", h.queueTarget("q", nil))
		in.GroupName = new("TestGroup")
		wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, in)), "ResourceNotFoundException", 404, "")
	})
}

// Deleting a group deletes every schedule in it, and the group is gone
// when the call returns (model #DeleteScheduleGroup; aws-doc
// DeleteScheduleGroup; scheduler.md 3.3, 14.7.3): its schedules no longer
// read, no longer fire, and their deletion is journaled; schedules in
// other groups stay.
func TestDeleteScheduleGroupDeletesItsSchedules(t *testing.T) {
	run(t, func(h *harness) {
		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g"})
		for _, name := range []string{"a", "b"} {
			in := schedule(name, "rate(1 hour)", h.queueTarget("in-g", nil))
			in.GroupName = new("g")
			in.StartDate = new(h.now().Add(30 * time.Minute))
			h.create(in)
		}
		keep := schedule("a", "rate(1 hour)", h.queueTarget("kept", nil))
		keep.StartDate = new(h.now().Add(30 * time.Minute))
		h.create(keep)
		before := len(h.Journal.Query(journal.Filter{Limit: 10000}).Events)

		must(h, schedulerapi.OpDeleteScheduleGroup, &schedulerapi.DeleteScheduleGroupInput{Name: "g"})
		wantError(h, errOf(call(h, schedulerapi.OpGetScheduleGroup, &schedulerapi.GetScheduleGroupInput{Name: "g"})),
			"ResourceNotFoundException", 404, "Schedule group g does not exist.")
		for _, name := range []string{"a", "b"} {
			wantError(h, errOf(call(h, schedulerapi.OpGetSchedule, &schedulerapi.GetScheduleInput{Name: name, GroupName: new("g")})), "ResourceNotFoundException", 404, "")
		}
		h.get("", "a")
		if after := len(h.Journal.Query(journal.Filter{Limit: 10000}).Events); after-before < 3 {
			h.Errorf("the group delete journaled %d events, want at least the group and one per schedule", after-before)
		}
		h.advanceBy(2*time.Hour, time.Hour)
		if n := len(h.sentTo("in-g")); n != 0 {
			h.Errorf("deleted schedules sent %d messages, want none", n)
		}
		if n := len(h.sentTo("kept")); n == 0 {
			h.Error("the schedule in default stopped firing")
		}
	})
}

// G1: tags added with TagResource are listed back (third-party-recorded,
// localstack@8b9a79f
// tests/aws/services/scheduler/test_scheduler.snapshot.json:L2-L18,
// validated 2024-12-04). Tags passed at create are listed back (tf-acc
// schedule_group_test.go:L174-L232). scheduler.md 3.4.
func TestTagResourceThenList(t *testing.T) {
	run(t, func(h *harness) {
		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g"})
		must(h, schedulerapi.OpTagResource, &schedulerapi.TagResourceInput{ResourceArn: h.groupArn("g"), Tags: []schedulerapi.Tag{{Key: "TagKey", Value: "TagValue"}}})
		got := must(h, schedulerapi.OpListTagsForResource, &schedulerapi.ListTagsForResourceInput{ResourceArn: h.groupArn("g")}).Tags
		if !slices.Equal(got, []schedulerapi.Tag{{Key: "TagKey", Value: "TagValue"}}) {
			h.Errorf("tags = %+v, want TagKey=TagValue", got)
		}

		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "tagged", Tags: []schedulerapi.Tag{{Key: "k1", Value: "v1"}, {Key: "k2", Value: "v2"}}})
		created := must(h, schedulerapi.OpListTagsForResource, &schedulerapi.ListTagsForResourceInput{ResourceArn: h.groupArn("tagged")}).Tags
		byKey := map[string]string{}
		for _, tg := range created {
			byKey[tg.Key] = tg.Value
		}
		if len(created) != 2 || byKey["k1"] != "v1" || byKey["k2"] != "v2" {
			h.Errorf("create tags listed = %+v, want k1=v1, k2=v2", created)
		}
	})
}

// G2: a group created with one tag, then untagged, lists "Tags": [], a
// present empty list (third-party-recorded, same file L19-L30, validated
// 2024-12-04; scheduler.md 3.4, 14.8).
func TestUntagLeavesAnEmptyList(t *testing.T) {
	run(t, func(h *harness) {
		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g", Tags: []schedulerapi.Tag{{Key: "TagKey", Value: "TagValue"}}})
		must(h, schedulerapi.OpUntagResource, &schedulerapi.UntagResourceInput{ResourceArn: h.groupArn("g"), TagKeys: []string{"TagKey"}})
		got := must(h, schedulerapi.OpListTagsForResource, &schedulerapi.ListTagsForResourceInput{ResourceArn: h.groupArn("g")}).Tags
		if got == nil || len(got) != 0 {
			h.Errorf("Tags = %#v, want a present empty list", got)
		}
	})
}

// Tags exist only on schedule groups: a schedule ARN fails the
// TagResourceArn pattern (model #TagResourceArn; aws-doc TagResource;
// scheduler.md 2.3, 3.4).
func TestTagsRefuseScheduleArns(t *testing.T) {
	run(t, func(h *harness) {
		arn := h.create(schedule("s", "rate(1 hour)", h.queueTarget("q", nil)))
		wantError(h, errOf(call(h, schedulerapi.OpTagResource, &schedulerapi.TagResourceInput{ResourceArn: arn, Tags: []schedulerapi.Tag{{Key: "k", Value: "v"}}})),
			"ValidationException", 400, "")
		wantError(h, errOf(call(h, schedulerapi.OpListTagsForResource, &schedulerapi.ListTagsForResourceInput{ResourceArn: arn})),
			"ValidationException", 400, "")
	})
}

// The account holds at most 500 schedule groups (aws-doc
// https://docs.aws.amazon.com/scheduler/latest/UserGuide/scheduler-quotas.html);
// past it CreateScheduleGroup answers ServiceQuotaExceededException at 402
// (model; scheduler.md 2.4, 10, 14.7.5). Whether default counts is not
// documented, so 499 creates must pass and the 501st must fail.
func TestScheduleGroupQuota(t *testing.T) {
	run(t, func(h *harness) {
		var refused int
		for i := range 501 {
			_, err := call(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: fmt.Sprintf("g-%03d", i)})
			switch {
			case err == nil:
			case i < 499:
				h.Fatalf("group %d refused: %s %d %s", i+1, err.ID.Shape.Name(), err.ID.Status, err.Message)
			default:
				wantError(h, err, "ServiceQuotaExceededException", 402, "")
				refused++
			}
		}
		if refused == 0 {
			h.Error("501 groups created, want the 500-group quota to refuse one")
		}
	})
}
