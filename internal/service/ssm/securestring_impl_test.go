package ssm_test

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// The GetParameter half of the blind TestSecureStringCiphertextOpensWithKMS
// and the DescribeParameters half of TestKeyIDReadsBackAsGiven, which also
// need GetParameters and GetParameterHistory: for the default key and a
// customer managed key, the value read without decryption is the base64
// CiphertextBlob KMS Decrypt opens only under PARAMETER_ARN=<the parameter's
// ARN>, and with decryption the plaintext (ssm-parameters.md 5.2, 5.3).
// Under switch ssm.kms.key-id-echo (verbatim) DescribeParameters reads the
// KeyId back as given, alias/aws/ssm when none was (5.4).
func TestSecureStringThroughGetParameterAndDescribe(t *testing.T) {
	run(t, func(h *harness) {
		h.follows("ssm.kms.key-id-echo", "verbatim")
		key := must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata
		must(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/mine", TargetKeyId: key.KeyId})
		const plain = "s3cr3t-value"
		for name, keyID := range map[string]*string{
			"/impl/default":   nil,
			"/impl/id":        new(key.KeyId),
			"/impl/arn":       key.Arn,
			"/impl/alias-arn": new("arn:aws:kms:us-east-1:000000000000:alias/mine"),
			"/impl/alias":     new("alias/mine"),
			"/impl/managed":   new("alias/aws/ssm"),
		} {
			h.putSecure(name, plain, keyID)
			sealed := h.mustGet(name, false)
			arn := arnPrefix + strings.TrimPrefix(name, "/")
			if v := val(sealed.Value); strings.Contains(v, plain) {
				h.Errorf("%s read without decryption holds the plaintext", name)
			}
			if got, err := h.openWithKMS(val(sealed.Value), arn); err != nil || got != plain {
				h.Errorf("%s: KMS Decrypt with PARAMETER_ARN = %q, %v; want the plaintext", name, got, err)
			}
			if _, err := h.openWithKMS(val(sealed.Value), arnPrefix+"another"); err == nil {
				h.Errorf("%s: KMS Decrypt with another PARAMETER_ARN opened the value", name)
			}
			if got := h.mustGet(name, true); val(got.Value) != plain {
				h.Errorf("%s with decryption = %q, want the plaintext", name, val(got.Value))
			}
			want := "alias/aws/ssm"
			if keyID != nil {
				want = *keyID
			}
			if got := val(h.mustDescribeOne(name).KeyId); got != want {
				h.Errorf("DescribeParameters %s KeyId = %q, want %q", name, got, want)
			}
		}
	})
}

// A root parameter changes type in both directions, as Terraform's update
// scenario does (ssm-parameters.md 4.1): String to
// SecureString encrypts and gains a KeyId; back to String reads plain and
// drops it.
func TestRootParameterTypeChangeThroughSecureString(t *testing.T) {
	run(t, func(h *harness) {
		h.put("rootSecret", "a")
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "rootSecret", Value: "b", Type: new(ssmapi.ParameterTypeSecureString), Overwrite: new(true)})
		if got := h.mustGet("rootSecret", false); val(got.Value) == "b" {
			h.Error("SecureString read without decryption returned the plaintext")
		}
		if d := h.mustDescribeOne("rootSecret"); val(d.KeyId) != "alias/aws/ssm" {
			h.Errorf("KeyId after String to SecureString = %q, want alias/aws/ssm", val(d.KeyId))
		}
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "rootSecret", Value: "c", Type: new(ssmapi.ParameterTypeString), Overwrite: new(true)})
		if got := h.mustGet("rootSecret", false); val(got.Value) != "c" {
			h.Errorf("String after SecureString = %q, want c", val(got.Value))
		}
		if d := h.mustDescribeOne("rootSecret"); d.KeyId != nil {
			h.Errorf("KeyId after SecureString to String = %q, want none", *d.KeyId)
		}
	})
}

// Every name of the default key aws/ssm calls KMS as ssm.amazonaws.com, so
// nothing is recorded under the caller: no KeyId, the alias name, its alias
// ARN in the call's scope and the key id KMS lists that alias for.
// UNVERIFIED (SSM-22 step 08), ssm-parameters.md 5.6.
func TestEveryNameOfTheDefaultKeyCallsKMSAsSSM(t *testing.T) {
	root, perr := access.ParsePrincipal("arn:aws:iam::000000000000:root")
	if perr != nil {
		t.Fatal(perr)
	}
	run(t, func(h *harness) {
		h.putSecure("/dflt/none", "v", nil)
		managed := must(h, kmsapi.OpDescribeKey, &kmsapi.DescribeKeyInput{KeyId: "alias/aws/ssm"}).KeyMetadata
		for name, keyID := range map[string]string{
			"/dflt/alias":     "alias/aws/ssm",
			"/dflt/alias-arn": "arn:aws:kms:us-east-1:000000000000:alias/aws/ssm",
			"/dflt/key-id":    managed.KeyId,
			"/dflt/key-arn":   val(managed.Arn),
		} {
			h.putSecure(name, "v", new(keyID))
			if got := h.mustGet(name, true); val(got.Value) != "v" {
				h.Errorf("%s with decryption = %q, want v", name, val(got.Value))
			}
		}
		h.mustGet("/dflt/none", true)
		for _, u := range h.Access.Usage(access.Filter{Principal: root, Service: "kms"}) {
			if _, ok := u.Mode.(access.AsCaller); ok {
				h.Errorf("a default-key SecureString recorded %s under the caller", u.Action)
			}
		}
	})
}

// A SecureString refused by a check before KMS stores nothing and never
// echoes the value; the kept customer key of an overwrite is used again
// (switch ssm.put.omitted-fields, preserved).
func TestSecureStringRefusalsBeforeKMS(t *testing.T) {
	run(t, func(h *harness) {
		const plain = "pattern-refused-secret"
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/pre/a", Value: plain, Type: new(ssmapi.ParameterTypeSecureString), AllowedPattern: new(`\d+`),
		})
		wantCode(h.T, "SecureString against its pattern", err, ssmapi.IdentityParameterPatternMismatchException())
		wantOwnMessage(h.T, "SecureString against its pattern", err, plain)
		if err := h.get("/pre/a"); err == nil {
			h.Error("a refused SecureString was stored")
		}

		key := h.createKey()
		h.putSecure("/pre/kept", "one", new(key))
		h.overwrite("/pre/kept", "two")
		if d := h.mustDescribeOne("/pre/kept"); val(d.KeyId) != key {
			h.Errorf("KeyId after a value-only overwrite = %q, want %q", val(d.KeyId), key)
		}
		if got := h.mustGet("/pre/kept", true); val(got.Value) != "two" || got.Version != 2 {
			h.Errorf("after the overwrite = %q version %d, want two 2", val(got.Value), got.Version)
		}
	})
}
