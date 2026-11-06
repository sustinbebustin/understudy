package sfn

import (
	"context"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// maxTags is the tag quota of a resource (stepfunctions-api.md 4.2).
const maxTags = 50

// errTooManyTags refuses a resource past maxTags tags
// ([M] com.amazonaws.sfn#TooManyTags); the text is understudy's.
func errTooManyTags(resource string) *awserr.Error {
	return sfnapi.ErrTooManyTagsMembers("A resource can have at most 50 tags", sfnapi.TooManyTags{ResourceName: new(resource)})
}

// tagsOf adds in to tags: a key already there takes the new value in its
// place, a new key goes last.
func tagsOf(tags []Tag, in []sfnapi.Tag) []Tag {
	out := slices.Clone(tags)
	for _, t := range in {
		key, value := deref(t.Key), deref(t.Value)
		if i := slices.IndexFunc(out, func(x Tag) bool { return x.Key == key }); i >= 0 {
			out[i].Value = value
			continue
		}
		out = append(out, Tag{Key: key, Value: value})
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// tagged reads the machine a tagging ARN names. A version or alias ARN, an
// activity ARN, and a machine that does not exist are ResourceNotFound
// (stepfunctions-api.md 5.8).
func tagged(scope aws.Scope, raw string) (machineRef, *awserr.Error) {
	a, err := aws.ParseARN(raw)
	if err != nil || a.Service != "states" {
		return machineRef{}, sfnapi.ErrInvalidArn("Invalid Arn: 'Invalid ARN prefix: " + raw + "'")
	}
	typ, name, _ := strings.Cut(a.Resource, ":")
	switch {
	case typ == "stateMachine" && inScope(scope, a) && name != "" && !strings.Contains(name, ":"):
		return machineRef{scope: scope, name: name}, nil
	case typ == "stateMachine" || typ == "activity":
		return machineRef{}, errResourceNotFound(raw)
	}
	return machineRef{}, sfnapi.ErrInvalidArn("Invalid Arn: 'Resource type not valid in this context: " + typ + "'")
}

// errResourceNotFound is the recorded text for a tagging ARN that names no
// resource (stepfunctions-api.md 3.2).
func errResourceNotFound(raw string) *awserr.Error {
	return sfnapi.ErrResourceNotFoundMembers("Resource not found: '"+raw+"'", sfnapi.ResourceNotFound{ResourceName: new(raw)})
}

// retag changes the tags of the machine raw names with fn.
func (s *Service) retag(c *kernel.Call, raw string, fn func([]Tag) ([]Tag, *awserr.Error)) *awserr.Error {
	ref, err := tagged(c.Scope(), raw)
	if err != nil {
		return err
	}
	_, err = kernel.Write(c, s.cells, ref.scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		m, ok := st.Machines[ref.name]
		if !ok {
			return struct{}{}, errResourceNotFound(raw)
		}
		tags, err := fn(m.Tags)
		if err != nil {
			return struct{}{}, err
		}
		m.Tags = tags
		st.Machines[ref.name] = m
		return struct{}{}, nil
	})
	return err
}

func (s *Service) tagResource(_ context.Context, c *kernel.Call, in *sfnapi.TagResourceInput) (*sfnapi.TagResourceOutput, *awserr.Error) {
	err := s.retag(c, in.ResourceArn, func(tags []Tag) ([]Tag, *awserr.Error) {
		out := tagsOf(tags, in.Tags)
		if len(out) > maxTags {
			return nil, errTooManyTags(in.ResourceArn)
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return &sfnapi.TagResourceOutput{}, nil
}

func (s *Service) untagResource(_ context.Context, c *kernel.Call, in *sfnapi.UntagResourceInput) (*sfnapi.UntagResourceOutput, *awserr.Error) {
	err := s.retag(c, in.ResourceArn, func(tags []Tag) ([]Tag, *awserr.Error) {
		return slices.DeleteFunc(slices.Clone(tags), func(t Tag) bool { return slices.Contains(in.TagKeys, t.Key) }), nil
	})
	if err != nil {
		return nil, err
	}
	return &sfnapi.UntagResourceOutput{}, nil
}

func (s *Service) listTagsForResource(_ context.Context, c *kernel.Call, in *sfnapi.ListTagsForResourceInput) (*sfnapi.ListTagsForResourceOutput, *awserr.Error) {
	ref, err := tagged(c.Scope(), in.ResourceArn)
	if err != nil {
		return nil, err
	}
	cell, ok := s.cells.Lookup(ref.scope)
	if !ok {
		return nil, errResourceNotFound(in.ResourceArn)
	}
	got := store.Read(cell, func(st *State) found[[]Tag] {
		m, ok := st.Machines[ref.name]
		return found[[]Tag]{value: slices.Clone(m.Tags), ok: ok}
	})
	if !got.ok {
		return nil, errResourceNotFound(in.ResourceArn)
	}
	out := &sfnapi.ListTagsForResourceOutput{Tags: make([]sfnapi.Tag, 0, len(got.value))}
	for _, t := range got.value {
		out.Tags = append(out.Tags, sfnapi.Tag{Key: new(t.Key), Value: new(t.Value)})
	}
	return out, nil
}
