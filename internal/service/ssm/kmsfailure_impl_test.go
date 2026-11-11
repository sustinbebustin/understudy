package ssm

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// kmsRequestID stands in for the request id of the failed KMS call.
const kmsRequestID = "a4b192f8-f524-48f2-a667-fec195c0c008"

// A KMS access denial answers SSM's AccessDeniedException carrying KMS's
// message with the KMS client's SDK v1 suffix, status 400 (ssm-parameters.md
// 5.5; SSM-22 step 07, testdata/parity/ssm/SSM-22/07-no-kms-decrypt.json).
func TestKMSAccessDenialIsAccessDeniedException(t *testing.T) {
	const text = "User: arn:aws:sts::123456789012:assumed-role/us1103-ssm-k7/ssm is not authorized to perform: kms:Decrypt on resource: " +
		"arn:aws:kms:us-east-2:123456789012:key/15db6cb0-3df0-4f41-957d-55fad776664b because no identity-based policy allows the kms:Decrypt action"
	const want = text + " (Service: AWSKMS; Status Code: 400; Error Code: AccessDeniedException; Request ID: " + kmsRequestID + "; Proxy: null)"
	got := kmsFailure("Decrypt", forRead, kmsapi.ErrAccessDeniedException(text), kmsRequestID)
	if got.ID != ssmapi.IdentityAccessDeniedException() || got.Message != want {
		t.Errorf("kmsFailure(AccessDeniedException) = %s %q, want %s %q", got.ID.Shape, got.Message, ssmapi.IdentityAccessDeniedException().Shape, want)
	}
}

// Any other KMS client error answers InvalidKeyId carrying KMS's message
// with the suffix (SSM-22 steps 04-06).
func TestKMSClientErrorIsInvalidKeyIDWithSuffix(t *testing.T) {
	const text = "arn:aws:kms:us-east-2:123456789012:key/15db6cb0-3df0-4f41-957d-55fad776664b is disabled."
	const want = text + " (Service: AWSKMS; Status Code: 400; Error Code: DisabledException; Request ID: " + kmsRequestID + "; Proxy: null)"
	got := kmsFailure("Encrypt", forWrite, kmsapi.ErrDisabledException(text), kmsRequestID)
	if got.ID != ssmapi.IdentityInvalidKeyId() || got.Message != want {
		t.Errorf("kmsFailure(DisabledException) = %s %q, want InvalidKeyId %q", got.ID.Shape, got.Message, want)
	}
}

// A KMS server error answers InternalServerError saying what stayed as it
// was: on a write nothing was stored; on a read the value was not returned
// and the parameter is unchanged (ssm-parameters.md 12.3). The text is
// understudy's own, so it carries no KMS client suffix.
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
		got := kmsFailure(c.op, c.purpose, kmsapi.ErrKMSInternalException("boom"), kmsRequestID)
		if got.ID != ssmapi.IdentityInternalServerError() {
			t.Errorf("%s: identity %v, want InternalServerError", c.op, got.ID)
		}
		if !strings.Contains(got.Message, "KMS "+c.op+" call") || !strings.Contains(got.Message, c.want) || strings.Contains(got.Message, c.notWant) {
			t.Errorf("%s: message %q, want it to name the call and say %q, not %q", c.op, got.Message, c.want, c.notWant)
		}
		if strings.Contains(got.Message, "(Service: ") || strings.Contains(got.Message, kmsRequestID) {
			t.Errorf("%s: message %q carries a KMS client suffix", c.op, got.Message)
		}
	}
}
