package lambda

import (
	"context"
	"maps"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

func (s *Service) bindTags(b lambdaapi.Binder) {
	b.TagResource(s.tagResource, full())
	b.UntagResource(s.untagResource, full())
	b.ListTags(s.listTags, full())
}

// taggable is a tag operation's Resource: a function, or an event source
// mapping (Terraform tags mappings by their ARN).
//
//sumtype:decl
type taggable interface {
	isTaggable()
	scope() aws.Scope
	arn() string
	// tags reads the resource's tags from st.
	tags(st *State) (map[string]string, *awserr.Error)
	// retag stores tags as the resource's, once its state allows it.
	retag(st *State, tags map[string]string) *awserr.Error
}

type taggedFunction struct{ ref functionRef }

type taggedMapping struct {
	in   aws.Scope
	uuid string
}

func (taggedFunction) isTaggable() {}
func (taggedMapping) isTaggable()  {}

func (t taggedFunction) scope() aws.Scope { return t.ref.scope }
func (t taggedMapping) scope() aws.Scope  { return t.in }

func (t taggedFunction) arn() string { return t.ref.arn() }
func (t taggedMapping) arn() string  { return mappingARN(t.in, t.uuid) }

func (t taggedFunction) tags(st *State) (map[string]string, *awserr.Error) {
	f, err := st.function(t.ref)
	return f.Tags, err
}

func (t taggedMapping) tags(st *State) (map[string]string, *awserr.Error) {
	m, ok := st.Mappings[t.uuid]
	if !ok {
		return nil, errMappingNotFoundARN(t.arn())
	}
	return m.Tags, nil
}

// retag refuses a function whose update is in progress
// (lambda-api.md 7.5).
func (t taggedFunction) retag(st *State, tags map[string]string) *awserr.Error {
	f, err := st.function(t.ref)
	if err != nil {
		return err
	}
	if err := mutable(t.ref.arn(), &f.Latest); err != nil {
		return err
	}
	f = f.clone()
	f.Tags = tags
	st.Functions[t.ref.name] = f
	return nil
}

func (t taggedMapping) retag(st *State, tags map[string]string) *awserr.Error {
	m, ok := st.Mappings[t.uuid]
	if !ok {
		return errMappingNotFoundARN(t.arn())
	}
	m.Tags = tags
	st.Mappings[t.uuid] = m
	return nil
}

// parseTaggable reads a Resource ARN. A qualified function ARN is refused:
// tags belong to the function, not its versions and aliases
// (lambda-api.md 7.5).
func parseTaggable(c *kernel.Call, resource string) (taggable, *awserr.Error) {
	if a, err := aws.ParseARN(resource); err == nil && a.Service == "lambda" && strings.HasPrefix(a.Resource, "event-source-mapping:") {
		if a.Region != c.Scope().Region.String() {
			return nil, errNotReachable(a.Region, c.Scope().Region.String())
		}
		scope, err := kernel.ResolveScope(c, kernel.SameScope{ARN: a, Mismatch: errOtherAccount()})
		if err != nil {
			return nil, err
		}
		return taggedMapping{in: scope, uuid: strings.TrimPrefix(a.Resource, "event-source-mapping:")}, nil
	}
	ref, err := parseFunctionRef(c, resource, nil)
	if err != nil {
		return nil, err
	}
	if ref.qualifier != "" && ref.qualifier != latest {
		return nil, errTagsOnQualified()
	}
	ref.qualifier = ""
	return taggedFunction{ref: ref}, nil
}

// changeTags applies change to the tags of the resource, refusing a
// result above the tag limit.
func (s *Service) changeTags(c *kernel.Call, resource string, change func(tags map[string]string)) *awserr.Error {
	t, err := parseTaggable(c, resource)
	if err != nil {
		return err
	}
	_, err = write(s, c, t.scope(), func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		current, err := t.tags(st)
		if err != nil {
			return struct{}{}, err
		}
		tags := maps.Clone(current)
		if tags == nil {
			tags = map[string]string{}
		}
		change(tags)
		if len(tags) > maxTags {
			return struct{}{}, errTooManyTags()
		}
		return struct{}{}, t.retag(st, tags)
	})
	if err != nil {
		return err
	}
	touch(c, t.arn())
	return nil
}

func (s *Service) tagResource(_ context.Context, c *kernel.Call, in *lambdaapi.TagResourceInput) (*lambdaapi.TagResourceOutput, *awserr.Error) {
	if len(in.Tags) == 0 {
		return nil, errEmptyTags()
	}
	if err := s.changeTags(c, in.Resource, func(tags map[string]string) { maps.Copy(tags, in.Tags) }); err != nil {
		return nil, err
	}
	return &lambdaapi.TagResourceOutput{}, nil
}

func (s *Service) untagResource(_ context.Context, c *kernel.Call, in *lambdaapi.UntagResourceInput) (*lambdaapi.UntagResourceOutput, *awserr.Error) {
	err := s.changeTags(c, in.Resource, func(tags map[string]string) {
		for _, k := range in.TagKeys {
			delete(tags, k)
		}
	})
	if err != nil {
		return nil, err
	}
	return &lambdaapi.UntagResourceOutput{}, nil
}

func (s *Service) listTags(_ context.Context, c *kernel.Call, in *lambdaapi.ListTagsInput) (*lambdaapi.ListTagsOutput, *awserr.Error) {
	t, err := parseTaggable(c, in.Resource)
	if err != nil {
		return nil, err
	}
	tags, err := view(s, t.scope(), func(st *State) (map[string]string, *awserr.Error) {
		tags, err := t.tags(st)
		return maps.Clone(tags), err
	})
	if err != nil {
		return nil, err
	}
	if tags == nil {
		tags = map[string]string{}
	}
	return &lambdaapi.ListTagsOutput{Tags: tags}, nil
}
