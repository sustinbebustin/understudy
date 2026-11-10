package ssm_test

import (
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// Name defects AWS documents are refused with a ValidationException. A
// hand-coded rule raises SSM's own identity com.amazonaws.ssm#ValidationException
// (ssm-parameters.md 12.4), never the Coral identity of a
// generated constraint. The documented failing examples (ssm-parameters.md
// 3.1, aws-doc
// https://docs.aws.amazon.com/systems-manager/latest/userguide/what-is-a-parameter.html#sysman-parameter-name-constraints):
// the aws/ssm prefixes, any character outside a-zA-Z0-9_.- and /, an inner
// space, and a hierarchy without its leading slash. The reserved first
// level /aws/ is AccessDeniedException instead (SSM-04).
func TestNameDefectsAreValidationExceptions(t *testing.T) {
	run(t, func(h *harness) {
		for _, name := range []string{
			"awsTestParameter", "SSM-testparameter", // the doc's examples
			"/ssm/x",             // "can't be prefixed with aws or ssm (case-insensitive)"
			"my param", "/a/b c", // inner spaces
			"bad#name", "/a/b!", "a*", // characters outside the set
			"MyParameter3/L1", // "is not" fully qualified
		} {
			_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: name, Value: "v", Type: new(ssmapi.ParameterTypeString)})
			wantCode(h.T, "PutParameter "+name, err, ssmapi.IdentityValidationException())
		}
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/aws/testparam1", Value: "v", Type: new(ssmapi.ParameterTypeString)})
		wantCode(h.T, "PutParameter /aws/testparam1", err, ssmapi.IdentityAccessDeniedException())
		if got := h.svc.Resources(h.Scope); len(got) != 0 {
			h.Errorf("refused names were stored: %+v", got)
		}
	})
}

// A slash without a leading slash and an empty level are refused on reads
// too, with a message naming "sub-paths divided by slash symbol" (asserted
// against AWS, ssm-parameters.md 3.1 [R-weak]: localstack@8b9a79f
// tests/aws/services/ssm/test_ssm.py:L47-L62), while the well-formed name
// reads.
func TestHierarchyDefectsOnRead(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/pa/b/c", "123")
		if got := h.mustGet("/pa/b/c", false); val(got.Value) != "123" {
			h.Errorf("Value = %q, want 123", val(got.Value))
		}
		for _, name := range []string{"/pa//b//c", "pa/b/c"} {
			err := h.get(name)
			wantCode(h.T, "GetParameter "+name, err, ssmapi.IdentityValidationException())
			if err != nil && !strings.Contains(err.Message, "sub-paths divided by slash symbol") {
				h.Errorf("GetParameter %s message %q does not contain %q", name, err.Message, "sub-paths divided by slash symbol")
			}
		}
	})
}

// A constraint of the model (PSParameterName @length(1, 2048),
// ssm-parameters.md 3.4, 12.4) keeps the generator's Coral identity
// com.amazon.coral.validate#ValidationException (error-matrix.md 4).
func TestGeneratedConstraintsKeepTheCoralIdentity(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, ssmapi.OpGetParameter, &ssmapi.GetParameterInput{Name: strings.Repeat("a", 2049)})
		wantCode(h.T, "GetParameter of 2049 characters", err, ssmapi.IdentityCoralValidateValidationException())
		// GetParameters takes 1 to 10 names (ssm-parameters.md 7.3, 12.4).
		eleven := make([]string, 11)
		for i := range eleven {
			eleven[i] = fmt.Sprintf("p%d", i)
		}
		_, err = call(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: eleven})
		wantCode(h.T, "GetParameters of 11 names", err, ssmapi.IdentityCoralValidateValidationException())
	})
}

// "The service removes any spaces specified for the beginning or end of a
// parameter name" (ssm-parameters.md 3.1, aws-doc
// https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_PutParameter.html):
// a name written with outer spaces is the trimmed one.
func TestOuterSpacesAreTrimmed(t *testing.T) {
	run(t, func(h *harness) {
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "  /trim/a ", Value: "v", Type: new(ssmapi.ParameterTypeString)})
		if got := h.mustGet("/trim/a", false); val(got.ARN) != arnPrefix+"trim/a" {
			h.Errorf("ARN = %q, want %strim/a", val(got.ARN), arnPrefix)
		}
	})
}

// "Parameter hierarchies are limited to a maximum depth of fifteen
// levels": 15 levels counting the name is valid, 17 fails with
// HierarchyLevelLimitExceededException (ssm-parameters.md 3.4, aws-doc
// name constraints), whose wire message is "Depth of the tree cannot
// exceed 15", not the model's documented text (SSM-05 step 03).
func TestHierarchyDepth(t *testing.T) {
	run(t, func(h *harness) {
		levels := func(n int) string {
			var b strings.Builder
			for i := 1; i < n; i++ {
				fmt.Fprintf(&b, "/L%d", i)
			}
			return b.String() + "/parameter-name"
		}
		h.put(levels(15), "v")
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: levels(17), Value: "v", Type: new(ssmapi.ParameterTypeString)})
		wantError(h.T, "PutParameter of 17 levels", err, ssmapi.IdentityHierarchyLevelLimitExceededException(), "Depth of the tree cannot exceed 15")
	})
}

// "The maximum length for a parameter name that you specify is 1011
// characters. This count of 1011 characters includes the characters in the
// ARN that precede the name" (ssm-parameters.md 3.4, aws-doc
// API_PutParameter): the ARN is at most 1011 characters, and a longer one
// is AccessDeniedException (SSM-06). The harness ARN prefix is 45
// characters, as the doc's example arn:aws:ssm:us-east-2:111122223333:parameter/ is.
func TestNameLengthIsTheARNLength(t *testing.T) {
	run(t, func(h *harness) {
		if len(arnPrefix) != 45 {
			h.Fatalf("arnPrefix is %d characters", len(arnPrefix))
		}
		fits := strings.Repeat("n", 1011-len(arnPrefix))
		h.put(fits, "v")
		if got := h.mustGet(fits, false); len(val(got.ARN)) != 1011 {
			h.Errorf("ARN length = %d, want 1011", len(val(got.ARN)))
		}
		long := strings.Repeat("n", 1012-len(arnPrefix))
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: long, Value: "v", Type: new(ssmapi.ParameterTypeString)})
		wantCode(h.T, "PutParameter with a 1012-character ARN", err, ssmapi.IdentityAccessDeniedException())
	})
}

// /foo and foo are one parameter: they "aren't unique" (ssm-parameters.md
// 3.2, aws-doc name constraints) and share the ARN parameter/foo
// (third-party-recorded, test_ssm.snapshot.json:L6). Under switch
// ssm.name.root-slash-echo (request-spelling), GetParameter and
// GetParameters echo whichever spelling was asked, DescribeParameters
// reports the canonical foo for a Name filter in either spelling, and
// GetParameterHistory reports foo too.
func TestRootParameterSpellingsAreOneParameter(t *testing.T) {
	run(t, func(h *harness) {
		h.follows("ssm.name.root-slash-echo", "request-spelling")
		n := 0
		rapid.Check(bubbleTB{h.T}, func(rt *rapid.T) {
			n++
			base := rapid.StringMatching(`[a-zA-Z0-9_.-]{1,24}`).Filter(func(s string) bool {
				l := strings.ToLower(s)
				return !strings.HasPrefix(l, "aws") && !strings.HasPrefix(l, "ssm")
			}).Draw(rt, "base")
			name := fmt.Sprintf("%s-%d", base, n)
			slashed := rapid.Bool().Draw(rt, "written with a slash")
			written, other := name, "/"+name
			if slashed {
				written, other = other, written
			}
			if _, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: written, Value: "v", Type: new(ssmapi.ParameterTypeString)}); err != nil {
				rt.Fatalf("PutParameter %s = %s %q", written, err.ID.Shape, err.Message)
			}
			_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: other, Value: "w", Type: new(ssmapi.ParameterTypeString)})
			if err == nil || err.ID.Shape != ssmapi.IdentityParameterAlreadyExists().Shape {
				rt.Errorf("PutParameter %s after %s = %v, want ParameterAlreadyExists", other, written, err)
			}
			for _, ask := range []string{written, other} {
				out, err := call(h, ssmapi.OpGetParameter, &ssmapi.GetParameterInput{Name: ask})
				if err != nil {
					rt.Fatalf("GetParameter %s = %s %q", ask, err.ID.Shape, err.Message)
				}
				if p := out.Parameter; val(p.Name) != ask || val(p.ARN) != arnPrefix+name {
					rt.Errorf("GetParameter %s = Name %q ARN %q, want %q %s%s", ask, val(p.Name), val(p.ARN), ask, arnPrefix, name)
				}
				many, err := call(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: []string{ask}})
				if err != nil || len(many.Parameters) != 1 || val(many.Parameters[0].Name) != ask {
					rt.Errorf("GetParameters [%s] = %+v %v, want one entry named %q", ask, many, err, ask)
				}
				d, err := call(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{
					ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Name", Option: new("Equals"), Values: []string{ask}}},
				})
				if err != nil || len(d.Parameters) != 1 || val(d.Parameters[0].Name) != name || val(d.Parameters[0].ARN) != arnPrefix+name {
					rt.Errorf("DescribeParameters Name Equals %s = %+v %v, want one entry named %q", ask, d, err, name)
				}
				hist, err := call(h, ssmapi.OpGetParameterHistory, &ssmapi.GetParameterHistoryInput{Name: ask})
				if err != nil || len(hist.Parameters) != 1 || val(hist.Parameters[0].Name) != name {
					rt.Errorf("GetParameterHistory %s = %+v %v, want one entry named %q", ask, hist, err, name)
				}
			}
		})
	})
}

// The canonical identity of a hierarchical name is the name as written,
// one leading slash and all, by name and by ARN (third-party-recorded,
// test_ssm.snapshot.json:L27-L56; ssm-parameters.md 3.2, 3.3). A name of
// valid characters and 2 to 15 levels is accepted, reads back under its
// own name, and its ARN is parameter<name>.
func TestHierarchicalNamesRoundTrip(t *testing.T) {
	run(t, func(h *harness) {
		n := 0
		rapid.Check(bubbleTB{h.T}, func(rt *rapid.T) {
			n++
			segs := rapid.SliceOfN(rapid.StringMatching(`[a-zA-Z0-9_.-]{1,12}`), 1, 13).Draw(rt, "levels")
			name := fmt.Sprintf("/h%d/%s", n, strings.Join(segs, "/"))
			if _, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: name, Value: "v", Type: new(ssmapi.ParameterTypeString)}); err != nil {
				rt.Fatalf("PutParameter %s = %s %q", name, err.ID.Shape, err.Message)
			}
			arn := "arn:aws:ssm:us-east-1:000000000000:parameter" + name
			for _, ask := range []string{name, arn} {
				out, err := call(h, ssmapi.OpGetParameter, &ssmapi.GetParameterInput{Name: ask})
				if err != nil || val(out.Parameter.Name) != name || val(out.Parameter.ARN) != arn {
					rt.Errorf("GetParameter %s = %+v %v, want Name %s ARN %s", ask, out, err, name, arn)
				}
			}
		})
	})
}

// A name holding a character outside a-zA-Z0-9_.- and / is refused with
// SSM's ValidationException wherever the character sits (ssm-parameters.md
// 3.1, aws-doc), and nothing is stored.
func TestInvalidCharactersAreRefused(t *testing.T) {
	run(t, func(h *harness) {
		rapid.Check(bubbleTB{h.T}, func(rt *rapid.T) {
			prefix := rapid.StringMatching(`/?[a-zA-Z0-9_.-]{0,10}`).Draw(rt, "prefix")
			bad := rapid.SampledFrom([]string{" ", "#", "!", "@", "*", "$", "%", "&", "(", "+", ",", ";", "=", "?", "~", "é", "\t"}).Draw(rt, "bad")
			suffix := rapid.StringMatching(`[a-zA-Z0-9_.-]{1,10}`).Draw(rt, "suffix")
			name := "x" + prefix + bad + suffix
			_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: name, Value: "v", Type: new(ssmapi.ParameterTypeString)})
			if err == nil || err.ID.Shape != ssmapi.IdentityValidationException().Shape {
				rt.Errorf("PutParameter %q = %v, want com.amazonaws.ssm#ValidationException", name, err)
			}
		})
		if got := h.svc.Resources(h.Scope); len(got) != 0 {
			h.Errorf("refused names were stored: %+v", got)
		}
	})
}

// Names under /aws/service/ (public parameters) and
// /aws/reference/secretsmanager/ (Secrets Manager references) answer 501
// NotImplemented on every one of the thirteen operations, before any other
// hand-coded rule, in any spelling: without the leading slash, with outer
// spaces, with a selector, or as an ARN (ssm-parameters.md 12.1, 12.6:
// never ParameterNotFound, never a faked value).
func TestReservedNamespacesAnswer501(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/kept", "v")
		for _, prefix := range []string{"/aws/service/", "/aws/reference/secretsmanager/"} {
			feature := prefix
			name := prefix + "x"
			for _, spelling := range []string{name, strings.TrimPrefix(name, "/"), "  " + name + " ", name + ":1", "arn:aws:ssm:us-east-1:000000000000:parameter" + name} {
				wantNotImplemented(h.T, "GetParameter "+spelling, errOf(call(h, ssmapi.OpGetParameter, &ssmapi.GetParameterInput{Name: spelling})), feature)
			}
			tagged := []ssmapi.Tag{{Key: "k", Value: "v"}}
			for op, err := range map[string]*awserr.Error{
				"PutParameter":            errOf(call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: name, Value: "v", Type: new(ssmapi.ParameterTypeString)})),
				"GetParameters":           errOf(call(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: []string{"/kept", name}})),
				"GetParametersByPath":     errOf(call(h, ssmapi.OpGetParametersByPath, &ssmapi.GetParametersByPathInput{Path: prefix + "x"})),
				"DescribeParameters":      errOf(call(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Name", Option: new("BeginsWith"), Values: []string{prefix}}}})),
				"DescribeParameters Path": errOf(call(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Path", Values: []string{prefix + "x"}}}})),
				"GetParameterHistory":     errOf(call(h, ssmapi.OpGetParameterHistory, &ssmapi.GetParameterHistoryInput{Name: name})),
				"DeleteParameter":         errOf(call(h, ssmapi.OpDeleteParameter, &ssmapi.DeleteParameterInput{Name: name})),
				"DeleteParameters":        errOf(call(h, ssmapi.OpDeleteParameters, &ssmapi.DeleteParametersInput{Names: []string{"/kept", name}})),
				"LabelParameterVersion":   errOf(call(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: name, Labels: []string{"l"}})),
				"UnlabelParameterVersion": errOf(call(h, ssmapi.OpUnlabelParameterVersion, &ssmapi.UnlabelParameterVersionInput{Name: name, ParameterVersion: 1, Labels: []string{"l"}})),
				"AddTagsToResource":       errOf(call(h, ssmapi.OpAddTagsToResource, &ssmapi.AddTagsToResourceInput{ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: name, Tags: tagged})),
				"RemoveTagsFromResource":  errOf(call(h, ssmapi.OpRemoveTagsFromResource, &ssmapi.RemoveTagsFromResourceInput{ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: name, TagKeys: []string{"k"}})),
				"ListTagsForResource":     errOf(call(h, ssmapi.OpListTagsForResource, &ssmapi.ListTagsForResourceInput{ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: name})),
			} {
				wantNotImplemented(h.T, op+" under "+prefix, err, feature)
			}
		}
		// One reserved name makes DeleteParameters 501 as a whole: nothing
		// is deleted.
		if err := h.get("/kept"); err != nil {
			h.Errorf("/kept after the refused DeleteParameters: %s %q", err.ID.Shape, err.Message)
		}
	})
}
