package ssm_test

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// A Type outside the model's enum (String, StringList, SecureString;
// ssm-parameters.md 4.1) is refused by the generated constraint check with
// the Coral ValidationException (spec conflict 1; error-matrix.md 4) and
// stores nothing, so the handler's parse into the closed set never sees
// it. UNVERIFIED (SSM-01 step 03): AWS's wire identity for it.
func TestPutParameterRefusesAnUnknownType(t *testing.T) {
	run(t, func(h *harness) {
		bogus := ssmapi.ParameterType("Bogus")
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/type/bogus", Value: "v", Type: &bogus})
		if err == nil || err.ID.Status != 400 || !strings.HasSuffix(string(err.ID.Shape), "#ValidationException") {
			h.Fatalf("PutParameter Type Bogus = %v, want a 400 ValidationException", err)
		}
		if err := h.get("/type/bogus"); err == nil || err.ID != ssmapi.IdentityParameterNotFound() {
			h.Errorf("GetParameter after the refused put = %v, want ParameterNotFound", err)
		}
	})
}
