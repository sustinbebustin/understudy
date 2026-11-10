package ssm_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// Under switch ssm.put.omitted-fields (preserved), an overwrite that leaves
// out Type, Description and DataType keeps them (ssm-parameters.md 7.1);
// an empty Description clears it. The blind
// TestOmittedFieldsOnOverwrite covers KeyId and AllowedPattern, which need
// SecureString and patterns.
func TestOverwriteKeepsOmittedFields(t *testing.T) {
	run(t, func(h *harness) {
		h.follows("ssm.put.omitted-fields", "preserved")
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/keep/list", Value: "a,b", Type: new(ssmapi.ParameterTypeStringList), Description: new("kept"), DataType: new("text"),
		})
		h.overwrite("/keep/list", "c,d")
		d := h.mustDescribeOne("/keep/list")
		if val(d.Type) != ssmapi.ParameterTypeStringList || val(d.Description) != "kept" || val(d.DataType) != "text" || d.Version != 2 {
			h.Errorf("after a value-only overwrite = Type %q Description %q DataType %q Version %d, want StringList kept text 2",
				val(d.Type), val(d.Description), val(d.DataType), d.Version)
		}
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/keep/list", Value: "e", Overwrite: new(true), Description: new("")})
		if d := h.mustDescribeOne("/keep/list"); d.Description != nil {
			h.Errorf("Description after an empty one = %q, want absent", *d.Description)
		}
	})
}

// An overwrite stamps the new version with its own instant on the emulator
// clock and the caller (ssm-parameters.md 9.1, 12.3; invariant 5).
func TestOverwriteStampsTheWrite(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/stamp/a", "v")
		h.advance(time.Hour)
		written := h.Clock.Now().Wire()
		h.overwrite("/stamp/a", "w")
		d := h.mustDescribeOne("/stamp/a")
		if d.LastModifiedDate == nil || !d.LastModifiedDate.Equal(written) {
			h.Errorf("LastModifiedDate = %v, want the overwrite instant %v", d.LastModifiedDate, written)
		}
		if val(d.LastModifiedUser) != "arn:aws:iam::000000000000:root" {
			h.Errorf("LastModifiedUser = %q, want the caller's ARN", val(d.LastModifiedUser))
		}
	})
}

// Tags on a create are stored in first-set order; a key given twice keeps
// its last value, as AddTagsToResource "Adds or overwrites"
// (ssm-parameters.md 7.9). More than 50 tags is TooManyTagsError and
// nothing is created ("Most resources can have a maximum of 50 tags", 7.9
// [D]).
func TestTagsOnCreate(t *testing.T) {
	run(t, func(h *harness) {
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/tags/a", Value: "v", Type: new(ssmapi.ParameterTypeString),
			Tags: []ssmapi.Tag{{Key: "b", Value: "1"}, {Key: "a", Value: "2"}, {Key: "b", Value: "3"}},
		})
		tags := must(h, ssmapi.OpListTagsForResource, &ssmapi.ListTagsForResourceInput{
			ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: "/tags/a",
		}).TagList
		if len(tags) != 2 || tags[0] != (ssmapi.Tag{Key: "b", Value: "3"}) || tags[1] != (ssmapi.Tag{Key: "a", Value: "2"}) {
			h.Errorf("TagList = %+v, want b=3 a=2", tags)
		}

		many := make([]ssmapi.Tag, 51)
		for i := range many {
			many[i] = ssmapi.Tag{Key: fmt.Sprintf("k%02d", i), Value: "v"}
		}
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/tags/many", Value: "v", Type: new(ssmapi.ParameterTypeString), Tags: many})
		wantCode(h.T, "a create with 51 tags", err, ssmapi.IdentityTooManyTagsError())
		if err := h.get("/tags/many"); err == nil {
			h.Error("the create with 51 tags was stored")
		}
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/tags/fifty", Value: "v", Type: new(ssmapi.ParameterTypeString), Tags: many[:50]})
	})
}

// An overwrite keeps the parameter's tags.
func TestOverwriteKeepsTags(t *testing.T) {
	run(t, func(h *harness) {
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/tags/kept", Value: "v", Type: new(ssmapi.ParameterTypeString), Tags: []ssmapi.Tag{{Key: "env", Value: "dev"}},
		})
		h.overwrite("/tags/kept", "w")
		tags := must(h, ssmapi.OpListTagsForResource, &ssmapi.ListTagsForResourceInput{
			ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: "/tags/kept",
		}).TagList
		if len(tags) != 1 || tags[0] != (ssmapi.Tag{Key: "env", Value: "dev"}) {
			h.Errorf("TagList after an overwrite = %+v, want env=dev", tags)
		}
	})
}
