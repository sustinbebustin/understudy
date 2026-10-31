package scheduler_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// getGroup reads a group's dates.
func getGroup(h *harness, name string) (created, modified time.Time) {
	out := must(h, schedulerapi.OpGetScheduleGroup, &schedulerapi.GetScheduleGroupInput{Name: name})
	return *out.CreationDate, *out.LastModificationDate
}

// listTags lists a group's tags.
func listTags(h *harness, name string) []schedulerapi.Tag {
	return must(h, schedulerapi.OpListTagsForResource, &schedulerapi.ListTagsForResourceInput{ResourceArn: h.groupArn(name)}).Tags
}

// The default group cannot be created or deleted; a held name cannot be
// created again. UNVERIFIED (SCH-06), scheduler.md 3.1, 14.7.1: the codes
// and texts are inferred.
func TestDefaultAndDuplicateGroupRefusals(t *testing.T) {
	run(t, func(h *harness) {
		wantError(h, errOf(call(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "default"})),
			"ConflictException", 409, "Schedule group default already exists: every account has it, and it cannot be created or deleted.")
		wantError(h, errOf(call(h, schedulerapi.OpDeleteScheduleGroup, &schedulerapi.DeleteScheduleGroupInput{Name: "default"})),
			"ValidationException", 400, "The default schedule group cannot be deleted; delete its schedules with DeleteSchedule instead.")
		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g"})
		wantError(h, errOf(call(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g"})),
			"ConflictException", 409, "Schedule group g already exists.")
	})
}

// The quota counts default: 499 created groups fill it, and a delete
// makes room. UNVERIFIED (SCH-06), scheduler.md 10, 14.7.5.
func TestGroupQuotaCountsDefault(t *testing.T) {
	run(t, func(h *harness) {
		for i := range 499 {
			must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: fmt.Sprintf("g-%03d", i)})
		}
		wantError(h, errOf(call(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "over"})),
			"ServiceQuotaExceededException", 402, "The account holds 500 schedule groups in this Region, the default group included, "+
				"which is the quota; delete a schedule group with DeleteScheduleGroup before creating another.")
		must(h, schedulerapi.OpDeleteScheduleGroup, &schedulerapi.DeleteScheduleGroupInput{Name: "g-000"})
		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "over"})
	})
}

// ListScheduleGroups lists default first, then by name, across pages.
// UNVERIFIED (SCH-06), scheduler.md 3.1, 3.2.
func TestListScheduleGroupsOrderAndPages(t *testing.T) {
	run(t, func(h *harness) {
		for _, name := range []string{"b", "a", "d-x"} {
			must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: name})
		}
		list := func(in schedulerapi.ListScheduleGroupsInput) []string {
			var names []string
			for {
				out := must(h, schedulerapi.OpListScheduleGroups, &in)
				for _, g := range out.ScheduleGroups {
					names = append(names, val(g.Name))
				}
				if out.NextToken == nil {
					return names
				}
				in.NextToken = out.NextToken
			}
		}
		if got := list(schedulerapi.ListScheduleGroupsInput{}); !slices.Equal(got, []string{"default", "a", "b", "d-x"}) {
			h.Errorf("ListScheduleGroups = %v, want default, a, b, d-x", got)
		}
		if got := list(schedulerapi.ListScheduleGroupsInput{MaxResults: new(int32(1))}); !slices.Equal(got, []string{"default", "a", "b", "d-x"}) {
			h.Errorf("ListScheduleGroups by one = %v, want default, a, b, d-x", got)
		}
		if got := list(schedulerapi.ListScheduleGroupsInput{NamePrefix: new("d")}); !slices.Equal(got, []string{"default", "d-x"}) {
			h.Errorf("ListScheduleGroups prefix d = %v, want default, d-x", got)
		}
		wantError(h, errOf(call(h, schedulerapi.OpListScheduleGroups, &schedulerapi.ListScheduleGroupsInput{NextToken: new("bogus")})), "ValidationException", 400, "")
	})
}

// The default group is dated at the instant the state was created, loaded
// or reset until a tag write materializes it, which keeps those dates
// across a snapshot. UNVERIFIED (SCH-06), scheduler.md 3.1, 14.2.2.
func TestDefaultGroupDates(t *testing.T) {
	run(t, func(h *harness) {
		born := h.now()
		h.advance(time.Hour)
		if c, m := getGroup(h, "default"); !c.Equal(born) || !m.Equal(born) {
			h.Errorf("default dates = %s, %s, want the state's creation %s", c, m, born)
		}
		h.svc.Reset(kernel.ResetScope{})
		reset := h.now()
		if c, _ := getGroup(h, "default"); !c.Equal(reset) {
			h.Errorf("default created after reset = %s, want %s", c, reset)
		}
		must(h, schedulerapi.OpTagResource, &schedulerapi.TagResourceInput{ResourceArn: h.groupArn("default"), Tags: []schedulerapi.Tag{{Key: "k", Value: "v"}}})
		sections, err := h.svc.Export()
		if err != nil || len(sections) == 0 {
			h.Fatalf("Export = %d sections, %v; want the materialized default", len(sections), err)
		}
		h.advance(time.Hour)
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		if c, m := getGroup(h, "default"); !c.Equal(reset) || !m.Equal(reset) {
			h.Errorf("materialized default dates after import = %s, %s, want %s", c, m, reset)
		}
		if got := listTags(h, "default"); !slices.Equal(got, []schedulerapi.Tag{{Key: "k", Value: "v"}}) {
			h.Errorf("default tags after import = %+v", got)
		}
	})
}

// Tags keep insertion order, an updated key keeps its place, and untagging
// a key the group does not carry answers 200. UNVERIFIED (SCH-06),
// scheduler.md 14.8.
func TestTagOrderAndAbsentKeys(t *testing.T) {
	run(t, func(h *harness) {
		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g", Tags: []schedulerapi.Tag{{Key: "k1", Value: "v1"}, {Key: "k2", Value: "v2"}}})
		must(h, schedulerapi.OpTagResource, &schedulerapi.TagResourceInput{ResourceArn: h.groupArn("g"), Tags: []schedulerapi.Tag{{Key: "k3", Value: "v3"}, {Key: "k1", Value: "new"}}})
		want := []schedulerapi.Tag{{Key: "k1", Value: "new"}, {Key: "k2", Value: "v2"}, {Key: "k3", Value: "v3"}}
		if got := listTags(h, "g"); !slices.Equal(got, want) {
			h.Errorf("tags = %+v, want %+v", got, want)
		}
		must(h, schedulerapi.OpUntagResource, &schedulerapi.UntagResourceInput{ResourceArn: h.groupArn("g"), TagKeys: []string{"absent", "k2"}})
		if got := listTags(h, "g"); !slices.Equal(got, []schedulerapi.Tag{{Key: "k1", Value: "new"}, {Key: "k3", Value: "v3"}}) {
			h.Errorf("tags after untag = %+v", got)
		}
	})
}

// A tag call on a group the scope does not hold, or on another account's
// group, answers the missing-group text (aws-doc, scheduler.md 2.4; reuse
// inferred, SCH-06).
func TestTagsOnMissingGroups(t *testing.T) {
	run(t, func(h *harness) {
		wantError(h, errOf(call(h, schedulerapi.OpTagResource, &schedulerapi.TagResourceInput{ResourceArn: h.groupArn("nope"), Tags: []schedulerapi.Tag{{Key: "k", Value: "v"}}})),
			"ResourceNotFoundException", 404, "Schedule group nope does not exist.")
		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g"})
		other := "arn:aws:scheduler:us-east-1:111111111111:schedule-group/g"
		wantError(h, errOf(call(h, schedulerapi.OpListTagsForResource, &schedulerapi.ListTagsForResourceInput{ResourceArn: other})),
			"ResourceNotFoundException", 404, "Schedule group g does not exist.")
		wantError(h, errOf(call(h, schedulerapi.OpUntagResource, &schedulerapi.UntagResourceInput{ResourceArn: other, TagKeys: []string{"k"}})),
			"ResourceNotFoundException", 404, "Schedule group g does not exist.")
	})
}

// Schedule operations naming a missing group answer the missing-group
// text, after the members are validated (scheduler.md 14.7.4, 14.9.3);
// a deleted group's members journal one state change each.
func TestMissingGroupOnScheduleOperations(t *testing.T) {
	run(t, func(h *harness) {
		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g"})
		on := schedule("on", "rate(1 hour)", h.queueTarget("q", nil))
		on.GroupName = new("g")
		h.create(on)
		off := schedule("off", "rate(1 hour)", h.queueTarget("q", nil))
		off.GroupName, off.State = new("g"), new(schedulerapi.ScheduleStateDisabled)
		h.create(off)
		must(h, schedulerapi.OpDeleteScheduleGroup, &schedulerapi.DeleteScheduleGroupInput{Name: "g"})

		changes := map[string]journal.StateChange{}
		for _, ev := range h.Journal.Query(journal.Filter{Kind: journal.KindStateChange}).Events {
			if sc, ok := ev.Detail.(journal.StateChange); ok {
				changes[sc.Resource.String()] = sc
			}
		}
		if c := changes[h.scheduleArn("g", "on")]; c.From != "ENABLED" || c.To != "DELETED" {
			h.Errorf("on's state change = %+v, want ENABLED to DELETED", c)
		}
		if c := changes[h.scheduleArn("g", "off")]; c.From != "DISABLED" || c.To != "DELETED" {
			h.Errorf("off's state change = %+v, want DISABLED to DELETED", c)
		}

		const missing = "Schedule group g does not exist."
		wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, on)), "ResourceNotFoundException", 404, missing)
		wantError(h, errOf(call(h, schedulerapi.OpUpdateSchedule, update(on))), "ResourceNotFoundException", 404, missing)
		wantError(h, errOf(call(h, schedulerapi.OpListSchedules, &schedulerapi.ListSchedulesInput{GroupName: new("g")})), "ResourceNotFoundException", 404, missing)
		bad := schedule("bad", "rate(0 hours)", h.queueTarget("q", nil))
		bad.GroupName = new("g")
		wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, bad)), "ValidationException", 400, "")
	})
}

// Resources lists created groups beside the schedules.
func TestResourcesListGroups(t *testing.T) {
	run(t, func(h *harness) {
		must(h, schedulerapi.OpCreateScheduleGroup, &schedulerapi.CreateScheduleGroupInput{Name: "g"})
		resources := h.svc.Resources(h.Scope)
		arns := make([]string, 0, len(resources))
		for _, r := range resources {
			arns = append(arns, r.ARN.String())
		}
		if !slices.Contains(arns, h.groupArn("g")) || slices.Contains(arns, h.groupArn("default")) {
			h.Errorf("Resources = %v, want g and no unmaterialized default", arns)
		}
	})
}
