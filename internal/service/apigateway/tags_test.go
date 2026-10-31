package apigateway_test

import (
	"maps"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
)

// Tags on CreateRestApi and CreateStage are the resource's tags, returned
// by GetRestApi and GetStage, where Terraform reads them; TagResource
// merges, UntagResource removes tagKeys and GetTags answers them, for the
// REST API ARN arn:aws:apigateway:<region>::/restapis/<id> and the stage
// ARN .../stages/<name> (aws-doc arn-format-reference; apigateway-rest.md
// 3.9).
func TestTags(t *testing.T) {
	run(t, func(h *harness) {
		out := must(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "t", Tags: map[string]string{"a": "1"}})
		id := *out.Id
		if got := must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id}).Tags; !maps.Equal(got, map[string]string{"a": "1"}) {
			t.Errorf("GetRestApi tags = %v, want a=1", got)
		}
		must(h, apigatewayapi.OpTagResource, &apigatewayapi.TagResourceInput{ResourceArn: apiARN(id), Tags: map[string]string{"b": "2", "a": "3"}})
		if got := must(h, apigatewayapi.OpGetTags, &apigatewayapi.GetTagsInput{ResourceArn: apiARN(id)}).Tags; !maps.Equal(got, map[string]string{"a": "3", "b": "2"}) {
			t.Errorf("GetTags after TagResource = %v, want a=3 b=2", got)
		}
		must(h, apigatewayapi.OpUntagResource, &apigatewayapi.UntagResourceInput{ResourceArn: apiARN(id), TagKeys: []string{"a"}})
		if got := must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id}).Tags; !maps.Equal(got, map[string]string{"b": "2"}) {
			t.Errorf("GetRestApi tags after UntagResource = %v, want b=2", got)
		}

		root := *out.RootResourceId
		h.method(id, root, "GET")
		must(h, apigatewayapi.OpPutIntegration, &apigatewayapi.PutIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", Type: apigatewayapi.IntegrationTypeMock})
		dep := *must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id}).Id
		must(h, apigatewayapi.OpCreateStage, &apigatewayapi.CreateStageInput{RestApiId: id, StageName: "s", DeploymentId: dep, Tags: map[string]string{"st": "x"}})
		stageARN := apiARN(id) + "/stages/s"
		if got := must(h, apigatewayapi.OpGetStage, &apigatewayapi.GetStageInput{RestApiId: id, StageName: "s"}).Tags; !maps.Equal(got, map[string]string{"st": "x"}) {
			t.Errorf("GetStage tags = %v, want st=x", got)
		}
		must(h, apigatewayapi.OpTagResource, &apigatewayapi.TagResourceInput{ResourceArn: stageARN, Tags: map[string]string{"k": "v"}})
		if got := must(h, apigatewayapi.OpGetTags, &apigatewayapi.GetTagsInput{ResourceArn: stageARN}).Tags; !maps.Equal(got, map[string]string{"st": "x", "k": "v"}) {
			t.Errorf("GetTags on the stage = %v, want st=x k=v", got)
		}
		// An ARN naming no REST API of the caller's scope is NotFoundException
		// (contract 7.6; text inferred: identity only).
		wantError(t, fail(h, apigatewayapi.OpGetTags, &apigatewayapi.GetTagsInput{ResourceArn: apiARN("zzzzzzzzzz")}), notFound)
	})
}
