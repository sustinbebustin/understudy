package kms

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// maxTags is the tag quota of one key.
const maxTags = 50

// bindTags binds the key tag operations.
func (s *Service) bindTags(b kmsapi.Binder) {
	b.TagResource(s.tagResource, registry.Full{},
		kernel.WithCheck(func(in *kmsapi.TagResourceInput) *awserr.Error { return checkTags(in.Tags) }))
	b.UntagResource(s.untagResource, registry.Full{})
	b.ListResourceTags(s.listResourceTags, registry.Full{})
}

// checkTags answers the recorded TagException texts for one request's tags:
// a reserved aws: key in any case (AWS refuses AWS:key1 too), a key given
// twice and more than the quota.
func checkTags(tags []kmsapi.Tag) *awserr.Error {
	const reserved = "aws:"
	seen := make(map[string]bool, len(tags))
	for _, t := range tags {
		if len(t.TagKey) >= len(reserved) && strings.EqualFold(t.TagKey[:len(reserved)], reserved) {
			return errReservedTag()
		}
		if seen[t.TagKey] {
			return errDuplicateTagKeys()
		}
		seen[t.TagKey] = true
	}
	if len(tags) > maxTags {
		return errTooManyTags()
	}
	return nil
}

// tagMap returns existing with tags set over it, as a new map.
func tagMap(existing map[string]string, tags []kmsapi.Tag) map[string]string {
	out := maps.Clone(existing)
	if out == nil {
		out = make(map[string]string, len(tags))
	}
	for _, t := range tags {
		out[t.TagKey] = t.TagValue
	}
	return out
}

func (s *Service) tagResource(_ context.Context, c *kernel.Call, in *kmsapi.TagResourceInput) (*kmsapi.TagResourceOutput, *awserr.Error) {
	_, err := updateKey(s, c, in.KeyId, notPending, func(k Key, _ string, _ clock.Instant, _ *kernel.Tx) (Key, *awserr.Error) {
		tags := tagMap(k.Tags, in.Tags)
		if len(tags) > maxTags {
			return Key{}, errTooManyTags()
		}
		k.Tags = tags
		return k, nil
	})
	if err != nil {
		return nil, err
	}
	return &kmsapi.TagResourceOutput{}, nil
}

func (s *Service) untagResource(_ context.Context, c *kernel.Call, in *kmsapi.UntagResourceInput) (*kmsapi.UntagResourceOutput, *awserr.Error) {
	_, err := updateKey(s, c, in.KeyId, notPending, func(k Key, _ string, _ clock.Instant, _ *kernel.Tx) (Key, *awserr.Error) {
		tags := maps.Clone(k.Tags)
		for _, key := range in.TagKeys {
			delete(tags, key)
		}
		k.Tags = tags
		return k, nil
	})
	if err != nil {
		return nil, err
	}
	return &kmsapi.UntagResourceOutput{}, nil
}

func (s *Service) listResourceTags(_ context.Context, c *kernel.Call, in *kmsapi.ListResourceTagsInput) (*kmsapi.ListResourceTagsOutput, *awserr.Error) {
	k, err := readKey(s, c, in.KeyId, keyOnly, ownAccount)
	if err != nil {
		return nil, err
	}
	p, err := page(listingTags, slices.Collect(maps.Keys(k.Tags)), func(key string) string { return key }, in.Limit, limits{def: maxTags, max: maxTags}, in.Marker)
	if err != nil {
		return nil, err
	}
	out := &kmsapi.ListResourceTagsOutput{Tags: make([]kmsapi.Tag, 0, len(p.Items)), NextMarker: p.Next, Truncated: p.Truncated}
	for _, key := range p.Items {
		out.Tags = append(out.Tags, kmsapi.Tag{TagKey: key, TagValue: k.Tags[key]})
	}
	return out, nil
}
