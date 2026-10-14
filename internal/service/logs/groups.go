package logs

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Limits of secrets-logs-events-alarms.md 3.7 and 3.8.
const (
	maxGroups        = 1_000_000
	maxTags          = 50
	defaultListLimit = 50
)

// bindGroups binds the log group control plane, retention and tags.
func (s *Service) bindGroups(b logsapi.Binder) {
	b.CreateLogGroup(s.createLogGroup,
		registry.Partial{Note: "kmsKeyId must name an enabled KMS key and is stored and echoed; events are not encrypted with it"},
		kernel.WithCheck(checkCreateLogGroup),
		groupNamed(func(in *logsapi.CreateLogGroupInput) string { return in.LogGroupName }))
	b.DeleteLogGroup(s.deleteLogGroup, registry.Full{},
		groupNamed(func(in *logsapi.DeleteLogGroupInput) string { return in.LogGroupName }))
	b.PutLogGroupDeletionProtection(s.putLogGroupDeletionProtection, registry.Full{},
		groupIdentified(
			func(in *logsapi.PutLogGroupDeletionProtectionInput) (*string, *string) {
				return nil, &in.LogGroupIdentifier
			},
			func(ref groupRef, _ *logsapi.PutLogGroupDeletionProtectionInput) aws.ARN { return ref.arn() }))
	b.DescribeLogGroups(s.describeLogGroups,
		registry.Partial{Note: "no linked accounts: accountIdentifiers and includeLinkedAccounts select nothing more"},
		kernel.WithCheck(checkDescribeLogGroups))
	b.PutRetentionPolicy(s.putRetentionPolicy,
		registry.Partial{Note: "events expire exactly at timestamp plus retention on the emulator clock, not lazily within 72 hours"},
		kernel.WithCheck(checkPutRetentionPolicy),
		groupNamed(func(in *logsapi.PutRetentionPolicyInput) string { return in.LogGroupName }))
	b.DeleteRetentionPolicy(s.deleteRetentionPolicy, registry.Full{},
		groupNamed(func(in *logsapi.DeleteRetentionPolicyInput) string { return in.LogGroupName }))
	b.TagResource(s.tagResource, registry.Partial{Note: "log groups only; destination and delivery ARNs name no resource"},
		groupTagged(func(in *logsapi.TagResourceInput) string { return in.ResourceArn }))
	b.UntagResource(s.untagResource, registry.Partial{Note: "log groups only; destination and delivery ARNs name no resource"},
		groupTagged(func(in *logsapi.UntagResourceInput) string { return in.ResourceArn }))
	b.ListTagsForResource(s.listTagsForResource, registry.Partial{Note: "log groups only; destination and delivery ARNs name no resource"},
		groupTagged(func(in *logsapi.ListTagsForResourceInput) string { return in.ResourceArn }))
	b.TagLogGroup(s.tagLogGroup, registry.Full{},
		groupNamed(func(in *logsapi.TagLogGroupInput) string { return in.LogGroupName }))
	b.UntagLogGroup(s.untagLogGroup, registry.Full{},
		groupNamed(func(in *logsapi.UntagLogGroupInput) string { return in.LogGroupName }))
	b.ListTagsLogGroup(s.listTagsLogGroup, registry.Full{},
		groupNamed(func(in *logsapi.ListTagsLogGroupInput) string { return in.LogGroupName }))
}

// groupNamed names the group an input addresses by logGroupName as the
// call's resource, before fault rules run.
func groupNamed[In any](nameOf func(*In) string) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		return []aws.ARN{named(c, nameOf(in)).arn()}
	})
}

// createLogGroup creates a group; its class defaults to STANDARD. A
// DELIVERY group gets the class's fixed retention, and a kmsKeyId must name
// an enabled key.
func (s *Service) createLogGroup(ctx context.Context, c *kernel.Call, in *logsapi.CreateLogGroupInput) (*logsapi.CreateLogGroupOutput, *awserr.Error) {
	ref := named(c, in.LogGroupName)
	g := Group{
		Name:  ref.name,
		Class: string(logsapi.LogGroupClassStandard),
		Tags:  maps.Clone(in.Tags),
	}
	if in.LogGroupClass != nil {
		g.Class = string(*in.LogGroupClass)
	}
	if g.Class == string(logsapi.LogGroupClassDelivery) {
		g.RetentionDays = deliveryRetention().Value
	}
	if in.KmsKeyId != nil {
		if err := s.checkKey(ctx, c, *in.KmsKeyId); err != nil {
			return nil, err
		}
		g.KMSKeyID = *in.KmsKeyId
	}
	if in.DeletionProtectionEnabled != nil {
		g.DeletionProtection = *in.DeletionProtectionEnabled
	}
	_, err := kernel.Write(c, s.cells, ref.scope, func(st *State, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if _, ok := st.Groups[ref.name]; ok {
			return struct{}{}, errGroupExists()
		}
		if len(st.Groups) >= maxGroups {
			return struct{}{}, errGroupLimit()
		}
		g.Created = now
		st.Groups[ref.name] = g
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &logsapi.CreateLogGroupOutput{}, nil
}

// deleteLogGroup deletes a group with its streams, events and retention
// timer.
func (s *Service) deleteLogGroup(_ context.Context, c *kernel.Call, in *logsapi.DeleteLogGroupInput) (*logsapi.DeleteLogGroupOutput, *awserr.Error) {
	ref := named(c, in.LogGroupName)
	if !s.exists(ref) {
		return nil, errGroupNotFound()
	}
	_, err := kernel.Write(c, s.cells, ref.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		g, ok := st.Groups[ref.name]
		if !ok {
			return struct{}{}, errGroupNotFound()
		}
		if g.DeletionProtection {
			return struct{}{}, errDeletionProtected()
		}
		delete(st.Groups, ref.name)
		tx.Disarm(s.retention, ref.arn().String())
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &logsapi.DeleteLogGroupOutput{}, nil
}

// putLogGroupDeletionProtection turns a group's deletion protection on or
// off.
func (s *Service) putLogGroupDeletionProtection(_ context.Context, c *kernel.Call, in *logsapi.PutLogGroupDeletionProtectionInput) (*logsapi.PutLogGroupDeletionProtectionOutput, *awserr.Error) {
	ref, err := resolveIdentifier(c, in.LogGroupIdentifier)
	if err != nil {
		return nil, err
	}
	err = s.updateGroup(c, ref, func(g *Group, _ clock.Instant, _ *kernel.Tx) *awserr.Error {
		g.DeletionProtection = in.DeletionProtectionEnabled
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &logsapi.PutLogGroupDeletionProtectionOutput{}, nil
}

// describeLogGroups lists the scope's groups by name.
func (s *Service) describeLogGroups(_ context.Context, c *kernel.Call, in *logsapi.DescribeLogGroupsInput) (*logsapi.DescribeLogGroupsOutput, *awserr.Error) {
	scope := c.Scope()
	fields := allGroupFields
	if in.LogGroupNamePattern != nil {
		fields = patternGroupFields
	}
	var items []keyed[logsapi.LogGroup]
	if cell, ok := s.cells.Lookup(scope); ok {
		items = store.Read(cell, func(st *State) []keyed[logsapi.LogGroup] {
			var out []keyed[logsapi.LogGroup]
			for name, g := range st.Groups {
				ref := groupRef{scope: scope, name: name}
				if listsGroup(in, ref, &g) {
					out = append(out, keyed[logsapi.LogGroup]{key: name, item: describeGroup(ref, &g, fields)})
				}
			}
			return out
		})
	}
	limit := defaultListLimit
	if in.Limit != nil {
		limit = int(*in.Limit)
	}
	page, next, ok := pageOf(items, groupListing(scope), false, in.NextToken, limit, c.Admitted())
	if !ok {
		return nil, errInvalidToken()
	}
	if page == nil {
		page = []logsapi.LogGroup{}
	}
	return &logsapi.DescribeLogGroupsOutput{LogGroups: page, NextToken: next}, nil
}

// listsGroup reports whether DescribeLogGroups' filters select the group.
func listsGroup(in *logsapi.DescribeLogGroupsInput, ref groupRef, g *Group) bool {
	switch {
	case in.LogGroupNamePrefix != nil && !strings.HasPrefix(g.Name, *in.LogGroupNamePrefix):
		return false
	case in.LogGroupNamePattern != nil && !strings.Contains(g.Name, *in.LogGroupNamePattern):
		return false
	case in.LogGroupClass != nil && string(*in.LogGroupClass) != g.Class:
		return false
	case len(in.LogGroupIdentifiers) > 0:
		return slices.ContainsFunc(in.LogGroupIdentifiers, func(id string) bool {
			return id == g.Name || id == ref.arn().String()
		})
	}
	return true
}

// groupFields is which LogGroup fields a DescribeLogGroups call returns.
type groupFields uint8

const (
	// allGroupFields is every stored field, for a call without
	// logGroupNamePattern.
	allGroupFields groupFields = iota + 1
	// patternGroupFields is the reduced set AWS returns for a call with
	// logGroupNamePattern: arn, creationTime, logGroupArn, logGroupClass,
	// logGroupName and metricFilterCount (AWS-REC
	// test_describe_log_groups_with_pattern, secrets-logs-events-alarms.md
	// 3.1), a superset of the three fields the documentation promises.
	patternGroupFields
)

// describeGroup is the LogGroup DescribeLogGroups returns: both ARN forms,
// and with allGroupFields also storedBytes, deletionProtectionEnabled, and
// retentionInDays and kmsKeyId when set (secrets-logs-events-alarms.md
// 3.1, tf-compute.md 5).
func describeGroup(ref groupRef, g *Group, fields groupFields) logsapi.LogGroup {
	out := logsapi.LogGroup{
		LogGroupName:      new(g.Name),
		CreationTime:      new(millis(g.Created)),
		MetricFilterCount: new(int32(0)),
		Arn:               new(ref.wildcardARN()),
		LogGroupArn:       new(ref.arn().String()),
		LogGroupClass:     new(logsapi.LogGroupClass(g.Class)),
	}
	if fields == patternGroupFields {
		return out
	}
	out.StoredBytes = new(g.storedBytes())
	out.DeletionProtectionEnabled = new(g.DeletionProtection)
	if g.RetentionDays != 0 {
		out.RetentionInDays = new(g.RetentionDays)
	}
	if g.KMSKeyID != "" {
		out.KmsKeyId = new(g.KMSKeyID)
	}
	return out
}

// putRetentionPolicy sets the group's retention, expiring what it already
// outlived and arming the timer for the rest.
func (s *Service) putRetentionPolicy(_ context.Context, c *kernel.Call, in *logsapi.PutRetentionPolicyInput) (*logsapi.PutRetentionPolicyOutput, *awserr.Error) {
	ref := named(c, in.LogGroupName)
	err := s.updateGroup(c, ref, func(g *Group, now clock.Instant, tx *kernel.Tx) *awserr.Error {
		if g.fixedRetention() {
			return errFixedRetention()
		}
		g.RetentionDays = in.RetentionInDays
		expire(g, now)
		s.armExpiry(tx, ref, g)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &logsapi.PutRetentionPolicyOutput{}, nil
}

// deleteRetentionPolicy makes the group's events never expire.
func (s *Service) deleteRetentionPolicy(_ context.Context, c *kernel.Call, in *logsapi.DeleteRetentionPolicyInput) (*logsapi.DeleteRetentionPolicyOutput, *awserr.Error) {
	ref := named(c, in.LogGroupName)
	err := s.updateGroup(c, ref, func(g *Group, _ clock.Instant, tx *kernel.Tx) *awserr.Error {
		if g.fixedRetention() {
			return errFixedRetention()
		}
		g.RetentionDays = 0
		s.armExpiry(tx, ref, g)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &logsapi.DeleteRetentionPolicyOutput{}, nil
}

// tagResource adds tags to the group an ARN names.
func (s *Service) tagResource(_ context.Context, c *kernel.Call, in *logsapi.TagResourceInput) (*logsapi.TagResourceOutput, *awserr.Error) {
	ref, err := taggable(c, in.ResourceArn)
	if err != nil {
		return nil, err
	}
	if err := s.addTags(c, ref, in.Tags, errTooManyTags); err != nil {
		return nil, err
	}
	return &logsapi.TagResourceOutput{}, nil
}

// untagResource removes tag keys from the group an ARN names.
func (s *Service) untagResource(_ context.Context, c *kernel.Call, in *logsapi.UntagResourceInput) (*logsapi.UntagResourceOutput, *awserr.Error) {
	ref, err := taggable(c, in.ResourceArn)
	if err != nil {
		return nil, err
	}
	if err := s.removeTags(c, ref, in.TagKeys); err != nil {
		return nil, err
	}
	return &logsapi.UntagResourceOutput{}, nil
}

// listTagsForResource returns the tags of the group an ARN names.
func (s *Service) listTagsForResource(_ context.Context, c *kernel.Call, in *logsapi.ListTagsForResourceInput) (*logsapi.ListTagsForResourceOutput, *awserr.Error) {
	ref, err := taggable(c, in.ResourceArn)
	if err != nil {
		return nil, err
	}
	tags, err := s.tags(ref)
	if err != nil {
		return nil, err
	}
	return &logsapi.ListTagsForResourceOutput{Tags: tags}, nil
}

// tagLogGroup is the legacy TagResource by group name; both families
// operate on one tag set (secrets-logs-events-alarms.md 3.7).
func (s *Service) tagLogGroup(_ context.Context, c *kernel.Call, in *logsapi.TagLogGroupInput) (*logsapi.TagLogGroupOutput, *awserr.Error) {
	if err := s.addTags(c, named(c, in.LogGroupName), in.Tags, errTooManyTagsLegacy); err != nil {
		return nil, err
	}
	return &logsapi.TagLogGroupOutput{}, nil
}

// untagLogGroup is the legacy UntagResource by group name.
func (s *Service) untagLogGroup(_ context.Context, c *kernel.Call, in *logsapi.UntagLogGroupInput) (*logsapi.UntagLogGroupOutput, *awserr.Error) {
	if err := s.removeTags(c, named(c, in.LogGroupName), in.Tags); err != nil {
		return nil, err
	}
	return &logsapi.UntagLogGroupOutput{}, nil
}

// listTagsLogGroup is the legacy ListTagsForResource by group name.
func (s *Service) listTagsLogGroup(_ context.Context, c *kernel.Call, in *logsapi.ListTagsLogGroupInput) (*logsapi.ListTagsLogGroupOutput, *awserr.Error) {
	tags, err := s.tags(named(c, in.LogGroupName))
	if err != nil {
		return nil, err
	}
	return &logsapi.ListTagsLogGroupOutput{Tags: tags}, nil
}

// taggable is the group a tagging ARN names, in the caller's scope.
func taggable(c *kernel.Call, arn string) (groupRef, *awserr.Error) {
	ref, ok := groupOfARN(arn)
	if !ok {
		return groupRef{}, errGroupNotFound()
	}
	if _, err := kernel.ResolveScope(c, kernel.SameScope{ARN: ref.arn(), Mismatch: errGroupNotFound()}); err != nil {
		return groupRef{}, err
	}
	return ref, nil
}

// addTags merges tags into the group's; tooMany is the calling operation's
// error for a tag set over maxTags, since the two tag families model
// different ones.
func (s *Service) addTags(c *kernel.Call, ref groupRef, tags map[string]string, tooMany func(arn string) *awserr.Error) *awserr.Error {
	return s.updateGroup(c, ref, func(g *Group, _ clock.Instant, _ *kernel.Tx) *awserr.Error {
		merged := maps.Clone(g.Tags)
		if merged == nil {
			merged = map[string]string{}
		}
		maps.Copy(merged, tags)
		if len(merged) > maxTags {
			return tooMany(ref.arn().String())
		}
		g.Tags = merged
		return nil
	})
}

func (s *Service) removeTags(c *kernel.Call, ref groupRef, keys []string) *awserr.Error {
	return s.updateGroup(c, ref, func(g *Group, _ clock.Instant, _ *kernel.Tx) *awserr.Error {
		kept := maps.Clone(g.Tags)
		for _, k := range keys {
			delete(kept, k)
		}
		if len(kept) == 0 {
			kept = nil
		}
		g.Tags = kept
		return nil
	})
}

// tags returns a copy of the group's tags, an empty map when it has none.
func (s *Service) tags(ref groupRef) (map[string]string, *awserr.Error) {
	tags, ok := group(s, ref, func(g *Group) map[string]string { return maps.Clone(g.Tags) })
	if !ok {
		return nil, errGroupNotFound()
	}
	if tags == nil {
		tags = map[string]string{}
	}
	return tags, nil
}

// updateGroup runs fn on the group ref names under the scope's lock and
// stores the result; ResourceNotFoundException when there is none. fn
// changes the copy it is given, so an error leaves the state unchanged.
func (s *Service) updateGroup(c *kernel.Call, ref groupRef, fn func(g *Group, now clock.Instant, tx *kernel.Tx) *awserr.Error) *awserr.Error {
	if !s.exists(ref) {
		return errGroupNotFound()
	}
	_, err := kernel.Write(c, s.cells, ref.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		g, ok := st.Groups[ref.name]
		if !ok {
			return struct{}{}, errGroupNotFound()
		}
		g.Streams = maps.Clone(g.Streams)
		if err := fn(&g, now, tx); err != nil {
			return struct{}{}, err
		}
		st.Groups[ref.name] = g
		return struct{}{}, nil
	})
	return err
}

// exists reports whether ref names a group, without creating a cell for
// its scope.
func (s *Service) exists(ref groupRef) bool {
	_, ok := group(s, ref, func(*Group) struct{} { return struct{}{} })
	return ok
}

// group reads the group ref names through fn, which must return values
// that do not alias the state.
func group[R any](s *Service, ref groupRef, fn func(g *Group) R) (R, bool) {
	var zero R
	cell, ok := s.cells.Lookup(ref.scope)
	if !ok {
		return zero, false
	}
	type found struct {
		r  R
		ok bool
	}
	f := store.Read(cell, func(st *State) found {
		g, ok := st.Groups[ref.name]
		if !ok {
			return found{}
		}
		return found{r: fn(&g), ok: true}
	})
	return f.r, f.ok
}
