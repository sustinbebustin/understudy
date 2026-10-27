package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/server"
)

// ssmCall sends one Parameter Store request signed by s.
func ssmCall(t *testing.T, s signer, endpoint, op, body string) wire {
	t.Helper()
	return jsonCall(t, s, endpoint, "ssm", "1.1", "AmazonSSM."+op, body)
}

// Under iam.enforce=enforce a default-key SecureString is encrypted and
// decrypted as ssm.amazonaws.com (spec conflict 9; ssm-parameters.md 5.6,
// inferred), so an explicit identity-policy deny of kms:Encrypt and
// kms:Decrypt does not stop the caller, as the value-bearing operations'
// coverage notes say. UNVERIFIED (SSM-22 step 08).
func TestEnforceDefaultKeySecureStringIgnoresKMSDeny(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[`+
		`{"Effect":"Allow","Action":"ssm:*","Resource":"*"},`+
		`{"Effect":"Deny","Action":["kms:Encrypt","kms:Decrypt"],"Resource":"*"}]}`)

	if w := ssmCall(t, u, endpoint, "PutParameter", `{"Name":"/enf/secret","Type":"SecureString","Value":"s3cr3t"}`); w.status != http.StatusOK {
		t.Fatalf("PutParameter default-key SecureString = %d %s; want 200", w.status, w.body)
	}
	w := ssmCall(t, u, endpoint, "GetParameter", `{"Name":"/enf/secret","WithDecryption":true}`)
	if w.status != http.StatusOK || !strings.Contains(w.body, `"Value":"s3cr3t"`) {
		t.Fatalf("GetParameter WithDecryption = %d %s; want 200 with the plaintext", w.status, w.body)
	}
}

// Under enforce, GetParameter by the ARN of another account's parameter is
// denied at step 8 with SSM's denial identity before any lookup: the
// resource is named by the request and SSM has no resource policy to grant
// it (spec enforce matrix; AWS's answer is unrecorded, SSM-29). Under off
// the same call answers ParameterNotFound, and another Region's ARN in the
// caller's own account answers ParameterNotFound under enforce too.
func TestEnforceForeignParameterARN(t *testing.T) {
	const (
		foreignAccount = `{"Name":"arn:aws:ssm:us-east-1:111111111111:parameter/x"}`
		foreignRegion  = `{"Name":"arn:aws:ssm:eu-west-1:000000000000:parameter/x"}`
	)
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:*","Resource":"*"}]}`)

	deny := ssmapi.DenialError()
	if w := ssmCall(t, u, endpoint, "GetParameter", foreignAccount); w.status != deny.Status || jsonType(w) != shortName(deny) {
		t.Errorf("enforce: GetParameter by another account's ARN = %d %q %s; want %d %q", w.status, jsonType(w), w.body, deny.Status, shortName(deny))
	}
	if w := ssmCall(t, u, endpoint, "GetParameter", foreignRegion); w.status != http.StatusBadRequest || jsonType(w) != "ParameterNotFound" {
		t.Errorf("enforce: GetParameter by another Region's ARN = %d %q %s; want 400 ParameterNotFound", w.status, jsonType(w), w.body)
	}

	_, offEndpoint, offUser := enforced(t, server.IAMEnforceOff)
	if w := ssmCall(t, offUser, offEndpoint, "GetParameter", foreignAccount); w.status != http.StatusBadRequest || jsonType(w) != "ParameterNotFound" {
		t.Errorf("off: GetParameter by another account's ARN = %d %q %s; want 400 ParameterNotFound", w.status, jsonType(w), w.body)
	}
}
