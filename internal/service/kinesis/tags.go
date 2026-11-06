package kinesis

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// maxTags is the tag quota of one stream (aws-doc kinesis.md 4.10).
const maxTags = 50

const tagNote = "a 51st tag and an aws: key answer InvalidArgumentException; AWS's code and text are UNVERIFIED (kinesis.md 4.10)"

// bindTags binds the stream and resource tag operations.
func (s *Service) bindTags(b kinesisapi.Binder) {
	cov := registry.Partial{Note: tagNote}
	b.AddTagsToStream(s.addTagsToStream, cov)
	b.RemoveTagsFromStream(s.removeTagsFromStream, cov)
	b.ListTagsForStream(s.listTagsForStream, cov)
	b.TagResource(s.tagResource, cov)
	b.UntagResource(s.untagResource, cov)
	b.ListTagsForResource(s.listTagsForResource, cov)
}

// checkTags refuses adding add to existing on the stream name of account:
// a key with the reserved aws: prefix in any case, then more than 50 tags
// after the merge (aws-doc kinesis.md 4.10; refusals understudy's,
// KIN-07).
func checkTags(name, account string, existing, add map[string]string) *awserr.Error {
	const reserved = "aws:"
	for _, k := range slices.Sorted(maps.Keys(add)) {
		if len(k) >= len(reserved) && strings.EqualFold(k[:len(reserved)], reserved) {
			return errReservedTag(k)
		}
	}
	n := len(existing)
	for k := range add {
		if _, ok := existing[k]; !ok {
			n++
		}
	}
	if n > maxTags {
		return errTooManyTags(name, account)
	}
	return nil
}

// tag merges tags into the stream r names, overwriting keys.
func (s *Service) tag(c *kernel.Call, r streamRef, tags map[string]string) *awserr.Error {
	return s.updateStream(c, r, func(str *Stream, _ clock.Instant) *awserr.Error {
		if err := checkTags(str.Name, c.Scope().Account.String(), str.Tags, tags); err != nil {
			return err
		}
		if str.Tags == nil {
			str.Tags = make(map[string]string, len(tags))
		}
		maps.Copy(str.Tags, tags)
		return nil
	})
}

// untag removes keys from the stream r names; absent keys are ignored
// (aws-doc kinesis.md 4.10).
func (s *Service) untag(c *kernel.Call, r streamRef, keys []string) *awserr.Error {
	return s.updateStream(c, r, func(str *Stream, _ clock.Instant) *awserr.Error {
		for _, k := range keys {
			delete(str.Tags, k)
		}
		return nil
	})
}

// tagList renders tags in key order.
func tagList(tags map[string]string, keys []string) []kinesisapi.Tag {
	out := make([]kinesisapi.Tag, 0, len(keys))
	for _, k := range keys {
		out = append(out, kinesisapi.Tag{Key: k, Value: new(tags[k])})
	}
	return out
}

func (s *Service) addTagsToStream(_ context.Context, c *kernel.Call, in *kinesisapi.AddTagsToStreamInput) (*kinesisapi.AddTagsToStreamOutput, *awserr.Error) {
	r, err := streamOf(c, in.StreamName, in.StreamARN)
	if err != nil {
		return nil, err
	}
	if err := s.tag(c, r, in.Tags); err != nil {
		return nil, err
	}
	return &kinesisapi.AddTagsToStreamOutput{}, nil
}

func (s *Service) removeTagsFromStream(_ context.Context, c *kernel.Call, in *kinesisapi.RemoveTagsFromStreamInput) (*kinesisapi.RemoveTagsFromStreamOutput, *awserr.Error) {
	r, err := streamOf(c, in.StreamName, in.StreamARN)
	if err != nil {
		return nil, err
	}
	if err := s.untag(c, r, in.TagKeys); err != nil {
		return nil, err
	}
	return &kinesisapi.RemoveTagsFromStreamOutput{}, nil
}

// defaultTagPage is ListTagsForStream's page size without Limit. AWS's
// default is unrecorded (KIN-15); 50 is the quota, so one page holds
// every tag.
const defaultTagPage = maxTags

// listTagsForStream pages the tags in key order after ExclusiveStartTagKey,
// HasMoreTags when more follow (kinesis.md 13.6 item 4).
func (s *Service) listTagsForStream(_ context.Context, c *kernel.Call, in *kinesisapi.ListTagsForStreamInput) (*kinesisapi.ListTagsForStreamOutput, *awserr.Error) {
	r, err := streamOf(c, in.StreamName, in.StreamARN)
	if err != nil {
		return nil, err
	}
	str, err := s.readStream(c, r)
	if err != nil {
		return nil, err
	}
	keys := slices.Sorted(maps.Keys(str.Tags))
	if in.ExclusiveStartTagKey != nil {
		start := *in.ExclusiveStartTagKey
		keys = slices.DeleteFunc(keys, func(k string) bool { return k <= start })
	}
	n := defaultTagPage
	if in.Limit != nil {
		n = int(*in.Limit)
	}
	more := len(keys) > n
	if more {
		keys = keys[:n]
	}
	return &kinesisapi.ListTagsForStreamOutput{Tags: tagList(str.Tags, keys), HasMoreTags: more}, nil
}

// resourceOf reads a ResourceARN: a stream ARN names its stream, a consumer
// or channel ARN answers 501 (contract 3.7), anything else is refused
// (understudy's text, KIN-07).
func resourceOf(c *kernel.Call, arn, op string) (streamRef, *awserr.Error) {
	if a, err := aws.ParseARN(arn); err == nil && a.Service == "kinesis" {
		if strings.HasPrefix(a.Resource, "channel/") || (strings.HasPrefix(a.Resource, "stream/") && strings.Contains(a.Resource, "/consumer/")) {
			return streamRef{}, errNotImplemented("tags on Kinesis consumers and channels", op)
		}
	}
	if _, ok := parseStreamARN(arn); !ok {
		return streamRef{}, errInvalidResourceARN(arn)
	}
	return streamOf(c, nil, &arn)
}

func (s *Service) tagResource(_ context.Context, c *kernel.Call, in *kinesisapi.TagResourceInput) (*kinesisapi.TagResourceOutput, *awserr.Error) {
	r, err := resourceOf(c, in.ResourceARN, "TagResource")
	if err != nil {
		return nil, err
	}
	if err := s.tag(c, r, in.Tags); err != nil {
		return nil, err
	}
	return &kinesisapi.TagResourceOutput{}, nil
}

func (s *Service) untagResource(_ context.Context, c *kernel.Call, in *kinesisapi.UntagResourceInput) (*kinesisapi.UntagResourceOutput, *awserr.Error) {
	r, err := resourceOf(c, in.ResourceARN, "UntagResource")
	if err != nil {
		return nil, err
	}
	if err := s.untag(c, r, in.TagKeys); err != nil {
		return nil, err
	}
	return &kinesisapi.UntagResourceOutput{}, nil
}

func (s *Service) listTagsForResource(_ context.Context, c *kernel.Call, in *kinesisapi.ListTagsForResourceInput) (*kinesisapi.ListTagsForResourceOutput, *awserr.Error) {
	r, err := resourceOf(c, in.ResourceARN, "ListTagsForResource")
	if err != nil {
		return nil, err
	}
	str, err := s.readStream(c, r)
	if err != nil {
		return nil, err
	}
	return &kinesisapi.ListTagsForResourceOutput{Tags: tagList(str.Tags, slices.Sorted(maps.Keys(str.Tags)))}, nil
}
