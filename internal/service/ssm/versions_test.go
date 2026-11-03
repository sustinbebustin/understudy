package ssm_test

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// Under switch ssm.error.parameter-not-found-message
// (empty-except-label), DeleteParameter and GetParameterHistory answer a
// missing parameter with ParameterNotFound, 400, and no message, as
// GetParameter does, while LabelParameterVersion and
// UnlabelParameterVersion name it (ssm-parameters.md 2.2; SSM-01 step 01,
// SSM-02 steps 01-04).
func TestParameterNotFoundMessage(t *testing.T) {
	run(t, func(h *harness) {
		h.follows("ssm.error.parameter-not-found-message", "empty-except-label")
		for op, c := range map[string]struct {
			err  *awserr.Error
			want string
		}{
			"DeleteParameter":         {errOf(call(h, ssmapi.OpDeleteParameter, &ssmapi.DeleteParameterInput{Name: "/missing/a"})), ""},
			"GetParameterHistory":     {errOf(call(h, ssmapi.OpGetParameterHistory, &ssmapi.GetParameterHistoryInput{Name: "/missing/a"})), ""},
			"LabelParameterVersion":   {errOf(call(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/missing/a", Labels: []string{"l"}})), "Parameter /missing/a not found."},
			"UnlabelParameterVersion": {errOf(call(h, ssmapi.OpUnlabelParameterVersion, &ssmapi.UnlabelParameterVersionInput{Name: "/missing/a", ParameterVersion: 1, Labels: []string{"l"}})), "Parameter /missing/a not found."},
		} {
			wantError(h.T, op, c.err, ssmapi.IdentityParameterNotFound(), c.want)
		}
	})
}

// GetParameterHistory lists every retained version oldest first, each with
// its own value and description, Labels, and Policies [] present
// (ssm-parameters.md 7.6 doc examples, 7.10); a SecureString's value is the
// ciphertext without WithDecryption and the plaintext with it (5.3).
func TestGetParameterHistory(t *testing.T) {
	run(t, func(h *harness) {
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/hist/a", Value: "one", Type: new(ssmapi.ParameterTypeString), Description: new("first")})
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/hist/a", Value: "two", Overwrite: new(true), Description: new("second")})
		h.overwrite("/hist/a", "three")
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/hist/a", ParameterVersion: new(int64(2)), Labels: []string{"b", "a"}})

		hist := h.history("/hist/a")
		if got := versions(hist); !slices.Equal(got, []int64{1, 2, 3}) {
			h.Fatalf("versions = %v, want [1 2 3] oldest first", got)
		}
		for i, want := range []struct{ value, description string }{{"one", "first"}, {"two", "second"}, {"three", "second"}} {
			e := hist[i]
			if val(e.Value) != want.value || val(e.Name) != "/hist/a" || val(e.Type) != ssmapi.ParameterTypeString {
				h.Errorf("version %d = Name %q Type %q Value %q, want /hist/a String %q", e.Version, val(e.Name), val(e.Type), val(e.Value), want.value)
			}
			if i < 2 && val(e.Description) != want.description {
				h.Errorf("version %d Description = %q, want %q", e.Version, val(e.Description), want.description)
			}
			if e.Policies == nil || len(e.Policies) != 0 {
				h.Errorf("version %d Policies = %#v, want [] present", e.Version, e.Policies)
			}
		}
		if got := sortedCopy(hist[1].Labels); !slices.Equal(got, []string{"a", "b"}) {
			h.Errorf("version 2 Labels = %v, want a and b", hist[1].Labels)
		}
		if len(hist[0].Labels) != 0 || len(hist[2].Labels) != 0 {
			h.Errorf("unlabeled versions carry Labels %v %v", hist[0].Labels, hist[2].Labels)
		}

		h.putSecure("/hist/s", "plain-one", nil)
		sealed := h.history("/hist/s")
		if len(sealed) != 1 || val(sealed[0].Value) == "plain-one" {
			h.Errorf("SecureString history without decryption = %+v, want the ciphertext", sealed)
		} else if got, err := h.openWithKMS(val(sealed[0].Value), arnPrefix+"hist/s"); err != nil || got != "plain-one" {
			h.Errorf("KMS Decrypt of the history value = %q, %v", got, err)
		}
		open := must(h, ssmapi.OpGetParameterHistory, &ssmapi.GetParameterHistoryInput{Name: "/hist/s", WithDecryption: new(true)}).Parameters
		if len(open) != 1 || val(open[0].Value) != "plain-one" {
			h.Errorf("SecureString history with decryption = %+v, want the plaintext", open)
		}

		var paged []int64
		in := &ssmapi.GetParameterHistoryInput{Name: "/hist/a", MaxResults: new(int32(2))}
		for {
			page := must(h, ssmapi.OpGetParameterHistory, in)
			paged = append(paged, versions(page.Parameters)...)
			if page.NextToken == nil {
				break
			}
			in.NextToken = page.NextToken
		}
		if !slices.Equal(paged, []int64{1, 2, 3}) {
			h.Errorf("paged history = %v, want [1 2 3]", paged)
		}
	})
}

// LabelParameterVersion (ssm-parameters.md 6.2, aws-doc
// API_LabelParameterVersion and sysman-paramstore-labels.html): without a
// version it labels the latest; a label that begins with a digit, aws or
// ssm (any case) or holds a character other than letters, digits, . - _ is
// listed in InvalidLabels with a 200 and not applied; InvalidLabels is []
// when every label applied; a label moves when put on another version; a
// version holds at most 10 labels (ParameterVersionLabelLimitExceeded,
// nothing applied); a missing version is ParameterVersionNotFound.
func TestLabelParameterVersion(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/lab/a", "v1")
		h.overwrite("/lab/a", "v2")
		out := must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/lab/a", Labels: []string{"current"}})
		if out.ParameterVersion != 2 || out.InvalidLabels == nil || len(out.InvalidLabels) != 0 {
			h.Errorf("label without a version = %+v, want ParameterVersion 2 and InvalidLabels []", out)
		}
		bad := []string{"1st", "awsLabel", "SSM-label", "has/slash", "has:colon"}
		out = must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/lab/a", ParameterVersion: new(int64(1)), Labels: append([]string{"Good_1.x-y"}, bad...)})
		if !slices.Equal(sortedCopy(out.InvalidLabels), sortedCopy(bad)) {
			h.Errorf("InvalidLabels = %v, want %v", out.InvalidLabels, bad)
		}
		for _, l := range bad {
			if err := h.get("/lab/a:" + l); err == nil {
				h.Errorf("invalid label %q was applied", l)
			}
		}
		if got := h.mustGet("/lab/a:Good_1.x-y", false); got.Version != 1 {
			h.Errorf("valid label resolves to version %d, want 1", got.Version)
		}

		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/lab/a", ParameterVersion: new(int64(1)), Labels: []string{"current"}})
		if got := h.mustGet("/lab/a:current", false); got.Version != 1 || val(got.Value) != "v1" {
			h.Errorf("moved label resolves to version %d %q, want 1 v1", got.Version, val(got.Value))
		}
		if hist := h.history("/lab/a"); len(hist) == 2 && slices.Contains(hist[1].Labels, "current") {
			h.Errorf("version 2 still carries the moved label: %v", hist[1].Labels)
		}

		_, err := call(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/lab/a", Labels: []string{strings.Repeat("l", 101)}})
		wantCode(h.T, "a 101-character label (ParameterLabel @length(1, 100))", err, ssmapi.IdentityCoralValidateValidationException())

		ten := []string{"l0", "l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9"}
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/lab/a", ParameterVersion: new(int64(2)), Labels: ten})
		_, err = call(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/lab/a", ParameterVersion: new(int64(2)), Labels: []string{"eleventh"}})
		wantCode(h.T, "an 11th label", err, ssmapi.IdentityParameterVersionLabelLimitExceeded())
		if err := h.get("/lab/a:eleventh"); err == nil {
			h.Error("the 11th label was applied")
		}

		_, err = call(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/lab/a", ParameterVersion: new(int64(9)), Labels: []string{"x"}})
		wantCode(h.T, "label of a missing version", err, ssmapi.IdentityParameterVersionNotFound())
	})
}

// UnlabelParameterVersion removes the labels attached to the version and
// lists them in RemovedLabels; "The labels that aren't attached to the
// given parameter version" go to InvalidLabels (ssm-parameters.md 6.2 [D],
// output key RemovedLabels per the model, 11). A missing version is
// ParameterVersionNotFound.
func TestUnlabelParameterVersion(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/unl/a", "v1")
		h.overwrite("/unl/a", "v2")
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/unl/a", ParameterVersion: new(int64(1)), Labels: []string{"keep", "drop"}})
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/unl/a", ParameterVersion: new(int64(2)), Labels: []string{"other"}})
		out := must(h, ssmapi.OpUnlabelParameterVersion, &ssmapi.UnlabelParameterVersionInput{Name: "/unl/a", ParameterVersion: 1, Labels: []string{"drop", "other", "never"}})
		if !slices.Equal(out.RemovedLabels, []string{"drop"}) || !slices.Equal(sortedCopy(out.InvalidLabels), []string{"never", "other"}) {
			h.Errorf("unlabel = RemovedLabels %v InvalidLabels %v, want [drop] and [never other]", out.RemovedLabels, out.InvalidLabels)
		}
		if err := h.get("/unl/a:drop"); err == nil {
			h.Error("the removed label still resolves")
		}
		for label, version := range map[string]int64{"keep": 1, "other": 2} {
			if got := h.mustGet("/unl/a:"+label, false); got.Version != version {
				h.Errorf("label %s resolves to version %d, want %d", label, got.Version, version)
			}
		}
		_, err := call(h, ssmapi.OpUnlabelParameterVersion, &ssmapi.UnlabelParameterVersionInput{Name: "/unl/a", ParameterVersion: 7, Labels: []string{"keep"}})
		wantCode(h.T, "unlabel of a missing version", err, ssmapi.IdentityParameterVersionNotFound())
	})
}

// DeleteParameter drops the parameter; DeleteParameters deletes up to 10
// names, lists those deleted in alphabetical order for a request in
// another order and those not found in InvalidParameters, each [] and
// present when empty (ssm-parameters.md 7.7, aws-doc API_DeleteParameters
// example; 7.10).
func TestDeleteParameters(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/del/one", "v")
		must(h, ssmapi.OpDeleteParameter, &ssmapi.DeleteParameterInput{Name: "/del/one"})
		if err := h.get("/del/one"); err == nil {
			h.Error("a deleted parameter still reads")
		}
		for _, n := range []string{"/del/c", "/del/a", "/del/b"} {
			h.put(n, "v")
		}
		out := must(h, ssmapi.OpDeleteParameters, &ssmapi.DeleteParametersInput{Names: []string{"/del/c", "/del/missing", "/del/a"}})
		if !slices.Equal(out.DeletedParameters, []string{"/del/a", "/del/c"}) || !slices.Equal(out.InvalidParameters, []string{"/del/missing"}) {
			h.Errorf("DeleteParameters = Deleted %v Invalid %v, want [/del/a /del/c] and [/del/missing]", out.DeletedParameters, out.InvalidParameters)
		}
		out = must(h, ssmapi.OpDeleteParameters, &ssmapi.DeleteParametersInput{Names: []string{"/del/b"}})
		if !slices.Equal(out.DeletedParameters, []string{"/del/b"}) || out.InvalidParameters == nil || len(out.InvalidParameters) != 0 {
			h.Errorf("DeleteParameters of one found name = %#v, want Deleted [/del/b] and InvalidParameters [] present", out)
		}
		out = must(h, ssmapi.OpDeleteParameters, &ssmapi.DeleteParametersInput{Names: []string{"/del/b"}})
		if out.DeletedParameters == nil || len(out.DeletedParameters) != 0 || !slices.Equal(out.InvalidParameters, []string{"/del/b"}) {
			h.Errorf("DeleteParameters of a deleted name = %#v, want Deleted [] present and Invalid [/del/b]", out)
		}
		for _, n := range []string{"/del/a", "/del/b", "/del/c"} {
			if err := h.get(n); err == nil {
				h.Errorf("%s still reads after DeleteParameters", n)
			}
		}
	})
}

// Tags (ssm-parameters.md 7.9, aws-doc API_AddTagsToResource): the
// Parameter resource type names a parameter by name; AddTagsToResource
// "Adds or overwrites"; RemoveTagsFromResource removes keys; "Most
// resources can have a maximum of 50 tags" (TooManyTagsError, nothing
// applied). A missing parameter, and any other resource type (which names
// nothing understudy stores; coverage note), are InvalidResourceId.
func TestTags(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/tag/a", "v")
		param := ssmapi.ResourceTypeForTaggingParameter
		list := func() map[string]string {
			h.Helper()
			out := map[string]string{}
			for _, t := range must(h, ssmapi.OpListTagsForResource, &ssmapi.ListTagsForResourceInput{ResourceType: param, ResourceId: "/tag/a"}).TagList {
				out[t.Key] = t.Value
			}
			return out
		}
		must(h, ssmapi.OpAddTagsToResource, &ssmapi.AddTagsToResourceInput{ResourceType: param, ResourceId: "/tag/a", Tags: []ssmapi.Tag{{Key: "env", Value: "dev"}, {Key: "team", Value: "a"}}})
		must(h, ssmapi.OpAddTagsToResource, &ssmapi.AddTagsToResourceInput{ResourceType: param, ResourceId: "/tag/a", Tags: []ssmapi.Tag{{Key: "env", Value: "prod"}}})
		if got := list(); !maps.Equal(got, map[string]string{"env": "prod", "team": "a"}) {
			h.Errorf("tags = %v, want env=prod team=a", got)
		}
		must(h, ssmapi.OpRemoveTagsFromResource, &ssmapi.RemoveTagsFromResourceInput{ResourceType: param, ResourceId: "/tag/a", TagKeys: []string{"team"}})
		if got := list(); !maps.Equal(got, map[string]string{"env": "prod"}) {
			h.Errorf("tags after removing team = %v, want env=prod", got)
		}

		many := make([]ssmapi.Tag, 50)
		for i := range many {
			many[i] = ssmapi.Tag{Key: "k" + string(rune('A'+i/26)) + string(rune('a'+i%26)), Value: "v"}
		}
		_, err := call(h, ssmapi.OpAddTagsToResource, &ssmapi.AddTagsToResourceInput{ResourceType: param, ResourceId: "/tag/a", Tags: many})
		wantCode(h.T, "a 51st tag", err, ssmapi.IdentityTooManyTagsError())
		if got := list(); len(got) != 1 {
			h.Errorf("after the refused add the parameter holds %d tags, want 1", len(got))
		}
		must(h, ssmapi.OpAddTagsToResource, &ssmapi.AddTagsToResourceInput{ResourceType: param, ResourceId: "/tag/a", Tags: many[:49]})
		if got := list(); len(got) != 50 {
			h.Errorf("parameter holds %d tags, want 50", len(got))
		}

		_, err = call(h, ssmapi.OpListTagsForResource, &ssmapi.ListTagsForResourceInput{ResourceType: param, ResourceId: "/tag/missing"})
		wantCode(h.T, "ListTagsForResource of a missing parameter", err, ssmapi.IdentityInvalidResourceId())
		_, err = call(h, ssmapi.OpAddTagsToResource, &ssmapi.AddTagsToResourceInput{ResourceType: param, ResourceId: "/tag/missing", Tags: []ssmapi.Tag{{Key: "k", Value: "v"}}})
		wantCode(h.T, "AddTagsToResource of a missing parameter", err, ssmapi.IdentityInvalidResourceId())
		for _, rt := range []ssmapi.ResourceTypeForTagging{ssmapi.ResourceTypeForTaggingDocument, ssmapi.ResourceTypeForTaggingManagedInstance} {
			_, err = call(h, ssmapi.OpListTagsForResource, &ssmapi.ListTagsForResourceInput{ResourceType: rt, ResourceId: "/tag/a"})
			wantCode(h.T, "ListTagsForResource of type "+string(rt), err, ssmapi.IdentityInvalidResourceId())
			_, err = call(h, ssmapi.OpAddTagsToResource, &ssmapi.AddTagsToResourceInput{ResourceType: rt, ResourceId: "/tag/a", Tags: []ssmapi.Tag{{Key: "k", Value: "v"}}})
			wantCode(h.T, "AddTagsToResource of type "+string(rt), err, ssmapi.IdentityInvalidResourceId())
		}
	})
}
