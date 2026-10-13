package secretsmanager

import (
	"context"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// maxTags is the quota of user tags on one resource (Tagging service
// quotas, which the TagResource reference points to).
const maxTags = 50

// setTags returns existing with tags set over it, as a new slice: a key
// already present keeps its position and takes the new value. No tags
// leaves existing as it is, so a secret created without tags has none.
// A result over maxTags keys is refused and existing is left as it is.
func setTags(existing []Tag, tags []secretsmanagerapi.Tag) ([]Tag, *awserr.Error) {
	if len(tags) == 0 {
		return existing, nil
	}
	out := slices.Clone(existing)
	for _, t := range tags {
		key, value := deref(t.Key), deref(t.Value)
		if i := slices.IndexFunc(out, func(x Tag) bool { return x.Key == key }); i >= 0 {
			out[i].Value = value
			continue
		}
		out = append(out, Tag{Key: key, Value: value})
	}
	if len(out) > maxTags {
		return nil, errTooManyTags(len(existing), len(out))
	}
	return out, nil
}

// removeTags returns existing without keys, as a new slice that is empty,
// never nil, once every tag is gone: DescribeSecret then shows Tags as [].
func removeTags(existing []Tag, keys []string) []Tag {
	out := make([]Tag, 0, len(existing))
	for _, t := range existing {
		if !slices.Contains(keys, t.Key) {
			out = append(out, t)
		}
	}
	return out
}

// renderTags keeps the difference between never tagged (nil) and every tag
// removed (empty).
func renderTags(tags []Tag) []secretsmanagerapi.Tag {
	if tags == nil {
		return nil
	}
	out := make([]secretsmanagerapi.Tag, 0, len(tags))
	for _, t := range tags {
		out = append(out, secretsmanagerapi.Tag{Key: new(t.Key), Value: new(t.Value)})
	}
	return out
}

func (s *Service) tagResource(_ context.Context, c *kernel.Call, in *secretsmanagerapi.TagResourceInput) (*secretsmanagerapi.TagResourceOutput, *awserr.Error) {
	if _, _, err := s.update(c, in.SecretId, func(sec *Secret) *awserr.Error {
		tags, err := setTags(sec.Tags, in.Tags)
		if err != nil {
			return err
		}
		sec.Tags = tags
		return nil
	}); err != nil {
		return nil, err
	}
	return &secretsmanagerapi.TagResourceOutput{}, nil
}

func (s *Service) untagResource(_ context.Context, c *kernel.Call, in *secretsmanagerapi.UntagResourceInput) (*secretsmanagerapi.UntagResourceOutput, *awserr.Error) {
	if _, _, err := s.update(c, in.SecretId, func(sec *Secret) *awserr.Error {
		sec.Tags = removeTags(sec.Tags, in.TagKeys)
		return nil
	}); err != nil {
		return nil, err
	}
	return &secretsmanagerapi.UntagResourceOutput{}, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
