package s3

import (
	"context"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/s3/notify"
)

// maxObjectTags is the object tag limit (s3.md 11.1).
const maxObjectTags = 10

// The object tag texts (s3.md 11.1); the count text is UNVERIFIED.
const (
	objectReservedTag  = "Your TagKey cannot be prefixed with aws:"
	tooManyObjectTags  = "Object tags cannot be greater than 10"
	taggingHeaderShape = "The header 'x-amz-tagging' shall be encoded as UTF-8 then URLEncoded URL query parameters without tag name duplicates."
)

// bindObjectTagging binds the object tagging operations.
func (s *Service) bindObjectTagging(b s3api.Binder) {
	get := func(in *s3api.GetObjectTaggingInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetObjectTagging(reader(s, get, getObjectTagging), registry.Full{}, touches(get))
	put := func(in *s3api.PutObjectTaggingInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutObjectTagging(s.putObjectTagging, registry.Full{}, touches(put),
		kernel.WithCheck(func(in *s3api.PutObjectTaggingInput) *awserr.Error {
			_, err := objectTagSet(in.Tagging.TagSet)
			return err
		}))
	del := func(in *s3api.DeleteObjectTaggingInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.DeleteObjectTagging(s.deleteObjectTagging, registry.Full{}, touches(del))
}

func getObjectTagging(in *s3api.GetObjectTaggingInput, b *Bucket) (*s3api.GetObjectTaggingOutput, *awserr.Error) {
	v, err := findVersion(b, in.Key, in.VersionId, "GET")
	if err != nil {
		return nil, err
	}
	return &s3api.GetObjectTaggingOutput{TagSet: tagList(v.Object.Tags), VersionId: reportedVersion(b, v.ID)}, nil
}

func (s *Service) putObjectTagging(ctx context.Context, c *kernel.Call, in *s3api.PutObjectTaggingInput) (*s3api.PutObjectTaggingOutput, *awserr.Error) {
	tags, err := objectTagSet(in.Tagging.TagSet)
	if err != nil {
		return nil, err
	}
	id, err := s.retag(ctx, c, at(in.Bucket, in.ExpectedBucketOwner), in.Key, in.VersionId, "PUT", tags, notify.TaggingPut)
	if err != nil {
		return nil, err
	}
	return &s3api.PutObjectTaggingOutput{VersionId: id}, nil
}

func (s *Service) deleteObjectTagging(ctx context.Context, c *kernel.Call, in *s3api.DeleteObjectTaggingInput) (*s3api.DeleteObjectTaggingOutput, *awserr.Error) {
	id, err := s.retag(ctx, c, at(in.Bucket, in.ExpectedBucketOwner), in.Key, in.VersionId, "DELETE", nil, notify.TaggingDelete)
	if err != nil {
		return nil, err
	}
	return &s3api.DeleteObjectTaggingOutput{VersionId: id}, nil
}

// retag replaces the tags of the version a request addresses. A tag change
// keeps the ETag and Last-Modified (s3.md 11.1).
func (s *Service) retag(ctx context.Context, c *kernel.Call, a access, key string, id *string, method string, tags map[string]string, action notify.TaggingAction) (*string, *awserr.Error) {
	got, _, err := writeObjects(ctx, s, c, a, func(b *Bucket, w objectTx) (*string, []blob.Ref, *awserr.Error) {
		v, err := findVersion(b, key, id, method)
		if err != nil {
			return nil, nil, err
		}
		obj := *v.Object
		obj.Tags = tags
		v.Object = &obj
		b.Objects.replace(key, v)
		s.commitObject(w, b, event{key: key, versionID: v.ID, change: notify.Tagged{Action: action, ETag: obj.ETag}})
		return reportedVersion(b, v.ID), nil, nil
	})
	return got, err
}

// objectTagSet checks an object tag set: at most 10 tags, with the bucket
// tag rules otherwise (s3.md 11.1). An empty set is no tags.
func objectTagSet(tags []s3api.Tag) (map[string]string, *awserr.Error) {
	if len(tags) > maxObjectTags {
		return nil, s3api.ErrInvalidTag(tooManyObjectTags)
	}
	out := make(map[string]string, len(tags))
	for _, t := range tags {
		if err := checkObjectTag(t.Key, t.Value); err != nil {
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

// checkObjectTag checks one object tag. A comma in a key is refused, as
// for bucket tags (LocalStack aws_validated snapshot test_tagging_validation,
// step put-object-tags-invalid-field); a comma in a value is accepted, which
// is UNVERIFIED.
func checkObjectTag(key, value string) *awserr.Error {
	switch {
	case strings.HasPrefix(key, systemTagPrefix):
		return errInvalidTag(objectReservedTag, key)
	case key == "" || strings.Contains(key, ",") || utf8.RuneCountInString(key) > maxTagKey:
		return errInvalidTag("The TagKey you have provided is invalid", key)
	case utf8.RuneCountInString(value) > maxTagValue:
		return errInvalidTagValue(key, value)
	}
	return nil
}

// parseTaggingHeader reads x-amz-tagging, URL query encoded: k1=v1&k2=v2.
// A key without "=" has an empty value; a repeated key, an invalid escape
// or a pair that is not query encoded (key1=val1,key2=val2) is
// InvalidArgument (s3.md 11.1).
func parseTaggingHeader(v *string) (map[string]string, *awserr.Error) {
	if v == nil || *v == "" {
		return nil, nil
	}
	bad := errInvalidArgument(taggingHeaderShape, "x-amz-tagging", *v)
	tags := make([]s3api.Tag, 0, strings.Count(*v, "&")+1)
	seen := map[string]bool{}
	for pair := range strings.SplitSeq(*v, "&") {
		rawKey, rawValue, _ := strings.Cut(pair, "=")
		if strings.Contains(rawValue, "=") {
			return nil, bad
		}
		key, kerr := url.QueryUnescape(rawKey)
		value, verr := url.QueryUnescape(rawValue)
		if kerr != nil || verr != nil || seen[key] {
			return nil, bad
		}
		seen[key] = true
		tags = append(tags, s3api.Tag{Key: key, Value: value})
	}
	return objectTagSet(tags)
}

// tagCount is x-amz-tagging-count: omitted when the object has no tags.
func tagCount(tags map[string]string) *int32 {
	if len(tags) == 0 {
		return nil
	}
	return count(len(tags))
}
