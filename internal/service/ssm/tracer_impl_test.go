package ssm_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// Terraform's read after a create (ssm-parameters.md 10.1, 12.5): the
// parameter by GetParameter with WithDecryption true, exactly one
// DescribeParameters entry for Name Equals with the resolved tier, text
// DataType, no KeyId, Description and AllowedPattern absent when unset,
// Policies [] (7.10) and no NextToken, then an empty tag list.
func TestTerraformReadAfterCreate(t *testing.T) {
	run(t, func(h *harness) {
		const name = "/tf/read"
		h.put(name, "v")
		if got := h.mustGet(name, true); val(got.Value) != "v" || val(got.Name) != name || got.Version != 1 {
			h.Errorf("GetParameter = Name %q Value %q Version %d, want %s v 1", val(got.Name), val(got.Value), got.Version, name)
		}
		out := must(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{
			ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Name", Option: new("Equals"), Values: []string{name}}},
		})
		if len(out.Parameters) != 1 || out.NextToken != nil {
			h.Fatalf("DescribeParameters = %d entries, NextToken %v; want 1 and none", len(out.Parameters), out.NextToken)
		}
		m := out.Parameters[0]
		switch {
		case val(m.Name) != name || val(m.ARN) != arnPrefix+"tf/read" || val(m.Type) != ssmapi.ParameterTypeString || m.Version != 1:
			h.Errorf("metadata = Name %q ARN %q Type %q Version %d", val(m.Name), val(m.ARN), val(m.Type), m.Version)
		case val(m.Tier) != ssmapi.ParameterTierStandard || val(m.DataType) != "text":
			h.Errorf("metadata Tier %q DataType %q, want Standard text", val(m.Tier), val(m.DataType))
		case m.KeyId != nil || m.Description != nil || m.AllowedPattern != nil:
			h.Errorf("metadata KeyId %v Description %v AllowedPattern %v, want all absent", m.KeyId, m.Description, m.AllowedPattern)
		case m.Policies == nil || len(m.Policies) != 0:
			h.Errorf("metadata Policies = %#v, want [] present", m.Policies)
		case val(m.LastModifiedUser) != "arn:aws:iam::000000000000:root":
			h.Errorf("LastModifiedUser = %q, want the caller's ARN", val(m.LastModifiedUser))
		}
	})
}

// Terraform's DescribeParameters for a name that does not exist answers
// Parameters [] with no NextToken (ssm-parameters.md 7.10), which the
// provider reads as gone.
func TestDescribeMissingNameIsEmpty(t *testing.T) {
	run(t, func(h *harness) {
		got := h.describeName("/tf/missing")
		if got == nil || len(got) != 0 {
			h.Errorf("Parameters = %#v, want [] present", got)
		}
	})
}

// Destroy is DeleteParameter (ssm-parameters.md 10.1); afterwards the name
// is ParameterNotFound and its tag listing InvalidResourceId (7.9).
func TestDeleteRemovesTheParameter(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/tf/gone", "v")
		must(h, ssmapi.OpDeleteParameter, &ssmapi.DeleteParameterInput{Name: "/tf/gone"})
		wantError(h.T, "GetParameter after delete", h.get("/tf/gone"), ssmapi.IdentityParameterNotFound(), "")
		_, err := call(h, ssmapi.OpListTagsForResource, &ssmapi.ListTagsForResourceInput{ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: "/tf/gone"})
		wantCode(h.T, "ListTagsForResource after delete", err, ssmapi.IdentityInvalidResourceId())
		if got := h.describeName("/tf/gone"); len(got) != 0 {
			h.Errorf("DescribeParameters after delete = %d entries, want 0", len(got))
		}
	})
}
