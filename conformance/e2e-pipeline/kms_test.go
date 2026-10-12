package e2e_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// TestKMSAliasResolvesToThePipelineKey describes the key through its alias
// and round-trips data through Encrypt and Decrypt (iam-sts-kms.md 5).
func TestKMSAliasResolvesToThePipelineKey(t *testing.T) {
	p := pipe(t)
	d, err := env.kms.DescribeKey(t.Context(), &kms.DescribeKeyInput{KeyId: aws.String(keyAlias)})
	if err != nil {
		t.Fatalf("kms DescribeKey %s: %v", keyAlias, err)
	}
	if aws.ToString(d.KeyMetadata.Arn) != p.keyARN || !d.KeyMetadata.Enabled {
		t.Errorf("DescribeKey %s: Arn %s, Enabled %v; want %s, true", keyAlias, aws.ToString(d.KeyMetadata.Arn), d.KeyMetadata.Enabled, p.keyARN)
	}
	plain := []byte("pipeline secret material")
	enc, err := env.kms.Encrypt(t.Context(), &kms.EncryptInput{KeyId: aws.String(keyAlias), Plaintext: plain})
	if err != nil {
		t.Fatalf("kms Encrypt: %v", err)
	}
	if aws.ToString(enc.KeyId) != p.keyARN || bytes.Contains(enc.CiphertextBlob, plain) {
		t.Errorf("Encrypt KeyId %s, want the key ARN %s, and a ciphertext that does not hold the plaintext", aws.ToString(enc.KeyId), p.keyARN)
	}
	dec, err := env.kms.Decrypt(t.Context(), &kms.DecryptInput{CiphertextBlob: enc.CiphertextBlob})
	if err != nil {
		t.Fatalf("kms Decrypt: %v", err)
	}
	if !bytes.Equal(dec.Plaintext, plain) || aws.ToString(dec.KeyId) != p.keyARN {
		t.Errorf("Decrypt gave %q under %s, want %q under %s", dec.Plaintext, aws.ToString(dec.KeyId), plain, p.keyARN)
	}
}

// TestKMSDisabledKeyBlocksSSEKMSReads disables the bucket key: reading an
// object it encrypts fails with 400 KMS.DisabledException, and works again
// once the key is enabled (s3.md 5.7).
func TestKMSDisabledKeyBlocksSSEKMSReads(t *testing.T) {
	p := pipe(t)
	id := newID(t)
	publish(t, message{ID: id}, map[string]string{"kind": "order"})
	waitProcessed(t, goWorker, id)

	if _, err := env.kms.DisableKey(t.Context(), &kms.DisableKeyInput{KeyId: aws.String(p.keyARN)}); err != nil {
		t.Fatalf("kms DisableKey: %v", err)
	}
	t.Cleanup(func() {
		if _, err := env.kms.EnableKey(context.WithoutCancel(t.Context()), &kms.EnableKeyInput{KeyId: aws.String(p.keyARN)}); err != nil {
			t.Errorf("kms EnableKey (cleanup): %v", err)
		}
	})
	_, err := env.s3.GetObject(t.Context(), &s3.GetObjectInput{Bucket: aws.String(bucketName), Key: aws.String(processedKey(goWorker, id))})
	wantError(t, "GetObject under a disabled key", err, "KMS.DisabledException", 400)
	if err != nil {
		if _, msg, _ := awsError(err); msg != p.keyARN+" is disabled." {
			t.Errorf("GetObject under a disabled key: message %q, want %q", msg, p.keyARN+" is disabled.")
		}
	}

	if _, err := env.kms.EnableKey(t.Context(), &kms.EnableKeyInput{KeyId: aws.String(p.keyARN)}); err != nil {
		t.Fatalf("kms EnableKey: %v", err)
	}
	if _, ok := getObject(t, processedKey(goWorker, id)); !ok {
		t.Errorf("the object is missing after the key was enabled again")
	}
}
