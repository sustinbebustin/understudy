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

// An overwrite identical to the latest version still creates a version.
// UNVERIFIED (SSM-08), ssm-parameters.md 6.1.
func TestIdenticalOverwriteCreatesAVersion(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/choice/same", "v")
		if out := h.overwrite("/choice/same", "v"); out.Version != 2 {
			h.Errorf("identical overwrite Version = %d, want 2", out.Version)
		}
	})
}

// Tags with Overwrite are refused for a name that does not exist yet too,
// and nothing is created. UNVERIFIED (SSM-03), ssm-parameters.md 7.1.
func TestTagsWithOverwriteRefusedForANewName(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/choice/new", Value: "v", Type: new(ssmapi.ParameterTypeString), Overwrite: new(true),
			Tags: []ssmapi.Tag{{Key: "k", Value: "v"}},
		})
		wantCode(h.T, "Tags with Overwrite on a new name", err, ssmapi.IdentityValidationException())
		if err := h.get("/choice/new"); err == nil {
			h.Error("the refused create was stored")
		}
	})
}

// A create without Type, and KeyId on a String or StringList, on a create
// or an overwrite, are SSM's ValidationException naming the parameter.
// UNVERIFIED (SSM-11), ssm-parameters.md 7.1.
func TestCreateWithoutTypeAndKeyIDOnStringAreRefused(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/choice/untyped", Value: "secret-value"})
		wantCode(h.T, "create without Type", err, ssmapi.IdentityValidationException())
		wantOwnMessage(h.T, "create without Type", err, "secret-value", "/choice/untyped", "Type")
		if err := h.get("/choice/untyped"); err == nil {
			h.Error("the untyped create was stored")
		}

		_, err = call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/choice/kid", Value: "secret-value", Type: new(ssmapi.ParameterTypeStringList), KeyId: new("alias/aws/ssm"),
		})
		wantCode(h.T, "KeyId on a StringList create", err, ssmapi.IdentityValidationException())
		wantOwnMessage(h.T, "KeyId on a StringList create", err, "secret-value", "/choice/kid", "KeyId")

		h.put("/choice/kid", "v")
		_, err = call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/choice/kid", Value: "w", Overwrite: new(true), KeyId: new("alias/aws/ssm")})
		wantCode(h.T, "KeyId on a String overwrite", err, ssmapi.IdentityValidationException())
		if got := h.mustGet("/choice/kid", false); val(got.Value) != "v" || got.Version != 1 {
			h.Errorf("after the refusal = %q version %d, want v 1", val(got.Value), got.Version)
		}
	})
}

// An empty Value is accepted. UNVERIFIED (SSM-11 step 03),
// ssm-parameters.md 7.1.
func TestEmptyValueIsAccepted(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/choice/empty", "")
		if got := h.mustGet("/choice/empty", false); got.Value == nil || *got.Value != "" {
			h.Errorf("Value = %v, want \"\"", got.Value)
		}
	})
}

// A value with {{...}} is SSM's ValidationException naming the parameter;
// an unclosed or reversed pair is an ordinary value. UNVERIFIED (SSM-11
// step 05), ssm-parameters.md 4.1.
func TestDoubleBracesAreSSMValidationException(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/choice/braces", Value: "a{{b}}c", Type: new(ssmapi.ParameterTypeString)})
		wantCode(h.T, "a value with {{b}}", err, ssmapi.IdentityValidationException())
		wantOwnMessage(h.T, "a value with {{b}}", err, "a{{b}}c", "/choice/braces")
		h.put("/choice/half", "only {{ opened")
		h.put("/choice/reversed", "}} then {{")
	})
}

// A hierarchical parameter can't change its type
// (HierarchyTypeMismatchException, nothing written); a root parameter can.
// UNVERIFIED (SSM-09), ssm-parameters.md 4.1.
func TestTypeChange(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/choice/typed", "a")
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/choice/typed", Value: "a,b", Type: new(ssmapi.ParameterTypeStringList), Overwrite: new(true),
		})
		wantCode(h.T, "hierarchical String to StringList", err, ssmapi.IdentityHierarchyTypeMismatchException())
		wantOwnMessage(h.T, "hierarchical String to StringList", err, "a,b", "/choice/typed", "StringList")
		if got := h.mustGet("/choice/typed", false); val(got.Type) != ssmapi.ParameterTypeString || got.Version != 1 {
			h.Errorf("after the refusal = %s version %d, want String 1", val(got.Type), got.Version)
		}

		h.put("rootTyped", "a")
		out := must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "rootTyped", Value: "a,b", Type: new(ssmapi.ParameterTypeStringList), Overwrite: new(true),
		})
		if got := h.mustGet("rootTyped", false); out.Version != 2 || val(got.Type) != ssmapi.ParameterTypeStringList {
			h.Errorf("root type change = version %d type %s, want 2 StringList", out.Version, val(got.Type))
		}
	})
}

// A StringList value is stored verbatim, spaces after commas included.
// UNVERIFIED (SSM-28), ssm-parameters.md 4.1.
func TestStringListIsStoredVerbatim(t *testing.T) {
	run(t, func(h *harness) {
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/choice/list", Value: "a, b,c", Type: new(ssmapi.ParameterTypeStringList)})
		if got := h.mustGet("/choice/list", false); val(got.Value) != "a, b,c" || val(got.Type) != ssmapi.ParameterTypeStringList {
			h.Errorf("StringList = %q %s, want \"a, b,c\" StringList", val(got.Value), val(got.Type))
		}
	})
}

// A deleted name can be created again at once, at version 1 with no tags.
// UNVERIFIED (SSM-26), ssm-parameters.md 7.7.
func TestRecreateAfterDeleteStartsFresh(t *testing.T) {
	run(t, func(h *harness) {
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/choice/again", Value: "v", Type: new(ssmapi.ParameterTypeString), Tags: []ssmapi.Tag{{Key: "k", Value: "v"}},
		})
		h.overwrite("/choice/again", "w")
		must(h, ssmapi.OpDeleteParameter, &ssmapi.DeleteParameterInput{Name: "/choice/again"})
		if out := h.put("/choice/again", "x"); out.Version != 1 {
			h.Errorf("re-create Version = %d, want 1", out.Version)
		}
		tags := must(h, ssmapi.OpListTagsForResource, &ssmapi.ListTagsForResourceInput{
			ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: "/choice/again",
		}).TagList
		if len(tags) != 0 {
			h.Errorf("re-created parameter has tags %+v, want none", tags)
		}
	})
}

// Past 100 versions the oldest unlabeled one is dropped and numbering keeps
// counting: the 101st write is version 101. UNVERIFIED (SSM-30),
// ssm-parameters.md 6.1.
func TestVersionNumbersContinuePastTheCap(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/choice/many", "v")
		for range 100 {
			h.overwrite("/choice/many", "v")
		}
		if got := h.mustGet("/choice/many", false); got.Version != 101 {
			h.Errorf("Version after 101 writes = %d, want 101", got.Version)
		}
	})
}
