package s3

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
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

// customerKeyed reports whether a read or a copy source sent SSE-C
// headers: any of the algorithm, the key or its MD5.
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

// customerHeaders is one SSE-C header trio as a request sent it: the
// target's x-amz-server-side-encryption-customer-* headers, or a copy
// source's x-amz-copy-source-server-side-encryption-customer-* headers
// (s3-ssec-object-lock.md 3.1).
type customerHeaders struct {
	algorithm, key, keyMD5 *string
}

// sent reports whether any of the three was sent. Not implemented yet: it
// reports false.
func (customerHeaders) sent() bool { return false }

// customerKey is an SSE-C key that passed parsing: its decoded bytes,
// which are never stored (s3-ssec-object-lock.md 3.11), and the key-MD5
// header as sent, which responses echo (11.2).
type customerKey struct {
	key    []byte
	keyMD5 string
}

// parseCustomerWrite checks a write's SSE-C headers in the order of
// s3-ssec-object-lock.md 3.2 and 11.3: the conflict with sse, the
// algorithm, the three headers' presence, the key's length, its MD5. It
// returns nil when none was sent. Not implemented yet: it answers
// errNotImplementedYet.
func parseCustomerWrite(customerHeaders, *s3api.ServerSideEncryption) (*customerKey, *awserr.Error) {
	return nil, errNotImplementedYet("parseCustomerWrite")
}

// parseCustomerRead checks a read's or a copy source's SSE-C headers as
// parseCustomerWrite does, without the conflict and the length checks: a
// key of another length with a matching MD5 then fails the fingerprint
// with 403, as S3 answers it (s3-ssec-object-lock.md 3.3). Not implemented
// yet: it answers errNotImplementedYet.
func parseCustomerRead(customerHeaders) (*customerKey, *awserr.Error) {
	return nil, errNotImplementedYet("parseCustomerRead")
}

// CustomerEncryption is an SSE-C object version's or upload's key
// fingerprint: a random salt and HMAC-SHA256(salt, key), never the key
// (s3-ssec-object-lock.md 3.11, 11.2). Nothing makes one yet.
type CustomerEncryption struct {
	Salt []byte `json:"salt"`
	MAC  []byte `json:"mac"`
}

// newCustomerEncryption fingerprints the key under salt. Not implemented
// yet: it returns the zero CustomerEncryption.
func newCustomerEncryption([]byte, customerKey) CustomerEncryption { return CustomerEncryption{} }

// matches reports whether the key is the one e was made with, comparing
// in constant time. Not implemented yet: it reports false.
func (CustomerEncryption) matches(customerKey) bool { return false }

// checkCustomerWrite refuses an SSE-C write whose headers parsed: 403 on a
// bucket that blocks SSE-C (switch s3.sse-c.blocked-write), then 400 over
// plain HTTP (switch s3.sse-c.plain-http); scheme is the call's. Not
// implemented yet: it answers errNotImplementedYet.
func checkCustomerWrite(*Bucket, string) *awserr.Error {
	return errNotImplementedYet("checkCustomerWrite")
}

// checkCustomerRead judges a read's or copy source's SSE-C headers against
// the version's stored encryption (s3-ssec-object-lock.md 3.3, 3.5): the
// stored-form 400 without headers on an SSE-C version, the header checks,
// plain HTTP, the not-applicable 400 on another version, the wrong-key
// 403. It returns the key whose MD5 the response echoes, nil without
// headers. Not implemented yet: it answers errNotImplementedYet.
func checkCustomerRead(Encryption, customerHeaders, string) (*customerKey, *awserr.Error) {
	return nil, errNotImplementedYet("checkCustomerRead")
}

// checkCustomerPart judges an UploadPart's or UploadPartCopy target's
// SSE-C headers against its upload's encryption (s3-ssec-object-lock.md
// 3.6): the header checks, the bucket block, plain HTTP, then the match
// with the upload. Not implemented yet: it answers errNotImplementedYet.
func checkCustomerPart(Encryption, customerHeaders, *Bucket, string) (*customerKey, *awserr.Error) {
	return nil, errNotImplementedYet("checkCustomerPart")
}

// BlockedEncryption is a bucket's BlockedEncryptionTypes
// (s3-ssec-object-lock.md 4.2).
type BlockedEncryption uint8

// BlockedEncryption values.
const (
	// BlockSSEC refuses SSE-C writes: every new bucket since April 2026
	// outside me-south-1 and me-central-1 (s3-ssec-object-lock.md 4.1).
	BlockSSEC BlockedEncryption = iota
	// BlockNone accepts SSE-C writes.
	BlockNone
)

// MarshalText writes SSE-C or NONE, the EncryptionType values. Not
// implemented yet: it fails.
func (BlockedEncryption) MarshalText() ([]byte, error) {
	return nil, &NotImplementedYetError{Func: "BlockedEncryption.MarshalText"}
}

// UnmarshalText reads SSE-C or NONE and refuses any other text. Not
// implemented yet: it fails.
func (*BlockedEncryption) UnmarshalText([]byte) error {
	return &NotImplementedYetError{Func: "BlockedEncryption.UnmarshalText"}
}

// newBucketBlock is a new bucket's BlockedEncryption in region: BlockNone
// in me-south-1 and me-central-1, BlockSSEC elsewhere
// (s3-ssec-object-lock.md 4.1). Not implemented yet: it returns BlockSSEC.
func newBucketBlock(aws.Region) BlockedEncryption { return BlockSSEC }

// blockedEncryptionOf reads a PutBucketEncryption rule's
// BlockedEncryptionTypes; false when the rule has none, so the bucket
// keeps its setting (switch s3.encryption.blocked-types-omitted). Not
// implemented yet: it answers errNotImplementedYet.
func blockedEncryptionOf(s3api.ServerSideEncryptionRule) (BlockedEncryption, bool, *awserr.Error) {
	return BlockSSEC, false, errNotImplementedYet("blockedEncryptionOf")
}

// encryptionRead is GetBucketEncryption's document: the stored rule or the
// SSE-S3 default, with the AES256 default, BucketKeyEnabled false and
// BlockedEncryptionTypes filled in (s3-ssec-object-lock.md 2, 4.2). Not
// implemented yet: it returns nil.
func encryptionRead(*Bucket) *s3api.ServerSideEncryptionConfiguration { return nil }
