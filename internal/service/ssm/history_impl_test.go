package ssm_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// Without MaxResults GetParameterHistory answers 50 versions a page.
// UNVERIFIED (SSM-30), ssm-parameters.md 6.1.
func TestHistoryDefaultPageIsFifty(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/hp/a", "v")
		for range 50 {
			h.overwrite("/hp/a", "v")
		}
		first := must(h, ssmapi.OpGetParameterHistory, &ssmapi.GetParameterHistoryInput{Name: "/hp/a"})
		if len(first.Parameters) != 50 || first.NextToken == nil {
			h.Fatalf("first page = %d versions, NextToken %v; want 50 and a token", len(first.Parameters), first.NextToken)
		}
		rest := must(h, ssmapi.OpGetParameterHistory, &ssmapi.GetParameterHistoryInput{Name: "/hp/a", NextToken: first.NextToken})
		if got := versions(rest.Parameters); !slices.Equal(got, []int64{51}) || rest.NextToken != nil {
			h.Errorf("second page = %v, NextToken %v; want [51] and none", got, rest.NextToken)
		}
	})
}

// Labeling a version changes no LastModifiedDate. UNVERIFIED (SSM-08),
// ssm-parameters.md 6.1.
func TestLabelingKeepsLastModifiedDate(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/lm/a", "v")
		written := *h.mustDescribeOne("/lm/a").LastModifiedDate
		h.advance(time.Minute)
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/lm/a", Labels: []string{"x"}})
		if got := *h.mustDescribeOne("/lm/a").LastModifiedDate; !got.Equal(written) {
			h.Errorf("DescribeParameters LastModifiedDate after labeling = %v, want %v", got, written)
		}
		if got := *h.history("/lm/a")[0].LastModifiedDate; !got.Equal(written) {
			h.Errorf("history LastModifiedDate after labeling = %v, want %v", got, written)
		}
	})
}

// History reports a version's labels sorted, and [] present for a version
// without any. UNVERIFIED (SSM-16), ssm-parameters.md 6.2.
func TestHistoryLabelsSortedAndPresent(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/hl/a", "v1")
		h.overwrite("/hl/a", "v2")
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/hl/a", Labels: []string{"zeta", "alpha", "mid"}})
		hist := h.history("/hl/a")
		if hist[0].Labels == nil || len(hist[0].Labels) != 0 {
			h.Errorf("version 1 Labels = %#v, want [] present", hist[0].Labels)
		}
		if !slices.Equal(hist[1].Labels, []string{"alpha", "mid", "zeta"}) {
			h.Errorf("version 2 Labels = %v, want sorted [alpha mid zeta]", hist[1].Labels)
		}
	})
}

// GetParameterHistory takes an ARN and reports the canonical name
// (ssm-parameters.md 3.3, 7.6); a selector there is not a valid name.
func TestHistoryByARNAndNoSelector(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/ha/a", "v")
		hist := must(h, ssmapi.OpGetParameterHistory, &ssmapi.GetParameterHistoryInput{Name: arnPrefix + "ha/a"}).Parameters
		if len(hist) != 1 || val(hist[0].Name) != "/ha/a" {
			h.Errorf("history by ARN = %+v, want one entry named /ha/a", hist)
		}
		_, err := call(h, ssmapi.OpGetParameterHistory, &ssmapi.GetParameterHistoryInput{Name: "/ha/a:1"})
		wantCode(h.T, "history with a selector", err, ssmapi.IdentityValidationException())
	})
}

// Label and Unlabel take a name only, never an ARN (ssm-parameters.md 7.8).
func TestLabelTakesNoARN(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/ln/a", "v")
		_, err := call(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: arnPrefix + "ln/a", Labels: []string{"x"}})
		wantCode(h.T, "label by ARN", err, ssmapi.IdentityValidationException())
		_, err = call(h, ssmapi.OpUnlabelParameterVersion, &ssmapi.UnlabelParameterVersionInput{Name: arnPrefix + "ln/a", ParameterVersion: 1, Labels: []string{"x"}})
		wantCode(h.T, "unlabel by ARN", err, ssmapi.IdentityValidationException())
	})
}

// The 11th label answers AWS's refusal (ssm-parameters.md 6.2; SSM-16 step
// 06), and a refused call applies none of its labels, the valid ones
// before the limit included.
func TestLabelLimitAppliesNothing(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/ll/a", "v")
		nine := make([]string, 9)
		for i := range nine {
			nine[i] = fmt.Sprintf("l%d", i)
		}
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/ll/a", Labels: nine})
		_, err := call(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/ll/a", Labels: []string{"tenth", "eleventh"}})
		wantCode(h.T, "two labels past nine", err, ssmapi.IdentityParameterVersionLabelLimitExceeded())
		if got := h.history("/ll/a")[0].Labels; len(got) != 9 {
			h.Errorf("labels after the refusal = %v, want the nine", got)
		}
	})
}

// A label given twice is applied once; relabeling the version a label
// already names changes nothing and reports nothing invalid.
func TestLabelRepeatsAndRelabel(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/lr/a", "v")
		out := must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/lr/a", Labels: []string{"x", "x"}})
		if out.ParameterVersion != 1 || len(out.InvalidLabels) != 0 {
			h.Errorf("label x twice = %+v, want version 1 and nothing invalid", out)
		}
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/lr/a", ParameterVersion: new(int64(1)), Labels: []string{"x"}})
		if got := h.history("/lr/a")[0].Labels; !slices.Equal(got, []string{"x"}) {
			h.Errorf("Labels = %v, want [x]", got)
		}
	})
}

// Unlabel of labels the version does not carry removes nothing and still
// answers RemovedLabels [] present (ssm-parameters.md 6.2, 7.10).
func TestUnlabelNothingAttached(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/un/a", "v")
		out := must(h, ssmapi.OpUnlabelParameterVersion, &ssmapi.UnlabelParameterVersionInput{Name: "/un/a", ParameterVersion: 1, Labels: []string{"nope"}})
		if out.RemovedLabels == nil || len(out.RemovedLabels) != 0 || !slices.Equal(out.InvalidLabels, []string{"nope"}) {
			h.Errorf("unlabel = %#v, want RemovedLabels [] and InvalidLabels [nope]", out)
		}
	})
}

// A label moved to another version leaves the first one (ssm-parameters.md
// 6.2: one label names at most one version), as history shows.
func TestMovedLabelLeavesTheOldVersion(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/mv/a", "v1")
		h.overwrite("/mv/a", "v2")
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/mv/a", ParameterVersion: new(int64(1)), Labels: []string{"live"}})
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/mv/a", Labels: []string{"live"}})
		hist := h.history("/mv/a")
		if len(hist[0].Labels) != 0 || !slices.Equal(hist[1].Labels, []string{"live"}) {
			h.Errorf("Labels after the move = %v and %v, want [] and [live]", hist[0].Labels, hist[1].Labels)
		}
	})
}
