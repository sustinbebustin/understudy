package s3

import (
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The SSE condition keys S3 sends with an object write
// (iam-enforcement-surfaces.md 9.2).
const (
	sseKey          = "s3:x-amz-server-side-encryption"
	sseKMSKeyIDKey  = "s3:x-amz-server-side-encryption-aws-kms-key-id"
	sseCustomerAlgo = "s3:x-amz-server-side-encryption-customer-algorithm"
)

// sseWrite is what an object write's headers say about its encryption,
// which its SSE condition keys are read from.
type sseWrite struct {
	bucket    string
	algorithm *s3api.ServerSideEncryption
	kmsKeyID  *string
	customer  *string
}

// sendsNoEncryption reports whether the write sent none of the encryption
// headers, so its bucket's default encrypts it (resolveSSE).
func (w sseWrite) sendsNoEncryption() bool {
	return w.algorithm == nil && w.kmsKeyID == nil && w.customer == nil
}

// declareSSEConditionKeys gives PutObject, CopyObject and
// CreateMultipartUpload, each authorized as s3:PutObject, the SSE condition
// keys of sseConditionKeys. IAM-ENF-30 recorded PutObject; the other two
// take the same headers, and that S3 supplies the keys for them alike is
// inferred (switch s3.condition.sse-from-default). It runs after the bind
// sites, whose bindings it extends.
//
// The keys describe the write and are documented for s3:PutObject alone
// (iam-enforcement-surfaces.md 9.2), so only the s3:PutObject question
// carries them: a CopyObject's read of its source, s3:GetObject or
// s3:GetObjectVersion, finds them unsupplied, as every s3: key S3 does not
// compute.
func (s *Service) declareSSEConditionKeys(b *kernel.Binder) {
	kernel.Declare(b, s3api.OpPutObject, sseKeysOf(s, func(in *s3api.PutObjectInput) sseWrite {
		return sseWrite{bucket: in.Bucket, algorithm: in.ServerSideEncryption, kmsKeyID: in.SSEKMSKeyId, customer: in.SSECustomerAlgorithm}
	}))
	kernel.Declare(b, s3api.OpCopyObject, sseKeysOf(s, func(in *s3api.CopyObjectInput) sseWrite {
		return sseWrite{bucket: in.Bucket, algorithm: in.ServerSideEncryption, kmsKeyID: in.SSEKMSKeyId, customer: in.SSECustomerAlgorithm}
	}))
	kernel.Declare(b, s3api.OpCreateMultipartUpload, sseKeysOf(s, func(in *s3api.CreateMultipartUploadInput) sseWrite {
		return sseWrite{bucket: in.Bucket, algorithm: in.ServerSideEncryption, kmsKeyID: in.SSEKMSKeyId, customer: in.SSECustomerAlgorithm}
	}))
}

// sseKeysOf supplies, for the s3:PutObject question alone, the SSE
// condition keys of the write an input of type In describes. The three
// inputs share the header members but no type, so each reads its own into
// an sseWrite.
func sseKeysOf[In any](s *Service, write func(*In) sseWrite) kernel.BindOption {
	return kernel.WithConditionKeysFor([]registry.IAMAction{s3Action("PutObject")}, func(_ *kernel.Call, in *In) kernel.ConditionKeys {
		return s.sseConditionKeys(write(in))
	})
}

// sseConditionKeys are the SSE condition keys of the write w, each from
// its header, and a key with no values for a header the write did not
// send, so a policy testing it reads it as absent rather than unsupplied.
// A write that names no encryption is encrypted by its bucket's default
// (resolveSSE), and an SSE-KMS default supplies the key-id key with the
// key the default names, while the algorithm key stays absent (IAM-ENF-30,
// recorded 2026-11-03; iam-enforcement-surfaces.md 9.2). An SSE-KMS
// default that names no key encrypts under the AWS managed key, whose
// supplying the key-id key is unrecorded, so it stays absent there;
// UNVERIFIED.
func (s *Service) sseConditionKeys(w sseWrite) kernel.ConditionKeys {
	keys := kernel.ConditionKeys{sseKey: nil, sseKMSKeyIDKey: nil, sseCustomerAlgo: nil}
	if w.algorithm != nil {
		keys[sseKey] = []string{string(*w.algorithm)}
	}
	if w.kmsKeyID != nil {
		keys[sseKMSKeyIDKey] = []string{*w.kmsKeyID}
	}
	if w.customer != nil {
		keys[sseCustomerAlgo] = []string{*w.customer}
	}
	if w.sendsNoEncryption() {
		if key, ok := s.defaultKMSKeyID(w.bucket); ok {
			keys[sseKMSKeyIDKey] = []string{key}
		}
	}
	return keys
}

// defaultKMSKeyID is the KMS key bucket's SSE-KMS default names; false for
// a bucket without one, or that does not exist, whose call the handler
// answers.
func (s *Service) defaultKMSKeyID(bucket string) (string, bool) {
	key, err := readBucket(s, bucket, func(b *Bucket) (string, *awserr.Error) {
		alg, keyID, _ := bucketDefault(b)
		if (alg != s3api.ServerSideEncryptionAwsKms && alg != s3api.ServerSideEncryptionAwsKmsDsse) || keyID == nil {
			return "", nil
		}
		return *keyID, nil
	})
	return key, err == nil && key != ""
}

// sseFromDefaultSwitches are switch s3.condition.sse-from-default: a write
// without encryption headers into a bucket whose default is SSE-KMS takes
// the s3:x-amz-server-side-encryption-aws-kms-key-id condition key from
// the default and leaves s3:x-amz-server-side-encryption absent. IAM-ENF-30
// recorded it for PutObject; CopyObject and CreateMultipartUpload follow it
// by inference, so the switch is listed once per label.
func sseFromDefaultSwitches() []fidelity.Entry {
	entry := func(label evidence.Label, ops []string) fidelity.Entry {
		return fidelity.Switch[string]{
			Name:    "s3.condition.sse-from-default",
			Value:   "key-id-only",
			Label:   label,
			Ref:     "docs/research/iam-enforcement-surfaces.md#9.2",
			Record:  "IAM-ENF-30",
			Ops:     ops,
			Settles: fidelity.NoScenario{Reason: s3Unreplayed("s3/IAM-ENF/30-no-header.json, 30-no-header-sse.json and 30-with-header.json are")},
		}.Entry()
	}
	return []fidelity.Entry{
		entry(evidence.AWSRecorded, []string{"s3.PutObject"}),
		entry(evidence.Inferred, []string{"s3.CopyObject", "s3.CreateMultipartUpload"}),
	}
}
