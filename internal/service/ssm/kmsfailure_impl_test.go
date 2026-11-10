package ssm

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// A KMS access denial answers SSM's AccessDeniedException carrying KMS's
// message, not InvalidKeyId (ssm-parameters.md 5.5; SSM-22 step 07).
func TestKMSAccessDenialIsAccessDeniedException(t *testing.T) {
	const text = "User: arn:aws:sts::123456789012:assumed-role/r/s is not authorized to perform: kms:Decrypt on resource: " +
		"arn:aws:kms:us-east-2:123456789012:key/k because no identity-based policy allows the kms:Decrypt action"
	got := kmsFailure("Decrypt", forRead, kmsapi.ErrAccessDeniedException(text))
	if got.ID != ssmapi.IdentityAccessDeniedException() || got.Message != text {
		t.Errorf("kmsFailure(AccessDeniedException) = %s %q, want %s %q", got.ID.Shape, got.Message, ssmapi.IdentityAccessDeniedException().Shape, text)
	}
	if got := kmsFailure("Encrypt", forWrite, kmsapi.ErrDisabledException("k is disabled.")); got.ID != ssmapi.IdentityInvalidKeyId() {
		t.Errorf("kmsFailure(DisabledException) = %s, want InvalidKeyId", got.ID.Shape)
	}
}

// A KMS server error answers InternalServerError saying what stayed as it
// was: on a write nothing was stored; on a read the value was not returned
// and the parameter is unchanged (ssm-parameters.md 12.3).
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
