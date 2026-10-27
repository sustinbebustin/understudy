package ssm_test

import (
	"fmt"
	"strings"
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

// Value size is counted in UTF-8 bytes: 4096 bytes fit Standard, 4097 do
// not, and 2049 two-byte characters (4098 bytes) do not either; Advanced
// takes 8192 bytes, not 8193. An oversize value is SSM's
// ValidationException naming the parameter, the limit and the measured
// bytes, never the value, and nothing is written. UNVERIFIED (SSM-12),
// ssm-parameters.md 4.2.
func TestValueSizeIsCountedInBytes(t *testing.T) {
	run(t, func(h *harness) {
		std, adv := new(ssmapi.ParameterTierStandard), new(ssmapi.ParameterTierAdvanced)
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/size/std", Value: strings.Repeat("a", 4096), Type: new(ssmapi.ParameterTypeString), Tier: std})
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/size/adv", Value: strings.Repeat("a", 8192), Type: new(ssmapi.ParameterTypeString), Tier: adv})
		for _, c := range []struct {
			name, value, limit, size string
			tier                     *ssmapi.ParameterTier
		}{
			{"/size/std-over", strings.Repeat("b", 4097), "4096", "4097", std},
			{"/size/std-utf8", strings.Repeat("é", 2049), "4096", "4098", std},
			{"/size/adv-over", strings.Repeat("c", 8193), "8192", "8193", adv},
		} {
			_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: c.name, Value: c.value, Type: new(ssmapi.ParameterTypeString), Tier: c.tier})
			wantCode(h.T, c.name, err, ssmapi.IdentityValidationException())
			wantOwnMessage(h.T, c.name, err, c.value, c.name, c.limit, c.size)
			if err := h.get(c.name); err == nil {
				h.Errorf("%s was stored", c.name)
			}
		}
	})
}

// The six structural regexp errors no dialect accepts answer
// InvalidAllowedPatternException naming the parameter and the pattern, and
// nothing is written. UNVERIFIED (SSM-15), ssm-parameters.md 4.4.
func TestStructuralAllowedPatternErrors(t *testing.T) {
	run(t, func(h *harness) {
		// Missing bracket, missing paren, unexpected paren, trailing
		// backslash, missing repeat argument, invalid character range; the
		// last two would pair with an anchoring group if wrapped first.
		for _, p := range []string{`[a`, `(a`, `a)`, `a\`, `*a`, `[z-a]`, `a)(b`} {
			_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/pat/bad", Value: "secret-value", Type: new(ssmapi.ParameterTypeString), AllowedPattern: new(p)})
			wantCode(h.T, "AllowedPattern "+p, err, ssmapi.IdentityInvalidAllowedPatternException())
			wantOwnMessage(h.T, "AllowedPattern "+p, err, "secret-value", "/pat/bad", p)
		}
		if err := h.get("/pat/bad"); err == nil {
			h.Error("a parameter with an invalid pattern was stored")
		}
	})
}

// An Intelligent-Tiering request answers the resolved tier, never
// Intelligent-Tiering; an Advanced parameter overwritten without Tier, or
// with Intelligent-Tiering and a small value, stays Advanced. UNVERIFIED
// (SSM-13), ssm-parameters.md 4.2.
func TestIntelligentTieringAnswersTheResolvedTier(t *testing.T) {
	run(t, func(h *harness) {
		it := new(ssmapi.ParameterTierIntelligentTiering)
		out := must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/it/a", Value: "v", Type: new(ssmapi.ParameterTypeString), Tier: it})
		if val(out.Tier) != ssmapi.ParameterTierStandard {
			h.Errorf("Intelligent-Tiering small response Tier = %q, want Standard", val(out.Tier))
		}
		out = must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/it/a", Value: strings.Repeat("x", 4097), Overwrite: new(true), Tier: it})
		if val(out.Tier) != ssmapi.ParameterTierAdvanced {
			h.Errorf("Intelligent-Tiering 4097 bytes response Tier = %q, want Advanced", val(out.Tier))
		}
		if out := h.overwrite("/it/a", "small"); val(out.Tier) != ssmapi.ParameterTierAdvanced {
			h.Errorf("overwrite without Tier response Tier = %q, want Advanced", val(out.Tier))
		}
		out = must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/it/a", Value: "tiny", Overwrite: new(true), Tier: it})
		if val(out.Tier) != ssmapi.ParameterTierAdvanced {
			h.Errorf("Intelligent-Tiering small on Advanced response Tier = %q, want Advanced", val(out.Tier))
		}
		if got := val(h.mustDescribeOne("/it/a").Tier); got != ssmapi.ParameterTierAdvanced {
			h.Errorf("DescribeParameters Tier = %q, want Advanced", got)
		}
	})
}

// An empty AllowedPattern on an overwrite clears the stored pattern, so a
// value the old pattern refused is accepted. UNVERIFIED (SSM-10 step 04),
// ssm-parameters.md 7.1.
func TestEmptyAllowedPatternClears(t *testing.T) {
	run(t, func(h *harness) {
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/pat/clear", Value: "12", Type: new(ssmapi.ParameterTypeString), AllowedPattern: new(`\d+`)})
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/pat/clear", Value: "letters", Overwrite: new(true), AllowedPattern: new("")})
		if d := h.mustDescribeOne("/pat/clear"); d.AllowedPattern != nil {
			h.Errorf("AllowedPattern = %q after an empty one, want absent", *d.AllowedPattern)
		}
		h.overwrite("/pat/clear", "more letters")
	})
}

// The 10,001st Standard parameter of a scope is ParameterLimitExceeded
// (ssm-parameters.md 4.2, 9.3 [D]) naming the parameter and the limit, and
// is not created; an Intelligent-Tiering create then resolves to Advanced
// (4.2 [D]), and an overwrite of a Standard parameter still fits.
// UNVERIFIED (SSM-33), ssm-parameters.md 9.3: the text.
func TestStandardParameterCount(t *testing.T) {
	run(t, func(h *harness) {
		for i := range 10_000 {
			h.put(fmt.Sprintf("/count/p%05d", i), "v")
		}
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/count/over", Value: "secret-value", Type: new(ssmapi.ParameterTypeString)})
		wantCode(h.T, "the 10,001st Standard parameter", err, ssmapi.IdentityParameterLimitExceeded())
		wantOwnMessage(h.T, "the 10,001st Standard parameter", err, "secret-value", "/count/over", "10000")
		if err := h.get("/count/over"); err == nil {
			h.Error("the 10,001st Standard parameter was stored")
		}
		out := must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/count/it", Value: "v", Type: new(ssmapi.ParameterTypeString), Tier: new(ssmapi.ParameterTierIntelligentTiering),
		})
		if val(out.Tier) != ssmapi.ParameterTierAdvanced {
			h.Errorf("Intelligent-Tiering with 10,000 Standard parameters = %q, want Advanced", val(out.Tier))
		}
		if out := h.overwrite("/count/p00000", "w"); val(out.Tier) != ssmapi.ParameterTierStandard || out.Version != 2 {
			h.Errorf("overwrite of a Standard parameter at the limit = %q version %d, want Standard 2", val(out.Tier), out.Version)
		}
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/count/adv", Value: "v", Type: new(ssmapi.ParameterTypeString), Tier: new(ssmapi.ParameterTierAdvanced),
		})
	})
}
