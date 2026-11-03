package ssm_test

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The tests in this file assert answers recorded against AWS on 2026-11-03
// (testdata/parity/ssm/SSM-<n>; ssm-parameters.md 13).

// coralValidation is the identity of a generated constraint check, which
// SSM answers with the Coral ValidationException (ssm-parameters.md 12.4).
func coralValidation(tb testing.TB, what string, err *awserr.Error, message string) {
	tb.Helper()
	switch {
	case err == nil:
		tb.Errorf("%s: no error, want the Coral ValidationException %q", what, message)
	case err.ID.Shape != "com.amazon.coral.validate#ValidationException" || err.Message != message:
		tb.Errorf("%s = %s %q\nwant com.amazon.coral.validate#ValidationException %q", what, err.ID.Shape, err.Message, message)
	}
}

// pathText is SSM's text for a name defect in a path (ssm-parameters.md
// 3.1; SSM-01 step 03, SSM-04).
const pathText = `Parameter name: can't be prefixed with "ssm" (case-insensitive). If formed as a path, ` +
	"it can consist of sub-paths divided by slash symbol; each sub-path can be formed as a mix of letters, numbers and the following 3 symbols .-_"

// Each name defect answers AWS's own text (ssm-parameters.md 3.1; SSM-01
// step 03, SSM-04): the path text for a character outside the set or an
// empty level, and for a hierarchical name under /ssm; its own sentences
// for a slash without a leading slash, a trailing slash and an aws or ssm
// prefix; AccessDeniedException for the reserved /aws/ level.
func TestNameDefectTextsAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		for name, want := range map[string]struct {
			id   awserr.Identity
			text string
		}{
			"a b":        {ssmapi.IdentityValidationException(), pathText},
			"a$b":        {ssmapi.IdentityValidationException(), pathText},
			"/us1103//x": {ssmapi.IdentityValidationException(), pathText},
			"/ssm/x":     {ssmapi.IdentityValidationException(), pathText},
			"us1103/x":   {ssmapi.IdentityValidationException(), "Parameter name must be a fully qualified name."},
			"/us1103/x/": {ssmapi.IdentityValidationException(), "Parameter name must not end with slash."},
			"awsome":     {ssmapi.IdentityValidationException(), `Parameter name: can't be prefixed with "aws" or "ssm" (case-insensitive).`},
			"SSMx":       {ssmapi.IdentityValidationException(), `Parameter name: can't be prefixed with "aws" or "ssm" (case-insensitive).`},
			"/aws/x":     {ssmapi.IdentityAccessDeniedException(), "No access to reserved parameter name: aws/x."},
		} {
			_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: name, Value: "v", Type: new(ssmapi.ParameterTypeString)})
			wantError(h.T, "PutParameter "+name, err, want.id, want.text)
		}
		// aws is reserved only at the first level.
		if _, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/us1103/aws/x", Value: "v", Type: new(ssmapi.ParameterTypeString)}); err != nil {
			h.Errorf("PutParameter /us1103/aws/x = %s %q, want it accepted (SSM-04)", err.ID.Shape, err.Message)
		}
	})
}

// A name of 16 levels is accepted and one of 17 refused with AWS's text
// (ssm-parameters.md 3.4; SSM-05).
func TestDepthAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		levels := func(n int) string {
			var b strings.Builder
			for i := range n - 1 {
				b.WriteString("/l" + string(rune('a'+i)))
			}
			return b.String() + "/p"
		}
		h.put(levels(16), "v")
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: levels(17), Value: "v", Type: new(ssmapi.ParameterTypeString)})
		wantError(h.T, "PutParameter of 17 levels", err, ssmapi.IdentityHierarchyLevelLimitExceededException(), "Depth of the tree cannot exceed 15")
	})
}

// A name whose ARN passes 1011 characters answers AccessDeniedException in
// the IAM grammar without a reason clause, although the caller may do
// anything (ssm-parameters.md 3.4; SSM-06 step 02).
func TestARNPast1011IsDenied(t *testing.T) {
	run(t, func(h *harness) {
		name := "/us1103/" + strings.Repeat("a", 1011-len(arnPrefix+"us1103/"))
		h.put(name, "v")
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: name + "a", Value: "v", Type: new(ssmapi.ParameterTypeString)})
		wantError(h.T, "PutParameter with a 1012-character ARN", err, ssmapi.IdentityAccessDeniedException(),
			"User: arn:aws:iam::000000000000:root is not authorized to perform: ssm:PutParameter on resource: "+arnPrefix+"us1103/"+strings.Repeat("a", 1011-len(arnPrefix+"us1103/")+1))
	})
}

// LabelParameterVersion and UnlabelParameterVersion on a missing name say
// which, while DeleteParameter and GetParameterHistory send no message
// (ssm-parameters.md 2.2; SSM-02).
func TestParameterNotFoundMessagesAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		const missing = "/us1103/ssm/missing"
		wantError(h.T, "DeleteParameter", errOf(call(h, ssmapi.OpDeleteParameter, &ssmapi.DeleteParameterInput{Name: missing})), ssmapi.IdentityParameterNotFound(), "")
		wantError(h.T, "GetParameterHistory", errOf(call(h, ssmapi.OpGetParameterHistory, &ssmapi.GetParameterHistoryInput{Name: missing})), ssmapi.IdentityParameterNotFound(), "")
		wantError(h.T, "LabelParameterVersion", errOf(call(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: missing, Labels: []string{"l1"}})),
			ssmapi.IdentityParameterNotFound(), "Parameter /us1103/ssm/missing not found.")
		wantError(h.T, "UnlabelParameterVersion", errOf(call(h, ssmapi.OpUnlabelParameterVersion, &ssmapi.UnlabelParameterVersionInput{Name: missing, ParameterVersion: 1, Labels: []string{"l1"}})),
			ssmapi.IdentityParameterNotFound(), "Parameter /us1103/ssm/missing not found.")
	})
}

// GetParameters answers a root parameter once per spelling asked, each
// with its own Name (ssm-parameters.md 3.2; SSM-07 step 03).
func TestGetParametersAnswersEachSpelling(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/us1103-root", "v")
		out := must(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: []string{"us1103-root", "/us1103-root"}})
		names := make([]string, 0, len(out.Parameters))
		for _, p := range out.Parameters {
			names = append(names, val(p.Name))
		}
		if strings.Join(names, " ") != "/us1103-root us1103-root" || len(out.InvalidParameters) != 0 {
			h.Errorf("GetParameters both spellings = %v, invalid %v; want [/us1103-root us1103-root], none invalid", names, out.InvalidParameters)
		}
	})
}

// A hierarchical parameter changes type on overwrite like a root one
// (ssm-parameters.md 4.1; SSM-09).
func TestHierarchicalTypeChangeAccepted(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/us1103/ssm/t9", "v")
		for i, typ := range []ssmapi.ParameterType{ssmapi.ParameterTypeSecureString, ssmapi.ParameterTypeStringList} {
			out := must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/us1103/ssm/t9", Value: "v", Type: new(typ), Overwrite: new(true)})
			if out.Version != int64(i+2) {
				h.Errorf("overwrite to %s = version %d, want %d", typ, out.Version, i+2)
			}
		}
	})
}

// Create-time refusals answer AWS's texts (ssm-parameters.md 7.1; SSM-11).
// A {{...}} value is refused with AWS's sentence minus the value it quotes,
// which understudy keeps out of every message (12.4).
func TestCreateRefusalsAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		wantError(h.T, "create without Type", errOf(call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/us1103/ssm/c11a", Value: "v"})),
			ssmapi.IdentityValidationException(), "A parameter type is required when you create a parameter.")
		wantError(h.T, "KeyId on a String", errOf(call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/us1103/ssm/c11b", Value: "v", Type: new(ssmapi.ParameterTypeString), KeyId: new("alias/aws/ssm")})),
			ssmapi.IdentityValidationException(), "KeyId is required for SecureString type parameter only.")
		coralValidation(h.T, "empty Value", errOf(call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/us1103/ssm/c11c", Value: "", Type: new(ssmapi.ParameterTypeString)})),
			"1 validation error detected: Value at 'value' failed to satisfy constraint: Member must have length greater than or equal to 1")
		wantError(h.T, "{{ssm:x}}", errOf(call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/us1103/ssm/c11e", Value: "{{ssm:x}}", Type: new(ssmapi.ParameterTypeString)})),
			ssmapi.IdentityValidationException(), `Parameter value failed to satisfy constraint: Parameter value can't nest another parameter. Do not use "{{}}" in the value.`)
	})
}

// Value size counts characters, and the refusals are AWS's texts
// (ssm-parameters.md 4.2; SSM-12, SSM-13 step 04).
func TestValueSizeInCharactersAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		put := func(name, value string, t ssmapi.ParameterTier) *awserr.Error {
			return errOf(call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: name, Value: value, Type: new(ssmapi.ParameterTypeString), Tier: new(t)}))
		}
		if err := put("/us1103/ssm/s12d", strings.Repeat("é", 2049), ssmapi.ParameterTierStandard); err != nil {
			h.Errorf("2049 x é (4098 bytes) Standard = %s %q, want it accepted", err.ID.Shape, err.Message)
		}
		wantError(h.T, "4097 ASCII Standard", put("/us1103/ssm/s12b", strings.Repeat("a", 4097), ssmapi.ParameterTierStandard), ssmapi.IdentityValidationException(),
			"Standard tier parameters support a maximum parameter value of 4096 characters. To create a larger parameter value, upgrade the parameter to use the advanced-parameter tier. "+
				"For more information, see https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-store-advanced-parameters.html")
		wantError(h.T, "8193 ASCII Advanced", put("/us1103/ssm/s12f", strings.Repeat("a", 8193), ssmapi.ParameterTierAdvanced), ssmapi.IdentityValidationException(),
			"The specified parameter value is too large. Advanced-tier parameters support a maximum parameter value of 8192 characters.")
		if err := put("/us1103/ssm/i13b", strings.Repeat("a", 4097), ssmapi.ParameterTierIntelligentTiering); err != nil {
			h.Fatal(err.Message)
		}
		downgrade := errOf(call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/us1103/ssm/i13b", Value: "v", Type: new(ssmapi.ParameterTypeString), Tier: new(ssmapi.ParameterTierStandard), Overwrite: new(true),
		}))
		wantError(h.T, "downgrade", downgrade, ssmapi.IdentityValidationException(),
			"This parameter uses the advanced-parameter tier. You can't downgrade a parameter from the advanced-parameter tier to the standard-parameter tier. "+
				"If necessary, you can delete the advanced parameter and recreate it as a standard parameter. Be aware that standard parameters have a value limit of 4096 characters.")
	})
}

// AllowedPattern, label and version-cap refusals answer AWS's texts
// (ssm-parameters.md 4.4, 6.1, 6.2; SSM-15 step 04, SSM-16 step 06, SSM-30
// step 01).
func TestPutAndLabelRefusalsAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		wantError(h.T, "AllowedPattern (", errOf(call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/us1103/ssm/a15d", Value: "abc", Type: new(ssmapi.ParameterTypeString), AllowedPattern: new("(")})),
			ssmapi.IdentityInvalidAllowedPatternException(), "The syntax of the allowedPattern parameter is invalid.")

		h.put("/us1103/ssm/l16", "v1")
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/us1103/ssm/l16", Labels: []string{"b1", "b2", "b3", "b4", "b5", "b6", "b7", "b8", "b9", "b10"}})
		wantError(h.T, "eleventh label", errOf(call(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/us1103/ssm/l16", Labels: []string{"b11"}})),
			ssmapi.IdentityParameterVersionLabelLimitExceeded(), "A parameter version can have maximum 10 labels.Move one or more labels to another version and try again.")

		h.put("/us1103/ssm/c30", "v1")
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/us1103/ssm/c30", Labels: []string{"keep"}})
		for range 99 {
			h.overwrite("/us1103/ssm/c30", "v")
		}
		wantError(h.T, "101st version", errOf(call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/us1103/ssm/c30", Value: "v", Overwrite: new(true)})),
			ssmapi.IdentityParameterMaxVersionLimitExceeded(),
			"You attempted to create a new version of us1103/ssm/c30 by calling the PutParameter API with the overwrite flag. "+
				"Version 1, the oldest version, can’t be deleted because it has a label associated with it. Move the label to another version of the parameter, and try again.")
	})
}

// Selectors that name no version, and a second selector, answer AWS's
// texts (ssm-parameters.md 6.3; SSM-17 steps 03-06).
func TestSelectorRefusalsAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/us1103/ssm/l16", "v1")
		for name, want := range map[string]string{
			"/us1103/ssm/l16:99":      "Systems Manager could not find version 99 of /us1103/ssm/l16. Verify the version and try again.",
			"/us1103/ssm/l16:0":       "Systems Manager could not find version 0 of /us1103/ssm/l16. Verify the version and try again.",
			"/us1103/ssm/l16:nolabel": "Systems Manager could not find a version of /us1103/ssm/l16 that has the specified label, nolabel. Verify the parameter label and try again.",
		} {
			wantError(h.T, "GetParameter "+name, h.get(name), ssmapi.IdentityParameterVersionNotFound(), want)
		}
		wantError(h.T, "GetParameter name:1:2", h.get("/us1103/ssm/l16:1:2"), ssmapi.IdentityValidationException(),
			"Invalid parameter name. Please use correct syntax for referencing a version/label  <name>:<version/label>")
	})
}

// The list operations' refusals answer AWS's texts, DescribeParameters
// pages 10 results without MaxResults, and Contains ignores case
// (ssm-parameters.md 7.4, 7.5, 7.10; SSM-19, SSM-20, SSM-21, SSM-32).
func TestListAnswersAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		for i := range 12 {
			h.put("/us1103/ssm/d20/k"+string(rune('a'+i)), "v")
		}
		h.put("/us1103/ssm/f21/Top", "v")
		describe := func(in *ssmapi.DescribeParametersInput) (*ssmapi.DescribeParametersOutput, *awserr.Error) {
			return call(h, ssmapi.OpDescribeParameters, in)
		}
		page := must(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Path", Values: []string{"/us1103/ssm/d20"}}}})
		if len(page.Parameters) != 10 || page.NextToken == nil {
			h.Errorf("DescribeParameters without MaxResults = %d entries, NextToken %v; want 10 and a token", len(page.Parameters), page.NextToken)
		}
		contains := must(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Name", Option: new("Contains"), Values: []string{"top"}}}})
		if len(contains.Parameters) != 1 || val(contains.Parameters[0].Name) != "/us1103/ssm/f21/Top" {
			h.Errorf("Name Contains top = %+v, want /us1103/ssm/f21/Top", contains.Parameters)
		}
		wantError(h.T, "Filters with ParameterFilters", errOf(describe(&ssmapi.DescribeParametersInput{
			Filters:          []ssmapi.ParametersFilter{{Key: ssmapi.ParametersFilterKeyName, Values: []string{"/us1103/ssm/f21/Top"}}},
			ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Type", Values: []string{"String"}}},
		})), ssmapi.IdentityValidationException(), "You can use either Filters or ParameterFilters in a single request.")
		wantError(h.T, "describe Label", errOf(describe(&ssmapi.DescribeParametersInput{ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Label", Values: []string{"x"}}}})),
			ssmapi.IdentityInvalidFilterKey(), "The following filter key is not valid: Label. Valid filter keys include: [Path, Name, Type, KeyId, Tier, DataType].")
		wantError(h.T, "describe Type Contains", errOf(describe(&ssmapi.DescribeParametersInput{ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Type", Option: new("Contains"), Values: []string{"String"}}}})),
			ssmapi.IdentityInvalidFilterOption(), "The following filter option is not valid: Contains. Valid options include: [BeginsWith, Equals].")
		wantError(h.T, "describe garbage token", errOf(describe(&ssmapi.DescribeParametersInput{NextToken: new("garbage")})),
			ssmapi.IdentityInvalidNextToken(), "The following token is invalid: garbage.")

		byPath := func(in *ssmapi.GetParametersByPathInput) *awserr.Error {
			return errOf(call(h, ssmapi.OpGetParametersByPath, in))
		}
		wantError(h.T, "by path Name filter", byPath(&ssmapi.GetParametersByPathInput{Path: "/us1103/ssm", ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Name", Values: []string{"x"}}}}),
			ssmapi.IdentityInvalidFilterKey(), "The following filter key is not valid: Name. Valid filter keys include: [Type, KeyId, Label].")
		wantError(h.T, "by path Label BeginsWith", byPath(&ssmapi.GetParametersByPathInput{Path: "/us1103/ssm", ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Label", Option: new("BeginsWith"), Values: []string{"o"}}}}),
			ssmapi.IdentityInvalidFilterOption(), "The following filter option is not valid: BeginsWith. Valid options include: [Equals].")
		wantError(h.T, "by path without a leading slash", byPath(&ssmapi.GetParametersByPathInput{Path: "us1103/ssm/p19"}), ssmapi.IdentityValidationException(),
			`The parameter doesn't meet the parameter name requirements. The parameter name must begin with a forward slash "/". `+
				`It can't be prefixed with \"aws\" or \"ssm\" (case-insensitive). It must use only letters, numbers, or the following symbols: . (period), - (hyphen), _ (underscore). `+
				`Special characters are not allowed. All sub-paths, if specified, must use the forward slash symbol "/". Valid example: /get/parameters2-/by1./path0_.`)
		wantError(h.T, "by path garbage token", byPath(&ssmapi.GetParametersByPathInput{Path: "/us1103/ssm", NextToken: new("garbage")}),
			ssmapi.IdentityInvalidNextToken(), "The following token is invalid: garbage.")
		wantError(h.T, "history garbage token", errOf(call(h, ssmapi.OpGetParameterHistory, &ssmapi.GetParameterHistoryInput{Name: "/us1103/ssm/f21/Top", NextToken: new("garbage")})),
			ssmapi.IdentityInvalidNextToken(), "The following token is invalid: garbage.")
	})
}

// Tag keys starting aws: are refused, and a parameter ARN works as the
// ResourceId (ssm-parameters.md 7.9; SSM-25 steps 02, 06; SSM-07 step 10).
func TestTagsAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/us1103/ssm/t25", "v")
		wantError(h.T, "aws: tag key", errOf(call(h, ssmapi.OpAddTagsToResource, &ssmapi.AddTagsToResourceInput{
			ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: "/us1103/ssm/t25", Tags: []ssmapi.Tag{{Key: "aws:x", Value: "v"}},
		})), ssmapi.IdentityValidationException(), "Caller is an end user and not allowed to mutate system tags")
		must(h, ssmapi.OpAddTagsToResource, &ssmapi.AddTagsToResourceInput{
			ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: arnPrefix + "us1103/ssm/t25", Tags: []ssmapi.Tag{{Key: "k", Value: "v"}},
		})
		out := must(h, ssmapi.OpListTagsForResource, &ssmapi.ListTagsForResourceInput{ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: arnPrefix + "us1103/ssm/t25"})
		if len(out.TagList) != 1 || out.TagList[0].Key != "k" {
			h.Errorf("ListTagsForResource by ARN = %+v, want [k=v]", out.TagList)
		}
	})
}

// A parameter ARN of another Region or another resource type answers
// ValidationException naming it, and one of another account is denied
// whatever the caller may do (ssm-parameters.md 3.3; SSM-29).
func TestForeignARNsAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		h.put("us1103-a29", "v")
		const other = "arn:aws:ssm:us-east-1:111111111111:parameter/us1103-a29"
		wantError(h.T, "other account", h.get(other), ssmapi.IdentityAccessDeniedException(),
			"User: arn:aws:iam::000000000000:root is not authorized to perform: ssm:GetParameter on resource: "+other+
				" because no resource-based policy allows the ssm:GetParameter action")
		const region = "arn:aws:ssm:us-west-2:000000000000:parameter/us1103-a29"
		wantError(h.T, "other Region", h.get(region), ssmapi.IdentityValidationException(), "Incorrect region in: "+region)
		const document = "arn:aws:ssm:us-east-1:000000000000:document/us1103-a29"
		wantError(h.T, "document ARN", h.get(document), ssmapi.IdentityValidationException(), "Incorrect resource type document in: "+document)
	})
}
