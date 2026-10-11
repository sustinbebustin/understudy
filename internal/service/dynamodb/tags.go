package dynamodb

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// Tags live in the shim: Local drops CreateTable's Tags and answers the
// tag operations with UnknownOperationException (dynamodb-local.md 4.2), so
// they are served from the record, keyed by the ARN DescribeTable reports
// (tf-storage.md 3.4 item 2).

// maxTags is the most tags a table holds (docs.aws.amazon.com/
// amazondynamodb/latest/developerguide/Tagging.html, tagging
// restrictions).
const maxTags = 50

// reservedTagPrefix starts the tag keys AWS reserves for its own use, in
// any letter case.
const reservedTagPrefix = "aws:"

// checkTags refuses adding tags to a table holding have when the table
// would hold more than maxTags, or a key is reserved. The texts are
// UNVERIFIED against AWS.
func checkTags(have map[string]string, adding []dynamodbapi.Tag) *awserr.Error {
	keys := maps.Clone(have)
	if keys == nil {
		keys = make(map[string]string, len(adding))
	}
	for _, tag := range adding {
		if strings.HasPrefix(strings.ToLower(tag.Key), reservedTagPrefix) {
			return dynamodbapi.ErrValidationException("Invalid tag key " + tag.Key + ": tag keys starting with " + reservedTagPrefix + " are reserved for AWS use")
		}
		keys[tag.Key] = tag.Value
	}
	if len(keys) > maxTags {
		return dynamodbapi.ErrValidationException(fmt.Sprintf("Too many tags: the table would have %d tags; a table can have at most %d", len(keys), maxTags))
	}
	return nil
}

func (s *Service) tagResource(ctx context.Context, c *kernel.Call, in *dynamodbapi.TagResourceInput) (smithy.Serializable, *awserr.Error) {
	name, err := taggedName(c.Scope(), in.ResourceArn)
	if err != nil {
		return nil, err
	}
	missing := errTaggedNotFound(in.ResourceArn)
	if _, err := s.recorded(ctx, c, name, missing); err != nil {
		return nil, err
	}
	_, err = s.alter(c, name, missing, func(t *Table, _ clock.Instant) *awserr.Error {
		if err := checkTags(t.Tags, in.Tags); err != nil {
			return err
		}
		if t.Tags == nil {
			t.Tags = make(map[string]string, len(in.Tags))
		}
		for _, tag := range in.Tags {
			t.Tags[tag.Key] = tag.Value
		}
		return nil
	})
	return &dynamodbapi.TagResourceOutput{}, err
}

func (s *Service) untagResource(ctx context.Context, c *kernel.Call, in *dynamodbapi.UntagResourceInput) (smithy.Serializable, *awserr.Error) {
	name, err := taggedName(c.Scope(), in.ResourceArn)
	if err != nil {
		return nil, err
	}
	missing := errTaggedNotFound(in.ResourceArn)
	if _, err := s.recorded(ctx, c, name, missing); err != nil {
		return nil, err
	}
	_, err = s.alter(c, name, missing, func(t *Table, _ clock.Instant) *awserr.Error {
		for _, k := range in.TagKeys {
			delete(t.Tags, k)
		}
		if len(t.Tags) == 0 {
			t.Tags = nil
		}
		return nil
	})
	return &dynamodbapi.UntagResourceOutput{}, err
}

// listTags answers every tag in one page, sorted by key: a table holds at
// most maxTags tags (checkTags), which AWS also answers in one page, so
// NextToken is never set and one given is ignored.
func (s *Service) listTags(ctx context.Context, c *kernel.Call, in *dynamodbapi.ListTagsOfResourceInput) (smithy.Serializable, *awserr.Error) {
	name, err := taggedName(c.Scope(), in.ResourceArn)
	if err != nil {
		return nil, err
	}
	t, err := s.recorded(ctx, c, name, errTaggedNotFound(in.ResourceArn))
	if err != nil {
		return nil, err
	}
	out := &dynamodbapi.ListTagsOfResourceOutput{Tags: make([]dynamodbapi.Tag, 0, len(t.Tags))}
	for _, k := range slices.Sorted(maps.Keys(t.Tags)) {
		out.Tags = append(out.Tags, dynamodbapi.Tag{Key: k, Value: t.Tags[k]})
	}
	return out, nil
}

// taggedName is the table a tag operation's ResourceArn names. An ARN that
// is not a table's is refused with ValidationException; a table of another
// account or region is not found, as for every table operation (inScope).
func taggedName(scope aws.Scope, resource string) (string, *awserr.Error) {
	arn, err := aws.ParseARN(resource)
	name, isTable := strings.CutPrefix(arn.Resource, "table/")
	if err != nil || arn.Service != "dynamodb" || !isTable || name == "" || strings.Contains(name, "/") {
		return "", dynamodbapi.ErrValidationException("Invalid TableArn: Invalid ResourceArn provided as input " + resource)
	}
	if arn.Partition != scope.Partition.String() || arn.Region != scope.Region.String() || arn.Account != scope.Account.String() {
		return "", errTaggedNotFound(resource)
	}
	return name, nil
}

// errTaggedNotFound answers a tag operation on a table the scope lacks.
// The text, with AWS's "ResourcArn", is UNVERIFIED against AWS.
func errTaggedNotFound(resource string) *awserr.Error {
	return dynamodbapi.ErrResourceNotFoundException("Requested resource not found: ResourcArn: " + resource + " not found")
}

// taggedTable names the table a tag operation addresses, so fault rules
// match it by ARN; an ARN that does not parse names nothing.
func taggedTable[In any](resource func(*In) string) kernel.BindOption {
	return kernel.WithResources(func(_ *kernel.Call, in *In) []aws.ARN {
		arn, err := aws.ParseARN(resource(in))
		if err != nil {
			return nil
		}
		return []aws.ARN{arn}
	})
}
