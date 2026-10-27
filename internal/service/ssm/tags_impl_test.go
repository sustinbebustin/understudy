package ssm_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// DeleteParameters lists a deleted name as the request spelled it,
// trimmed, answers a name given twice once, and lists a name that is not
// valid in InvalidParameters. UNVERIFIED (SSM-27), ssm-parameters.md 7.7.
func TestDeleteParametersChoices(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/delc/a", "v")
		h.put("root", "v")
		out := must(h, ssmapi.OpDeleteParameters, &ssmapi.DeleteParametersInput{
			Names: []string{" /delc/a ", "/delc/a", "bad name", "/root"},
		})
		if !slices.Equal(out.DeletedParameters, []string{"/delc/a", "/root"}) {
			h.Errorf("DeletedParameters = %v, want [/delc/a /root]", out.DeletedParameters)
		}
		if !slices.Equal(out.InvalidParameters, []string{"bad name"}) {
			h.Errorf("InvalidParameters = %v, want [bad name]", out.InvalidParameters)
		}
		if err := h.get("root"); err == nil {
			h.Error("root still reads after DeleteParameters named it /root")
		}
	})
}

// One reserved name makes the whole DeleteParameters call 501, and nothing
// is deleted (ssm-parameters.md 12.1).
func TestDeleteParametersReservedDeletesNothing(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/delr/a", "v")
		_, err := call(h, ssmapi.OpDeleteParameters, &ssmapi.DeleteParametersInput{
			Names: []string{"/delr/a", "/aws/service/x"},
		})
		wantCode(h.T, "DeleteParameters with a reserved name", err, ssmapi.IdentityNotImplemented())
		if err := h.get("/delr/a"); err != nil {
			h.Errorf("/delr/a was deleted by a refused call: %v", err)
		}
	})
}

// RemoveTagsFromResource ignores a key the parameter does not carry
// (UNVERIFIED (SSM-25), ssm-parameters.md 7.9); a missing parameter, a
// ResourceId that is not a valid name and another resource type are
// InvalidResourceId (ListTagsForResource's coverage note).
func TestRemoveTagsChoices(t *testing.T) {
	run(t, func(h *harness) {
		param := ssmapi.ResourceTypeForTaggingParameter
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/rmt/a", Value: "v", Type: new(ssmapi.ParameterTypeString), Tags: []ssmapi.Tag{{Key: "k", Value: "v"}},
		})
		must(h, ssmapi.OpRemoveTagsFromResource, &ssmapi.RemoveTagsFromResourceInput{ResourceType: param, ResourceId: "/rmt/a", TagKeys: []string{"absent"}})
		out := must(h, ssmapi.OpListTagsForResource, &ssmapi.ListTagsForResourceInput{ResourceType: param, ResourceId: "/rmt/a"})
		got := map[string]string{}
		for _, t := range out.TagList {
			got[t.Key] = t.Value
		}
		if !maps.Equal(got, map[string]string{"k": "v"}) {
			h.Errorf("tags after removing an absent key = %v, want k=v", got)
		}
		for _, id := range []string{"/rmt/missing", "bad name"} {
			_, err := call(h, ssmapi.OpRemoveTagsFromResource, &ssmapi.RemoveTagsFromResourceInput{ResourceType: param, ResourceId: id, TagKeys: []string{"k"}})
			wantCode(h.T, "RemoveTagsFromResource of "+id, err, ssmapi.IdentityInvalidResourceId())
		}
		_, err := call(h, ssmapi.OpRemoveTagsFromResource, &ssmapi.RemoveTagsFromResourceInput{ResourceType: ssmapi.ResourceTypeForTaggingDocument, ResourceId: "/rmt/a", TagKeys: []string{"k"}})
		wantCode(h.T, "RemoveTagsFromResource of a Document", err, ssmapi.IdentityInvalidResourceId())
	})
}
