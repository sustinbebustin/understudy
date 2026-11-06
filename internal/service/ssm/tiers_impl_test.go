package ssm_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// AllowedPattern is a whole-value match (ssm-parameters.md 4.4 [D]): an
// alternation whose first branch matches only a prefix still accepts the
// value its second branch matches whole, and a pattern kept by a
// value-only overwrite of a String refuses with AWS's text, the stored
// value unchanged (switch ssm.put.omitted-fields, 7.1).
func TestAllowedPatternWholeMatchAndKept(t *testing.T) {
	run(t, func(h *harness) {
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/pat/alt", Value: "ab", Type: new(ssmapi.ParameterTypeString), AllowedPattern: new(`a|ab`)})
		h.overwrite("/pat/alt", "a")
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/pat/alt", Value: "abc", Overwrite: new(true)})
		wantError(h.T, "abc against the kept a|ab", err, ssmapi.IdentityParameterPatternMismatchException(), "Parameter value, cannot be validated against allowedPattern: a|ab")
		if got := h.mustGet("/pat/alt", false); val(got.Value) != "a" || got.Version != 2 {
			h.Errorf("after the mismatch = %q version %d, want a 2", val(got.Value), got.Version)
		}
	})
}

// DataType aws:ssm:integration is accepted for a String too, and is kept
// by a value-only overwrite; the empty DataType is not one of the
// documented values (ssm-parameters.md 4.3) and is SSM's
// ValidationException.
func TestDataTypeValues(t *testing.T) {
	run(t, func(h *harness) {
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/dt/int", Value: "v", Type: new(ssmapi.ParameterTypeString), DataType: new("aws:ssm:integration")})
		h.overwrite("/dt/int", "w")
		if got := h.mustGet("/dt/int", false); val(got.DataType) != "aws:ssm:integration" {
			h.Errorf("DataType after a value-only overwrite = %q, want aws:ssm:integration", val(got.DataType))
		}
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/dt/empty", Value: "v", Type: new(ssmapi.ParameterTypeString), DataType: new("")})
		wantCode(h.T, "DataType empty", err, ssmapi.IdentityValidationException())
		wantOwnMessage(h.T, "DataType empty", err, "", "/dt/empty")
	})
}

// An explicit Advanced create answers Tier Advanced; a refused downgrade
// writes nothing (ssm-parameters.md 4.2, 12.4).
func TestAdvancedCreateAndRefusedDowngrade(t *testing.T) {
	run(t, func(h *harness) {
		out := must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/adv/a", Value: "v", Type: new(ssmapi.ParameterTypeString), Tier: new(ssmapi.ParameterTierAdvanced)})
		if val(out.Tier) != ssmapi.ParameterTierAdvanced || out.Version != 1 {
			h.Errorf("Advanced create = %q version %d, want Advanced 1", val(out.Tier), out.Version)
		}
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/adv/a", Value: "w", Overwrite: new(true), Tier: new(ssmapi.ParameterTierStandard)})
		wantCode(h.T, "Advanced to Standard", err, ssmapi.IdentityValidationException())
		if got := h.mustGet("/adv/a", false); val(got.Value) != "v" || got.Version != 1 {
			h.Errorf("after the refused downgrade = %q version %d, want v 1", val(got.Value), got.Version)
		}
	})
}
