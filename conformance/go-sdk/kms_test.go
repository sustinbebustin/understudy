package gosdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// Research: docs/research/iam-sts-kms.md section 5, error-matrix.md 5.9.

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// createKey creates a symmetric key and schedules its deletion when the
// test ends.
func createKey(t *testing.T, description string) *types.KeyMetadata {
	t.Helper()
	client := kms.NewFromConfig(awsCfg)
	out, err := client.CreateKey(t.Context(), &kms.CreateKeyInput{Description: aws.String(description)})
	ok(t, err, "CreateKey")
	t.Cleanup(func() {
		_, _ = client.ScheduleKeyDeletion(context.Background(), &kms.ScheduleKeyDeletionInput{KeyId: out.KeyMetadata.KeyId, PendingWindowInDays: aws.Int32(7)})
	})
	return out.KeyMetadata
}

func TestKMSCreateAndDescribeKey(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := kms.NewFromConfig(awsCfg)
	acct := account(t)

	created, err := client.CreateKey(ctx, &kms.CreateKeyInput{})
	ok(t, err, "CreateKey")
	m := created.KeyMetadata
	t.Cleanup(func() {
		_, _ = client.ScheduleKeyDeletion(context.Background(), &kms.ScheduleKeyDeletionInput{KeyId: m.KeyId, PendingWindowInDays: aws.Int32(7)})
	})
	id := aws.ToString(m.KeyId)
	if !uuidPattern.MatchString(id) {
		t.Errorf("KeyId = %q, want a lowercase UUID", id)
	}
	if got, want := aws.ToString(m.Arn), "arn:aws:kms:"+region()+":"+acct+":key/"+id; got != want {
		t.Errorf("Arn = %q, want %q", got, want)
	}
	if aws.ToString(m.AWSAccountId) != acct || !m.Enabled || m.KeyState != types.KeyStateEnabled ||
		m.KeyUsage != types.KeyUsageTypeEncryptDecrypt || m.KeySpec != types.KeySpecSymmetricDefault ||
		m.CustomerMasterKeySpec != types.CustomerMasterKeySpecSymmetricDefault || m.KeyManager != types.KeyManagerTypeCustomer || //nolint:staticcheck // AWS still returns the deprecated field
		m.Origin != types.OriginTypeAwsKms || aws.ToBool(m.MultiRegion) || aws.ToString(m.Description) != "" {
		t.Errorf("default KeyMetadata = %+v", m)
	}
	if !slices.Equal(m.EncryptionAlgorithms, []types.EncryptionAlgorithmSpec{types.EncryptionAlgorithmSpecSymmetricDefault}) {
		t.Errorf("EncryptionAlgorithms = %v, want [SYMMETRIC_DEFAULT]", m.EncryptionAlgorithms)
	}
	if m.CreationDate == nil || m.DeletionDate != nil {
		t.Errorf("CreationDate %v, DeletionDate %v; want a creation date and no deletion date", m.CreationDate, m.DeletionDate)
	}

	alias := "alias/" + name("describe")
	_, err = client.CreateAlias(ctx, &kms.CreateAliasInput{AliasName: aws.String(alias), TargetKeyId: m.KeyId})
	ok(t, err, "CreateAlias")
	t.Cleanup(func() {
		_, _ = client.DeleteAlias(context.Background(), &kms.DeleteAliasInput{AliasName: aws.String(alias)})
	})
	aliasArn := "arn:aws:kms:" + region() + ":" + acct + ":" + alias
	for _, ref := range []string{id, aws.ToString(m.Arn), alias, aliasArn} {
		got, err := client.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(ref)})
		ok(t, err, "DescribeKey "+ref)
		if aws.ToString(got.KeyMetadata.KeyId) != id {
			t.Errorf("DescribeKey %s resolved KeyId %q, want %q", ref, aws.ToString(got.KeyMetadata.KeyId), id)
		}
	}

	list, err := client.ListKeys(ctx, &kms.ListKeysInput{})
	ok(t, err, "ListKeys")
	if !slices.ContainsFunc(list.Keys, func(k types.KeyListEntry) bool {
		return aws.ToString(k.KeyId) == id && aws.ToString(k.KeyArn) == aws.ToString(m.Arn)
	}) {
		t.Errorf("ListKeys does not list %s", id)
	}

	_, err = client.UpdateKeyDescription(ctx, &kms.UpdateKeyDescriptionInput{KeyId: m.KeyId, Description: aws.String("updated")})
	ok(t, err, "UpdateKeyDescription")
	got, err := client.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: m.KeyId})
	ok(t, err, "DescribeKey")
	if aws.ToString(got.KeyMetadata.Description) != "updated" {
		t.Errorf("Description = %q after UpdateKeyDescription", aws.ToString(got.KeyMetadata.Description))
	}

	t.Run("missing key", func(t *testing.T) {
		missing := "11111111-2222-3333-4444-555555555555"
		_, err := client.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(missing)})
		errAs[*types.NotFoundException](t, err, "DescribeKey of a missing key")
		wantStatus(t, err, "DescribeKey of a missing key", 400)
		wantMessage(t, err, "DescribeKey of a missing key", "Key 'arn:aws:kms:"+region()+":"+acct+":key/"+missing+"' does not exist")
	})
}

func TestKMSCryptography(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := kms.NewFromConfig(awsCfg)
	key := createKey(t, "crypto")
	other := createKey(t, "crypto other")
	plaintext := []byte("understudy conformance plaintext")
	encCtx := map[string]string{"purpose": "conformance"}

	enc, err := client.Encrypt(ctx, &kms.EncryptInput{KeyId: key.KeyId, Plaintext: plaintext, EncryptionContext: encCtx})
	ok(t, err, "Encrypt")
	if aws.ToString(enc.KeyId) != aws.ToString(key.Arn) || enc.EncryptionAlgorithm != types.EncryptionAlgorithmSpecSymmetricDefault {
		t.Errorf("Encrypt KeyId %q, EncryptionAlgorithm %q; want the key ARN and SYMMETRIC_DEFAULT", aws.ToString(enc.KeyId), enc.EncryptionAlgorithm)
	}
	if len(enc.CiphertextBlob) == 0 || bytes.Contains(enc.CiphertextBlob, plaintext) {
		t.Error("CiphertextBlob is empty or carries the plaintext")
	}

	t.Run("Decrypt", func(t *testing.T) {
		dec, err := client.Decrypt(ctx, &kms.DecryptInput{CiphertextBlob: enc.CiphertextBlob, EncryptionContext: encCtx})
		ok(t, err, "Decrypt without KeyId")
		if !bytes.Equal(dec.Plaintext, plaintext) || aws.ToString(dec.KeyId) != aws.ToString(key.Arn) {
			t.Errorf("Decrypt = %q under %q, want the plaintext under %s", dec.Plaintext, aws.ToString(dec.KeyId), aws.ToString(key.Arn))
		}
		_, err = client.Decrypt(ctx, &kms.DecryptInput{CiphertextBlob: enc.CiphertextBlob, EncryptionContext: map[string]string{"purpose": "other"}})
		errAs[*types.InvalidCiphertextException](t, err, "Decrypt with a different encryption context")
		_, err = client.Decrypt(ctx, &kms.DecryptInput{CiphertextBlob: enc.CiphertextBlob, EncryptionContext: encCtx, KeyId: other.KeyId})
		errAs[*types.IncorrectKeyException](t, err, "Decrypt naming a different key")
		_, err = client.Decrypt(ctx, &kms.DecryptInput{CiphertextBlob: []byte("not a ciphertext blob at all")})
		errAs[*types.InvalidCiphertextException](t, err, "Decrypt of garbage")
	})

	t.Run("Encrypt above 4096 bytes", func(t *testing.T) {
		_, err := client.Encrypt(ctx, &kms.EncryptInput{KeyId: key.KeyId, Plaintext: bytes.Repeat([]byte("a"), 4097)})
		apiErr(t, err, "Encrypt of 4097 bytes", "ValidationException")
		wantMessage(t, err, "Encrypt of 4097 bytes", "1 validation error detected: Value at 'plaintext' failed to satisfy constraint: Member must have length less than or equal to 4096")
	})

	t.Run("GenerateDataKey", func(t *testing.T) {
		for _, tc := range []struct {
			spec types.DataKeySpec
			n    int32
			size int
		}{{types.DataKeySpecAes256, 0, 32}, {types.DataKeySpecAes128, 0, 16}, {"", 64, 64}} {
			in := &kms.GenerateDataKeyInput{KeyId: key.KeyId, EncryptionContext: encCtx, KeySpec: tc.spec}
			if tc.n > 0 {
				in.NumberOfBytes = aws.Int32(tc.n)
			}
			out, err := client.GenerateDataKey(ctx, in)
			ok(t, err, "GenerateDataKey")
			if len(out.Plaintext) != tc.size || aws.ToString(out.KeyId) != aws.ToString(key.Arn) {
				t.Errorf("GenerateDataKey(%s/%d) = %d bytes under %q, want %d bytes under the key ARN", tc.spec, tc.n, len(out.Plaintext), aws.ToString(out.KeyId), tc.size)
			}
			dec, err := client.Decrypt(ctx, &kms.DecryptInput{CiphertextBlob: out.CiphertextBlob, EncryptionContext: encCtx})
			ok(t, err, "Decrypt of the data key")
			if !bytes.Equal(dec.Plaintext, out.Plaintext) {
				t.Error("the data key's CiphertextBlob does not decrypt to its Plaintext")
			}
		}
		_, err := client.GenerateDataKey(ctx, &kms.GenerateDataKeyInput{KeyId: key.KeyId, KeySpec: types.DataKeySpecAes256, NumberOfBytes: aws.Int32(32)})
		apiErr(t, err, "GenerateDataKey with KeySpec and NumberOfBytes", "ValidationException")
	})

	t.Run("GenerateDataKeyWithoutPlaintext", func(t *testing.T) {
		out, err := client.GenerateDataKeyWithoutPlaintext(ctx, &kms.GenerateDataKeyWithoutPlaintextInput{KeyId: key.KeyId, KeySpec: types.DataKeySpecAes256})
		ok(t, err, "GenerateDataKeyWithoutPlaintext")
		dec, err := client.Decrypt(ctx, &kms.DecryptInput{CiphertextBlob: out.CiphertextBlob})
		ok(t, err, "Decrypt")
		if len(dec.Plaintext) != 32 {
			t.Errorf("the data key decrypts to %d bytes, want 32", len(dec.Plaintext))
		}
	})

	t.Run("ReEncrypt", func(t *testing.T) {
		out, err := client.ReEncrypt(ctx, &kms.ReEncryptInput{
			CiphertextBlob: enc.CiphertextBlob, SourceEncryptionContext: encCtx,
			DestinationKeyId: other.KeyId, DestinationEncryptionContext: map[string]string{"stage": "two"},
		})
		ok(t, err, "ReEncrypt")
		if aws.ToString(out.SourceKeyId) != aws.ToString(key.Arn) || aws.ToString(out.KeyId) != aws.ToString(other.Arn) {
			t.Errorf("ReEncrypt SourceKeyId %q, KeyId %q; want the two key ARNs", aws.ToString(out.SourceKeyId), aws.ToString(out.KeyId))
		}
		dec, err := client.Decrypt(ctx, &kms.DecryptInput{CiphertextBlob: out.CiphertextBlob, EncryptionContext: map[string]string{"stage": "two"}})
		ok(t, err, "Decrypt of the re-encrypted blob")
		if !bytes.Equal(dec.Plaintext, plaintext) || aws.ToString(dec.KeyId) != aws.ToString(other.Arn) {
			t.Errorf("re-encrypted blob decrypts to %q under %q", dec.Plaintext, aws.ToString(dec.KeyId))
		}
	})

	t.Run("GenerateRandom", func(t *testing.T) {
		out, err := client.GenerateRandom(ctx, &kms.GenerateRandomInput{NumberOfBytes: aws.Int32(32)})
		ok(t, err, "GenerateRandom")
		if len(out.Plaintext) != 32 {
			t.Errorf("GenerateRandom returned %d bytes, want 32", len(out.Plaintext))
		}
		_, err = client.GenerateRandom(ctx, &kms.GenerateRandomInput{NumberOfBytes: aws.Int32(0)})
		apiErr(t, err, "GenerateRandom of 0 bytes", "ValidationException")
		wantMessage(t, err, "GenerateRandom of 0 bytes", "1 validation error detected: Value '0' at 'numberOfBytes' failed to satisfy constraint: Member must have value greater than or equal to 1")
		_, err = client.GenerateRandom(ctx, &kms.GenerateRandomInput{})
		apiErr(t, err, "GenerateRandom without NumberOfBytes", "ValidationException")
		wantMessage(t, err, "GenerateRandom without NumberOfBytes", "NumberOfBytes is required.")
	})
}

func TestKMSKeyStates(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := kms.NewFromConfig(awsCfg)
	key := createKey(t, "states")
	arn := aws.ToString(key.Arn)

	_, err := client.DisableKey(ctx, &kms.DisableKeyInput{KeyId: key.KeyId})
	ok(t, err, "DisableKey")
	got, err := client.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: key.KeyId})
	ok(t, err, "DescribeKey")
	if got.KeyMetadata.Enabled || got.KeyMetadata.KeyState != types.KeyStateDisabled {
		t.Errorf("after DisableKey Enabled %t, KeyState %s", got.KeyMetadata.Enabled, got.KeyMetadata.KeyState)
	}
	_, err = client.Encrypt(ctx, &kms.EncryptInput{KeyId: key.KeyId, Plaintext: []byte("x")})
	errAs[*types.DisabledException](t, err, "Encrypt under a disabled key")
	wantMessage(t, err, "Encrypt under a disabled key", arn+" is disabled.")
	_, err = client.EnableKeyRotation(ctx, &kms.EnableKeyRotationInput{KeyId: key.KeyId})
	errAs[*types.DisabledException](t, err, "EnableKeyRotation on a disabled key")
	_, err = client.EnableKey(ctx, &kms.EnableKeyInput{KeyId: key.KeyId})
	ok(t, err, "EnableKey")

	t.Run("rotation", func(t *testing.T) {
		st, err := client.GetKeyRotationStatus(ctx, &kms.GetKeyRotationStatusInput{KeyId: key.KeyId})
		ok(t, err, "GetKeyRotationStatus")
		if st.KeyRotationEnabled {
			t.Error("a new key reports rotation enabled")
		}
		_, err = client.EnableKeyRotation(ctx, &kms.EnableKeyRotationInput{KeyId: key.KeyId, RotationPeriodInDays: aws.Int32(180)})
		ok(t, err, "EnableKeyRotation")
		st, err = client.GetKeyRotationStatus(ctx, &kms.GetKeyRotationStatusInput{KeyId: key.KeyId})
		ok(t, err, "GetKeyRotationStatus")
		if !st.KeyRotationEnabled || aws.ToInt32(st.RotationPeriodInDays) != 180 {
			t.Errorf("after EnableKeyRotation enabled %t, period %d; want true, 180", st.KeyRotationEnabled, aws.ToInt32(st.RotationPeriodInDays))
		}
		_, err = client.DisableKeyRotation(ctx, &kms.DisableKeyRotationInput{KeyId: key.KeyId})
		ok(t, err, "DisableKeyRotation")
		st, err = client.GetKeyRotationStatus(ctx, &kms.GetKeyRotationStatusInput{KeyId: key.KeyId})
		ok(t, err, "GetKeyRotationStatus")
		if st.KeyRotationEnabled {
			t.Error("rotation still enabled after DisableKeyRotation")
		}
	})

	t.Run("scheduled deletion", func(t *testing.T) {
		victim := createKey(t, "deletion")
		varn := aws.ToString(victim.Arn)
		sched, err := client.ScheduleKeyDeletion(ctx, &kms.ScheduleKeyDeletionInput{KeyId: victim.KeyId, PendingWindowInDays: aws.Int32(7)})
		ok(t, err, "ScheduleKeyDeletion")
		if aws.ToString(sched.KeyId) != varn || sched.KeyState != types.KeyStatePendingDeletion || aws.ToInt32(sched.PendingWindowInDays) != 7 {
			t.Errorf("ScheduleKeyDeletion = {KeyId %q, KeyState %s, PendingWindowInDays %d}, want the key ARN, PendingDeletion, 7",
				aws.ToString(sched.KeyId), sched.KeyState, aws.ToInt32(sched.PendingWindowInDays))
		}
		if d := time.Until(aws.ToTime(sched.DeletionDate)); d < 7*24*time.Hour-time.Hour || d > 7*24*time.Hour+time.Hour {
			t.Errorf("DeletionDate is %s from now, want about 7 days", d)
		}
		desc, err := client.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: victim.KeyId})
		ok(t, err, "DescribeKey of a key pending deletion")
		if desc.KeyMetadata.Enabled || desc.KeyMetadata.KeyState != types.KeyStatePendingDeletion || desc.KeyMetadata.DeletionDate == nil {
			t.Errorf("pending key Enabled %t, KeyState %s, DeletionDate %v", desc.KeyMetadata.Enabled, desc.KeyMetadata.KeyState, desc.KeyMetadata.DeletionDate)
		}
		_, err = client.Encrypt(ctx, &kms.EncryptInput{KeyId: victim.KeyId, Plaintext: []byte("x")})
		errAs[*types.KMSInvalidStateException](t, err, "Encrypt under a key pending deletion")
		_, err = client.ScheduleKeyDeletion(ctx, &kms.ScheduleKeyDeletionInput{KeyId: victim.KeyId, PendingWindowInDays: aws.Int32(7)})
		errAs[*types.KMSInvalidStateException](t, err, "ScheduleKeyDeletion of a key pending deletion")
		wantMessageContains(t, err, "ScheduleKeyDeletion of a key pending deletion", "is pending deletion")
		st, err := client.GetKeyRotationStatus(ctx, &kms.GetKeyRotationStatusInput{KeyId: victim.KeyId})
		ok(t, err, "GetKeyRotationStatus of a key pending deletion")
		if st.KeyRotationEnabled {
			t.Error("a key pending deletion reports rotation enabled")
		}

		cancel, err := client.CancelKeyDeletion(ctx, &kms.CancelKeyDeletionInput{KeyId: victim.KeyId})
		ok(t, err, "CancelKeyDeletion")
		if aws.ToString(cancel.KeyId) != varn {
			t.Errorf("CancelKeyDeletion KeyId = %q, want the key ARN", aws.ToString(cancel.KeyId))
		}
		desc, err = client.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: victim.KeyId})
		ok(t, err, "DescribeKey")
		if desc.KeyMetadata.KeyState != types.KeyStateDisabled || desc.KeyMetadata.DeletionDate != nil {
			t.Errorf("after CancelKeyDeletion KeyState %s, DeletionDate %v; want Disabled and no date", desc.KeyMetadata.KeyState, desc.KeyMetadata.DeletionDate)
		}
		_, err = client.CancelKeyDeletion(ctx, &kms.CancelKeyDeletionInput{KeyId: victim.KeyId})
		errAs[*types.KMSInvalidStateException](t, err, "CancelKeyDeletion of a key not pending deletion")
	})
}

func TestKMSPoliciesAliasesTags(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := kms.NewFromConfig(awsCfg)
	acct := account(t)
	key := createKey(t, "policy")
	other := createKey(t, "policy other")

	t.Run("key policy", func(t *testing.T) {
		pol, err := client.GetKeyPolicy(ctx, &kms.GetKeyPolicyInput{KeyId: key.KeyId, PolicyName: aws.String("default")})
		ok(t, err, "GetKeyPolicy")
		var doc struct {
			Version   string
			Statement []struct {
				Effect    string
				Principal struct{ AWS any }
				Action    any
				Resource  any
			}
		}
		if err := json.Unmarshal([]byte(aws.ToString(pol.Policy)), &doc); err != nil {
			t.Fatalf("default key policy is not JSON: %v: %s", err, aws.ToString(pol.Policy))
		}
		root := "arn:aws:iam::" + acct + ":root"
		if doc.Version != "2012-10-17" || len(doc.Statement) != 1 || doc.Statement[0].Effect != "Allow" ||
			!principalIs(doc.Statement[0].Principal.AWS, root) || doc.Statement[0].Action != "kms:*" || doc.Statement[0].Resource != "*" {
			t.Errorf("default key policy = %s, want one statement allowing %s kms:* on *", aws.ToString(pol.Policy), root)
		}
		names, err := client.ListKeyPolicies(ctx, &kms.ListKeyPoliciesInput{KeyId: key.KeyId})
		ok(t, err, "ListKeyPolicies")
		if !slices.Equal(names.PolicyNames, []string{"default"}) {
			t.Errorf("ListKeyPolicies = %v, want [default]", names.PolicyNames)
		}
		custom := `{"Version":"2012-10-17","Id":"custom","Statement":[{"Sid":"root","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::` + acct + `:root"},"Action":"kms:*","Resource":"*"}]}`
		_, err = client.PutKeyPolicy(ctx, &kms.PutKeyPolicyInput{KeyId: key.KeyId, PolicyName: aws.String("default"), Policy: aws.String(custom)})
		ok(t, err, "PutKeyPolicy")
		pol, err = client.GetKeyPolicy(ctx, &kms.GetKeyPolicyInput{KeyId: key.KeyId, PolicyName: aws.String("default")})
		ok(t, err, "GetKeyPolicy")
		wantJSONEqual(t, "GetKeyPolicy after PutKeyPolicy", aws.ToString(pol.Policy), custom)
	})

	t.Run("aliases", func(t *testing.T) {
		alias := "alias/" + name("alias")
		_, err := client.CreateAlias(ctx, &kms.CreateAliasInput{AliasName: aws.String(alias), TargetKeyId: key.KeyId})
		ok(t, err, "CreateAlias")
		aliasArn := "arn:aws:kms:" + region() + ":" + acct + ":" + alias
		list, err := client.ListAliases(ctx, &kms.ListAliasesInput{KeyId: key.KeyId})
		ok(t, err, "ListAliases")
		if !slices.ContainsFunc(list.Aliases, func(a types.AliasListEntry) bool {
			return aws.ToString(a.AliasName) == alias && aws.ToString(a.AliasArn) == aliasArn && aws.ToString(a.TargetKeyId) == aws.ToString(key.KeyId)
		}) {
			t.Errorf("ListAliases(KeyId) = %+v, want %s -> %s with ARN %s", list.Aliases, alias, aws.ToString(key.KeyId), aliasArn)
		}
		_, err = client.CreateAlias(ctx, &kms.CreateAliasInput{AliasName: aws.String(alias), TargetKeyId: other.KeyId})
		errAs[*types.AlreadyExistsException](t, err, "CreateAlias of an existing alias")
		_, err = client.CreateAlias(ctx, &kms.CreateAliasInput{AliasName: aws.String(name("noprefix")), TargetKeyId: key.KeyId})
		apiErr(t, err, "CreateAlias without alias/", "ValidationException")
		wantMessage(t, err, "CreateAlias without alias/", `Alias must start with the prefix "alias/". Please see https://docs.aws.amazon.com/kms/latest/developerguide/kms-alias.html`)

		_, err = client.UpdateAlias(ctx, &kms.UpdateAliasInput{AliasName: aws.String(alias), TargetKeyId: other.KeyId})
		ok(t, err, "UpdateAlias")
		desc, err := client.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(alias)})
		ok(t, err, "DescribeKey by alias")
		if aws.ToString(desc.KeyMetadata.KeyId) != aws.ToString(other.KeyId) {
			t.Errorf("alias resolves to %q after UpdateAlias, want %q", aws.ToString(desc.KeyMetadata.KeyId), aws.ToString(other.KeyId))
		}
		_, err = client.DeleteAlias(ctx, &kms.DeleteAliasInput{AliasName: aws.String(alias)})
		ok(t, err, "DeleteAlias")
		_, err = client.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(alias)})
		errAs[*types.NotFoundException](t, err, "DescribeKey by a deleted alias")
	})

	t.Run("tags", func(t *testing.T) {
		_, err := client.TagResource(ctx, &kms.TagResourceInput{KeyId: key.KeyId, Tags: []types.Tag{
			{TagKey: aws.String("team"), TagValue: aws.String("platform")},
			{TagKey: aws.String("empty"), TagValue: aws.String("")},
		}})
		ok(t, err, "TagResource")
		tags, err := client.ListResourceTags(ctx, &kms.ListResourceTagsInput{KeyId: key.KeyId})
		ok(t, err, "ListResourceTags")
		if !kmsHasTag(tags.Tags, "team", "platform") || !kmsHasTag(tags.Tags, "empty", "") {
			t.Errorf("ListResourceTags = %+v", tags.Tags)
		}
		_, err = client.UntagResource(ctx, &kms.UntagResourceInput{KeyId: key.KeyId, TagKeys: []string{"empty"}})
		ok(t, err, "UntagResource")
		tags, err = client.ListResourceTags(ctx, &kms.ListResourceTagsInput{KeyId: key.KeyId})
		ok(t, err, "ListResourceTags")
		if len(tags.Tags) != 1 || !kmsHasTag(tags.Tags, "team", "platform") {
			t.Errorf("after UntagResource ListResourceTags = %+v", tags.Tags)
		}
	})
}

// principalIs reports whether a policy Principal value (a string or a
// one-element list) names want.
func principalIs(v any, want string) bool {
	switch p := v.(type) {
	case string:
		return p == want
	case []any:
		return len(p) == 1 && p[0] == want
	}
	return false
}

func kmsHasTag(tags []types.Tag, key, value string) bool {
	return slices.ContainsFunc(tags, func(t types.Tag) bool {
		return aws.ToString(t.TagKey) == key && aws.ToString(t.TagValue) == value
	})
}
