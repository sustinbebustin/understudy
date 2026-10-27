package ssm

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// A KMS server error answers InternalServerError saying what stayed as it
// was: on a write nothing was stored; on a read the value was not returned
// and the parameter is unchanged (spec conflict 6; ssm-parameters.md 12.3).
func TestKMSServerErrorSaysWhatWasLeftUnchanged(t *testing.T) {
	cases := []struct {
		op      string
		purpose kmsPurpose
		want    string
		notWant string
	}{
		{"Encrypt", forWrite, "Nothing was written", "not returned"},
		{"Decrypt", forRead, "The value was not returned and the parameter is unchanged", "Nothing was written"},
	}
	for _, c := range cases {
		got := kmsFailure(c.op, c.purpose, kmsapi.ErrKMSInternalException("boom"))
		if got.ID != ssmapi.IdentityInternalServerError() {
			t.Errorf("%s: identity %v, want InternalServerError", c.op, got.ID)
		}
		if !strings.Contains(got.Message, "KMS "+c.op+" call") || !strings.Contains(got.Message, c.want) || strings.Contains(got.Message, c.notWant) {
			t.Errorf("%s: message %q, want it to name the call and say %q, not %q", c.op, got.Message, c.want, c.notWant)
		}
	}
}
