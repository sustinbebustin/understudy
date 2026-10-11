package secretsmanager_test

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	sm "github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/kms"
	"github.com/sustinbebustin/understudy/internal/service/secretsmanager"
)

const day = 24 * time.Hour

// harness is a kernel with Secrets Manager and the KMS it encrypts through.
type harness struct {
	*kerneltest.K
	svc    *secretsmanager.Service
	tokens int
}

func run(t *testing.T, fn func(h *harness)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		kms.New(k.Deps(kmsapi.Service()), kms.Options{})
		svc := secretsmanager.New(k.Deps(sm.Service()), secretsmanager.Options{})
		fn(&harness{K: k, svc: svc})
	}, kerneltest.Services(kmsapi.Service(), sm.Service()))
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

// token returns a fresh ClientRequestToken in the form Terraform sends.
func (h *harness) token() string {
	h.tokens++
	return fmt.Sprintf("terraform-%026d", h.tokens)
}

func (h *harness) create(name, value string) *sm.CreateSecretOutput {
	h.Helper()
	return must(h, sm.OpCreateSecret, &sm.CreateSecretInput{Name: name, ClientRequestToken: new(h.token()), SecretString: new(value)})
}

func (h *harness) put(id, value string, stages ...string) *sm.PutSecretValueOutput {
	h.Helper()
	return must(h, sm.OpPutSecretValue, &sm.PutSecretValueInput{SecretId: id, ClientRequestToken: new(h.token()), SecretString: new(value), VersionStages: stages})
}

func (h *harness) get(id, stage string) (*sm.GetSecretValueOutput, *awserr.Error) {
	return call(h, sm.OpGetSecretValue, &sm.GetSecretValueInput{SecretId: id, VersionStage: new(stage)})
}

func (h *harness) describe(id string) *sm.DescribeSecretOutput {
	h.Helper()
	return must(h, sm.OpDescribeSecret, &sm.DescribeSecretInput{SecretId: id})
}

func (h *harness) advance(d time.Duration) {
	h.Helper()
	if _, err := h.Clock.Advance(d); err != nil {
		h.Fatal(err)
	}
}

func wantError(t *testing.T, err *awserr.Error, jsonType, message string) {
	t.Helper()
	switch {
	case err == nil:
		t.Fatalf("no error, want %s %q", jsonType, message)
	case err.ID.JSONType != jsonType || err.Message != message:
		t.Fatalf("error = %s %q\nwant  %s %q", err.ID.JSONType, err.Message, jsonType, message)
	}
}

func val[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// The aws_secretsmanager_secret, _secret_version and _secret_policy
// sequences of tf-storage.md 5 and tf-remaining.md 5: each read shows what
// the configuration declared, a second version demotes the first to
// AWSPREVIOUS, and destroy converges.
func TestTerraformSecretSequence(t *testing.T) {
	run(t, func(h *harness) {
		created := must(h, sm.OpCreateSecret, &sm.CreateSecretInput{
			Name: "probe/secret", ClientRequestToken: new(h.token()), Description: new(""),
			Tags: []sm.Tag{{Key: new("Name"), Value: new("probe")}},
		})
		arn := val(created.ARN)
		if !strings.HasPrefix(arn, "arn:aws:secretsmanager:us-east-1:000000000000:secret:probe/secret-") || len(arn) != len("arn:aws:secretsmanager:us-east-1:000000000000:secret:probe/secret-")+6 {
			h.Fatalf("ARN = %s, want the name, a hyphen and six characters", arn)
		}
		if created.VersionId != nil {
			h.Errorf("a secret created without a value answered VersionId %s", *created.VersionId)
		}

		d := h.describe(arn)
		switch {
		case val(d.Name) != "probe/secret" || val(d.ARN) != arn:
			h.Errorf("DescribeSecret names %s %s", val(d.Name), val(d.ARN))
		case d.Description != nil || d.KmsKeyId != nil || d.DeletedDate != nil || d.VersionIdsToStages != nil:
			h.Errorf("DescribeSecret = %+v, want no description, key, deletion or versions", d)
		case len(d.Tags) != 1 || val(d.Tags[0].Key) != "Name" || val(d.Tags[0].Value) != "probe":
			h.Errorf("Tags = %+v", d.Tags)
		}
		if p := must(h, sm.OpGetResourcePolicy, &sm.GetResourcePolicyInput{SecretId: arn}); p.ResourcePolicy != nil {
			h.Errorf("ResourcePolicy = %s before any was put", *p.ResourcePolicy)
		}
		policy := `{"Statement":[{"Action":"secretsmanager:GetSecretValue","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Resource":"*","Sid":"ReadByRoot"}],"Version":"2012-10-17"}`
		if out := must(h, sm.OpPutResourcePolicy, &sm.PutResourcePolicyInput{SecretId: arn, ResourcePolicy: policy}); val(out.ARN) != arn {
			h.Errorf("PutResourcePolicy ARN = %s", val(out.ARN))
		}
		if p := must(h, sm.OpGetResourcePolicy, &sm.GetResourcePolicyInput{SecretId: arn}); val(p.ResourcePolicy) != policy {
			h.Errorf("GetResourcePolicy = %s, want the document as put", val(p.ResourcePolicy))
		}

		v1 := h.put(arn, `{"user":"u","pass":"p"}`)
		if !slices.Equal(v1.VersionStages, []string{"AWSCURRENT"}) {
			h.Errorf("first PutSecretValue stages = %v", v1.VersionStages)
		}
		got := must(h, sm.OpGetSecretValue, &sm.GetSecretValueInput{SecretId: arn, VersionId: v1.VersionId})
		if val(got.SecretString) != `{"user":"u","pass":"p"}` || !slices.Equal(got.VersionStages, []string{"AWSCURRENT"}) || got.SecretBinary != nil {
			h.Errorf("GetSecretValue = %q %v", val(got.SecretString), got.VersionStages)
		}
		v2 := h.put(arn, "second")
		if got := must(h, sm.OpGetSecretValue, &sm.GetSecretValueInput{SecretId: arn, VersionId: v1.VersionId}); !slices.Equal(got.VersionStages, []string{"AWSPREVIOUS"}) {
			h.Errorf("v1 after a second put has %v, want [AWSPREVIOUS]", got.VersionStages)
		}

		// Destroy the first version: take AWSPREVIOUS off it.
		must(h, sm.OpUpdateSecretVersionStage, &sm.UpdateSecretVersionStageInput{SecretId: arn, VersionStage: "AWSPREVIOUS", RemoveFromVersionId: v1.VersionId})
		stages := h.describe(arn).VersionIdsToStages
		if len(stages) != 1 || !slices.Equal(stages[val(v2.VersionId)], []string{"AWSCURRENT"}) {
			h.Errorf("VersionIdsToStages = %v", stages)
		}

		// Destroy the secret with a 7-day window: it stays describable with
		// DeletedDate, which Terraform reads as gone.
		del := must(h, sm.OpDeleteSecret, &sm.DeleteSecretInput{SecretId: arn, RecoveryWindowInDays: new(int64(7))})
		due := h.Clock.Now().Add(7 * day).Wire()
		if !del.DeletionDate.Equal(due) || val(del.ARN) != arn {
			h.Errorf("DeleteSecret = %v %s", del.DeletionDate, val(del.ARN))
		}
		if d := h.describe(arn); d.DeletedDate == nil || !d.DeletedDate.Equal(due) {
			h.Errorf("DeletedDate = %v, want %v", d.DeletedDate, due)
		}
	})
}

// A PutSecretValue repeated with its token and value returns the same
// version and changes nothing; the token with another value is refused
// (secrets-logs-events-alarms.md 2.3).
func TestPutSecretValueIsIdempotent(t *testing.T) {
	run(t, func(h *harness) {
		arn := val(h.create("app", "v0").ARN)
		in := &sm.PutSecretValueInput{SecretId: arn, ClientRequestToken: new(h.token()), SecretString: new("v1")}
		first := must(h, sm.OpPutSecretValue, in)
		again := must(h, sm.OpPutSecretValue, in)
		if val(again.VersionId) != val(first.VersionId) || !slices.Equal(again.VersionStages, first.VersionStages) {
			h.Errorf("retry = %s %v, first = %s %v", val(again.VersionId), again.VersionStages, val(first.VersionId), first.VersionStages)
		}
		if n := len(must(h, sm.OpListSecretVersionIds, &sm.ListSecretVersionIdsInput{SecretId: arn, IncludeDeprecated: new(true)}).Versions); n != 2 {
			h.Errorf("versions after a retry = %d, want 2", n)
		}
		if out, _ := h.get(arn, "AWSPREVIOUS"); val(out.SecretString) != "v0" {
			h.Errorf("AWSPREVIOUS = %q after a retry, want v0", val(out.SecretString))
		}
		_, err := call(h, sm.OpPutSecretValue, &sm.PutSecretValueInput{SecretId: arn, ClientRequestToken: in.ClientRequestToken, SecretString: new("other")})
		wantError(h.T, err, "ResourceExistsException", "You can't use ClientRequestToken "+*in.ClientRequestToken+" because that value is already in use for a version of secret "+arn+".")
	})
}

// An UpdateSecret repeated with its token is ResourceExistsException even
// with the same value ("results in an error", secrets-logs-events-alarms.md
// 2.3), and the repeat changes nothing, its Description included.
func TestUpdateSecretTokenReuseConflicts(t *testing.T) {
	run(t, func(h *harness) {
		arn := val(h.create("upd", "v0").ARN)
		in := &sm.UpdateSecretInput{SecretId: arn, ClientRequestToken: new(h.token()), SecretString: new("v1")}
		must(h, sm.OpUpdateSecret, in)
		in.Description = new("changed by the repeat")
		_, err := call(h, sm.OpUpdateSecret, in)
		wantError(h.T, err, "ResourceExistsException", "You can't use ClientRequestToken "+*in.ClientRequestToken+" because that value is already in use for a version of secret "+arn+".")
		d := h.describe(arn)
		if d.Description != nil || len(d.VersionIdsToStages) != 2 {
			h.Errorf("after the refused repeat: Description %q, %d versions; want none and 2", val(d.Description), len(d.VersionIdsToStages))
		}
	})
}

// A CreateSecret retried with its token succeeds again; another token for
// the same name is ResourceExistsException.
func TestCreateSecretRetry(t *testing.T) {
	run(t, func(h *harness) {
		in := &sm.CreateSecretInput{Name: "db", ClientRequestToken: new(h.token()), SecretString: new("pw")}
		first := must(h, sm.OpCreateSecret, in)
		again := must(h, sm.OpCreateSecret, in)
		if val(again.ARN) != val(first.ARN) || val(again.VersionId) != val(first.VersionId) {
			h.Errorf("retry = %s %s, first = %s %s", val(again.ARN), val(again.VersionId), val(first.ARN), val(first.VersionId))
		}
		_, err := call(h, sm.OpCreateSecret, &sm.CreateSecretInput{Name: "db", ClientRequestToken: new(h.token()), SecretString: new("pw")})
		wantError(h.T, err, "ResourceExistsException", "The operation failed because the secret db already exists.")
	})
}

// A CreateSecret retry that arrives while the call it retries is still
// sealing through KMS succeeds like a retry after it: both answer the one
// secret and version. KMS is a fake here so the retry can run inside the
// original's GenerateDataKey.
func TestCreateSecretRetryRacingItsOriginal(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		secretsmanager.New(k.Deps(sm.Service()), secretsmanager.Options{})
		h := &harness{K: k}
		in := &sm.CreateSecretInput{Name: "raced", ClientRequestToken: new(h.token()), SecretString: new("pw")}
		dataKey := bytes.Repeat([]byte{7}, 32)
		var (
			retried bool
			retry   *sm.CreateSecretOutput
			rerr    *awserr.Error
		)
		k.Stub(kmsapi.OpGenerateDataKey, func(ctx context.Context, _ *kernel.Call, _ *kmsapi.GenerateDataKeyInput) (*kmsapi.GenerateDataKeyOutput, *awserr.Error) {
			if !retried {
				retried = true
				retry, rerr = h.Invoke(ctx, sm.OpCreateSecret, in)
			}
			return &kmsapi.GenerateDataKeyOutput{Plaintext: slices.Clone(dataKey), CiphertextBlob: []byte("sealed data key")}, nil
		})
		k.Stub(kmsapi.OpDecrypt, func(context.Context, *kernel.Call, *kmsapi.DecryptInput) (*kmsapi.DecryptOutput, *awserr.Error) {
			return &kmsapi.DecryptOutput{Plaintext: slices.Clone(dataKey)}, nil
		})
		first := must(h, sm.OpCreateSecret, in)
		if rerr != nil {
			h.Fatalf("racing retry = %s: %s", rerr.ID.JSONType, rerr.Message)
		}
		if val(first.ARN) == "" || val(first.ARN) != val(retry.ARN) || val(first.VersionId) != val(retry.VersionId) {
			h.Errorf("original = %s %s, racing retry = %s %s", val(first.ARN), val(first.VersionId), val(retry.ARN), val(retry.VersionId))
		}
	}, kerneltest.Services(kmsapi.Service(), sm.Service()))
}

// A customer key set without a value, by CreateSecret or by UpdateSecret,
// is checked at once: a disabled key is EncryptionFailure and is not
// stored. The check is forward access, recorded against the caller.
func TestKeyAccessCheckedWhenSetWithoutValue(t *testing.T) {
	run(t, func(h *harness) {
		usable := must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata.KeyId
		disabled := must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata.KeyId
		must(h, kmsapi.OpDisableKey, &kmsapi.DisableKeyInput{KeyId: disabled})
		refused := "Secrets Manager can't encrypt the secret value with the KMS key: DisabledException: arn:aws:kms:us-east-1:000000000000:key/" + disabled + " is disabled."

		_, err := call(h, sm.OpCreateSecret, &sm.CreateSecretInput{Name: "bad-key", ClientRequestToken: new(h.token()), KmsKeyId: new(disabled)})
		wantError(h.T, err, "EncryptionFailure", refused)
		if _, err := call(h, sm.OpDescribeSecret, &sm.DescribeSecretInput{SecretId: "bad-key"}); err == nil {
			h.Error("CreateSecret under a disabled key stored the secret")
		}

		must(h, sm.OpCreateSecret, &sm.CreateSecretInput{Name: "keyed", ClientRequestToken: new(h.token()), KmsKeyId: new(usable)})
		_, err = call(h, sm.OpUpdateSecret, &sm.UpdateSecretInput{SecretId: "keyed", ClientRequestToken: new(h.token()), KmsKeyId: new(disabled)})
		wantError(h.T, err, "EncryptionFailure", refused)
		if got := val(h.describe("keyed").KmsKeyId); got != usable {
			h.Errorf("KmsKeyId after a refused change = %s, want %s", got, usable)
		}

		root, perr := access.ParsePrincipal("arn:aws:iam::000000000000:root")
		if perr != nil {
			h.Fatal(perr)
		}
		actions := map[string]bool{}
		for _, u := range h.Access.Usage(access.Filter{Principal: root, Service: "kms"}) {
			if _, ok := u.Mode.(access.AsCaller); ok {
				actions[u.Action.String()] = true
			}
		}
		if !actions["kms:Decrypt"] || !actions["kms:GenerateDataKey"] {
			h.Errorf("forward-access KMS actions of a value-less secret with a key = %v, want kms:Decrypt and kms:GenerateDataKey", actions)
		}
	})
}

// A secret deleted with a recovery window refuses its data-plane calls,
// keeps its name, and comes back whole with RestoreSecret.
func TestDeleteThenRestoreWithinWindow(t *testing.T) {
	run(t, func(h *harness) {
		arn := val(h.create("restore/me", "kept").ARN)
		must(h, sm.OpDeleteSecret, &sm.DeleteSecretInput{SecretId: arn})
		_, err := h.get(arn, "AWSCURRENT")
		wantError(h.T, err, "InvalidRequestException", "You can't perform this operation on the secret because it was marked for deletion.")
		_, err = call(h, sm.OpTagResource, &sm.TagResourceInput{SecretId: arn, Tags: []sm.Tag{{Key: new("k"), Value: new("v")}}})
		wantError(h.T, err, "InvalidRequestException", "You can't perform this operation on the secret because it was marked for deletion.")
		_, err = call(h, sm.OpCreateSecret, &sm.CreateSecretInput{Name: "restore/me", ClientRequestToken: new(h.token())})
		wantError(h.T, err, "InvalidRequestException", "You can't create this secret because a secret with this name is already scheduled for deletion.")
		_, err = call(h, sm.OpDeleteSecret, &sm.DeleteSecretInput{SecretId: arn})
		wantError(h.T, err, "InvalidRequestException", "You can't perform this operation on the secret because it was marked for deletion.")
		if n := len(must(h, sm.OpListSecrets, &sm.ListSecretsInput{}).SecretList); n != 0 {
			h.Errorf("ListSecrets shows %d secrets scheduled for deletion", n)
		}
		if n := len(must(h, sm.OpListSecrets, &sm.ListSecretsInput{IncludePlannedDeletion: new(true)}).SecretList); n != 1 {
			h.Errorf("ListSecrets with IncludePlannedDeletion = %d secrets, want 1", n)
		}

		h.advance(29 * day)
		out := must(h, sm.OpRestoreSecret, &sm.RestoreSecretInput{SecretId: "restore/me"})
		if val(out.ARN) != arn {
			h.Errorf("RestoreSecret ARN = %s", val(out.ARN))
		}
		h.advance(30 * day)
		got, err := h.get(arn, "AWSCURRENT")
		if err != nil || val(got.SecretString) != "kept" {
			h.Errorf("after restore and the old deadline: %q, %v", val(got.SecretString), err)
		}
		if h.describe(arn).DeletedDate != nil {
			h.Error("DeletedDate after RestoreSecret")
		}
	})
}

// The recovery window runs on the emulator clock: the secret is there a
// second before it ends and gone at its end, and the name is free again for
// a secret with another ARN.
func TestRecoveryWindowElapsesUnderAdvance(t *testing.T) {
	run(t, func(h *harness) {
		arn := val(h.create("short/lived", "x").ARN)
		must(h, sm.OpDeleteSecret, &sm.DeleteSecretInput{SecretId: arn, RecoveryWindowInDays: new(int64(7))})
		h.advance(7*day - time.Second)
		if h.describe(arn).DeletedDate == nil {
			h.Fatal("the secret lost its DeletedDate before the window ended")
		}
		h.advance(time.Second)
		_, err := call(h, sm.OpDescribeSecret, &sm.DescribeSecretInput{SecretId: arn})
		wantError(h.T, err, "ResourceNotFoundException", "Secrets Manager can't find the specified secret.")
		again := val(h.create("short/lived", "y").ARN)
		if again == arn {
			h.Errorf("the recreated secret has the old ARN %s", arn)
		}
		_, err = call(h, sm.OpDescribeSecret, &sm.DescribeSecretInput{SecretId: arn})
		wantError(h.T, err, "ResourceNotFoundException", "Secrets Manager can't find the specified secret.")
	})
}

// ForceDeleteWithoutRecovery deletes at once, frees the name, and succeeds
// for a secret that does not exist.
func TestForceDelete(t *testing.T) {
	run(t, func(h *harness) {
		arn := val(h.create("gone", "x").ARN)
		must(h, sm.OpDeleteSecret, &sm.DeleteSecretInput{SecretId: arn, ForceDeleteWithoutRecovery: new(true)})
		_, err := call(h, sm.OpDescribeSecret, &sm.DescribeSecretInput{SecretId: arn})
		wantError(h.T, err, "ResourceNotFoundException", "Secrets Manager can't find the specified secret.")
		must(h, sm.OpDeleteSecret, &sm.DeleteSecretInput{SecretId: "never-existed", ForceDeleteWithoutRecovery: new(true)})
		h.create("gone", "y")
		_, err = call(h, sm.OpDeleteSecret, &sm.DeleteSecretInput{SecretId: "gone", ForceDeleteWithoutRecovery: new(true), RecoveryWindowInDays: new(int64(7))})
		wantError(h.T, err, "InvalidParameterException", "You can't use ForceDeleteWithoutRecovery in conjunction with RecoveryWindowInDays.")
		for _, days := range []int64{6, 31} {
			_, err = call(h, sm.OpDeleteSecret, &sm.DeleteSecretInput{SecretId: "gone", RecoveryWindowInDays: new(days)})
			wantError(h.T, err, "InvalidParameterException", "The RecoveryWindowInDays value must be between 7 and 30 days (inclusive).")
		}
	})
}

// The access recorder holds kms:Decrypt for the caller of GetSecretValue
// and kms:GenerateDataKey for the caller of PutSecretValue: Secrets
// Manager calls KMS with forward access (ARCHITECTURE.md 16).
func TestAccessRecorderHoldsForwardKMSCalls(t *testing.T) {
	run(t, func(h *harness) {
		h.create("recorded", "x")
		if _, err := h.get("recorded", "AWSCURRENT"); err != nil {
			h.Fatal(err.Message)
		}
		root, perr := access.ParsePrincipal("arn:aws:iam::000000000000:root")
		if perr != nil {
			h.Fatal(perr)
		}
		actions := map[string]bool{}
		for _, u := range h.Access.Usage(access.Filter{Principal: root, Service: "kms"}) {
			if _, ok := u.Mode.(access.AsCaller); ok {
				actions[u.Action.String()] = true
			}
		}
		if !actions["kms:Decrypt"] || !actions["kms:GenerateDataKey"] {
			h.Errorf("forward-access KMS actions recorded for the caller = %v, want kms:Decrypt and kms:GenerateDataKey", actions)
		}
	})
}

// A secret under a customer key the caller disables can no longer be read:
// KMS's refusal surfaces as DecryptionFailure.
func TestDisabledKeyIsDecryptionFailure(t *testing.T) {
	run(t, func(h *harness) {
		key := must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata.KeyId
		must(h, sm.OpCreateSecret, &sm.CreateSecretInput{Name: "cmk", ClientRequestToken: new(h.token()), KmsKeyId: new(key), SecretString: new("x")})
		if d := h.describe("cmk"); val(d.KmsKeyId) != key {
			h.Errorf("KmsKeyId = %s, want %s as sent", val(d.KmsKeyId), key)
		}
		must(h, kmsapi.OpDisableKey, &kmsapi.DisableKeyInput{KeyId: key})
		_, err := h.get("cmk", "AWSCURRENT")
		wantError(h.T, err, "DecryptionFailure", "Secrets Manager can't decrypt the secret value with the KMS key: DisabledException: arn:aws:kms:us-east-1:000000000000:key/"+key+" is disabled.")
		_, err = call(h, sm.OpPutSecretValue, &sm.PutSecretValueInput{SecretId: "cmk", ClientRequestToken: new(h.token()), SecretString: new("y")})
		if err == nil || err.ID.JSONType != "EncryptionFailure" {
			h.Errorf("PutSecretValue under a disabled key = %v, want EncryptionFailure", err)
		}
	})
}

// A SecretId is a name, a full ARN or a partial ARN without the suffix.
func TestSecretIDForms(t *testing.T) {
	run(t, func(h *harness) {
		arn := val(h.create("ids/secret", "x").ARN)
		partial := arn[:len(arn)-7]
		for _, id := range []string{"ids/secret", arn, partial} {
			if got := val(h.describe(id).ARN); got != arn {
				h.Errorf("DescribeSecret(%s) = %s", id, got)
			}
		}
		_, err := call(h, sm.OpDescribeSecret, &sm.DescribeSecretInput{SecretId: strings.Replace(arn, "us-east-1", "us-west-2", 1)})
		wantError(h.T, err, "ResourceNotFoundException", "Secrets Manager can't find the specified secret.")
		_, err = call(h, sm.OpDescribeSecret, &sm.DescribeSecretInput{SecretId: partial + "-XXXXXX"})
		wantError(h.T, err, "ResourceNotFoundException", "Secrets Manager can't find the specified secret.")
	})
}

// A full or partial ARN of another account resolves in that account, as
// cross-account access requires, and its value decrypts under that
// account's key; the bare name stays the caller's own.
func TestSecretIDOtherAccountARN(t *testing.T) {
	run(t, func(h *harness) {
		home := h.Scope
		other, perr := aws.ParseAccountID("111111111111")
		if perr != nil {
			h.Fatal(perr)
		}
		h.Scope.Account = other
		arn := val(h.create("shared", "theirs").ARN)
		h.Scope = home
		if !strings.HasPrefix(arn, "arn:aws:secretsmanager:us-east-1:111111111111:secret:shared-") {
			h.Fatalf("ARN created in the other account = %s", arn)
		}
		for _, id := range []string{arn, arn[:len(arn)-7]} {
			got, err := h.get(id, "AWSCURRENT")
			if err != nil || val(got.ARN) != arn || val(got.SecretString) != "theirs" {
				h.Errorf("GetSecretValue(%s) = %s %q, %v", id, val(got.ARN), val(got.SecretString), err)
			}
		}
		_, err := call(h, sm.OpDescribeSecret, &sm.DescribeSecretInput{SecretId: "shared"})
		wantError(h.T, err, "ResourceNotFoundException", "Secrets Manager can't find the specified secret.")
	})
}

// UpdateSecretVersionStage names the version that holds a label: absent
// and wrong RemoveFromVersionId each say which one it is.
func TestMoveStageNamesTheHolder(t *testing.T) {
	run(t, func(h *harness) {
		v1 := val(h.create("staged", "one").VersionId)
		v2 := val(h.put("staged", "two", "AWSPENDING").VersionId)
		_, err := call(h, sm.OpUpdateSecretVersionStage, &sm.UpdateSecretVersionStageInput{SecretId: "staged", VersionStage: "AWSCURRENT", MoveToVersionId: new(v2)})
		wantError(h.T, err, "InvalidParameterException", "The parameter RemoveFromVersionId can't be empty. Staging label AWSCURRENT is currently attached to version "+v1+", so you must explicitly reference that version in RemoveFromVersionId.")
		_, err = call(h, sm.OpUpdateSecretVersionStage, &sm.UpdateSecretVersionStageInput{SecretId: "staged", VersionStage: "AWSCURRENT", MoveToVersionId: new(v2), RemoveFromVersionId: new(v2)})
		wantError(h.T, err, "InvalidParameterException", "Staging label AWSCURRENT is attached to version "+v1+", not to RemoveFromVersionId "+v2+". Set RemoveFromVersionId to "+v1+" to move the label.")
	})
}

// GetSecretValue's lookup errors, with the texts recorded against AWS.
func TestGetSecretValueErrors(t *testing.T) {
	run(t, func(h *harness) {
		_, err := h.get("missing", "AWSCURRENT")
		wantError(h.T, err, "ResourceNotFoundException", "Secrets Manager can't find the specified secret.")
		must(h, sm.OpCreateSecret, &sm.CreateSecretInput{Name: "empty", ClientRequestToken: new(h.token())})
		_, err = h.get("empty", "AWSCURRENT")
		wantError(h.T, err, "ResourceNotFoundException", "Secrets Manager can't find the specified secret value for staging label: AWSCURRENT")
		v1 := val(h.put("empty", "one").VersionId)
		_, err = h.get("empty", "AWSPREVIOUS")
		wantError(h.T, err, "ResourceNotFoundException", "Secrets Manager can't find the specified secret value for staging label: AWSPREVIOUS")
		missing := strings.Repeat("0", 36)
		_, err = call(h, sm.OpGetSecretValue, &sm.GetSecretValueInput{SecretId: "empty", VersionId: new(missing)})
		wantError(h.T, err, "ResourceNotFoundException", "Secrets Manager can't find the specified secret value for VersionId: "+missing)
		_, err = call(h, sm.OpGetSecretValue, &sm.GetSecretValueInput{SecretId: "empty", VersionId: new(v1), VersionStage: new("AWSPREVIOUS")})
		wantError(h.T, err, "InvalidRequestException", "You provided a VersionStage that is not associated to the provided VersionId.")
		_, err = call(h, sm.OpPutSecretValue, &sm.PutSecretValueInput{SecretId: "empty", SecretString: new("no token")})
		wantError(h.T, err, "InvalidRequestException", "You must provide a ClientRequestToken value. We recommend a UUID-type value.")
		_, err = call(h, sm.OpCreateSecret, &sm.CreateSecretInput{Name: "bad name", ClientRequestToken: new(h.token())})
		wantError(h.T, err, "ValidationException", "Invalid name. Must be a valid name containing alphanumeric characters, or any of the following: -/_+=.@!")
		_, err = call(h, sm.OpUpdateSecretVersionStage, &sm.UpdateSecretVersionStageInput{SecretId: "empty", VersionStage: "AWSCURRENT", RemoveFromVersionId: new(v1)})
		wantError(h.T, err, "InvalidParameterException", "You can only move staging label AWSCURRENT to a different secret version. It can't be completely removed.")
	})
}

// The first version of a secret takes AWSCURRENT even when PutSecretValue
// names other labels, as its API reference says.
func TestFirstVersionTakesAWSCurrent(t *testing.T) {
	run(t, func(h *harness) {
		must(h, sm.OpCreateSecret, &sm.CreateSecretInput{Name: "first", ClientRequestToken: new(h.token())})
		put := h.put("first", "pending", "AWSPENDING")
		if !slices.Equal(put.VersionStages, []string{"AWSCURRENT", "AWSPENDING"}) {
			h.Errorf("PutSecretValue stages = %v, want [AWSCURRENT AWSPENDING]", put.VersionStages)
		}
		for _, stage := range []string{"AWSCURRENT", "AWSPENDING"} {
			got, err := h.get("first", stage)
			if err != nil || val(got.VersionId) != val(put.VersionId) || !slices.Equal(got.VersionStages, []string{"AWSCURRENT", "AWSPENDING"}) {
				h.Errorf("GetSecretValue(%s) = %s %v, %v; want %s [AWSCURRENT AWSPENDING]", stage, val(got.VersionId), got.VersionStages, err, val(put.VersionId))
			}
		}
	})
}

// UpdateSecret with a new KmsKeyId moves the labeled versions under the new
// key, with or without a new value: with the old key disabled afterwards,
// every label still reads and ListSecretVersionIds reports the new key. A
// KMS refusal while moving them (the old key disabled first) fails the
// update with DecryptionFailure and changes nothing.
func TestUpdateSecretKeyReencryptsLabeledVersions(t *testing.T) {
	for _, c := range []struct {
		name  string
		value *string
		// refused disables the old key before the update rather than after.
		refused bool
		// want is each label's value after a successful update.
		want map[string]string
	}{
		{name: "key only", want: map[string]string{"AWSCURRENT": "two", "AWSPREVIOUS": "one", "AWSPENDING": "pending"}},
		{name: "key and value", value: new("three"), want: map[string]string{"AWSCURRENT": "three", "AWSPREVIOUS": "two", "AWSPENDING": "pending"}},
		{name: "old key refused", refused: true},
		{name: "old key refused with value", value: new("three"), refused: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			run(t, func(h *harness) {
				oldKey := must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata.KeyId
				newKey := must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata.KeyId
				must(h, sm.OpCreateSecret, &sm.CreateSecretInput{Name: "moving", ClientRequestToken: new(h.token()), KmsKeyId: new(oldKey), SecretString: new("one")})
				h.put("moving", "two")
				h.put("moving", "pending", "AWSPENDING")
				before := h.describe("moving")
				update := &sm.UpdateSecretInput{SecretId: "moving", ClientRequestToken: new(h.token()), KmsKeyId: new(newKey), SecretString: c.value}

				if c.refused {
					must(h, kmsapi.OpDisableKey, &kmsapi.DisableKeyInput{KeyId: oldKey})
					_, err := call(h, sm.OpUpdateSecret, update)
					if err == nil || err.ID.JSONType != "DecryptionFailure" {
						h.Fatalf("UpdateSecret with the old key disabled = %v, want DecryptionFailure", err)
					}
					after := h.describe("moving")
					if val(after.KmsKeyId) != oldKey || !maps.EqualFunc(after.VersionIdsToStages, before.VersionIdsToStages, slices.Equal) {
						h.Errorf("after a refused UpdateSecret: KmsKeyId %s, stages %v; want %s, %v", val(after.KmsKeyId), after.VersionIdsToStages, oldKey, before.VersionIdsToStages)
					}
					return
				}

				must(h, sm.OpUpdateSecret, update)
				must(h, kmsapi.OpDisableKey, &kmsapi.DisableKeyInput{KeyId: oldKey})
				for stage, want := range c.want {
					got, err := h.get("moving", stage)
					if err != nil {
						h.Fatalf("GetSecretValue(%s) with the old key disabled = %s: %s", stage, err.ID.JSONType, err.Message)
					}
					if val(got.SecretString) != want {
						h.Errorf("GetSecretValue(%s) = %q, want %q", stage, val(got.SecretString), want)
					}
				}
				for _, v := range must(h, sm.OpListSecretVersionIds, &sm.ListSecretVersionIdsInput{SecretId: "moving"}).Versions {
					if !slices.Equal(v.KmsKeyIds, []string{newKey}) {
						h.Errorf("version %s KmsKeyIds = %v, want [%s]", val(v.VersionId), v.KmsKeyIds, newKey)
					}
				}
			})
		})
	}
}

// A PutSecretValue whose value was sealed under the secret's key while an
// UpdateSecret moved the secret to another key seals it again under the new
// key, so the new AWSCURRENT follows the secret's key as the labeled
// versions do. KMS is a fake here so the UpdateSecret can run inside the
// put's GenerateDataKey.
func TestPutSecretValueRacingAKeyChangeSealsUnderTheNewKey(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		secretsmanager.New(k.Deps(sm.Service()), secretsmanager.Options{})
		h := &harness{K: k}
		dataKey := bytes.Repeat([]byte{7}, 32)
		create := &sm.CreateSecretInput{Name: "moving", ClientRequestToken: new(h.token()), KmsKeyId: new("old"), SecretString: new("one")}
		put := &sm.PutSecretValueInput{SecretId: "moving", ClientRequestToken: new(h.token()), SecretString: new("two")}
		var (
			sealedUnder []string
			moved       bool
			merr        *awserr.Error
		)
		k.Stub(kmsapi.OpGenerateDataKey, func(ctx context.Context, _ *kernel.Call, in *kmsapi.GenerateDataKeyInput) (*kmsapi.GenerateDataKeyOutput, *awserr.Error) {
			if in.EncryptionContext["SecretVersionId"] == *put.ClientRequestToken {
				sealedUnder = append(sealedUnder, in.KeyId)
				if !moved {
					moved = true
					_, merr = h.Invoke(ctx, sm.OpUpdateSecret, &sm.UpdateSecretInput{SecretId: "moving", ClientRequestToken: new(h.token()), KmsKeyId: new("new")})
				}
			}
			return &kmsapi.GenerateDataKeyOutput{Plaintext: slices.Clone(dataKey), CiphertextBlob: []byte("under " + in.KeyId)}, nil
		})
		k.Stub(kmsapi.OpDecrypt, func(context.Context, *kernel.Call, *kmsapi.DecryptInput) (*kmsapi.DecryptOutput, *awserr.Error) {
			return &kmsapi.DecryptOutput{Plaintext: slices.Clone(dataKey)}, nil
		})
		k.Stub(kmsapi.OpEncrypt, func(_ context.Context, _ *kernel.Call, in *kmsapi.EncryptInput) (*kmsapi.EncryptOutput, *awserr.Error) {
			return &kmsapi.EncryptOutput{CiphertextBlob: []byte("under " + in.KeyId)}, nil
		})
		must(h, sm.OpCreateSecret, create)
		must(h, sm.OpPutSecretValue, put)
		if merr != nil {
			h.Fatalf("the racing UpdateSecret = %s: %s", merr.ID.JSONType, merr.Message)
		}
		if !slices.Equal(sealedUnder, []string{"old", "new"}) {
			h.Errorf("the put's value was sealed under %v, want old, then again under new once the key moved", sealedUnder)
		}
		for _, v := range must(h, sm.OpListSecretVersionIds, &sm.ListSecretVersionIdsInput{SecretId: "moving"}).Versions {
			if !slices.Equal(v.KmsKeyIds, []string{"new"}) {
				h.Errorf("version %s KmsKeyIds = %v, want [new]", val(v.VersionId), v.KmsKeyIds)
			}
		}
	}, kerneltest.Services(kmsapi.Service(), sm.Service()))
}

// A binary secret comes back in SecretBinary only.
func TestBinarySecret(t *testing.T) {
	run(t, func(h *harness) {
		must(h, sm.OpCreateSecret, &sm.CreateSecretInput{Name: "bin", ClientRequestToken: new(h.token()), SecretBinary: []byte{0, 1, 2, 255}})
		got, err := h.get("bin", "AWSCURRENT")
		if err != nil || !slices.Equal(got.SecretBinary, []byte{0, 1, 2, 255}) || got.SecretString != nil {
			h.Errorf("GetSecretValue = %x %v, %v", got.SecretBinary, got.SecretString, err)
		}
	})
}

// Tags appear only once a secret is tagged and show as [] once every tag
// is removed (secrets-logs-events-alarms.md 2.4).
func TestTagsPresence(t *testing.T) {
	run(t, func(h *harness) {
		h.create("tagged", "x")
		if h.describe("tagged").Tags != nil {
			h.Error("Tags present on a secret never tagged")
		}
		must(h, sm.OpTagResource, &sm.TagResourceInput{SecretId: "tagged", Tags: []sm.Tag{{Key: new("a"), Value: new("1")}, {Key: new("b"), Value: new("2")}}})
		must(h, sm.OpTagResource, &sm.TagResourceInput{SecretId: "tagged", Tags: []sm.Tag{{Key: new("a"), Value: new("3")}}})
		tags := h.describe("tagged").Tags
		if len(tags) != 2 || val(tags[0].Key) != "a" || val(tags[0].Value) != "3" {
			h.Errorf("Tags = %+v", tags)
		}
		must(h, sm.OpUntagResource, &sm.UntagResourceInput{SecretId: "tagged", TagKeys: []string{"a", "b"}})
		if tags := h.describe("tagged").Tags; tags == nil || len(tags) != 0 {
			h.Errorf("Tags after removing every tag = %#v, want []", tags)
		}
	})
}

// ListSecrets filters by name prefix, description prefix ignoring case,
// tag key and negation, and pages through every match exactly once.
func TestListSecretsFiltersAndPaging(t *testing.T) {
	run(t, func(h *harness) {
		for i, name := range []string{"app/one", "app/two", "db/main", "db/replica", "web"} {
			must(h, sm.OpCreateSecret, &sm.CreateSecretInput{
				Name: name, ClientRequestToken: new(h.token()), Description: new(fmt.Sprintf("Secret %d", i)),
				Tags: []sm.Tag{{Key: new("team-" + name[:2]), Value: new("x")}},
			})
			h.advance(time.Second)
		}
		names := func(filters ...sm.Filter) []string {
			list := must(h, sm.OpListSecrets, &sm.ListSecretsInput{Filters: filters}).SecretList
			out := make([]string, 0, len(list))
			for _, e := range list {
				out = append(out, val(e.Name))
			}
			return out
		}
		filter := func(key sm.FilterNameStringType, values ...string) sm.Filter {
			return sm.Filter{Key: new(key), Values: values}
		}
		for _, c := range []struct {
			filters []sm.Filter
			want    []string
		}{
			{nil, []string{"app/one", "app/two", "db/main", "db/replica", "web"}},
			{[]sm.Filter{filter(sm.FilterNameStringTypeName, "app/")}, []string{"app/one", "app/two"}},
			{[]sm.Filter{filter(sm.FilterNameStringTypeName, "!app/")}, []string{"db/main", "db/replica", "web"}},
			{[]sm.Filter{filter(sm.FilterNameStringTypeName, "App/")}, nil},
			{[]sm.Filter{filter(sm.FilterNameStringTypeDescription, "secret 3")}, []string{"db/replica"}},
			{[]sm.Filter{filter(sm.FilterNameStringTypeTagKey, "team-db")}, []string{"db/main", "db/replica"}},
			{[]sm.Filter{filter(sm.FilterNameStringTypeName, "db/", "web")}, []string{"db/main", "db/replica", "web"}},
			{[]sm.Filter{filter(sm.FilterNameStringTypeName, "db/"), filter(sm.FilterNameStringTypeDescription, "secret 2")}, []string{"db/main"}},
			{[]sm.Filter{filter(sm.FilterNameStringTypeAll, "DB secret")}, []string{"db/main", "db/replica"}},
		} {
			if got := names(c.filters...); !slices.Equal(got, c.want) {
				h.Errorf("filters %+v = %v, want %v", c.filters, got, c.want)
			}
		}
		desc := must(h, sm.OpListSecrets, &sm.ListSecretsInput{SortBy: new(sm.SortByTypeName), SortOrder: new(sm.SortOrderTypeDesc)}).SecretList
		if val(desc[0].Name) != "web" || val(desc[4].Name) != "app/one" {
			h.Errorf("name desc order starts %s and ends %s", val(desc[0].Name), val(desc[4].Name))
		}
		for _, size := range []int32{1, 2, 4} {
			var seen []string
			var token *string
			for {
				out := must(h, sm.OpListSecrets, &sm.ListSecretsInput{MaxResults: new(size), NextToken: token})
				for _, e := range out.SecretList {
					seen = append(seen, val(e.Name))
				}
				if token = out.NextToken; token == nil {
					break
				}
			}
			if !slices.Equal(seen, names()) {
				h.Errorf("pages of %d = %v", size, seen)
			}
		}
	})
}

// A snapshot of the state imported back over later changes serves the
// secret values it held, sealed data keys and all.
func TestSnapshotRoundTrip(t *testing.T) {
	sections := map[string][]byte{}
	run(t, func(h *harness) {
		h.create("snap", "value")
		ss, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		for _, s := range ss {
			sections[s.Path] = s.Data
		}
		h.put("snap", "next")
		got, gerr := h.get("snap", "AWSPREVIOUS")
		if gerr != nil || val(got.SecretString) != "value" {
			h.Fatalf("AWSPREVIOUS = %q, %v", val(got.SecretString), gerr)
		}
		if err := h.svc.Import(ss); err != nil {
			h.Fatal(err)
		}
		got, gerr = h.get("snap", "AWSCURRENT")
		if gerr != nil || val(got.SecretString) != "value" {
			h.Errorf("after import AWSCURRENT = %q, %v", val(got.SecretString), gerr)
		}
		if _, gerr := h.get("snap", "AWSPREVIOUS"); gerr == nil {
			h.Error("AWSPREVIOUS survived an import of a snapshot taken before it existed")
		}
	})
	if len(sections) != 1 {
		t.Errorf("export wrote %d sections, want 1", len(sections))
	}
}

// A snapshot keeps a secret scheduled for deletion with its date, and Rearm
// after a load arms the deletion again: with timers cleared and the state
// imported, the secret still goes when its recovery window ends.
func TestSnapshotRearmsDeletion(t *testing.T) {
	run(t, func(h *harness) {
		arn := val(h.create("doomed", "x").ARN)
		due := must(h, sm.OpDeleteSecret, &sm.DeleteSecretInput{SecretId: arn, RecoveryWindowInDays: new(int64(7))}).DeletionDate
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		if d := h.describe(arn); d.DeletedDate == nil || !d.DeletedDate.Equal(*due) {
			h.Errorf("DeletedDate after import = %v, want %v", d.DeletedDate, *due)
		}
		h.svc.Rearm(h.Clock.Now())
		h.advance(7 * day)
		_, derr := call(h, sm.OpDescribeSecret, &sm.DescribeSecretInput{SecretId: arn})
		wantError(h.T, derr, "ResourceNotFoundException", "Secrets Manager can't find the specified secret.")
	})
}

// bubbleTB hands rapid a synctest bubble's test without its Deadline
// method, which panics inside a bubble; rapid only reads it to stop early.
type bubbleTB struct{ testing.TB }

// stageModel is what the stage rules of secrets-logs-events-alarms.md 2.2
// say the secret holds: the value of every version and which version holds
// AWSCURRENT, AWSPREVIOUS and AWSPENDING.
type stageModel struct {
	values                     map[string]string
	order                      []string
	current, previous, pending string
}

// Any sequence of default puts, AWSPENDING puts, AWSCURRENT moves and
// AWSPREVIOUS removals leaves GetSecretValue answering what the stage rules
// predict for AWSCURRENT, AWSPREVIOUS and AWSPENDING, and DescribeSecret
// showing each label on exactly one version and AWSCURRENT and AWSPREVIOUS
// never on the same one.
func TestStagesFollowTheModel(t *testing.T) {
	run(t, func(h *harness) {
		n := 0
		rapid.Check(bubbleTB{h.T}, func(rt *rapid.T) {
			n++
			name := fmt.Sprintf("model-%d", n)
			must(h, sm.OpCreateSecret, &sm.CreateSecretInput{Name: name, ClientRequestToken: new(h.token())})
			m := stageModel{values: map[string]string{}}
			for step := range rapid.IntRange(1, 12).Draw(rt, "steps") {
				switch op := rapid.IntRange(0, 3).Draw(rt, "op"); {
				case op == 0 || m.current == "" && op != 1:
					v := fmt.Sprintf("v%d", step)
					id := val(h.put(name, v).VersionId)
					m.values[id], m.order = v, append(m.order, id)
					if m.current != "" {
						m.previous = m.current
						if m.pending == m.current {
							m.pending = ""
						}
					}
					m.current = id
				case op == 1:
					v := fmt.Sprintf("p%d", step)
					id := val(h.put(name, v, "AWSPENDING").VersionId)
					m.values[id], m.order = v, append(m.order, id)
					m.pending = id
					// The first version takes AWSCURRENT whatever labels the
					// put names.
					if m.current == "" {
						m.current = id
					}
				case op == 2:
					to := rapid.SampledFrom(m.order).Draw(rt, "move to")
					must(h, sm.OpUpdateSecretVersionStage, &sm.UpdateSecretVersionStageInput{
						SecretId: name, VersionStage: "AWSCURRENT", MoveToVersionId: new(to), RemoveFromVersionId: new(m.current),
					})
					if to != m.current {
						m.previous, m.current = m.current, to
					}
				case m.previous != "":
					must(h, sm.OpUpdateSecretVersionStage, &sm.UpdateSecretVersionStageInput{
						SecretId: name, VersionStage: "AWSPREVIOUS", RemoveFromVersionId: new(m.previous),
					})
					m.previous = ""
				}
				for stage, want := range map[string]string{"AWSCURRENT": m.current, "AWSPREVIOUS": m.previous, "AWSPENDING": m.pending} {
					got, err := h.get(name, stage)
					switch {
					case want == "" && err == nil:
						rt.Fatalf("step %d: %s holds version %s, want none", step, stage, val(got.VersionId))
					case want == "":
					case err != nil:
						rt.Fatalf("step %d: %s = %s %s, want version %s", step, stage, err.ID.JSONType, err.Message, want)
					case val(got.VersionId) != want || val(got.SecretString) != m.values[want] || !slices.Contains(got.VersionStages, stage):
						rt.Fatalf("step %d: %s = version %s %q %v, want %s %q", step, stage, val(got.VersionId), val(got.SecretString), got.VersionStages, want, m.values[want])
					}
				}
				holders := map[string][]string{}
				for id, stages := range h.describe(name).VersionIdsToStages {
					if slices.Contains(stages, "AWSCURRENT") && slices.Contains(stages, "AWSPREVIOUS") {
						rt.Fatalf("step %d: version %s holds both AWSCURRENT and AWSPREVIOUS: %v", step, id, stages)
					}
					for _, stage := range stages {
						holders[stage] = append(holders[stage], id)
					}
				}
				for stage, ids := range holders {
					if len(ids) != 1 {
						rt.Fatalf("step %d: %s is on versions %v, want exactly one", step, stage, ids)
					}
				}
			}
		})
	})
}
