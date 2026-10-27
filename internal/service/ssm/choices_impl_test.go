package ssm_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// An untagged parameter lists TagList [], present and empty, which
// Terraform reads tags from. UNVERIFIED (SSM-25), ssm-parameters.md 7.9.
func TestUntaggedParameterListsEmptyTagList(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/choice/untagged", "v")
		out := must(h, ssmapi.OpListTagsForResource, &ssmapi.ListTagsForResourceInput{
			ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: "/choice/untagged",
		})
		if out.TagList == nil || len(out.TagList) != 0 {
			h.Errorf("TagList = %#v, want [] present", out.TagList)
		}
	})
}
