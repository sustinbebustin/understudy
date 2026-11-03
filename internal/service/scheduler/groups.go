package scheduler

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/paging"
	"github.com/sustinbebustin/understudy/internal/store"
)

// scheduleGroup is one schedule group: its tags sorted by key and its
// creation and last modification instants (default reports none). A
// group is ACTIVE from the
// write that creates it to the write that deletes it, so it stores no
// state (scheduler.md 14.2.2, 14.7).
type scheduleGroup struct {
	Tags     []groupTag    `json:"tags,omitempty"`
	Created  clock.Instant `json:"created"`
	Modified clock.Instant `json:"modified"`
}

// groupTag is one tag of a schedule group.
type groupTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// maxGroups is the schedule groups a scope holds at most, the default
// group counted (aws-doc scheduler-quotas: 500 per account). UNVERIFIED
// (SCH-06), scheduler.md 10, 14.7.5: whether default counts is
// undocumented.
const maxGroups = 500

// listGroupsPage is ListScheduleGroups' page without MaxResults.
// UNVERIFIED (SCH-06), scheduler.md 3.2: AWS's default page is unrecorded.
const listGroupsPage = 100

// listingGroups names ListScheduleGroups' markers.
const listingGroups paging.Listing = "scheduler.ListScheduleGroups"

// hasGroup reports whether the scope holds the group name: default
// always, any other from CreateScheduleGroup to DeleteScheduleGroup
// (scheduler.md 14.7.1).
func (s *State) hasGroup(name string) bool {
	_, ok := s.Groups[name]
	return ok || name == defaultGroup
}

// group returns the group name, or the default group until a tag write
// materializes it (14.2.2); false for a group the scope does not hold.
func (s *State) group(name string) (scheduleGroup, bool) {
	if g, ok := s.Groups[name]; ok {
		return g, true
	}
	return scheduleGroup{}, name == defaultGroup
}

// groupView is a group as GetScheduleGroup and ListScheduleGroups read it.
type groupView struct {
	name string
	g    scheduleGroup
}

// readGroups returns the scope's groups, the default group included
// whether or not a write materialized it, filtered by keep.
func (s *Service) readGroups(c *kernel.Call, keep func(name string) bool) []groupView {
	st := func(st *State) []groupView {
		var out []groupView
		if _, stored := st.Groups[defaultGroup]; !stored && keep(defaultGroup) {
			out = append(out, groupView{name: defaultGroup})
		}
		for _, name := range slices.Sorted(maps.Keys(st.Groups)) {
			if keep(name) {
				out = append(out, groupView{name: name, g: st.Groups[name]})
			}
		}
		return out
	}
	if cell, ok := s.cells.Lookup(c.Scope()); ok {
		return store.Read(cell, st)
	}
	empty := newState(c.Scope())
	return st(&empty)
}

// createScheduleGroup creates an ACTIVE group with its tags, which
// GetScheduleGroup reads at once (scheduler.md 3.2, 14.7.2). default
// answers ValidationException (3.1), a held name ConflictException (3.2),
// and past maxGroups ServiceQuotaExceededException (14.7.5). ClientToken
// is accepted and not used for idempotency (SCH-02).
func (s *Service) createScheduleGroup(_ context.Context, c *kernel.Call, in *schedulerapi.CreateScheduleGroupInput) (*schedulerapi.CreateScheduleGroupOutput, *awserr.Error) {
	if in.Name == defaultGroup {
		return nil, errDefaultGroup()
	}
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *State, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if st.hasGroup(in.Name) {
			return struct{}{}, errGroupExists(in.Name)
		}
		held := len(st.Groups) + 1
		if _, stored := st.Groups[defaultGroup]; stored {
			held--
		}
		if held >= maxGroups {
			return struct{}{}, errGroupQuota()
		}
		st.Groups[in.Name] = scheduleGroup{Tags: upsertTags(nil, in.Tags), Created: now, Modified: now}
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &schedulerapi.CreateScheduleGroupOutput{ScheduleGroupArn: groupARN(c.Scope(), in.Name).String()}, nil
}

// getScheduleGroup reads a group: its ARN, name, ACTIVE and dates
// (scheduler.md 3.2); default reads with no ARN and no dates (SCH-06
// 06-get-default, recorded 2026-11-03; 3.1). A group the scope does not
// hold answers "Schedule group <name> does not exist." (aws-doc, 2.4).
func (s *Service) getScheduleGroup(_ context.Context, c *kernel.Call, in *schedulerapi.GetScheduleGroupInput) (*schedulerapi.GetScheduleGroupOutput, *awserr.Error) {
	views := s.readGroups(c, func(name string) bool { return name == in.Name })
	if len(views) == 0 {
		return nil, errGroupMissing(in.Name)
	}
	v := groupSummary(c, views[0])
	c.Touch(groupARN(c.Scope(), in.Name))
	out := &schedulerapi.GetScheduleGroupOutput{
		Arn: v.Arn, Name: v.Name, State: v.State, CreationDate: v.CreationDate, LastModificationDate: v.LastModificationDate,
	}
	if in.Name == defaultGroup {
		out.Arn = nil
	}
	return out, nil
}

// listScheduleGroups lists the scope's groups by NamePrefix, a page at a
// time: default first, then the others in name order. UNVERIFIED
// (SCH-06), scheduler.md 3.1, 3.2: whether default is listed, the order,
// the page size and the token format are unrecorded.
func (s *Service) listScheduleGroups(_ context.Context, c *kernel.Call, in *schedulerapi.ListScheduleGroupsInput) (*schedulerapi.ListScheduleGroupsOutput, *awserr.Error) {
	views := s.readGroups(c, func(name string) bool { return in.NamePrefix == nil || strings.HasPrefix(name, *in.NamePrefix) })
	items := make([]schedulerapi.ScheduleGroupSummary, 0, len(views))
	for _, v := range views {
		items = append(items, groupSummary(c, v))
	}
	n := listGroupsPage
	if in.MaxResults != nil {
		n = int(*in.MaxResults)
	}
	p, perr := paging.SortedKeyset(listingGroups, items, groupKey, n, in.NextToken)
	if perr != nil {
		return nil, errBadNextToken("ListScheduleGroups")
	}
	out := &schedulerapi.ListScheduleGroupsOutput{ScheduleGroups: make([]schedulerapi.ScheduleGroupSummary, 0, len(p.Items)), NextToken: p.Next}
	out.ScheduleGroups = append(out.ScheduleGroups, p.Items...)
	return out, nil
}

// groupKey orders groups default first, then by name: the empty key sorts
// before every name the model admits ([0-9a-zA-Z-_.]+).
func groupKey(g schedulerapi.ScheduleGroupSummary) string {
	if g.Name == nil || *g.Name == defaultGroup {
		return ""
	}
	return *g.Name
}

// groupSummary is a group's read shape; default has no dates (SCH-06
// 06-list, recorded 2026-11-03; scheduler.md 3.1).
func groupSummary(c *kernel.Call, v groupView) schedulerapi.ScheduleGroupSummary {
	arn := groupARN(c.Scope(), v.name).String()
	name := v.name
	state := schedulerapi.ScheduleGroupStateActive
	out := schedulerapi.ScheduleGroupSummary{Arn: &arn, Name: &name, State: &state}
	if v.name != defaultGroup {
		created, modified := v.g.Created.Wire(), v.g.Modified.Wire()
		out.CreationDate, out.LastModificationDate = &created, &modified
	}
	return out
}

// deleteScheduleGroup deletes a group, its tags and every schedule in it,
// disarming their fires, in one write, and journals each schedule's
// deletion; the group is gone when the call returns, so DELETING is never
// reported (scheduler.md 3.3, 14.7.3; ARCHITECTURE.md 9). default answers
// ValidationException (3.1). ClientToken is accepted and not used for
// idempotency (SCH-02).
func (s *Service) deleteScheduleGroup(ctx context.Context, c *kernel.Call, in *schedulerapi.DeleteScheduleGroupInput) (*schedulerapi.DeleteScheduleGroupOutput, *awserr.Error) {
	if in.Name == defaultGroup {
		return nil, errDefaultGroup()
	}
	if err := s.authorizeMembers(ctx, c, in.Name); err != nil {
		return nil, err
	}
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if _, ok := st.Groups[in.Name]; !ok {
			return struct{}{}, errGroupMissing(in.Name)
		}
		members := st.Schedules[in.Name]
		for _, name := range slices.Sorted(maps.Keys(members)) {
			r := scheduleRef{scope: c.Scope(), group: in.Name, name: name}
			sc := members[name]
			tx.Disarm(s.fire, r.arn().String())
			tx.Record(r.scope, journal.StateChange{Resource: r.arn(), From: journal.ResourceState(sc.Echo.state()), To: scheduleDeleted})
		}
		delete(st.Schedules, in.Name)
		delete(st.Groups, in.Name)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &schedulerapi.DeleteScheduleGroupOutput{}, nil
}

// tagResource adds or replaces tags on a group (scheduler.md 3.4, 14.8).
// The first tag write to default materializes it with its origin dates.
func (s *Service) tagResource(_ context.Context, c *kernel.Call, in *schedulerapi.TagResourceInput) (*schedulerapi.TagResourceOutput, *awserr.Error) {
	err := s.retag(c, in.ResourceArn, func(tags []groupTag) []groupTag { return upsertTags(tags, in.Tags) })
	if err != nil {
		return nil, err
	}
	return &schedulerapi.TagResourceOutput{}, nil
}

// untagResource removes tags from a group; a key it does not carry is
// ignored (scheduler.md 14.8). UNVERIFIED (SCH-06).
func (s *Service) untagResource(_ context.Context, c *kernel.Call, in *schedulerapi.UntagResourceInput) (*schedulerapi.UntagResourceOutput, *awserr.Error) {
	err := s.retag(c, in.ResourceArn, func(tags []groupTag) []groupTag {
		return slices.DeleteFunc(slices.Clone(tags), func(t groupTag) bool { return slices.Contains(in.TagKeys, t.Key) })
	})
	if err != nil {
		return nil, err
	}
	return &schedulerapi.UntagResourceOutput{}, nil
}

// retag stores change applied to the tags of the group resource names,
// materializing default (scheduler.md 14.2.2). A tag write leaves
// LastModificationDate as it was.
func (s *Service) retag(c *kernel.Call, resource string, change func([]groupTag) []groupTag) *awserr.Error {
	name, inScope := parseGroupARN(c.Scope(), resource)
	if !inScope {
		return errGroupMissing(name)
	}
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		g, ok := st.group(name)
		if !ok {
			return struct{}{}, errGroupMissing(name)
		}
		tags := change(g.Tags)
		if _, stored := st.Groups[name]; stored && slices.Equal(tags, g.Tags) {
			tx.Unchanged()
			return struct{}{}, nil
		}
		g.Tags = tags
		st.Groups[name] = g
		return struct{}{}, nil
	})
	return err
}

// listTagsForResource lists a group's tags in insertion order, a present
// empty list when it has none (G1, G2; scheduler.md 3.4, 14.8).
func (s *Service) listTagsForResource(_ context.Context, c *kernel.Call, in *schedulerapi.ListTagsForResourceInput) (*schedulerapi.ListTagsForResourceOutput, *awserr.Error) {
	name, inScope := parseGroupARN(c.Scope(), in.ResourceArn)
	views := s.readGroups(c, func(n string) bool { return inScope && n == name })
	if len(views) == 0 {
		return nil, errGroupMissing(name)
	}
	out := &schedulerapi.ListTagsForResourceOutput{Tags: make([]schedulerapi.Tag, 0, len(views[0].g.Tags))}
	for _, t := range views[0].g.Tags {
		out.Tags = append(out.Tags, schedulerapi.Tag{Key: t.Key, Value: t.Value})
	}
	return out, nil
}

// upsertTags returns tags with each of add set, sorted by key, as
// ListTagsForResource lists them (SCH-06 06-tags, 06-tags-2, recorded
// 2026-11-03; scheduler.md 3.4).
func upsertTags(tags []groupTag, add []schedulerapi.Tag) []groupTag {
	out := slices.Clone(tags)
	for _, t := range add {
		if i := slices.IndexFunc(out, func(h groupTag) bool { return h.Key == t.Key }); i >= 0 {
			out[i].Value = t.Value
			continue
		}
		out = append(out, groupTag{Key: t.Key, Value: t.Value})
	}
	slices.SortFunc(out, func(a, b groupTag) int { return strings.Compare(a.Key, b.Key) })
	return out
}
