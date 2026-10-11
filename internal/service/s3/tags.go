package s3

import (
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Bucket tag limits (s3.md 4.2.9).
const (
	maxBucketTags = 50
	maxTagKey     = 128
	maxTagValue   = 256
	// systemTagPrefix starts the tag keys AWS reserves.
	systemTagPrefix = "aws:"
)

// The texts of a reserved tag key: CreateBucket's and the tagging
// operations' differ (s3.md 3.2, 4.2.9).
const (
	createReservedTag = `User-defined tag keys can't start with "aws:". This prefix is reserved for system tags. Remove "aws:" from your tag keys and try again.`
	systemReservedTag = "System tags cannot be added/updated by requester"
)

// bindTagging binds the bucket tagging operations, whose tag set the S3
// Control tag operations share.
func (s *Service) bindTagging(b s3api.Binder) {
	get := func(in *s3api.GetBucketTaggingInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetBucketTagging(reader(s, get, getTagging), registry.Full{}, touches(get))
	put := func(in *s3api.PutBucketTaggingInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutBucketTagging(writer[s3api.PutBucketTaggingInput, s3api.PutBucketTaggingOutput](s, put, putTagging), registry.Full{}, touches(put),
		kernel.WithCheck(func(in *s3api.PutBucketTaggingInput) *awserr.Error {
			_, err := tagSet(in.Tagging.TagSet, systemReservedTag)
			return err
		}))
	del := func(in *s3api.DeleteBucketTaggingInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.DeleteBucketTagging(writer[s3api.DeleteBucketTaggingInput, s3api.DeleteBucketTaggingOutput](s, del, func(_ *s3api.DeleteBucketTaggingInput, b *Bucket) *awserr.Error {
		b.Tags = nil
		return nil
	}), registry.Full{}, touches(del))
}

func getTagging(_ *s3api.GetBucketTaggingInput, b *Bucket) (*s3api.GetBucketTaggingOutput, *awserr.Error) {
	if len(b.Tags) == 0 {
		return nil, notConfigured(s3api.NotConfiguredGetBucketTagging(noSuchTagSetMessage), b)
	}
	return &s3api.GetBucketTaggingOutput{TagSet: tagList(b.Tags)}, nil
}

// putTagging replaces the tag set; an empty set removes it.
func putTagging(in *s3api.PutBucketTaggingInput, b *Bucket) *awserr.Error {
	tags, err := tagSet(in.Tagging.TagSet, systemReservedTag)
	if err != nil {
		return err
	}
	b.Tags = tags
	return nil
}

// createTags checks the tags of a CreateBucket.
func createTags(tags []s3api.Tag) (map[string]string, *awserr.Error) {
	return tagSet(tags, createReservedTag)
}

// tagSet checks a tag list and returns it as a map; reserved is the text
// for a key in the aws: namespace. Every limit is checked here, so a tag
// set built from it is always valid.
func tagSet(tags []s3api.Tag, reserved string) (map[string]string, *awserr.Error) {
	if len(tags) > maxBucketTags {
		return nil, s3api.ErrInvalidTag("Bucket tag count cannot be greater than 50")
	}
	out := make(map[string]string, len(tags))
	for _, t := range tags {
		if err := checkTag(t.Key, t.Value, reserved); err != nil {
			return nil, err
		}
		if _, dup := out[t.Key]; dup {
			return nil, errInvalidTag("Cannot provide multiple Tags with the same key", t.Key)
		}
		out[t.Key] = t.Value
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// checkTag checks one tag's key and value (s3.md 4.2.9); the length texts
// are UNVERIFIED.
func checkTag(key, value, reserved string) *awserr.Error {
	switch {
	case strings.HasPrefix(key, systemTagPrefix):
		return errInvalidTag(reserved, key)
	case strings.Contains(key, ","):
		return errInvalidTag("The TagKey you have provided is invalid", key)
	case utf8.RuneCountInString(key) > maxTagKey:
		return errInvalidTag("The TagKey you have provided is too long, max 128", key)
	case strings.Contains(value, ","):
		return errInvalidTagValue(key, value)
	case utf8.RuneCountInString(value) > maxTagValue:
		return withField(errInvalidTag("The TagValue you have provided is too long, max 256", key), "TagValue", value)
	}
	return nil
}

// tagList is a tag set in key order.
func tagList(tags map[string]string) []s3api.Tag {
	out := make([]s3api.Tag, 0, len(tags))
	for _, k := range slices.Sorted(maps.Keys(tags)) {
		out = append(out, s3api.Tag{Key: k, Value: tags[k]})
	}
	return out
}
