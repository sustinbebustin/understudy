package ssm_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// An untagged parameter lists TagList [], present and empty, which
// Terraform reads tags from (ssm-parameters.md 7.9; SSM-25 step 01).
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

// An overwrite identical to the latest version still creates a version
// (ssm-parameters.md 6.1; SSM-08 step 01).
func TestIdenticalOverwriteCreatesAVersion(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/choice/same", "v")
		if out := h.overwrite("/choice/same", "v"); out.Version != 2 {
			h.Errorf("identical overwrite Version = %d, want 2", out.Version)
		}
	})
}

// Tags with Overwrite are refused for a name that does not exist yet too,
// and nothing is created (ssm-parameters.md 7.1; SSM-03 step 03).
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
// or an overwrite, are SSM's ValidationException with AWS's texts, and
// nothing is written (ssm-parameters.md 7.1; SSM-11 steps 01-02).
func TestCreateWithoutTypeAndKeyIDOnStringAreRefused(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/choice/untyped", Value: "secret-value"})
		wantError(h.T, "create without Type", err, ssmapi.IdentityValidationException(), "A parameter type is required when you create a parameter.")
		if err := h.get("/choice/untyped"); err == nil {
			h.Error("the untyped create was stored")
		}

		const keyIDText = "KeyId is required for SecureString type parameter only."
		_, err = call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/choice/kid", Value: "secret-value", Type: new(ssmapi.ParameterTypeStringList), KeyId: new("alias/aws/ssm"),
		})
		wantError(h.T, "KeyId on a StringList create", err, ssmapi.IdentityValidationException(), keyIDText)

		h.put("/choice/kid", "v")
		_, err = call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/choice/kid", Value: "w", Overwrite: new(true), KeyId: new("alias/aws/ssm")})
		wantError(h.T, "KeyId on a String overwrite", err, ssmapi.IdentityValidationException(), keyIDText)
		if got := h.mustGet("/choice/kid", false); val(got.Value) != "v" || got.Version != 1 {
			h.Errorf("after the refusal = %q version %d, want v 1", val(got.Value), got.Version)
		}
	})
}

// An empty Value is refused by the generated length check, the sensitive
// value left out of the Coral text, and nothing is written
// (ssm-parameters.md 7.1; SSM-11 step 03).
func TestEmptyValueIsRefused(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/choice/empty", Value: "", Type: new(ssmapi.ParameterTypeString)})
		coralValidation(h.T, "empty Value", err, "1 validation error detected: Value at 'value' failed to satisfy constraint: Member must have length greater than or equal to 1")
		if err := h.get("/choice/empty"); err == nil {
			h.Error("the empty value was stored")
		}
	})
}

// A value with {{...}} is SSM's ValidationException without the value; an
// unclosed or reversed pair is an ordinary value (ssm-parameters.md 4.1;
// SSM-11 step 05).
func TestDoubleBracesAreSSMValidationException(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/choice/braces", Value: "a{{b}}c", Type: new(ssmapi.ParameterTypeString)})
		wantCode(h.T, "a value with {{b}}", err, ssmapi.IdentityValidationException())
		if err != nil && strings.Contains(err.Message, "a{{b}}c") {
			h.Errorf("message %q holds the request's value", err.Message)
		}
		h.put("/choice/half", "only {{ opened")
		h.put("/choice/reversed", "}} then {{")
	})
}

// A parameter changes type on overwrite, in a hierarchy as at the root
// (ssm-parameters.md 4.1; SSM-09).
func TestTypeChange(t *testing.T) {
	run(t, func(h *harness) {
		for _, name := range []string{"/choice/typed", "rootTyped"} {
			h.put(name, "a")
			out := must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
				Name: name, Value: "a,b", Type: new(ssmapi.ParameterTypeStringList), Overwrite: new(true),
			})
			if got := h.mustGet(name, false); out.Version != 2 || val(got.Type) != ssmapi.ParameterTypeStringList {
				h.Errorf("%s type change = version %d type %s, want 2 StringList", name, out.Version, val(got.Type))
			}
		}
	})
}

// A StringList value is stored verbatim, spaces after commas included
// (ssm-parameters.md 4.1; SSM-28).
func TestStringListIsStoredVerbatim(t *testing.T) {
	run(t, func(h *harness) {
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/choice/list", Value: "a, b,c", Type: new(ssmapi.ParameterTypeStringList)})
		if got := h.mustGet("/choice/list", false); val(got.Value) != "a, b,c" || val(got.Type) != ssmapi.ParameterTypeStringList {
			h.Errorf("StringList = %q %s, want \"a, b,c\" StringList", val(got.Value), val(got.Type))
		}
	})
}

// A deleted name can be created again at once, at version 1 with no tags
// (ssm-parameters.md 7.7; SSM-26).
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
// counting: the 101st write is version 101 (ssm-parameters.md 6.1; SSM-30
// step 02).
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

// Value size is counted in characters: 4096 fit Standard, 4097 do not,
// and 2049 two-byte characters (4098 bytes) do; Advanced takes 8192, not
// 8193. An oversize value answers AWS's ValidationException, never naming
// the value, and nothing is written (ssm-parameters.md 4.2; SSM-12).
func TestValueSizeIsCountedInCharacters(t *testing.T) {
	run(t, func(h *harness) {
		std, adv := new(ssmapi.ParameterTierStandard), new(ssmapi.ParameterTierAdvanced)
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/size/std", Value: strings.Repeat("a", 4096), Type: new(ssmapi.ParameterTypeString), Tier: std})
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/size/std-utf8", Value: strings.Repeat("é", 2049), Type: new(ssmapi.ParameterTypeString), Tier: std})
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/size/adv", Value: strings.Repeat("a", 8192), Type: new(ssmapi.ParameterTypeString), Tier: adv})
		for _, c := range []struct {
			name, value string
			tier        *ssmapi.ParameterTier
		}{
			{"/size/std-over", strings.Repeat("b", 4097), std},
			{"/size/adv-over", strings.Repeat("c", 8193), adv},
		} {
			_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: c.name, Value: c.value, Type: new(ssmapi.ParameterTypeString), Tier: c.tier})
			wantCode(h.T, c.name, err, ssmapi.IdentityValidationException())
			if err := h.get(c.name); err == nil {
				h.Errorf("%s was stored", c.name)
			}
		}
	})
}

// The six structural regexp errors no dialect accepts answer
// InvalidAllowedPatternException with AWS's text, and nothing is written
// (ssm-parameters.md 4.4; SSM-15 step 04).
func TestStructuralAllowedPatternErrors(t *testing.T) {
	run(t, func(h *harness) {
		// Missing bracket, missing paren, unexpected paren, trailing
		// backslash, missing repeat argument, invalid character range; the
		// last two would pair with an anchoring group if wrapped first.
		for _, p := range []string{`[a`, `(a`, `a)`, `a\`, `*a`, `[z-a]`, `a)(b`} {
			_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/pat/bad", Value: "secret-value", Type: new(ssmapi.ParameterTypeString), AllowedPattern: new(p)})
			wantError(h.T, "AllowedPattern "+p, err, ssmapi.IdentityInvalidAllowedPatternException(), "The syntax of the allowedPattern parameter is invalid.")
		}
		if err := h.get("/pat/bad"); err == nil {
			h.Error("a parameter with an invalid pattern was stored")
		}
	})
}

// An Intelligent-Tiering request answers the resolved tier, never
// Intelligent-Tiering; an Advanced parameter overwritten without Tier, or
// with Intelligent-Tiering and a small value, stays Advanced
// (ssm-parameters.md 4.2; SSM-13).
func TestIntelligentTieringAnswersTheResolvedTier(t *testing.T) {
	run(t, func(h *harness) {
		it := new(ssmapi.ParameterTierIntelligentTiering)
		out := must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/it/a", Value: "v", Type: new(ssmapi.ParameterTypeString), Tier: it})
		if val(out.Tier) != ssmapi.ParameterTierStandard {
			h.Errorf("Intelligent-Tiering small response Tier = %q, want Standard", val(out.Tier))
		}
		out = must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/it/a", Value: strings.Repeat("x", 4097), Overwrite: new(true), Tier: it})
		if val(out.Tier) != ssmapi.ParameterTierAdvanced {
			h.Errorf("Intelligent-Tiering 4097 characters response Tier = %q, want Advanced", val(out.Tier))
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
// value the old pattern refused is accepted (ssm-parameters.md 7.1; SSM-10
// step 04).
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

// The 10,001st Standard parameter of a scope is ParameterLimitExceeded with
// AWS's text under the member "message" (ssm-parameters.md 4.2, 9.3; SSM-33
// step 01, testdata/parity/ssm/SSM-33/01-10001st.json), and is not created;
// an Intelligent-Tiering create then resolves to Advanced (4.2 [D]), and an
// overwrite of a Standard parameter still fits.
func TestStandardParameterCount(t *testing.T) {
	run(t, func(h *harness) {
		for i := range 10_000 {
			h.put(fmt.Sprintf("/count/p%05d", i), "v")
		}
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/count/over", Value: "v", Type: new(ssmapi.ParameterTypeString), Tier: new(ssmapi.ParameterTierStandard),
		})
		wantError(h.T, "the 10,001st Standard parameter", err, ssmapi.IdentityParameterLimitExceeded(),
			"You have reached the maximum number of standard parameters for this AWS account and Region (10000). "+
				"To add more parameters, either delete existing parameters or upgrade to the advanced-parameter tier (charges apply).")
		if err != nil && err.ID.MessageKey != awserr.MessageLower {
			h.Errorf("the 10,001st Standard parameter's message key = %v, want MessageLower (\"message\")", err.ID.MessageKey)
		}
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

// GetParameters answers a name given twice once, and lists in
// InvalidParameters, as the request gave them and sorted, a name that is
// not valid, a selector naming no version and a malformed selector, while
// answering the rest (ssm-parameters.md 6.3, 7.3; SSM-17 step 08, SSM-18).
func TestGetParametersDuplicatesAndInvalidNames(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/gp/a", "a")
		h.put("/gp/b", "b")
		out := must(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{
			Names: []string{"/gp/b", "/gp/a", "/gp/b", "bad name", "/gp//x", "/gp/a:7", "/gp/a:1:2", "/gp/a:1"},
		})
		if got := names(out.Parameters, paramName); !slices.Equal(got, []string{"/gp/a", "/gp/a", "/gp/b"}) {
			h.Errorf("Parameters = %v, want /gp/a (latest), /gp/a (:1), /gp/b once", got)
		}
		if want := []string{"/gp//x", "/gp/a:1:2", "/gp/a:7", "bad name"}; !slices.Equal(out.InvalidParameters, want) {
			h.Errorf("InvalidParameters = %q, want %q", out.InvalidParameters, want)
		}
	})
}
