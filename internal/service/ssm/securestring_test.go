package ssm_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// A standard SecureString read without decryption is the base64 KMS
// ciphertext blob, which KMS Decrypt opens with encryption context
// PARAMETER_ARN=<parameter ARN> ("You can decrypt an encrypted SecureString
// parameter value by calling the AWS KMS Decrypt operation with the correct
// encryption context and the encrypted parameter value that the Systems
// Manager GetParameter operation returns", ssm-parameters.md 5.2, 5.3,
// aws-doc
// https://docs.aws.amazon.com/kms/latest/developerguide/services-parameter-store.html);
// a read with WithDecryption returns the plaintext. Both for the default
// key and for a customer managed key.
func TestSecureStringCiphertextOpensWithKMS(t *testing.T) {
	run(t, func(h *harness) {
		key := h.createKey()
		for _, c := range []struct {
			name  string
			keyID *string
		}{{"/sec/default", nil}, {"/sec/custom", new(key)}} {
			const plain = "s3cr3t-value"
			h.putSecure(c.name, plain, c.keyID)
			sealed := h.mustGet(c.name, false)
			if val(sealed.Type) != ssmapi.ParameterTypeSecureString {
				h.Errorf("%s Type = %q, want SecureString", c.name, val(sealed.Type))
			}
			if v := val(sealed.Value); v == plain || strings.Contains(v, plain) {
				h.Errorf("%s read without decryption returned the plaintext", c.name)
			}
			arn := arnPrefix + strings.TrimPrefix(c.name, "/")
			if val(sealed.ARN) != arn {
				h.Errorf("%s ARN = %q, want %q", c.name, val(sealed.ARN), arn)
			}
			if got, err := h.openWithKMS(val(sealed.Value), arn); err != nil || got != plain {
				h.Errorf("%s: KMS Decrypt with PARAMETER_ARN = %q, %v; want the plaintext", c.name, got, err)
			}
			if _, err := h.openWithKMS(val(sealed.Value), arnPrefix+"another"); err == nil {
				h.Errorf("%s: KMS Decrypt with another PARAMETER_ARN opened the value; the context must be the parameter's ARN", c.name)
			}
			if got := h.mustGet(c.name, true); val(got.Value) != plain {
				h.Errorf("%s with decryption = %q, want the plaintext", c.name, val(got.Value))
			}
			many := must(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: []string{c.name}, WithDecryption: new(true)})
			if len(many.Parameters) != 1 || val(many.Parameters[0].Value) != plain {
				h.Errorf("%s GetParameters with decryption = %+v, want the plaintext", c.name, many.Parameters)
			}
		}
	})
}

// WithDecryption "is ignored for String and StringList parameter types"
// (ssm-parameters.md 5.3 [M]).
func TestWithDecryptionIsIgnoredForPlainTypes(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/plain/s", "text")
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/plain/l", Value: "a,b", Type: new(ssmapi.ParameterTypeStringList)})
		for name, want := range map[string]string{"/plain/s": "text", "/plain/l": "a,b"} {
			for _, d := range []bool{false, true} {
				if got := h.mustGet(name, d); val(got.Value) != want {
					h.Errorf("%s WithDecryption %v = %q, want %q", name, d, val(got.Value), want)
				}
			}
		}
	})
}

// Under switch ssm.kms.key-id-echo (verbatim), the KeyId a SecureString was
// written with reads back exactly as given from DescribeParameters and
// GetParameterHistory, whether a key id, a key ARN, an alias ARN or an alias
// name; a SecureString written without one reads back alias/aws/ssm
// (ssm-parameters.md 5.4, [T] parameter_test.go:L1099, L1289-L1318); a
// String or StringList has no KeyId (5.4 [M]: "Applies to SecureString
// parameters only").
func TestKeyIDReadsBackAsGiven(t *testing.T) {
	run(t, func(h *harness) {
		h.follows("ssm.kms.key-id-echo", "verbatim")
		key := must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata
		must(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/mine", TargetKeyId: key.KeyId})
		forms := map[string]string{
			"/kid/id":        key.KeyId,
			"/kid/arn":       val(key.Arn),
			"/kid/alias-arn": "arn:aws:kms:us-east-1:000000000000:alias/mine",
			"/kid/alias":     "alias/mine",
			"/kid/managed":   "alias/aws/ssm",
		}
		for name, keyID := range forms {
			h.putSecure(name, "v", new(keyID))
		}
		h.putSecure("/kid/default", "v", nil)
		forms["/kid/default"] = "alias/aws/ssm"
		for name, want := range forms {
			if got := val(h.mustDescribeOne(name).KeyId); got != want {
				h.Errorf("DescribeParameters %s KeyId = %q, want %q", name, got, want)
			}
			hist := h.history(name)
			if len(hist) != 1 || val(hist[0].KeyId) != want {
				h.Errorf("GetParameterHistory %s = %+v, want one entry with KeyId %q", name, hist, want)
			}
			if got := h.mustGet(name, true); val(got.Value) != "v" {
				h.Errorf("%s with decryption = %q, want v", name, val(got.Value))
			}
		}
		h.put("/kid/plain", "v")
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/kid/list", Value: "a,b", Type: new(ssmapi.ParameterTypeStringList)})
		for _, name := range []string{"/kid/plain", "/kid/list"} {
			if d := h.mustDescribeOne(name); d.KeyId != nil {
				h.Errorf("DescribeParameters %s KeyId = %q, want none", name, *d.KeyId)
			}
			if hist := h.history(name); len(hist) != 1 || hist[0].KeyId != nil {
				h.Errorf("GetParameterHistory %s = %+v, want no KeyId", name, hist)
			}
		}
	})
}

// A KMS refusal surfaces as InvalidKeyId carrying KMS's own message ("The
// KMS key is not enabled. When this occurs, Parameter Store returns an
// InvalidKeyId exception with a detailed error message from AWS KMS",
// ssm-parameters.md 5.5, aws-doc KMS guide; understudy extends it to
// every KMS client error): a missing key on write, and a disabled key on
// write and on a read with decryption. Nothing is stored and no value is
// returned.
func TestKMSRefusalsAreInvalidKeyID(t *testing.T) {
	run(t, func(h *harness) {
		kmsMessage := func(keyID string) string {
			h.Helper()
			_, err := call(h, kmsapi.OpEncrypt, &kmsapi.EncryptInput{KeyId: keyID, Plaintext: []byte("probe")})
			if err == nil {
				h.Fatalf("KMS Encrypt under %s succeeded", keyID)
			}
			return err.Message
		}
		const plain = "never-stored-value"
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/kms/missing", Value: plain, Type: new(ssmapi.ParameterTypeSecureString), KeyId: new("alias/does-not-exist")})
		wantCode(h.T, "PutParameter under a missing key", err, ssmapi.IdentityInvalidKeyId())
		if want := kmsMessage("alias/does-not-exist"); err != nil && !strings.Contains(err.Message, want) {
			h.Errorf("message %q does not carry KMS's %q", err.Message, want)
		}
		// KMS's message ends with the KMS client's suffix (SSM-22 step 04).
		if want := " (Service: AWSKMS; Status Code: 400; Error Code: NotFoundException; Request ID: "; err != nil &&
			(!strings.Contains(err.Message, want) || !strings.HasSuffix(err.Message, "; Proxy: null)")) {
			h.Errorf("message %q does not end with the KMS client suffix %q...; Proxy: null)", err.Message, want)
		}
		wantOwnMessage(h.T, "PutParameter under a missing key", err, plain)
		if err := h.get("/kms/missing"); err == nil {
			h.Error("a parameter refused by KMS was stored")
		}

		key := h.createKey()
		h.putSecure("/kms/disabled", plain, new(key))
		sealed := h.mustGet("/kms/disabled", false)
		must(h, kmsapi.OpDisableKey, &kmsapi.DisableKeyInput{KeyId: key})

		blob, derr := base64.StdEncoding.DecodeString(val(sealed.Value))
		if derr != nil {
			h.Fatal(derr)
		}
		_, kerr := call(h, kmsapi.OpDecrypt, &kmsapi.DecryptInput{CiphertextBlob: blob, EncryptionContext: map[string]string{"PARAMETER_ARN": val(sealed.ARN)}})
		if kerr == nil {
			h.Fatal("KMS Decrypt under a disabled key succeeded")
		}
		out, err := call(h, ssmapi.OpGetParameter, &ssmapi.GetParameterInput{Name: "/kms/disabled", WithDecryption: new(true)})
		wantCode(h.T, "GetParameter with decryption under a disabled key", err, ssmapi.IdentityInvalidKeyId())
		if err != nil && !strings.Contains(err.Message, kerr.Message) {
			h.Errorf("message %q does not carry KMS's %q", err.Message, kerr.Message)
		}
		if out != nil {
			h.Errorf("a refused read returned %+v", out)
		}
		_, err = call(h, ssmapi.OpGetParametersByPath, &ssmapi.GetParametersByPathInput{Path: "/kms", WithDecryption: new(true)})
		wantCode(h.T, "GetParametersByPath with decryption under a disabled key", err, ssmapi.IdentityInvalidKeyId())

		_, err = call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/kms/disabled", Value: "next", Overwrite: new(true)})
		wantCode(h.T, "overwrite under a disabled key", err, ssmapi.IdentityInvalidKeyId())
		if want := kmsMessage(key); err != nil && !strings.Contains(err.Message, want) {
			h.Errorf("message %q does not carry KMS's %q", err.Message, want)
		}
		if got := h.mustGet("/kms/disabled", false); got.Version != 1 {
			h.Errorf("Version after the refused overwrite = %d, want 1", got.Version)
		}
	})
}

// Who calls KMS (ssm-parameters.md 5.6):
// for a customer managed key, kms:Encrypt on write and kms:Decrypt on a
// read with decryption are forward-access calls recorded under the caller;
// for the default key alias/aws/ssm the service principal calls, so nothing
// is recorded under the caller.
func TestKMSCallsAreRecordedOnlyForCustomerKeys(t *testing.T) {
	root, perr := access.ParsePrincipal("arn:aws:iam::000000000000:root")
	if perr != nil {
		t.Fatal(perr)
	}
	callerKMS := func(h *harness) map[string]bool {
		h.Helper()
		actions := map[string]bool{}
		for _, u := range h.Access.Usage(access.Filter{Principal: root, Service: "kms"}) {
			if _, ok := u.Mode.(access.AsCaller); ok {
				actions[u.Action.String()] = true
			}
		}
		return actions
	}
	run(t, func(h *harness) {
		h.putSecure("/who/default", "v", nil)
		h.mustGet("/who/default", true)
		if got := callerKMS(h); len(got) != 0 {
			h.Errorf("default-key SecureString recorded %v under the caller, want no kms action", got)
		}
	})
	run(t, func(h *harness) {
		key := h.createKey()
		h.putSecure("/who/custom", "v", new(key))
		h.mustGet("/who/custom", true)
		if got := callerKMS(h); !got["kms:Encrypt"] || !got["kms:Decrypt"] {
			h.Errorf("customer-key SecureString recorded %v under the caller, want kms:Encrypt and kms:Decrypt", got)
		}
	})
}
