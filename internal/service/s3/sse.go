package s3

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// defaultKMSKey is the key of an aws:kms write that names none: the AWS
// managed key, which KMS creates on first use (s3.md 5.7).
const defaultKMSKey = "alias/aws/s3"

// arnContextKey is the encryption context S3 adds for an object (s3.md
// 5.7, DOC UsingKMSEncryption).
const arnContextKey = "aws:s3:arn"

// Encryption is how an object version is encrypted at rest. Every object
// reports at least SSE-S3 (AES256), so there is no "none": KMS is nil for
// SSE-S3 (s3.md Implications 3).
type Encryption struct {
	KMS *KMSEncryption `json:"kms,omitempty"`
}

// KMSEncryption is an SSE-KMS object's key: S3 asks KMS for a data key on
// the write and to decrypt it on every read, so a read depends on the
// key's state then (s3.md 5.7). The bytes themselves are stored as sent.
type KMSEncryption struct {
	// Algorithm is aws:kms or aws:kms:dsse.
	Algorithm s3api.ServerSideEncryption `json:"algorithm"`
	// KeyARN is the full key ARN KMS resolved, which responses report
	// whatever form the request named.
	KeyARN    string            `json:"keyArn"`
	BucketKey bool              `json:"bucketKey,omitempty"`
	Context   map[string]string `json:"context"`
	// DataKey is the data key as KMS encrypted it.
	DataKey []byte `json:"dataKey"`
}

// sseRequest is the server-side encryption a write's headers ask for.
type sseRequest struct {
	algorithm *s3api.ServerSideEncryption
	keyID     *string
	context   *string
	bucketKey *bool
	// customer is set when the request sends SSE-C headers.
	customer bool
}

// sseChoice is the encryption a write resolves to before its body is
// read: SSE-S3, or SSE-KMS under a key KMS has yet to resolve.
//
//sumtype:decl
type sseChoice interface{ isSSEChoice() }

type sseS3 struct{}

type sseKMS struct {
	algorithm s3api.ServerSideEncryption
	keyID     string
	bucketKey bool
	context   map[string]string
}

func (sseS3) isSSEChoice()  {}
func (sseKMS) isSSEChoice() {}

// resolveSSE applies s3.md 5.7: the request's headers win; without them
// the bucket's default rule decides. objectARN is the object's ARN, the
// encryption context S3 adds.
func resolveSSE(r sseRequest, b *Bucket, objectARN string) (sseChoice, *awserr.Error) {
	if r.customer {
		return nil, errSSECBlocked()
	}
	alg, keyID, bucketKey := bucketDefault(b)
	if r.algorithm != nil {
		alg, keyID = *r.algorithm, r.keyID
	}
	// A key id without the header asking for aws:kms is refused whatever
	// the bucket's default: on an aws:kms default the object would
	// otherwise be encrypted under the bucket's key, not the one named.
	// That AWS refuses it there too is UNVERIFIED.
	if r.keyID != nil && (r.algorithm == nil || alg == s3api.ServerSideEncryptionAes256) {
		return nil, errKMSKeyWithoutKMS(alg)
	}
	if r.bucketKey != nil {
		bucketKey = *r.bucketKey
	}
	switch alg {
	case s3api.ServerSideEncryptionAes256:
		return sseS3{}, nil
	case s3api.ServerSideEncryptionAwsKms, s3api.ServerSideEncryptionAwsKmsDsse:
		ctx, err := encryptionContext(r.context, objectARN)
		if err != nil {
			return nil, err
		}
		key := defaultKMSKey
		if keyID != nil && *keyID != "" {
			key = *keyID
		}
		return sseKMS{algorithm: alg, keyID: key, bucketKey: bucketKey, context: ctx}, nil
	case s3api.ServerSideEncryptionAwsFsx, s3api.ServerSideEncryptionAwsBackup:
	}
	return nil, errInvalidArgument("The encryption method specified is not supported", "x-amz-server-side-encryption", string(alg))
}

// errKMSKeyWithoutKMS answers x-amz-server-side-encryption-aws-kms-key-id
// on a write that does not ask for aws:kms; alg is the algorithm the write
// would otherwise use.
func errKMSKeyWithoutKMS(alg s3api.ServerSideEncryption) *awserr.Error {
	return errInvalidArgument("Server Side Encryption with AWS KMS managed key requires HTTP header x-amz-server-side-encryption : aws:kms",
		"x-amz-server-side-encryption", string(alg))
}

// bucketDefault is the bucket's default encryption rule: SSE-S3 without a
// bucket key when none is configured (s3.md 4.1).
func bucketDefault(b *Bucket) (s3api.ServerSideEncryption, *string, bool) {
	if b.Encryption == nil || len(b.Encryption.Rules) == 0 {
		return s3api.ServerSideEncryptionAes256, nil, false
	}
	rule := b.Encryption.Rules[0]
	bucketKey := rule.BucketKeyEnabled != nil && *rule.BucketKeyEnabled
	if d := rule.ApplyServerSideEncryptionByDefault; d != nil {
		return d.SSEAlgorithm, d.KMSMasterKeyID, bucketKey
	}
	return s3api.ServerSideEncryptionAes256, nil, bucketKey
}

// encryptionContext reads x-amz-server-side-encryption-context, base64 of
// a JSON object of strings, and adds the object's ARN as S3 does.
func encryptionContext(header *string, objectARN string) (map[string]string, *awserr.Error) {
	ctx := map[string]string{}
	if header != nil {
		raw, err := base64.StdEncoding.DecodeString(*header)
		if err != nil || json.Unmarshal(raw, &ctx) != nil {
			return nil, errInvalidArgument("The header 'x-amz-server-side-encryption-context' shall be Base64-encoded UTF-8 string holding JSON which represents a string-string map",
				"x-amz-server-side-encryption-context", *header)
		}
	}
	if _, ok := ctx[arnContextKey]; !ok {
		ctx[arnContextKey] = objectARN
	}
	return ctx, nil
}

// encrypt asks KMS for the data key of a write under choice, before the
// write takes its bucket lock and before the body is read, so a key that
// is missing, disabled or pending deletion refuses the write early. The
// call is forward access charged to the caller (ARCHITECTURE.md 16).
func (s *Service) encrypt(ctx context.Context, c *kernel.Call, home Home, choice sseChoice) (Encryption, *awserr.Error) {
	k, ok := choice.(sseKMS)
	if !ok {
		return Encryption{}, nil
	}
	out, err := kernel.Invoke(ctx, s.k, c, kmsapi.OpGenerateDataKey, kmsTarget(home), &kmsapi.GenerateDataKeyInput{
		KeyId:             k.keyID,
		EncryptionContext: k.context,
		KeySpec:           new(kmsapi.DataKeySpecAes256),
	})
	if err != nil {
		return Encryption{}, kmsError(err)
	}
	return Encryption{KMS: &KMSEncryption{
		Algorithm: k.algorithm,
		KeyARN:    deref(out.KeyId),
		BucketKey: k.bucketKey,
		Context:   k.context,
		DataKey:   out.CiphertextBlob,
	}}, nil
}

// decrypt asks KMS to decrypt an SSE-KMS object's data key, as S3 does on
// every read, so the read fails when the key is now disabled or pending
// deletion (s3.md 5.7).
func (s *Service) decrypt(ctx context.Context, c *kernel.Call, home Home, e Encryption) *awserr.Error {
	if e.KMS == nil {
		return nil
	}
	_, err := kernel.Invoke(ctx, s.k, c, kmsapi.OpDecrypt, kmsTarget(home), &kmsapi.DecryptInput{
		CiphertextBlob:    e.KMS.DataKey,
		EncryptionContext: e.KMS.Context,
		KeyId:             new(e.KMS.KeyARN),
	})
	if err != nil {
		return kmsError(err)
	}
	return nil
}

// kmsTarget is a forward-access call to KMS in the bucket's home scope,
// charged to the caller.
func kmsTarget(home Home) kernel.Target {
	return kernel.Target{Scope: home.scope(), As: kernel.AsCaller{}, Via: ref.KMSForward}
}

// kmsError is S3's answer for a KMS refusal: the KMS code under S3's
// "KMS." prefix with KMS's message (s3.md 5.7), except that S3 words a key
// pending deletion as "<key ARN> is pending deletion." without KMS's
// replica clause (recorded through S3). An internal failure and any other
// refusal pass through as they are.
func kmsError(err *awserr.Error) *awserr.Error {
	switch err.ID.Shape {
	case kmsapi.IdentityNotFoundException().Shape:
		return s3api.ErrKMSNotFoundException(err.Message)
	case kmsapi.IdentityDisabledException().Shape:
		return s3api.ErrKMSDisabledException(err.Message)
	case kmsapi.IdentityKMSInvalidStateException().Shape:
		if arn, ok := strings.CutSuffix(err.Message, kmsPendingDeletion); ok {
			return s3api.ErrKMSInvalidStateException(arn + " is pending deletion.")
		}
		return s3api.ErrKMSInvalidStateException(err.Message)
	}
	return err
}

// kmsPendingDeletion ends KMS's text for a key pending deletion.
const kmsPendingDeletion = " is pending deletion (or pending replica deletion)."

// sseHeaders are the encryption members every object response reports.
type sseHeaders struct {
	algorithm *s3api.ServerSideEncryption
	keyID     *string
	bucketKey *bool
}

// headers reports e: AES256 for SSE-S3; for SSE-KMS the algorithm, the
// full key ARN and, only when true, the bucket key flag (s3.md 5.7).
func (e Encryption) headers() sseHeaders {
	if e.KMS == nil {
		return sseHeaders{algorithm: new(s3api.ServerSideEncryptionAes256)}
	}
	h := sseHeaders{algorithm: new(e.KMS.Algorithm), keyID: new(e.KMS.KeyARN)}
	if e.KMS.BucketKey {
		h.bucketKey = new(true)
	}
	return h
}

// customerKeyed reports SSE-C headers on a read or a copy source: any of
// the algorithm, the key or its MD5.
func customerKeyed(algorithm, key, keyMD5 *string) bool {
	return algorithm != nil || key != nil || keyMD5 != nil
}

// errSSECNotApplicable answers SSE-C headers on a read of an object that
// is not SSE-C encrypted, which every object is, since SSE-C writes are
// refused (s3.md 5.7). The text is UNVERIFIED.
func errSSECNotApplicable() *awserr.Error {
	return errInvalidRequest("The encryption parameters are not applicable to this object.")
}

// errSSECBlocked answers an SSE-C write: new general purpose buckets block
// SSE-C since April 2026 (s3.md 5.7). The text is UNVERIFIED.
func errSSECBlocked() *awserr.Error {
	return s3api.ErrAccessDenied("Requests specifying Server Side Encryption with Customer provided keys are blocked for this bucket")
}
