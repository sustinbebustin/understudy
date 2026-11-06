package apigateway

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// Tag limits: "50 tags per stage" is documented (apigateway-rest.md 3.9,
// 2.6) and held for REST APIs too; the key and value lengths are AWS's
// general tag limits, inferred for API Gateway (APIGW1-02).
const (
	maxTags        = 50
	maxTagKeyLen   = 128
	maxTagValueLen = 256
)

// checkTags refuses a set of tags over the limits; the texts are inferred.
func checkTags(tags map[string]string) *awserr.Error {
	if len(tags) > maxTags {
		return errBadRequest("a resource holds at most %d tags, not %d", maxTags, len(tags))
	}
	for _, k := range slices.Sorted(maps.Keys(tags)) {
		if n := utf8.RuneCountInString(k); n < 1 || n > maxTagKeyLen {
			return errBadRequest("tag key %q is %d characters long; it must be 1 to %d", k, n, maxTagKeyLen)
		}
		if n := utf8.RuneCountInString(tags[k]); n > maxTagValueLen {
			return errBadRequest("the value of tag %q is %d characters long; it must be at most %d", k, n, maxTagValueLen)
		}
	}
	return nil
}

// tagsOut is tags as GetTags and UpdateRestApi carry them: a copy, {} when
// there are none.
func tagsOut(tags map[string]string) map[string]string {
	out := make(map[string]string, len(tags))
	maps.Copy(out, tags)
	return out
}

// tagsOrNil is tags as GetRestApi and GetStage carry them: absent when
// there are none (recorded, apigateway-rest.md 2.3).
func tagsOrNil(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	return maps.Clone(tags)
}

// tagged is what a tag operation's resourceArn names: a REST API, or one
// of its stages when stage is set (apigateway-rest.md 3.9).
type tagged struct {
	apiID string
	stage string
}

// parseTagged reads a tag operation's resourceArn:
// arn:<partition>:apigateway:<region>::/restapis/<id>[/stages/<name>]
// (aws-doc arn-format-reference, apigateway-rest.md 3.9). Text that is no
// API Gateway ARN is BadRequestException; an ARN naming anything else, or
// another partition or region, is NotFoundException. The texts are
// inferred.
func parseTagged(scope aws.Scope, text string) (tagged, *awserr.Error) {
	arn, err := aws.ParseARN(text)
	if err != nil || arn.Service != "apigateway" {
		return tagged{}, errBadRequest("resourceArn %q is not an API Gateway resource ARN: arn:aws:apigateway:<region>::/restapis/<id>[/stages/<stage-name>]", text)
	}
	notFound := apigatewayapi.ErrNotFoundException(fmt.Sprintf("resourceArn %s names no REST API or stage of account %s in %s", text, scope.Account, scope.Region))
	if arn.Partition != scope.Partition.String() || arn.Region != scope.Region.String() || arn.Account != "" {
		return tagged{}, notFound
	}
	rest, ok := strings.CutPrefix(arn.Resource, "/restapis/")
	if !ok {
		return tagged{}, notFound
	}
	id, sub, hasSub := strings.Cut(rest, "/")
	if id == "" {
		return tagged{}, notFound
	}
	if !hasSub {
		return tagged{apiID: id}, nil
	}
	stage, ok := strings.CutPrefix(sub, "stages/")
	if !ok || stage == "" || strings.Contains(stage, "/") {
		return tagged{}, notFound
	}
	return tagged{apiID: id, stage: stage}, nil
}

// tagResource adds tags to a REST API or a stage, replacing the values of
// keys it already has; they are the tags CreateRestApi and CreateStage set
// (apigateway-rest.md 3.9).
func (s *Service) tagResource(_ context.Context, c *kernel.Call, in *apigatewayapi.TagResourceInput) (*apigatewayapi.TagResourceOutput, *awserr.Error) {
	if err := s.retag(c, in.ResourceArn, func(tags map[string]string) (map[string]string, *awserr.Error) {
		merged := tagsOut(tags)
		maps.Copy(merged, in.Tags)
		return merged, checkTags(merged)
	}); err != nil {
		return nil, err
	}
	return &apigatewayapi.TagResourceOutput{}, nil
}

// untagResource removes the tags with the keys given; a key the resource
// does not have is ignored.
func (s *Service) untagResource(_ context.Context, c *kernel.Call, in *apigatewayapi.UntagResourceInput) (*apigatewayapi.UntagResourceOutput, *awserr.Error) {
	if err := s.retag(c, in.ResourceArn, func(tags map[string]string) (map[string]string, *awserr.Error) {
		kept := tagsOut(tags)
		for _, k := range in.TagKeys {
			delete(kept, k)
		}
		return kept, nil
	}); err != nil {
		return nil, err
	}
	return &apigatewayapi.UntagResourceOutput{}, nil
}

// getTags answers the tags of a REST API or a stage, {} when it has none.
// It reads every tag at once: position and limit page nothing (a resource
// holds at most 50).
func (s *Service) getTags(_ context.Context, c *kernel.Call, in *apigatewayapi.GetTagsInput) (*apigatewayapi.GetTagsOutput, *awserr.Error) {
	scope := c.Scope()
	t, err := parseTagged(scope, in.ResourceArn)
	if err != nil {
		return nil, err
	}
	type found struct {
		tags map[string]string
		err  *awserr.Error
	}
	got := readState(s, scope, func(st *State) found {
		a, ok := st.APIs[t.apiID]
		if !ok {
			return found{err: errAPINotFound(scope, t.apiID)}
		}
		if t.stage == "" {
			return found{tags: tagsOut(a.Tags)}
		}
		stage, _, ok := a.stage(t.stage)
		if !ok {
			return found{err: errStageNotFound()}
		}
		return found{tags: tagsOut(stage.Tags)}
	})
	if got.err != nil {
		return nil, got.err
	}
	return &apigatewayapi.GetTagsOutput{Tags: got.tags}, nil
}

// retag sets the tags of what text names to what change makes of a copy
// of them.
func (s *Service) retag(c *kernel.Call, text string, change func(map[string]string) (map[string]string, *awserr.Error)) *awserr.Error {
	scope := c.Scope()
	t, err := parseTagged(scope, text)
	if err != nil {
		return err
	}
	_, err = kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		a, ok := st.APIs[t.apiID]
		if !ok {
			return struct{}{}, errAPINotFound(scope, t.apiID)
		}
		if t.stage == "" {
			tags, err := change(a.Tags)
			if err != nil {
				return struct{}{}, err
			}
			a.Tags = tags
			st.APIs[a.ID] = a
			return struct{}{}, nil
		}
		stage, i, ok := a.stage(t.stage)
		if !ok {
			return struct{}{}, errStageNotFound()
		}
		tags, err := change(stage.Tags)
		if err != nil {
			return struct{}{}, err
		}
		stage = stage.clone()
		stage.Tags = tags
		a.Stages = slices.Clone(a.Stages)
		a.Stages[i] = stage
		st.APIs[a.ID] = a
		return struct{}{}, nil
	})
	return err
}

// tagsResource is the kernel.WithResources resolver of a tag operation:
// the tagged resource itself, as AWS's denials name it, with the primary
// action GET or PATCH (APIGW1-22, recorded 2026-11-03: steps 13-16;
// apigateway-rest.md 3.10). Whether AWS also asks about /tags/... is
// unrecorded: the tagged resource's denial came first. A resourceArn that
// is not an ARN, which the call refuses, is named as /tags/<the text with
// every byte outside [A-Za-z0-9-_.~] written %XX>.
func tagsResource[In any](tagged func(*In) string) func(*kernel.Call, *In) []aws.ARN {
	return func(c *kernel.Call, in *In) []aws.ARN {
		const hex = "0123456789ABCDEF"
		text := tagged(in)
		if arn, err := aws.ParseARN(text); err == nil {
			return []aws.ARN{arn}
		}
		var b strings.Builder
		for i := range len(text) {
			switch ch := text[i]; {
			case 'A' <= ch && ch <= 'Z', 'a' <= ch && ch <= 'z', '0' <= ch && ch <= '9', strings.IndexByte("-_.~", ch) >= 0:
				b.WriteByte(ch)
			default:
				b.Write([]byte{'%', hex[ch>>4], hex[ch&0xF]})
			}
		}
		return []aws.ARN{resourceARN(c.Scope(), "/tags/"+b.String())}
	}
}
