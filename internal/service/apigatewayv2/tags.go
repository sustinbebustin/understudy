package apigatewayv2

import (
	"context"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// Tag limits. "Tags per stage: 50" is the HTTP API quota
// (apigateway-http.md 3.6), held for APIs too; keys are 1 to 128 and
// values 0 to 256 characters (APIGW2-01, recorded 2026-11-03: steps 24
// and 25).
const (
	maxTags        = 50
	maxTagKeyLen   = 128
	maxTagValueLen = 256
)

// checkTags refuses a set of tags over the limits. A key or value too
// long gets the Coral text AWS sent for TagResource, naming member
// (addedTags there; APIGW2-01 steps 24 and 25) and the map as Java prints
// it, {k=v, ...}, in sorted order (the order is inferred).
func checkTags(member string, tags map[string]string) *awserr.Error {
	if len(tags) > maxTags {
		return errBadRequest("a resource holds at most %d tags, not %d", maxTags, len(tags))
	}
	keys := slices.Sorted(maps.Keys(tags))
	shown := make([]string, len(keys))
	for i, k := range keys {
		shown[i] = k + "=" + tags[k]
	}
	refuse := func(what string, most, least int) *awserr.Error {
		return errBadRequest("1 validation error detected: Value '{%s}' at '%s' failed to satisfy constraint: %s must satisfy constraint: "+
			"[Member must have length less than or equal to %d, Member must have length greater than or equal to %d]",
			strings.Join(shown, ", "), member, what, most, least)
	}
	for _, k := range keys {
		if n := utf8.RuneCountInString(k); n < 1 || n > maxTagKeyLen {
			return refuse("Map keys", maxTagKeyLen, 1)
		}
		if utf8.RuneCountInString(tags[k]) > maxTagValueLen {
			return refuse("Map value", maxTagValueLen, 0)
		}
	}
	return nil
}

// tagsOut is tags as an output carries them: a copy, present and {} when
// there are none (apigateway-http.md 3.1, 3.4).
func tagsOut(tags map[string]string) map[string]string {
	out := make(map[string]string, len(tags))
	maps.Copy(out, tags)
	return out
}

// tagged is what a tag operation's ResourceArn names: an API, or one of
// its stages when stage is set (apigateway-http.md 2.5, 3.6).
type tagged struct {
	apiID string
	stage string
}

// parseTagged reads a tag operation's ResourceArn, the path label clients
// send with its "/" escaped as %2F (apigateway-http.md 2.2):
// arn:<partition>:apigateway:<region>::/apis/<api-id>[/stages/<name>].
// Text that is no API Gateway ARN is BadRequestException; an ARN naming
// anything else, or another partition, region or account, is
// NotFoundException. The texts are inferred (APIGW2-01 case 23).
func parseTagged(scope aws.Scope, text string) (tagged, *awserr.Error) {
	arn, err := aws.ParseARN(text)
	if err != nil || arn.Service != "apigateway" {
		return tagged{}, errBadRequest("resourceArn %q is not an API Gateway resource ARN: arn:aws:apigateway:<region>::/apis/<api-id>[/stages/<stage-name>]", text)
	}
	notFound := errNotFound("resourceArn %s names no HTTP API or stage of account %s in %s", text, scope.Account, scope.Region)
	if arn.Partition != scope.Partition.String() || arn.Region != scope.Region.String() || arn.Account != "" {
		return tagged{}, notFound
	}
	rest, ok := strings.CutPrefix(arn.Resource, "/apis/")
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

// tagResource adds tags to an API or a stage, replacing the values of keys
// it already has; the tags are the same ones CreateApi and CreateStage set
// (apigateway-http.md 3.6).
func (s *Service) tagResource(_ context.Context, c *kernel.Call, in *apigatewayv2api.TagResourceInput) (*apigatewayv2api.TagResourceOutput, *awserr.Error) {
	t, err := parseTagged(c.Scope(), in.ResourceArn)
	if err != nil {
		return nil, err
	}
	if _, err := writeAPI(s, c, t.apiID, func(a *API, _ clock.Instant) (struct{}, *awserr.Error) {
		return struct{}{}, a.retag(t, func(tags map[string]string) (map[string]string, *awserr.Error) {
			merged := tagsOut(tags)
			maps.Copy(merged, in.Tags)
			if len(merged) > maxTags {
				return nil, errBadRequest("%s would hold %d tags; a resource holds at most %d", in.ResourceArn, len(merged), maxTags)
			}
			return merged, nil
		})
	}); err != nil {
		return nil, err
	}
	return &apigatewayv2api.TagResourceOutput{}, nil
}

// untagResource removes the tags with the keys given from an API or a
// stage; a key it does not have is ignored.
func (s *Service) untagResource(_ context.Context, c *kernel.Call, in *apigatewayv2api.UntagResourceInput) (*apigatewayv2api.UntagResourceOutput, *awserr.Error) {
	t, err := parseTagged(c.Scope(), in.ResourceArn)
	if err != nil {
		return nil, err
	}
	if _, err := writeAPI(s, c, t.apiID, func(a *API, _ clock.Instant) (struct{}, *awserr.Error) {
		return struct{}{}, a.retag(t, func(tags map[string]string) (map[string]string, *awserr.Error) {
			kept := tagsOut(tags)
			for _, k := range in.TagKeys {
				delete(kept, k)
			}
			return kept, nil
		})
	}); err != nil {
		return nil, err
	}
	return &apigatewayv2api.UntagResourceOutput{}, nil
}

// getTags answers the tags of an API or a stage, {} when it has none.
func (s *Service) getTags(_ context.Context, c *kernel.Call, in *apigatewayv2api.GetTagsInput) (*apigatewayv2api.GetTagsOutput, *awserr.Error) {
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
			// AWS's GetTags text (APIGW2-01, recorded 2026-11-03: step 23).
			return found{err: errNotFound("Resource was not found")}
		}
		if t.stage == "" {
			return found{tags: tagsOut(a.Tags)}
		}
		stage, ok := a.stage(t.stage)
		if !ok {
			return found{err: errStageNotFound()}
		}
		return found{tags: tagsOut(stage.Tags)}
	})
	if got.err != nil {
		return nil, got.err
	}
	return &apigatewayv2api.GetTagsOutput{Tags: got.tags}, nil
}

// retag sets the tags of the API or stage t names to what change makes of
// a copy of them.
func (a *API) retag(t tagged, change func(map[string]string) (map[string]string, *awserr.Error)) *awserr.Error {
	if t.stage == "" {
		tags, err := change(a.Tags)
		if err != nil {
			return err
		}
		a.Tags = tags
		return nil
	}
	i := slices.IndexFunc(a.Stages, func(st Stage) bool { return st.Name == t.stage })
	if i < 0 {
		return errStageNotFound()
	}
	tags, err := change(a.Stages[i].Tags)
	if err != nil {
		return err
	}
	a.Stages = slices.Clone(a.Stages)
	a.Stages[i].Tags = tags
	return nil
}

// errStageNotFound answers a stage the API does not have, with AWS's text
// (APIGW2-01 step 05).
func errStageNotFound() *awserr.Error {
	return errNotFound("Invalid stage identifier specified")
}
