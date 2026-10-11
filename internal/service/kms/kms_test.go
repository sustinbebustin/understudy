package kms_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/kms"
)

const (
	day       = 24 * time.Hour
	arnPrefix = "arn:aws:kms:us-east-1:000000000000:"
	// rootPolicy is a key policy as Terraform sends it: sorted keys, compact.
	rootPolicy = `{"Statement":[{"Action":"kms:*","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Resource":"*"}],"Version":"2012-10-17"}`
)

// harness is a kernel with the KMS service and typed shortcuts for its
// operations.
type harness struct {
	*kerneltest.K
	svc *kms.Service
}

func run(t *testing.T, fn func(h *harness)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := kms.New(k.Deps(kmsapi.Service()), kms.Options{})
		fn(&harness{K: k, svc: svc})
	}, kerneltest.Services(kmsapi.Service()))
}

func call[In, Out any](h *harness, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error) {
	return h.Invoke(h.Context(), op, in)
}

func must[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := call(h, op, in)
	if err != nil {
		h.Fatalf("%s = %s: %s", op.Descriptor().ID.Name, err.ID.JSONType, err.Message)
	}
	return out
}

// key creates a default key and returns its id.
func (h *harness) key() string {
	h.Helper()
	return must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata.KeyId
}

func (h *harness) describe(id string) *kmsapi.KeyMetadata {
	h.Helper()
	return must(h, kmsapi.OpDescribeKey, &kmsapi.DescribeKeyInput{KeyId: id}).KeyMetadata
}

func (h *harness) advance(d time.Duration) {
	h.Helper()
	if _, err := h.Clock.Advance(d); err != nil {
		h.Fatal(err)
	}
}

func keyARN(id string) string { return arnPrefix + "key/" + id }

func wantError(t *testing.T, err *awserr.Error, jsonType, message string) {
	t.Helper()
	switch {
	case err == nil:
		t.Fatalf("no error, want %s %q", jsonType, message)
	case err.ID.JSONType != jsonType || err.Message != message:
		t.Fatalf("error = %s %q\nwant  %s %q", err.ID.JSONType, err.Message, jsonType, message)
	}
}

// Two runs from one seed mint the same key id and CurrentKeyMaterialId: the
// material id is an identifier, which ADR 0018 draws from the seeded source,
// though the material itself is never seeded.
func TestSeededRunsAgreeOnMaterialID(t *testing.T) {
	freshKey := func() *kmsapi.KeyMetadata {
		var m *kmsapi.KeyMetadata
		run(t, func(h *harness) { m = must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata })
		return m
	}
	a, b := freshKey(), freshKey()
	if a.KeyId != b.KeyId || deref(a.CurrentKeyMaterialId) != deref(b.CurrentKeyMaterialId) {
		t.Errorf("seeded runs = (%s, %s) and (%s, %s), want equal", a.KeyId, deref(a.CurrentKeyMaterialId), b.KeyId, deref(b.CurrentKeyMaterialId))
	}
}

// CreateKey with no members answers the KeyMetadata of a default symmetric
// key (iam-sts-kms.md 5.2), and DescribeKey answers the same.
func TestCreateKeyDefaults(t *testing.T) {
	run(t, func(h *harness) {
		m := must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata
		switch {
		case len(m.KeyId) != 36:
			h.Errorf("KeyId = %q, want a UUID", m.KeyId)
		case deref(m.Arn) != keyARN(m.KeyId):
			h.Errorf("Arn = %q", deref(m.Arn))
		case deref(m.AWSAccountId) != "000000000000":
			h.Errorf("AWSAccountId = %q", deref(m.AWSAccountId))
		case !m.Enabled || *m.KeyState != kmsapi.KeyStateEnabled:
			h.Errorf("Enabled = %v, KeyState = %v", m.Enabled, *m.KeyState)
		case deref(m.Description) != "":
			h.Errorf("Description = %q, want empty", deref(m.Description))
		case *m.KeyUsage != kmsapi.KeyUsageTypeEncryptDecrypt || *m.KeySpec != kmsapi.KeySpecSymmetricDefault ||
			*m.CustomerMasterKeySpec != kmsapi.CustomerMasterKeySpecSymmetricDefault:
			h.Errorf("usage and spec = %v %v %v", *m.KeyUsage, *m.KeySpec, *m.CustomerMasterKeySpec)
		case *m.Origin != kmsapi.OriginTypeAwsKms || *m.KeyManager != kmsapi.KeyManagerTypeCustomer || *m.MultiRegion:
			h.Errorf("origin, manager, multi-Region = %v %v %v", *m.Origin, *m.KeyManager, *m.MultiRegion)
		case !slices.Equal(m.EncryptionAlgorithms, []kmsapi.EncryptionAlgorithmSpec{kmsapi.EncryptionAlgorithmSpecSymmetricDefault}):
			h.Errorf("EncryptionAlgorithms = %v", m.EncryptionAlgorithms)
		case len(deref(m.CurrentKeyMaterialId)) != 64:
			h.Errorf("CurrentKeyMaterialId = %q, want 64 hex digits", deref(m.CurrentKeyMaterialId))
		case m.DeletionDate != nil:
			h.Errorf("DeletionDate = %v, want absent", m.DeletionDate)
		case !m.CreationDate.Equal(h.Clock.Now().Wire()):
			h.Errorf("CreationDate = %v, want the emulator clock", m.CreationDate)
		}
		if d := h.describe(m.KeyId); deref(d.Arn) != deref(m.Arn) || *d.KeyState != *m.KeyState {
			h.Errorf("DescribeKey = %+v, want CreateKey's metadata", d)
		}
	})
}

// A key policy CreateKey did not get is the default one, with the account
// root as principal, and it parses as JSON (tf-storage.md 4.3).
func TestDefaultKeyPolicy(t *testing.T) {
	run(t, func(h *harness) {
		id := h.key()
		p := must(h, kmsapi.OpGetKeyPolicy, &kmsapi.GetKeyPolicyInput{KeyId: id, PolicyName: new("default")})
		var doc struct {
			Version   string
			ID        string `json:"Id"`
			Statement []struct {
				Sid       string
				Principal struct{ AWS string }
				Action    string
			}
		}
		if err := json.Unmarshal([]byte(deref(p.Policy)), &doc); err != nil {
			h.Fatalf("policy is not JSON: %v", err)
		}
		if doc.Version != "2012-10-17" || doc.ID != "key-default-1" || len(doc.Statement) != 1 ||
			doc.Statement[0].Principal.AWS != "arn:aws:iam::000000000000:root" || doc.Statement[0].Action != "kms:*" {
			h.Errorf("default policy = %+v", doc)
		}
		_, err := call(h, kmsapi.OpGetKeyPolicy, &kmsapi.GetKeyPolicyInput{KeyId: id, PolicyName: new("other")})
		wantError(h.T, err, "NotFoundException", "No such policy exists")
	})
}

// usesKey checks that DescribeKey, Encrypt, GenerateDataKey and Decrypt's
// KeyId all resolve ref to the key with ARN want.
func (h *harness) usesKey(ref, want string) {
	h.Helper()
	if got := deref(h.describe(ref).Arn); got != want {
		h.Errorf("DescribeKey(%s) = %s, want %s", ref, got, want)
	}
	enc := must(h, kmsapi.OpEncrypt, &kmsapi.EncryptInput{KeyId: ref, Plaintext: []byte("p")})
	if got := deref(enc.KeyId); got != want {
		h.Errorf("Encrypt(%s) KeyId = %s, want %s", ref, got, want)
	}
	if got := deref(must(h, kmsapi.OpGenerateDataKey, &kmsapi.GenerateDataKeyInput{KeyId: ref, KeySpec: new(kmsapi.DataKeySpecAes256)}).KeyId); got != want {
		h.Errorf("GenerateDataKey(%s) KeyId = %s, want %s", ref, got, want)
	}
	out := must(h, kmsapi.OpDecrypt, &kmsapi.DecryptInput{CiphertextBlob: enc.CiphertextBlob, KeyId: new(ref)})
	if string(out.Plaintext) != "p" || deref(out.KeyId) != want {
		h.Errorf("Decrypt with KeyId %s = %q under %s, want %q under %s", ref, out.Plaintext, deref(out.KeyId), "p", want)
	}
}

// Every operation that takes a KeyId accepts a key id, a key ARN, an alias
// name and an alias ARN; an ARN in another Region is refused
// (iam-sts-kms.md 5.1).
func TestKeyIdentifierForms(t *testing.T) {
	run(t, func(h *harness) {
		id := h.key()
		must(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/app", TargetKeyId: id})
		for _, ref := range []string{id, keyARN(id), "alias/app", arnPrefix + "alias/app"} {
			h.usesKey(ref, keyARN(id))
		}
		_, err := call(h, kmsapi.OpDescribeKey, &kmsapi.DescribeKeyInput{KeyId: "arn:aws:kms:us-west-2:000000000000:key/" + id})
		wantError(h.T, err, "NotFoundException", "Invalid arn us-west-2")
		_, err = call(h, kmsapi.OpDescribeKey, &kmsapi.DescribeKeyInput{KeyId: "fake-key-id"})
		wantError(h.T, err, "NotFoundException", "Invalid keyId 'fake-key-id'")
		missing := "1234abcd-12ab-34cd-56ef-1234567890ab"
		_, err = call(h, kmsapi.OpDescribeKey, &kmsapi.DescribeKeyInput{KeyId: missing})
		wantError(h.T, err, "NotFoundException", "Key '"+keyARN(missing)+"' does not exist")
		_, err = call(h, kmsapi.OpDescribeKey, &kmsapi.DescribeKeyInput{KeyId: "alias/none"})
		wantError(h.T, err, "NotFoundException", "Alias "+arnPrefix+"alias/none is not found.")
		// GetKeyRotationStatus takes only a key id or key ARN.
		_, err = call(h, kmsapi.OpGetKeyRotationStatus, &kmsapi.GetKeyRotationStatusInput{KeyId: "alias/app"})
		wantError(h.T, err, "NotFoundException", "Invalid keyId 'alias/app'")
	})
}

// A key ARN or alias ARN of another account resolves in that account, as
// cross-account use requires, while that account's keys stay out of the
// caller's own listing. An alias/aws/ alias ARN of another account creates
// no AWS managed key there.
func TestOtherAccountARN(t *testing.T) {
	run(t, func(h *harness) {
		home := h.Scope
		h.Scope.Account = mustAccount(h, "111111111111")
		id := h.key()
		must(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/shared", TargetKeyId: id})
		h.Scope = home

		other := "arn:aws:kms:us-east-1:111111111111:"
		for _, ref := range []string{other + "key/" + id, other + "alias/shared"} {
			h.usesKey(ref, other+"key/"+id)
		}
		if keys := must(h, kmsapi.OpListKeys, &kmsapi.ListKeysInput{}).Keys; len(keys) != 0 {
			h.Errorf("ListKeys of the caller's account = %d keys, want none", len(keys))
		}
		_, err := call(h, kmsapi.OpDescribeKey, &kmsapi.DescribeKeyInput{KeyId: other + "alias/aws/s3"})
		wantError(h.T, err, "NotFoundException", "Alias "+other+"alias/aws/s3 is not found.")

		h.Scope.Account = mustAccount(h, "111111111111")
		aliases := must(h, kmsapi.OpListAliases, &kmsapi.ListAliasesInput{}).Aliases
		if len(aliases) != 1 || deref(aliases[0].AliasName) != "alias/shared" {
			h.Errorf("aliases of the other account = %+v, want alias/shared alone", aliases)
		}
	})
}

// The key management operations are not usable across accounts: another
// account's key ARN is refused whatever the key policy says, and the key is
// unchanged.
func TestOtherAccountKeyManagementRefused(t *testing.T) {
	run(t, func(h *harness) {
		home := h.Scope
		h.Scope.Account = mustAccount(h, "111111111111")
		id := h.key()
		h.Scope = home

		arn := "arn:aws:kms:us-east-1:111111111111:key/" + id
		_, err := call(h, kmsapi.OpScheduleKeyDeletion, &kmsapi.ScheduleKeyDeletionInput{KeyId: arn, PendingWindowInDays: new(int32(7))})
		wantError(h.T, err, "NotFoundException", "Key '"+arn+"' does not exist")
		_, err = call(h, kmsapi.OpGetKeyPolicy, &kmsapi.GetKeyPolicyInput{KeyId: arn})
		wantError(h.T, err, "NotFoundException", "Key '"+arn+"' does not exist")
		if m := h.describe(arn); *m.KeyState != kmsapi.KeyStateEnabled {
			h.Errorf("the other account's key after a refused ScheduleKeyDeletion is %v, want Enabled", *m.KeyState)
		}
	})
}

// A read of a key ARN in an account nobody used leaves no state behind: no
// cell, so no scope is listed for it.
func TestReadOfUnknownScopeLeavesNoTrace(t *testing.T) {
	run(t, func(h *harness) {
		missing := "arn:aws:kms:us-east-1:222222222222:key/1234abcd-12ab-34cd-56ef-1234567890ab"
		_, err := call(h, kmsapi.OpEncrypt, &kmsapi.EncryptInput{KeyId: missing, Plaintext: []byte("p")})
		wantError(h.T, err, "NotFoundException", "Key '"+missing+"' does not exist")
		h.describe("alias/aws/s3")
		for _, s := range h.svc.Scopes() {
			if s.Account.String() == "222222222222" {
				h.Errorf("Scopes() = %v, want no scope for the account only read", h.svc.Scopes())
			}
		}
	})
}

// keyState puts a fresh key in one of the three states that matter for a
// single-Region symmetric key.
type keyState string

const (
	stEnabled  keyState = "Enabled"
	stDisabled keyState = "Disabled"
	stPending  keyState = "PendingDeletion"
)

// fixture is a key in a state, with a ciphertext made while it was enabled
// and an alias that names it.
type fixture struct {
	id, arn string
	blob    []byte
}

func (h *harness) keyIn(st keyState) fixture {
	h.Helper()
	id := h.key()
	blob := must(h, kmsapi.OpEncrypt, &kmsapi.EncryptInput{KeyId: id, Plaintext: []byte("x")}).CiphertextBlob
	must(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/" + id, TargetKeyId: id})
	switch st {
	case stDisabled:
		must(h, kmsapi.OpDisableKey, &kmsapi.DisableKeyInput{KeyId: id})
	case stPending:
		must(h, kmsapi.OpScheduleKeyDeletion, &kmsapi.ScheduleKeyDeletionInput{KeyId: id, PendingWindowInDays: new(int32(7))})
	case stEnabled:
	}
	return fixture{id: id, arn: keyARN(id), blob: blob}
}

// The key-state matrix of iam-sts-kms.md 5.6: every operation against every
// state, with the documented error for the states it refuses.
func TestKeyStateMatrix(t *testing.T) {
	type outcome uint8
	const (
		ok outcome = iota
		disabledErr
		pendingErr
		destinationPendingErr
		notPendingErr
	)
	type row struct {
		op     string
		run    func(h *harness, f fixture) *awserr.Error
		expect map[keyState]outcome
	}
	usable := map[keyState]outcome{stEnabled: ok, stDisabled: disabledErr, stPending: pendingErr}
	anyState := map[keyState]outcome{stEnabled: ok, stDisabled: ok, stPending: ok}
	notPending := map[keyState]outcome{stEnabled: ok, stDisabled: ok, stPending: pendingErr}
	errOf := func(_ any, err *awserr.Error) *awserr.Error { return err }
	rows := []row{
		{"Encrypt", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpEncrypt, &kmsapi.EncryptInput{KeyId: f.id, Plaintext: []byte("p")}))
		}, usable},
		{"Decrypt", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpDecrypt, &kmsapi.DecryptInput{CiphertextBlob: f.blob}))
		}, usable},
		{"ReEncrypt", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpReEncrypt, &kmsapi.ReEncryptInput{CiphertextBlob: f.blob, DestinationKeyId: f.id}))
		}, usable},
		{"GenerateDataKey", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpGenerateDataKey, &kmsapi.GenerateDataKeyInput{KeyId: f.id, KeySpec: new(kmsapi.DataKeySpecAes256)}))
		}, usable},
		{"GenerateDataKeyWithoutPlaintext", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpGenerateDataKeyWithoutPlaintext, &kmsapi.GenerateDataKeyWithoutPlaintextInput{KeyId: f.id, NumberOfBytes: new(int32(64))}))
		}, usable},
		{"EnableKeyRotation", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpEnableKeyRotation, &kmsapi.EnableKeyRotationInput{KeyId: f.id}))
		}, usable},
		{"DisableKeyRotation", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpDisableKeyRotation, &kmsapi.DisableKeyRotationInput{KeyId: f.id}))
		}, usable},
		{"DescribeKey", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpDescribeKey, &kmsapi.DescribeKeyInput{KeyId: f.id}))
		}, anyState},
		{"GetKeyPolicy", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpGetKeyPolicy, &kmsapi.GetKeyPolicyInput{KeyId: f.id}))
		}, anyState},
		{"PutKeyPolicy", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpPutKeyPolicy, &kmsapi.PutKeyPolicyInput{KeyId: f.id, Policy: rootPolicy}))
		}, anyState},
		{"ListKeyPolicies", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpListKeyPolicies, &kmsapi.ListKeyPoliciesInput{KeyId: f.id}))
		}, anyState},
		{"ListResourceTags", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpListResourceTags, &kmsapi.ListResourceTagsInput{KeyId: f.id}))
		}, anyState},
		{"GetKeyRotationStatus", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpGetKeyRotationStatus, &kmsapi.GetKeyRotationStatusInput{KeyId: f.id}))
		}, anyState},
		{"DeleteAlias", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpDeleteAlias, &kmsapi.DeleteAliasInput{AliasName: "alias/" + f.id}))
		}, anyState},
		{"EnableKey", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpEnableKey, &kmsapi.EnableKeyInput{KeyId: f.id}))
		}, notPending},
		{"DisableKey", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpDisableKey, &kmsapi.DisableKeyInput{KeyId: f.id}))
		}, notPending},
		{"ScheduleKeyDeletion", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpScheduleKeyDeletion, &kmsapi.ScheduleKeyDeletionInput{KeyId: f.id}))
		}, notPending},
		{"UpdateKeyDescription", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpUpdateKeyDescription, &kmsapi.UpdateKeyDescriptionInput{KeyId: f.id, Description: "d"}))
		}, notPending},
		{"TagResource", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpTagResource, &kmsapi.TagResourceInput{KeyId: f.id, Tags: []kmsapi.Tag{{TagKey: "k", TagValue: "v"}}}))
		}, notPending},
		{"UntagResource", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpUntagResource, &kmsapi.UntagResourceInput{KeyId: f.id, TagKeys: []string{"k"}}))
		}, notPending},
		{"CreateAlias", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/second-" + f.id, TargetKeyId: f.id}))
		}, notPending},
		{"UpdateAlias destination", func(h *harness, f fixture) *awserr.Error {
			other := h.key()
			must(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/moving-" + f.id, TargetKeyId: other})
			return errOf(call(h, kmsapi.OpUpdateAlias, &kmsapi.UpdateAliasInput{AliasName: "alias/moving-" + f.id, TargetKeyId: f.id}))
		}, map[keyState]outcome{stEnabled: ok, stDisabled: ok, stPending: destinationPendingErr}},
		{"UpdateAlias source", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpUpdateAlias, &kmsapi.UpdateAliasInput{AliasName: "alias/" + f.id, TargetKeyId: h.key()}))
		}, anyState},
		{"CancelKeyDeletion", func(h *harness, f fixture) *awserr.Error {
			return errOf(call(h, kmsapi.OpCancelKeyDeletion, &kmsapi.CancelKeyDeletionInput{KeyId: f.id}))
		}, map[keyState]outcome{stEnabled: notPendingErr, stDisabled: notPendingErr, stPending: ok}},
	}
	run(t, func(h *harness) {
		for _, r := range rows {
			for _, st := range []keyState{stEnabled, stDisabled, stPending} {
				f := h.keyIn(st)
				err := r.run(h, f)
				var wantType, wantMsg string
				switch r.expect[st] {
				case ok:
					if err != nil {
						h.Errorf("%s on a %s key = %s %q, want success", r.op, st, err.ID.JSONType, err.Message)
					}
					continue
				case disabledErr:
					wantType, wantMsg = "DisabledException", f.arn+" is disabled."
				case pendingErr:
					wantType, wantMsg = "KMSInvalidStateException", f.arn+" is pending deletion (or pending replica deletion)."
				case destinationPendingErr:
					wantType, wantMsg = "KMSInvalidStateException", f.arn+" is pending deletion."
				case notPendingErr:
					wantType, wantMsg = "KMSInvalidStateException", f.arn+" is not pending deletion (or pending replica deletion)."
				}
				if err == nil || err.ID.JSONType != wantType || err.Message != wantMsg {
					h.Errorf("%s on a %s key = %v, want %s %q", r.op, st, err, wantType, wantMsg)
				}
			}
		}
	})
}

// GenerateDataKey's blob decrypts to its plaintext with the same encryption
// context, and any other context is InvalidCiphertextException
// (iam-sts-kms.md 5.7).
func TestGenerateDataKeyDecryptRoundTrip(t *testing.T) {
	run(t, func(h *harness) {
		id := h.key()
		ctx := map[string]string{"bucket": "orders", "version": "v1"}
		dk := must(h, kmsapi.OpGenerateDataKey, &kmsapi.GenerateDataKeyInput{KeyId: "alias/aws/secretsmanager", EncryptionContext: ctx, KeySpec: new(kmsapi.DataKeySpecAes256)})
		if len(dk.Plaintext) != 32 {
			h.Fatalf("AES_256 data key is %d bytes", len(dk.Plaintext))
		}
		dk = must(h, kmsapi.OpGenerateDataKey, &kmsapi.GenerateDataKeyInput{KeyId: id, EncryptionContext: ctx, KeySpec: new(kmsapi.DataKeySpecAes128)})
		if len(dk.Plaintext) != 16 || deref(dk.KeyId) != keyARN(id) {
			h.Fatalf("AES_128 data key: %d bytes under %s", len(dk.Plaintext), deref(dk.KeyId))
		}
		out := must(h, kmsapi.OpDecrypt, &kmsapi.DecryptInput{CiphertextBlob: dk.CiphertextBlob, EncryptionContext: ctx})
		if !bytes.Equal(out.Plaintext, dk.Plaintext) || deref(out.KeyId) != keyARN(id) {
			h.Errorf("Decrypt = %x under %s, want %x under %s", out.Plaintext, deref(out.KeyId), dk.Plaintext, keyARN(id))
		}
		wrong := map[string]string{"bucket": "orders", "version": "v2"}
		_, err := call(h, kmsapi.OpDecrypt, &kmsapi.DecryptInput{CiphertextBlob: dk.CiphertextBlob, EncryptionContext: wrong})
		wantError(h.T, err, "InvalidCiphertextException", "")
		_, err = call(h, kmsapi.OpDecrypt, &kmsapi.DecryptInput{CiphertextBlob: dk.CiphertextBlob})
		wantError(h.T, err, "InvalidCiphertextException", "")
		_, err = call(h, kmsapi.OpDecrypt, &kmsapi.DecryptInput{CiphertextBlob: []byte("not a blob at all")})
		wantError(h.T, err, "InvalidCiphertextException", "")
		_, err = call(h, kmsapi.OpDecrypt, &kmsapi.DecryptInput{CiphertextBlob: dk.CiphertextBlob, EncryptionContext: ctx, KeyId: new(h.key())})
		wantError(h.T, err, "IncorrectKeyException", "The key ID in the request does not identify a CMK that can perform this operation.")
		_, err = call(h, kmsapi.OpGenerateDataKey, &kmsapi.GenerateDataKeyInput{KeyId: id, KeySpec: new(kmsapi.DataKeySpecAes256), NumberOfBytes: new(int32(32))})
		wantError(h.T, err, "ValidationException", "Please specify either number of bytes or key spec.")
	})
}

// ReEncrypt moves a ciphertext from one key and context to another; the
// result decrypts only under the destination context.
func TestReEncryptAcrossKeys(t *testing.T) {
	run(t, func(h *harness) {
		src, dst := h.key(), h.key()
		enc := must(h, kmsapi.OpEncrypt, &kmsapi.EncryptInput{KeyId: src, Plaintext: []byte("payload"), EncryptionContext: map[string]string{"a": "1"}})
		re := must(h, kmsapi.OpReEncrypt, &kmsapi.ReEncryptInput{
			CiphertextBlob: enc.CiphertextBlob, SourceEncryptionContext: map[string]string{"a": "1"},
			DestinationKeyId: dst, DestinationEncryptionContext: map[string]string{"b": "2"},
		})
		if deref(re.SourceKeyId) != keyARN(src) || deref(re.KeyId) != keyARN(dst) {
			h.Errorf("ReEncrypt keys = %s -> %s", deref(re.SourceKeyId), deref(re.KeyId))
		}
		out := must(h, kmsapi.OpDecrypt, &kmsapi.DecryptInput{CiphertextBlob: re.CiphertextBlob, EncryptionContext: map[string]string{"b": "2"}})
		if string(out.Plaintext) != "payload" || deref(out.KeyId) != keyARN(dst) {
			h.Errorf("Decrypt = %q under %s", out.Plaintext, deref(out.KeyId))
		}
	})
}

// ScheduleKeyDeletion keeps the key describable as PendingDeletion with its
// DeletionDate until the window ends on the emulator clock; then the key and
// every alias naming it are gone (iam-sts-kms.md 5.6).
func TestScheduledDeletionCompletesUnderAdvance(t *testing.T) {
	run(t, func(h *harness) {
		id := h.key()
		must(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/doomed", TargetKeyId: id})
		start := h.Clock.Now()
		out := must(h, kmsapi.OpScheduleKeyDeletion, &kmsapi.ScheduleKeyDeletionInput{KeyId: id, PendingWindowInDays: new(int32(7))})
		due := start.Add(7 * day).Wire()
		if deref(out.KeyId) != keyARN(id) || *out.KeyState != kmsapi.KeyStatePendingDeletion ||
			*out.PendingWindowInDays != 7 || !out.DeletionDate.Equal(due) {
			h.Fatalf("ScheduleKeyDeletion = %+v", out)
		}
		_, err := call(h, kmsapi.OpScheduleKeyDeletion, &kmsapi.ScheduleKeyDeletionInput{KeyId: id})
		wantError(h.T, err, "KMSInvalidStateException", keyARN(id)+" is pending deletion (or pending replica deletion).")

		h.advance(7*day - time.Second)
		m := h.describe(id)
		if *m.KeyState != kmsapi.KeyStatePendingDeletion || m.Enabled || !m.DeletionDate.Equal(due) {
			h.Fatalf("a second before the deadline: KeyState %v, Enabled %v, DeletionDate %v", *m.KeyState, m.Enabled, m.DeletionDate)
		}
		if rot := must(h, kmsapi.OpGetKeyRotationStatus, &kmsapi.GetKeyRotationStatusInput{KeyId: id}); rot.KeyRotationEnabled {
			h.Error("rotation reported enabled on a key pending deletion")
		}

		h.advance(time.Second)
		_, err = call(h, kmsapi.OpDescribeKey, &kmsapi.DescribeKeyInput{KeyId: id})
		wantError(h.T, err, "NotFoundException", "Key '"+keyARN(id)+"' does not exist")
		if aliases := must(h, kmsapi.OpListAliases, &kmsapi.ListAliasesInput{}).Aliases; len(aliases) != 0 {
			h.Errorf("aliases after deletion = %d, want none", len(aliases))
		}
		if keys := must(h, kmsapi.OpListKeys, &kmsapi.ListKeysInput{}).Keys; len(keys) != 0 {
			h.Errorf("keys after deletion = %d, want none", len(keys))
		}
	})
}

// CancelKeyDeletion brings the key back disabled, and the window's end no
// longer deletes it.
func TestCancelKeyDeletion(t *testing.T) {
	run(t, func(h *harness) {
		id := h.key()
		must(h, kmsapi.OpScheduleKeyDeletion, &kmsapi.ScheduleKeyDeletionInput{KeyId: id, PendingWindowInDays: new(int32(7))})
		out := must(h, kmsapi.OpCancelKeyDeletion, &kmsapi.CancelKeyDeletionInput{KeyId: id})
		if deref(out.KeyId) != keyARN(id) {
			h.Errorf("CancelKeyDeletion KeyId = %s", deref(out.KeyId))
		}
		h.advance(30 * day)
		if m := h.describe(id); *m.KeyState != kmsapi.KeyStateDisabled || m.Enabled || m.DeletionDate != nil {
			h.Errorf("after cancel: KeyState %v, Enabled %v, DeletionDate %v", *m.KeyState, m.Enabled, m.DeletionDate)
		}
	})
}

// The aws_kms_key and aws_kms_alias sequences of tf-storage.md 4.1 and 4.2:
// every read shows what the configuration declared, so a second plan has no
// diff, and destroy converges.
func TestTerraformKeyAndAliasSequence(t *testing.T) {
	run(t, func(h *harness) {
		created := must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{
			Description: new("probe key"), KeySpec: new(kmsapi.KeySpecSymmetricDefault), KeyUsage: new(kmsapi.KeyUsageTypeEncryptDecrypt),
			Tags: []kmsapi.Tag{{TagKey: "Name", TagValue: "probe"}},
		}).KeyMetadata
		id := created.KeyId
		must(h, kmsapi.OpEnableKeyRotation, &kmsapi.EnableKeyRotationInput{KeyId: id, RotationPeriodInDays: new(int32(90))})

		// Read: DescribeKey, GetKeyPolicy, GetKeyRotationStatus, ListResourceTags.
		if m := h.describe(id); deref(m.Description) != "probe key" || !m.Enabled {
			h.Errorf("DescribeKey = %+v", m)
		}
		if p := must(h, kmsapi.OpGetKeyPolicy, &kmsapi.GetKeyPolicyInput{KeyId: id, PolicyName: new("default")}); !json.Valid([]byte(deref(p.Policy))) {
			h.Errorf("policy %q is not JSON", deref(p.Policy))
		}
		rot := must(h, kmsapi.OpGetKeyRotationStatus, &kmsapi.GetKeyRotationStatusInput{KeyId: id})
		if !rot.KeyRotationEnabled || rot.RotationPeriodInDays == nil || *rot.RotationPeriodInDays != 90 {
			h.Errorf("GetKeyRotationStatus = %v, %v; want enabled with period 90", rot.KeyRotationEnabled, rot.RotationPeriodInDays)
		}
		tags := must(h, kmsapi.OpListResourceTags, &kmsapi.ListResourceTagsInput{KeyId: id}).Tags
		if len(tags) != 1 || tags[0] != (kmsapi.Tag{TagKey: "Name", TagValue: "probe"}) {
			h.Errorf("ListResourceTags = %+v", tags)
		}

		// aws_kms_key_policy stores the document verbatim (tf-remaining.md 6).
		must(h, kmsapi.OpPutKeyPolicy, &kmsapi.PutKeyPolicyInput{KeyId: keyARN(id), PolicyName: new("default"), Policy: rootPolicy})
		if p := must(h, kmsapi.OpGetKeyPolicy, &kmsapi.GetKeyPolicyInput{KeyId: id, PolicyName: new("default")}); deref(p.Policy) != rootPolicy {
			h.Errorf("GetKeyPolicy = %q, want the document as sent", deref(p.Policy))
		}

		// aws_kms_alias with an ARN target reads back the bare key id.
		must(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/probe", TargetKeyId: keyARN(id)})
		aliases := must(h, kmsapi.OpListAliases, &kmsapi.ListAliasesInput{}).Aliases
		if len(aliases) != 1 || deref(aliases[0].AliasName) != "alias/probe" || deref(aliases[0].AliasArn) != arnPrefix+"alias/probe" || deref(aliases[0].TargetKeyId) != id {
			h.Errorf("ListAliases = %+v", aliases)
		}
		_, err := call(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/probe", TargetKeyId: id})
		wantError(h.T, err, "AlreadyExistsException", "An alias with the name "+arnPrefix+"alias/probe already exists")

		// Destroy: alias first, then the key with a 7-day window; a second
		// schedule answers the "is pending deletion" text Terraform treats as
		// done.
		must(h, kmsapi.OpDeleteAlias, &kmsapi.DeleteAliasInput{AliasName: "alias/probe"})
		must(h, kmsapi.OpScheduleKeyDeletion, &kmsapi.ScheduleKeyDeletionInput{KeyId: id, PendingWindowInDays: new(int32(7))})
		if m := h.describe(id); *m.KeyState != kmsapi.KeyStatePendingDeletion {
			h.Errorf("KeyState after destroy = %v", *m.KeyState)
		}
		_, err = call(h, kmsapi.OpScheduleKeyDeletion, &kmsapi.ScheduleKeyDeletionInput{KeyId: id, PendingWindowInDays: new(int32(7))})
		if err == nil || !strings.Contains(err.Message, "is pending deletion") {
			h.Errorf("second ScheduleKeyDeletion = %v", err)
		}
	})
}

// Alias names follow CreateAlias's rules: the alias/ prefix is required,
// alias/aws/ is AWS's, and a target is a key, never an alias.
func TestAliasNameRules(t *testing.T) {
	run(t, func(h *harness) {
		id := h.key()
		_, err := call(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "nope", TargetKeyId: id})
		wantError(h.T, err, "ValidationException", `Alias must start with the prefix "alias/". Please see https://docs.aws.amazon.com/kms/latest/developerguide/kms-alias.html`)
		_, err = call(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/aws/mine", TargetKeyId: id})
		wantError(h.T, err, "InvalidAliasNameException", "The alias name for a customer managed CMK cannot begin with alias/aws/.")
		must(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/a", TargetKeyId: id})
		_, err = call(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/b", TargetKeyId: "alias/a"})
		wantError(h.T, err, "ValidationException", "Aliases must refer to keys. Not aliases")
	})
}

// An alias/aws/<service> alias names an AWS managed key, created on first
// use: KeyManager AWS, rotation always on (iam-sts-kms.md 5.4).
func TestAWSManagedKeyOnFirstUse(t *testing.T) {
	run(t, func(h *harness) {
		m := h.describe("alias/aws/secretsmanager")
		if *m.KeyManager != kmsapi.KeyManagerTypeAws {
			h.Errorf("KeyManager = %v, want AWS", *m.KeyManager)
		}
		if again := h.describe("alias/aws/secretsmanager"); again.KeyId != m.KeyId {
			h.Errorf("a second use made another key: %s then %s", m.KeyId, again.KeyId)
		}
		rot := must(h, kmsapi.OpGetKeyRotationStatus, &kmsapi.GetKeyRotationStatusInput{KeyId: m.KeyId})
		if !rot.KeyRotationEnabled || rot.RotationPeriodInDays == nil || *rot.RotationPeriodInDays != 365 {
			h.Errorf("AWS managed key rotation = %v, %v", rot.KeyRotationEnabled, rot.RotationPeriodInDays)
		}
	})
}

// Decrypt's KeyId only names a key: an alias/aws/ alias with no AWS managed
// key yet is IncorrectKeyException and creates none.
func TestDecryptKeyIDCreatesNoManagedKey(t *testing.T) {
	run(t, func(h *harness) {
		blob := must(h, kmsapi.OpEncrypt, &kmsapi.EncryptInput{KeyId: h.key(), Plaintext: []byte("x")}).CiphertextBlob
		_, err := call(h, kmsapi.OpDecrypt, &kmsapi.DecryptInput{CiphertextBlob: blob, KeyId: new("alias/aws/s3")})
		wantError(h.T, err, "IncorrectKeyException", "The key ID in the request does not identify a CMK that can perform this operation.")
		if aliases := must(h, kmsapi.OpListAliases, &kmsapi.ListAliasesInput{}).Aliases; len(aliases) != 0 {
			h.Errorf("aliases after Decrypt named alias/aws/s3 = %+v, want none", aliases)
		}
	})
}

// No caller changes an AWS managed key or deletes its alias, in any of the
// operations that would; reads still work (iam-sts-kms.md 5.2 and 5.4).
func TestAWSManagedKeyIsReadOnly(t *testing.T) {
	run(t, func(h *harness) {
		m := h.describe("alias/aws/secretsmanager")
		id, arn := m.KeyId, deref(m.Arn)
		must(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/customer", TargetKeyId: h.key()})
		errOf := func(_ any, err *awserr.Error) *awserr.Error { return err }
		for op, run := range map[string]func() *awserr.Error{
			"TagResource": func() *awserr.Error {
				return errOf(call(h, kmsapi.OpTagResource, &kmsapi.TagResourceInput{KeyId: id, Tags: []kmsapi.Tag{{TagKey: "k", TagValue: "v"}}}))
			},
			"UntagResource": func() *awserr.Error {
				return errOf(call(h, kmsapi.OpUntagResource, &kmsapi.UntagResourceInput{KeyId: id, TagKeys: []string{"k"}}))
			},
			"EnableKeyRotation": func() *awserr.Error {
				return errOf(call(h, kmsapi.OpEnableKeyRotation, &kmsapi.EnableKeyRotationInput{KeyId: id}))
			},
			"DisableKeyRotation": func() *awserr.Error {
				return errOf(call(h, kmsapi.OpDisableKeyRotation, &kmsapi.DisableKeyRotationInput{KeyId: id}))
			},
			"EnableKey":  func() *awserr.Error { return errOf(call(h, kmsapi.OpEnableKey, &kmsapi.EnableKeyInput{KeyId: id})) },
			"DisableKey": func() *awserr.Error { return errOf(call(h, kmsapi.OpDisableKey, &kmsapi.DisableKeyInput{KeyId: id})) },
			"ScheduleKeyDeletion": func() *awserr.Error {
				return errOf(call(h, kmsapi.OpScheduleKeyDeletion, &kmsapi.ScheduleKeyDeletionInput{KeyId: id}))
			},
			"PutKeyPolicy": func() *awserr.Error {
				return errOf(call(h, kmsapi.OpPutKeyPolicy, &kmsapi.PutKeyPolicyInput{KeyId: id, Policy: rootPolicy}))
			},
			"UpdateKeyDescription": func() *awserr.Error {
				return errOf(call(h, kmsapi.OpUpdateKeyDescription, &kmsapi.UpdateKeyDescriptionInput{KeyId: id, Description: "d"}))
			},
			"CreateAlias": func() *awserr.Error {
				return errOf(call(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/onto-managed", TargetKeyId: id}))
			},
			"UpdateAlias": func() *awserr.Error {
				return errOf(call(h, kmsapi.OpUpdateAlias, &kmsapi.UpdateAliasInput{AliasName: "alias/customer", TargetKeyId: id}))
			},
		} {
			want := arn + " is an AWS managed key. Its tags, rotation, key state, key policy, description, aliases and deletion are managed by AWS and cannot be changed; use a customer managed key (CreateKey) instead."
			if err := run(); err == nil || err.ID.JSONType != "UnsupportedOperationException" || err.Message != want {
				h.Errorf("%s on an AWS managed key = %v, want UnsupportedOperationException %q", op, err, want)
			}
		}
		_, err := call(h, kmsapi.OpDeleteAlias, &kmsapi.DeleteAliasInput{AliasName: "alias/aws/secretsmanager"})
		wantError(h.T, err, "UnsupportedOperationException", arnPrefix+"alias/aws/secretsmanager is the alias of an AWS managed key and cannot be deleted; aliases beginning with alias/aws/ are managed by AWS.")

		after := h.describe("alias/aws/secretsmanager")
		if after.KeyId != id || *after.KeyState != kmsapi.KeyStateEnabled || deref(after.Description) != "Default key" {
			h.Errorf("AWS managed key after refused changes = %s %v %q", after.KeyId, *after.KeyState, deref(after.Description))
		}
		if tags := must(h, kmsapi.OpListResourceTags, &kmsapi.ListResourceTagsInput{KeyId: id}).Tags; len(tags) != 0 {
			h.Errorf("AWS managed key tags = %+v, want none", tags)
		}
		if rot := must(h, kmsapi.OpGetKeyRotationStatus, &kmsapi.GetKeyRotationStatusInput{KeyId: id}); !rot.KeyRotationEnabled {
			h.Error("AWS managed key rotation reported off")
		}
	})
}

// NextRotationDate never lies in the past: rotation is not performed, so
// once the clock passes a rotation date the next one is a period later. A
// repeated EnableKeyRotation keeps the date rotation was turned on.
func TestNextRotationDateFollowsTheClock(t *testing.T) {
	run(t, func(h *harness) {
		id := h.key()
		since := h.Clock.Now()
		must(h, kmsapi.OpEnableKeyRotation, &kmsapi.EnableKeyRotationInput{KeyId: id})
		h.advance(10 * day)
		must(h, kmsapi.OpEnableKeyRotation, &kmsapi.EnableKeyRotationInput{KeyId: id, RotationPeriodInDays: new(int32(365))})
		next := func() time.Time {
			return *must(h, kmsapi.OpGetKeyRotationStatus, &kmsapi.GetKeyRotationStatusInput{KeyId: id}).NextRotationDate
		}
		if got, want := next(), since.Add(365*day).Wire(); !got.Equal(want) {
			h.Errorf("NextRotationDate after a repeated enable = %v, want %v", got, want)
		}
		h.advance(390 * day)
		if got, want := next(), since.Add(730*day).Wire(); !got.Equal(want) {
			h.Errorf("NextRotationDate 400 days in = %v, want %v", got, want)
		}
	})
}

// The constraints the model overlays add: a waiting period of 7 to 30 days
// (iam-sts-kms.md 1.4) and a key policy of at most 32768 characters.
func TestOverlayConstraints(t *testing.T) {
	run(t, func(h *harness) {
		id := h.key()
		for _, days := range []int32{6, 31} {
			_, err := call(h, kmsapi.OpScheduleKeyDeletion, &kmsapi.ScheduleKeyDeletionInput{KeyId: id, PendingWindowInDays: new(days)})
			if err == nil || err.ID.JSONType != "ValidationException" {
				h.Errorf("ScheduleKeyDeletion with %d days = %v, want ValidationException", days, err)
			}
		}
		policy := rootPolicy + strings.Repeat(" ", 32769-len(rootPolicy))
		_, err := call(h, kmsapi.OpPutKeyPolicy, &kmsapi.PutKeyPolicyInput{KeyId: id, Policy: policy})
		if err == nil || err.ID.JSONType != "ValidationException" {
			h.Errorf("PutKeyPolicy with a 32769-character policy = %v, want ValidationException", err)
		}
		if m := h.describe(id); *m.KeyState != kmsapi.KeyStateEnabled {
			h.Errorf("KeyState after refused requests = %v, want Enabled", *m.KeyState)
		}
	})
}

// ListAliases serves at most 100 aliases a page whatever Limit asks for.
// Only that cap is observable: ListResourceTags caps at 50, which is also
// the most tags a key holds, so its test pages by a smaller Limit instead.
func TestListLimitsAreCapped(t *testing.T) {
	run(t, func(h *harness) {
		id := h.key()
		for i := range 101 {
			must(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: fmt.Sprintf("alias/a%03d", i), TargetKeyId: id})
		}
		out := must(h, kmsapi.OpListAliases, &kmsapi.ListAliasesInput{Limit: new(int32(1000))})
		if len(out.Aliases) != 100 || !out.Truncated {
			h.Errorf("ListAliases with Limit 1000 = %d aliases, truncated %v; want 100, true", len(out.Aliases), out.Truncated)
		}
		tags := make([]kmsapi.Tag, 50)
		for i := range tags {
			tags[i] = kmsapi.Tag{TagKey: fmt.Sprintf("k%02d", i), TagValue: "v"}
		}
		must(h, kmsapi.OpTagResource, &kmsapi.TagResourceInput{KeyId: id, Tags: tags})
		var got []kmsapi.Tag
		in := &kmsapi.ListResourceTagsInput{KeyId: id, Limit: new(int32(10))}
		for pages := 1; ; pages++ {
			out := must(h, kmsapi.OpListResourceTags, in)
			got = append(got, out.Tags...)
			if !out.Truncated {
				if pages != 5 || out.NextMarker != nil {
					h.Errorf("ListResourceTags with Limit 10 ended after %d pages with NextMarker %v; want 5 pages, no marker", pages, out.NextMarker)
				}
				break
			}
			if pages >= 5 || len(out.Tags) != 10 || out.NextMarker == nil {
				h.Fatalf("truncated ListResourceTags page %d = %d tags, NextMarker %v; want at most 4 such pages of 10 with a marker", pages, len(out.Tags), out.NextMarker)
			}
			in.Marker = out.NextMarker
		}
		if !slices.Equal(got, tags) {
			h.Errorf("ListResourceTags pages = %v, want %v", got, tags)
		}
	})
}

// An alias whose key a hand-edited snapshot dropped names no key: it
// answers NotFoundException rather than an empty key.
func TestAliasWithoutItsKey(t *testing.T) {
	run(t, func(h *harness) {
		id := h.key()
		must(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/orphan", TargetKeyId: id})
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		for i := range sections {
			var st map[string]map[string]json.RawMessage
			if err := json.Unmarshal(sections[i].Data, &st); err != nil {
				h.Fatal(err)
			}
			delete(st["keys"], id)
			if sections[i].Data, err = json.Marshal(st); err != nil {
				h.Fatal(err)
			}
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		_, derr := call(h, kmsapi.OpEncrypt, &kmsapi.EncryptInput{KeyId: "alias/orphan", Plaintext: []byte("p")})
		wantError(h.T, derr, "NotFoundException", "Key '"+keyARN(id)+"' does not exist")
	})
}

// A snapshot keeps keys pending deletion with their deletion dates, and
// Rearm after a load arms the deletion again: with timers cleared and the
// state imported, the key still goes when its window ends. Key material
// survives, so a blob made before the export decrypts after the import.
func TestSnapshotRoundTripAndRearm(t *testing.T) {
	run(t, func(h *harness) {
		kept := h.key()
		blob := must(h, kmsapi.OpEncrypt, &kmsapi.EncryptInput{KeyId: kept, Plaintext: []byte("kept")}).CiphertextBlob
		doomed := h.key()
		due := must(h, kmsapi.OpScheduleKeyDeletion, &kmsapi.ScheduleKeyDeletionInput{KeyId: doomed, PendingWindowInDays: new(int32(7))}).DeletionDate

		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		if m := h.describe(doomed); *m.KeyState != kmsapi.KeyStatePendingDeletion || m.DeletionDate == nil || !m.DeletionDate.Equal(*due) {
			h.Errorf("after import: KeyState %v, DeletionDate %v, want PendingDeletion at %v", *m.KeyState, m.DeletionDate, *due)
		}
		if out := must(h, kmsapi.OpDecrypt, &kmsapi.DecryptInput{CiphertextBlob: blob}); string(out.Plaintext) != "kept" {
			h.Errorf("Decrypt after import = %q, want kept", out.Plaintext)
		}

		h.svc.Rearm(h.Clock.Now())
		h.advance(7 * day)
		_, derr := call(h, kmsapi.OpDescribeKey, &kmsapi.DescribeKeyInput{KeyId: doomed})
		wantError(h.T, derr, "NotFoundException", "Key '"+keyARN(doomed)+"' does not exist")
		h.describe(kept)
	})
}

func mustAccount(h *harness, id string) aws.AccountID {
	h.Helper()
	a, err := aws.ParseAccountID(id)
	if err != nil {
		h.Fatal(err)
	}
	return a
}

// Keys of another account or Region are invisible: ListKeys and ListAliases
// list only the caller's.
func TestKeysAreScoped(t *testing.T) {
	run(t, func(h *harness) {
		h.key()
		h.Scope.Region = mustRegion(h, "eu-west-1")
		if keys := must(h, kmsapi.OpListKeys, &kmsapi.ListKeysInput{}).Keys; len(keys) != 0 {
			h.Errorf("ListKeys in another Region = %d keys", len(keys))
		}
	})
}

// ListKeys pages through every key exactly once in any page size.
func TestListKeysPaging(t *testing.T) {
	run(t, func(h *harness) {
		want := map[string]bool{}
		for range 7 {
			want[h.key()] = true
		}
		for _, limit := range []int32{1, 2, 3, 7, 100} {
			seen := map[string]bool{}
			var marker *string
			for {
				out := must(h, kmsapi.OpListKeys, &kmsapi.ListKeysInput{Limit: new(limit), Marker: marker})
				for _, k := range out.Keys {
					if seen[deref(k.KeyId)] {
						h.Fatalf("limit %d: %s listed twice", limit, deref(k.KeyId))
					}
					seen[deref(k.KeyId)] = true
				}
				if !out.Truncated {
					break
				}
				marker = out.NextMarker
			}
			if len(seen) != len(want) {
				h.Errorf("limit %d listed %d keys, want %d", limit, len(seen), len(want))
			}
		}
	})
}

func mustRegion(h *harness, name string) aws.Region {
	h.Helper()
	r, err := aws.KnownRegions().Parse(name)
	if err != nil {
		h.Fatal(err)
	}
	return r
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
