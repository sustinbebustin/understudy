package ssm_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// What step 8 asks about the operations the blind
// TestStep8AsksAboutTheParameterARNs does not call:
// a selector is dropped and a root name loses its slash in the ARN
// (ssm-parameters.md 3.3); an ARN of another account keeps that account;
// GetParametersByPath asks about the path ARN
// without its leading slash and with a trailing slash kept, Path / being
// parameter/ (ssm-parameters.md 7.4; SSM-35 steps 02-03);
// DescribeParameters names no resource; the tag operations name
// the parameter of a Parameter ResourceId; an untagged put declares no
// ssm:AddTagsToResource.
func TestStep8ResourcesOfEveryOperation(t *testing.T) {
	runGated(t, kernel.Allowed{}, func(h *harness, g *gate) {
		id := func(op string) ref.OperationID { return ref.OperationID{Service: "ssm", Name: op} }
		const foreign = "arn:aws:ssm:us-east-1:111111111111:parameter/elsewhere"
		h.put("/iam/a", "v")
		h.put("/r", "v")
		h.mustGet("/iam/a:1", false)
		if err := h.get(foreign); err == nil {
			h.Error("a foreign ARN was found")
		}
		must(h, ssmapi.OpGetParametersByPath, &ssmapi.GetParametersByPathInput{Path: "/"})
		must(h, ssmapi.OpGetParametersByPath, &ssmapi.GetParametersByPathInput{Path: "/iam/"})
		must(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{})
		h.history("r")
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/iam/a", Labels: []string{"l"}})
		must(h, ssmapi.OpUnlabelParameterVersion, &ssmapi.UnlabelParameterVersionInput{Name: "/iam/a", ParameterVersion: 1, Labels: []string{"l"}})
		must(h, ssmapi.OpAddTagsToResource, &ssmapi.AddTagsToResourceInput{ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: "/iam/a", Tags: []ssmapi.Tag{{Key: "k", Value: "v"}}})
		must(h, ssmapi.OpListTagsForResource, &ssmapi.ListTagsForResourceInput{ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: "/iam/a"})
		must(h, ssmapi.OpRemoveTagsFromResource, &ssmapi.RemoveTagsFromResourceInput{ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: "/iam/a", TagKeys: []string{"k"}})
		must(h, ssmapi.OpDeleteParameters, &ssmapi.DeleteParametersInput{Names: []string{"/iam/a", "/iam/missing"}})

		for op, want := range map[string][]string{
			"PutParameter":            {"ssm:PutParameter " + arnPrefix + "iam/a", "ssm:PutParameter " + arnPrefix + "r"},
			"GetParameter":            {"ssm:GetParameter " + arnPrefix + "iam/a", "ssm:GetParameter " + foreign},
			"GetParametersByPath":     {"ssm:GetParametersByPath " + arnPrefix, "ssm:GetParametersByPath " + arnPrefix + "iam/"},
			"DescribeParameters":      {"ssm:DescribeParameters *"},
			"GetParameterHistory":     {"ssm:GetParameterHistory " + arnPrefix + "r"},
			"LabelParameterVersion":   {"ssm:LabelParameterVersion " + arnPrefix + "iam/a"},
			"UnlabelParameterVersion": {"ssm:UnlabelParameterVersion " + arnPrefix + "iam/a"},
			"AddTagsToResource":       {"ssm:AddTagsToResource " + arnPrefix + "iam/a"},
			"ListTagsForResource":     {"ssm:ListTagsForResource " + arnPrefix + "iam/a"},
			"RemoveTagsFromResource":  {"ssm:RemoveTagsFromResource " + arnPrefix + "iam/a"},
			"DeleteParameters":        {"ssm:DeleteParameters " + arnPrefix + "iam/a", "ssm:DeleteParameters " + arnPrefix + "iam/missing"},
		} {
			if got := g.pairs(id(op)); !slices.Equal(got, want) {
				h.Errorf("%s asked %v, want %v", op, got, want)
			}
		}
	})
}

// A tag call on another resource type names nothing understudy stores, so
// step 8 is asked about no parameter (coverage note).
func TestStep8TagsOfOtherResourceTypesNameNoParameter(t *testing.T) {
	runGated(t, kernel.Allowed{}, func(h *harness, g *gate) {
		_, err := call(h, ssmapi.OpListTagsForResource, &ssmapi.ListTagsForResourceInput{ResourceType: ssmapi.ResourceTypeForTaggingDocument, ResourceId: "doc"})
		wantCode(h.T, "ListTagsForResource of a Document", err, ssmapi.IdentityInvalidResourceId())
		if got := g.pairs(ref.OperationID{Service: "ssm", Name: "ListTagsForResource"}); !slices.Equal(got, []string{"ssm:ListTagsForResource *"}) {
			h.Errorf("asked %v, want only the action on *", got)
		}
	})
}
