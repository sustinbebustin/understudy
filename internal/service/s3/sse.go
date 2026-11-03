package s3

import (
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // SSE-C names the customer key by its MD5 (s3-ssec-object-lock.md 3.1); an integrity check, not a security control
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// defaultKMSKey is the key of an aws:kms write that names none: the AWS
// managed key, which KMS creates on first use (s3.md 5.7).
const defaultKMSKey = "alias/aws/s3"

// arnContextKey is the encryption context S3 adds for an object (s3.md
// 5.7, DOC UsingKMSEncryption).
const arnContextKey = "aws:s3:arn"

// Encryption is how an object version or an upload is encrypted at rest:
// SSE-S3, SSE-KMS or SSE-C (s3-ssec-object-lock.md 11.2). Every object is
// encrypted, so there is no "none" (s3.md Implications 3), and it is never
// nil in state. Snapshots hold it as a tagged union (unionCodec).
//
//sumtype:decl
type Encryption interface{ isEncryption() }

// SSES3Encryption is an object encrypted under S3's own keys, the default
// every bucket has (s3.md 4.1).
type SSES3Encryption struct{}

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

// CustomerEncryption is an SSE-C object version's or upload's key
// fingerprint: a random salt and HMAC-SHA256(salt, key), never the key,
// its base64 or its MD5 (s3-ssec-object-lock.md 3.11, 11.2). The bytes are
// stored as sent, as SSE-KMS stores them; the key check is the observable
// contract.
type CustomerEncryption struct {
	// Salt is 16 random bytes, drawn once per version or upload.
	Salt []byte `json:"salt"`
	// MAC is HMAC-SHA256(Salt, key), 32 bytes.
	MAC []byte `json:"mac"`
}

func (SSES3Encryption) isEncryption()    {}
func (KMSEncryption) isEncryption()      {}
func (CustomerEncryption) isEncryption() {}

// isKMS reports whether e is SSE-KMS, which a read decrypts through KMS
// and whose responses name the key (s3.md 5.7).
func isKMS(e Encryption) bool {
	_, ok := e.(KMSEncryption)
	return ok
}

// isCustomer reports whether e is SSE-C, which a read needs the key for
// (s3-ssec-object-lock.md 3.3).
func isCustomer(e Encryption) bool {
	_, ok := e.(CustomerEncryption)
	return ok
}

// opaqueETag reports whether e gives ETags that are not the MD5 of the
// bytes, random 128-bit values instead, and results that report no
// checksum: SSE-KMS (s3.md 5.3, 5.7) and SSE-C (s3-ssec-object-lock.md
// 3.4, 11.3).
func opaqueETag(e Encryption) bool { return isKMS(e) || isCustomer(e) }

// sseRequest is the server-side encryption a write's headers ask for.
type sseRequest struct {
	algorithm *s3api.ServerSideEncryption
	keyID     *string
	context   *string
	bucketKey *bool
	// customer is the SSE-C trio the write sent for its target.
	customer customerHeaders
}

// sseChoice is the encryption a write resolves to before its body is
// read: SSE-S3, SSE-KMS under a key KMS has yet to resolve, or SSE-C under
// a customer key that passed its checks.
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

type sseC struct{ key customerKey }

func (sseS3) isSSEChoice()  {}
func (sseKMS) isSSEChoice() {}
func (sseC) isSSEChoice()   {}

// customerOf is the customer key of an SSE-C choice, nil for any other.
func customerOf(choice sseChoice) *customerKey {
	if c, ok := choice.(sseC); ok {
		return &c.key
	}
	return nil
}

// writeCall is the call a write arrives on: its caller and the IAM action
// that a bucket blocking SSE-C names in its refusal
// (s3-ssec-object-lock.md 4.3), and its scheme, which SSE-C needs to be
// https.
type writeCall struct {
	principal identity.Principal
	action    string
	scheme    string
}

// writeCallOf is c as a write the block's refusal names action for.
func writeCallOf(c *kernel.Call, action string) writeCall {
	return writeCall{principal: c.Principal(), action: action, scheme: c.Endpoint().Scheme}
}

// resolveSSE applies s3.md 5.7: the request's headers win; without them
// the bucket's default rule decides. key is the object's key, whose ARN is
// the encryption context S3 adds. SSE-C headers are checked in the order
// of s3-ssec-object-lock.md 11.3: the trio, then the bucket's block, then
// the call's scheme.
func resolveSSE(r sseRequest, b *Bucket, key string, w writeCall) (sseChoice, *awserr.Error) {
	k, err := parseCustomerWrite(r.customer, r.algorithm)
	if err != nil {
		return nil, err
	}
	if k != nil {
		// A KMS key id beside SSE-C headers is refused as one without
		// aws:kms is; AWS's answer is UNVERIFIED.
		if r.keyID != nil {
			alg, _, _ := bucketDefault(b)
			return nil, errKMSKeyWithoutKMS(alg)
		}
		if err := checkCustomerWrite(b, key, w); err != nil {
			return nil, err
		}
		return sseC{key: *k}, nil
	}
	objectARN := objectResource(b.Name, key).String()
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

// hasOwnDefault reports whether the bucket's rule names a default of its
// own. A rule put with BlockedEncryptionTypes alone keeps the implicit
// SSE-S3 default (switch s3.encryption.unblocked-read), so it names none.
func hasOwnDefault(b *Bucket) bool {
	return b.Encryption != nil && len(b.Encryption.Rules) > 0 && b.Encryption.Rules[0].ApplyServerSideEncryptionByDefault != nil
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

// encrypt is the encryption a write under choice stores. For SSE-KMS it
// asks KMS for the data key, before the write takes its bucket lock and
// before the body is read, so a key that is missing, disabled or pending
// deletion refuses the write early; the call is forward access charged to
// the caller (ARCHITECTURE.md 16). For SSE-C it fingerprints the customer
// key under a fresh salt (s3-ssec-object-lock.md 3.11).
func (s *Service) encrypt(ctx context.Context, c *kernel.Call, home Home, choice sseChoice) (Encryption, *awserr.Error) {
	var k sseKMS
	switch ch := choice.(type) {
	case sseS3:
		return SSES3Encryption{}, nil
	case sseC:
		return newCustomerEncryption(s.k.IDs().SSECSalt(), ch.key), nil
	case sseKMS:
		k = ch
	}
	out, err := kernel.Invoke(ctx, s.k, c, kmsapi.OpGenerateDataKey, kmsTarget(home), &kmsapi.GenerateDataKeyInput{
		KeyId:             k.keyID,
		EncryptionContext: k.context,
		KeySpec:           new(kmsapi.DataKeySpecAes256),
	})
	if err != nil {
		return nil, kmsError(err)
	}
	return KMSEncryption{
		Algorithm: k.algorithm,
		KeyARN:    deref(out.KeyId),
		BucketKey: k.bucketKey,
		Context:   k.context,
		DataKey:   out.CiphertextBlob,
	}, nil
}

// decrypt asks KMS to decrypt an SSE-KMS object's data key, as S3 does on
// every read, so the read fails when the key is now disabled or pending
// deletion (s3.md 5.7).
func (s *Service) decrypt(ctx context.Context, c *kernel.Call, home Home, e Encryption) *awserr.Error {
	k, ok := e.(KMSEncryption)
	if !ok {
		return nil
	}
	_, err := kernel.Invoke(ctx, s.k, c, kmsapi.OpDecrypt, kmsTarget(home), &kmsapi.DecryptInput{
		CiphertextBlob:    k.DataKey,
		EncryptionContext: k.Context,
		KeyId:             new(k.KeyARN),
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

// encryptionHeaders reports e: AES256 for SSE-S3; for SSE-KMS the
// algorithm, the full key ARN and, only when true, the bucket key flag
// (s3.md 5.7); none of the three for SSE-C, whose responses carry the
// customer-key pair from the request instead (s3-ssec-object-lock.md 3.4).
func encryptionHeaders(e Encryption) sseHeaders {
	switch e := e.(type) {
	case SSES3Encryption:
		return sseHeaders{algorithm: new(s3api.ServerSideEncryptionAes256)}
	case KMSEncryption:
		h := sseHeaders{algorithm: new(e.Algorithm), keyID: new(e.KeyARN)}
		if e.BucketKey {
			h.bucketKey = new(true)
		}
		return h
	case CustomerEncryption:
	}
	return sseHeaders{}
}

// customerAlgorithm is the one algorithm SSE-C takes, which SSE-C
// responses report (s3-ssec-object-lock.md 3.1, 3.4).
const customerAlgorithm = "AES256"

// customerKeyLen is an SSE-C key's length in bytes once base64-decoded
// (s3-ssec-object-lock.md 11.5).
const customerKeyLen = 32

// sseArgumentName is the ArgumentName of every SSE-C header refusal, even
// one about a copy source's headers (s3-ssec-object-lock.md 3.2).
const sseArgumentName = "x-amz-server-side-encryption"

// The SSE-C refusal texts of s3-ssec-object-lock.md 3.2, 3.3 and 3.6, as
// LocalStack's AWS-validated snapshots record them (localstack@8b9a79f
// tests/aws/services/s3/test_s3.snapshot.json, validated 2026-02-21), and
// the texts of the switches s3.sse-c.missing-key-md5 and
// s3.sse-c.plain-http, which no AWS recording shows (11.4).
const (
	ssecConflictText    = "Server Side Encryption with Customer provided key is incompatible with the encryption method specified"
	ssecAlgorithmText   = "The Encryption request you specified is not valid. Supported value: AES256."
	ssecNoAlgorithmText = "Requests specifying Server Side Encryption with Customer provided keys must provide a valid encryption algorithm."
	ssecNoKeyText       = "Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key."
	ssecNoKeyMD5Text    = "Requests specifying Server Side Encryption with Customer provided keys must provide the client calculated MD5 of the secret key."
	ssecBadKeyText      = "The secret key was invalid for the specified algorithm."
	ssecBadKeyMD5Text   = "The calculated MD5 hash of the key did not match the hash that was provided."
	ssecNeedsKeyText    = "The object was stored using a form of Server Side Encryption. The correct parameters must be provided to retrieve the object."
	ssecWrongKeyText    = "Requests specifying Server Side Encryption with Customer provided keys must provide the correct secret key."
	ssecInsecureText    = "Requests specifying Server Side Encryption with Customer provided keys must be made over a secure connection."
	ssecInitiateText    = "The multipart upload initiate requested encryption. Subsequent part requests must include the appropriate encryption parameters."
	ssecMismatchText    = "The provided encryption parameters did not match the ones used originally."
)

// errSSECConflict answers SSE-C headers beside x-amz-server-side-encryption
// sse (LocalStack test_s3.snapshot.json L12706).
func errSSECConflict(sse s3api.ServerSideEncryption) *awserr.Error {
	return errInvalidArgument(ssecConflictText, sseArgumentName, string(sse))
}

// errSSECAlgorithm answers an SSE-C algorithm other than AES256, echoing
// it (LocalStack test_s3.snapshot.json L12718).
func errSSECAlgorithm(algorithm string) *awserr.Error {
	e := withField(s3api.ErrInvalidEncryptionAlgorithmError(ssecAlgorithmText), "ArgumentName", sseArgumentName)
	return withField(e, "ArgumentValue", algorithm)
}

// errSSECHeader answers an SSE-C trio that is incomplete or does not hold
// a usable key: InvalidArgument naming x-amz-server-side-encryption, never
// echoing the key or its MD5.
func errSSECHeader(text string) *awserr.Error {
	return withField(s3api.ErrInvalidArgument(text), "ArgumentName", sseArgumentName)
}

// errSSECNeedsKey answers a read of an SSE-C version without the key
// (LocalStack test_s3.snapshot.json L12801, L12950; switch
// s3.sse-c.metadata-without-key for HeadObject and GetObjectAttributes).
func errSSECNeedsKey() *awserr.Error { return errInvalidRequest(ssecNeedsKeyText) }

// errSSECWrongKey answers a read of an SSE-C version with another key
// (LocalStack test_s3.snapshot.json L12811, L12855; switch
// s3.sse-c.copy-source-wrong-key for a copy source).
func errSSECWrongKey() *awserr.Error { return s3api.ErrAccessDenied(ssecWrongKeyText) }

// errSSECNotApplicable answers SSE-C headers on a read of a version that
// is not SSE-C (testdata/parity/s3/R15/x-ssec-on-non-ssec-read.json;
// switch s3.sse-c.copy-source-not-applicable for a copy source).
func errSSECNotApplicable() *awserr.Error {
	return errInvalidRequest("The encryption parameters are not applicable to this object.")
}

// errSSECBlocked answers an SSE-C write w to bucket/key while the bucket's
// BlockedEncryptionTypes is SSE-C: 403 AccessDenied naming the caller, the
// action and the object, with no other element (s3-ssec-object-lock.md
// 4.3; S3SOL-01, recorded 2026-11-03, for an assumed role in the owner's
// account; the same form for any other caller is inferred).
func errSSECBlocked(w writeCall, bucket, key string) *awserr.Error {
	return s3api.ErrAccessDenied("User: " + identity.Render(w.principal) + " is not authorized to perform: " + w.action +
		` on resource: "` + objectResource(bucket, key).String() + `" because this bucket has blocked upload requests that specify` +
		" Server Side Encryption with Customer provided keys (SSE-C). Please specify a different server-side encryption type.")
}

// errSSECInsecure answers SSE-C headers over plain HTTP (switch
// s3.sse-c.plain-http).
func errSSECInsecure() *awserr.Error { return errInvalidRequest(ssecInsecureText) }

// customerHeaders is one SSE-C header trio as a request sent it: the
// target's x-amz-server-side-encryption-customer-* headers, or a copy
// source's x-amz-copy-source-server-side-encryption-customer-* headers
// (s3-ssec-object-lock.md 3.1).
type customerHeaders struct {
	algorithm, key, keyMD5 *string
}

// sent reports whether any of the three was sent.
func (h customerHeaders) sent() bool {
	return h.algorithm != nil || h.key != nil || h.keyMD5 != nil
}

// customerKey is an SSE-C key that passed parsing: its decoded bytes,
// which are never stored (s3-ssec-object-lock.md 3.11), and the key-MD5
// header as sent, which responses echo (11.2).
type customerKey struct {
	key    []byte
	keyMD5 string
}

// customerEcho is the SSE-C pair a response reports for k: AES256 and the
// key-MD5 header as sent; neither without k (s3-ssec-object-lock.md 3.4).
func customerEcho(k *customerKey) (algorithm, keyMD5 *string) {
	if k == nil {
		return nil, nil
	}
	return new(customerAlgorithm), new(k.keyMD5)
}

// parseCustomerWrite checks a write's SSE-C headers in the order of
// s3-ssec-object-lock.md 3.2 and 11.3: the conflict with sse, the
// algorithm, the three headers' presence, the key's encoding and length,
// its MD5. It returns nil when none was sent.
func parseCustomerWrite(h customerHeaders, sse *s3api.ServerSideEncryption) (*customerKey, *awserr.Error) {
	if !h.sent() {
		return nil, nil
	}
	if sse != nil {
		return nil, errSSECConflict(*sse)
	}
	key, err := decodeCustomerKey(h)
	if err != nil {
		return nil, err
	}
	if len(key) != customerKeyLen {
		return nil, errSSECHeader(ssecBadKeyText)
	}
	return checkCustomerKeyMD5(key, *h.keyMD5)
}

// parseCustomerRead checks a read's or a copy source's SSE-C headers as
// parseCustomerWrite does, without the conflict and the length checks: a
// key of another length with a matching MD5 then fails the fingerprint
// with 403, as S3 answers it (s3-ssec-object-lock.md 3.3; switch
// s3.sse-c.key-encoding). It returns nil when none was sent.
func parseCustomerRead(h customerHeaders) (*customerKey, *awserr.Error) {
	if !h.sent() {
		return nil, nil
	}
	key, err := decodeCustomerKey(h)
	if err != nil {
		return nil, err
	}
	return checkCustomerKeyMD5(key, *h.keyMD5)
}

// decodeCustomerKey checks a sent trio's algorithm and the presence of all
// three headers, then decodes the key, which must be standard base64
// (switch s3.sse-c.key-encoding for one that is not).
func decodeCustomerKey(h customerHeaders) ([]byte, *awserr.Error) {
	switch {
	case h.algorithm != nil && *h.algorithm != customerAlgorithm:
		return nil, errSSECAlgorithm(*h.algorithm)
	case h.algorithm == nil:
		return nil, errSSECHeader(ssecNoAlgorithmText)
	case h.key == nil:
		return nil, errSSECHeader(ssecNoKeyText)
	case h.keyMD5 == nil:
		return nil, errSSECHeader(ssecNoKeyMD5Text)
	}
	key, err := base64.StdEncoding.DecodeString(*h.key)
	if err != nil {
		return nil, errSSECHeader(ssecBadKeyText)
	}
	return key, nil
}

// checkCustomerKeyMD5 refuses a key whose MD5 header is not the base64 MD5
// of its bytes, and otherwise returns the parsed key.
func checkCustomerKeyMD5(key []byte, keyMD5 string) (*customerKey, *awserr.Error) {
	sum := md5.Sum(key) //nolint:gosec // see the import
	if base64.StdEncoding.EncodeToString(sum[:]) != keyMD5 {
		return nil, errSSECHeader(ssecBadKeyMD5Text)
	}
	return &customerKey{key: key, keyMD5: keyMD5}, nil
}

// newCustomerEncryption fingerprints k under salt.
func newCustomerEncryption(salt []byte, k customerKey) CustomerEncryption {
	return CustomerEncryption{Salt: salt, MAC: fingerprint(salt, k.key)}
}

// matches reports whether k is the key e was made with, comparing in
// constant time.
func (e CustomerEncryption) matches(k customerKey) bool {
	return hmac.Equal(e.MAC, fingerprint(e.Salt, k.key))
}

// fingerprint is HMAC-SHA256(salt, key).
func fingerprint(salt, key []byte) []byte {
	mac := hmac.New(sha256.New, salt)
	_, _ = mac.Write(key) // a hash.Hash never returns an error
	return mac.Sum(nil)
}

// checkCustomerWrite refuses an SSE-C write w of key whose headers parsed:
// 403 on a bucket that blocks SSE-C (errSSECBlocked), then 400 over plain
// HTTP (switch s3.sse-c.plain-http).
func checkCustomerWrite(b *Bucket, key string, w writeCall) *awserr.Error {
	if b.BlockedEncryption == BlockSSEC {
		return errSSECBlocked(w, b.Name, key)
	}
	return checkCustomerScheme(w.scheme)
}

// checkCustomerScheme refuses SSE-C headers sent over plain HTTP: "Amazon
// S3 rejects any requests made over HTTP when using SSE-C"
// (s3-ssec-object-lock.md 3.9; switch s3.sse-c.plain-http).
func checkCustomerScheme(scheme string) *awserr.Error {
	if scheme != "https" {
		return errSSECInsecure()
	}
	return nil
}

// checkCustomerRead judges a read's or copy source's SSE-C headers against
// the version's stored encryption (s3-ssec-object-lock.md 3.3, 3.5): the
// stored-form 400 without headers on an SSE-C version, the header checks,
// plain HTTP, the not-applicable 400 on another version, the wrong-key
// 403. It returns the key whose MD5 the response echoes, nil without
// headers. A blocked bucket never refuses a read (4.3).
func checkCustomerRead(stored Encryption, h customerHeaders, scheme string) (*customerKey, *awserr.Error) {
	c, isC := stored.(CustomerEncryption)
	if !h.sent() {
		if isC {
			return nil, errSSECNeedsKey()
		}
		return nil, nil
	}
	k, err := parseCustomerRead(h)
	if err != nil {
		return nil, err
	}
	if err := checkCustomerScheme(scheme); err != nil {
		return nil, err
	}
	switch {
	case !isC:
		return nil, errSSECNotApplicable()
	case !c.matches(*k):
		return nil, errSSECWrongKey()
	}
	return k, nil
}

// checkCustomerPart judges an UploadPart's or UploadPartCopy target's
// SSE-C headers against its upload's encryption (s3-ssec-object-lock.md
// 3.6): the header checks, the bucket block, plain HTTP, then the match
// with the upload. It returns the key whose MD5 the response echoes, nil
// for an upload that is not SSE-C.
func checkCustomerPart(upload Encryption, h customerHeaders, b *Bucket, key string, w writeCall) (*customerKey, *awserr.Error) {
	k, err := parseCustomerWrite(h, nil)
	if err != nil {
		return nil, err
	}
	if k != nil {
		if err := checkCustomerWrite(b, key, w); err != nil {
			return nil, err
		}
	}
	if err := matchUpload(upload, k); err != nil {
		return nil, err
	}
	return k, nil
}

// checkCustomerComplete judges a CompleteMultipartUpload's SSE-C headers
// against its upload (switch s3.sse-c.complete-headers): headers that are
// sent are checked as checkCustomerSent checks them; none is needed unless
// the upload is SSE-C and declared a checksum algorithm, as the model notes
// the key "is needed only when the object was created using a checksum
// algorithm" (s3-ssec-object-lock.md 3.6).
func checkCustomerComplete(u *Upload, h customerHeaders, scheme string) *awserr.Error {
	if !h.sent() && isCustomer(u.Encryption) && u.Checksum.Algorithm != "" {
		return errInvalidRequest(ssecInitiateText)
	}
	return checkCustomerSent(u.Encryption, h, scheme)
}

// checkCustomerSent judges the SSE-C headers a CompleteMultipartUpload or
// a ListParts sent against the upload's encryption as UploadPart judges
// them, without the bucket block: the header checks, plain HTTP, then the
// match with the upload (switch s3.sse-c.complete-headers, whose
// operations both are; plain HTTP as for any SSE-C request,
// s3-ssec-object-lock.md 3.9). None sent passes: ListParts never needs
// them (3.6), and checkCustomerComplete adds Complete's one exception.
func checkCustomerSent(upload Encryption, h customerHeaders, scheme string) *awserr.Error {
	if !h.sent() {
		return nil
	}
	k, err := parseCustomerWrite(h, nil)
	if err != nil {
		return err
	}
	if err := checkCustomerScheme(scheme); err != nil {
		return err
	}
	return matchUpload(upload, k)
}

// matchUpload refuses a part's key k, nil when none was sent, that is not
// the one its upload was created with: a key on an upload that is not
// SSE-C or none on one that is (LocalStack test_s3.snapshot.json L13133,
// L13154), or another key (L13164).
func matchUpload(upload Encryption, k *customerKey) *awserr.Error {
	c, isC := upload.(CustomerEncryption)
	switch {
	case isC != (k != nil):
		return errInvalidRequest(ssecInitiateText)
	case isC && !c.matches(*k):
		return errInvalidRequest(ssecMismatchText)
	}
	return nil
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

// blockedEncryptionTexts are the EncryptionType values each block is
// written as (s3-ssec-object-lock.md 4.2).
var blockedEncryptionTexts = textTable[BlockedEncryption]{ //nolint:gochecknoglobals // a constant table; Go has no constant maps.
	BlockSSEC: string(s3api.EncryptionTypeSseC),
	BlockNone: string(s3api.EncryptionTypeNone),
}

// MarshalText writes SSE-C or NONE, the EncryptionType values.
func (b BlockedEncryption) MarshalText() ([]byte, error) {
	return blockedEncryptionTexts.marshal("BlockedEncryption", b)
}

// UnmarshalText reads SSE-C or NONE and refuses any other text.
func (b *BlockedEncryption) UnmarshalText(text []byte) error {
	return blockedEncryptionTexts.unmarshal("BlockedEncryption", text, b)
}

// newBucketBlock is a new bucket's BlockedEncryption in region: BlockNone
// in me-south-1 and me-central-1, BlockSSEC elsewhere
// (s3-ssec-object-lock.md 4.1).
func newBucketBlock(region aws.Region) BlockedEncryption {
	switch region.String() {
	case "me-south-1", "me-central-1":
		return BlockNone
	}
	return BlockSSEC
}

// blockedEncryptionOf reads a PutBucketEncryption rule's
// BlockedEncryptionTypes; false when the rule has none, so the bucket
// keeps its setting (switch s3.encryption.blocked-types-omitted). [SSE-C]
// blocks and [NONE] unblocks; an empty list, or one that holds both, is
// MalformedXML, which no recording shows (inferred).
func blockedEncryptionOf(rule s3api.ServerSideEncryptionRule) (BlockedEncryption, bool, *awserr.Error) {
	t := rule.BlockedEncryptionTypes
	if t == nil {
		return BlockSSEC, false, nil
	}
	var sseC, none bool
	for _, e := range t.EncryptionType {
		switch e {
		case s3api.EncryptionTypeSseC:
			sseC = true
		case s3api.EncryptionTypeNone:
			none = true
		default:
			return BlockSSEC, false, errMalformedXML()
		}
	}
	switch {
	case sseC == none:
		return BlockSSEC, false, errMalformedXML()
	case none:
		return BlockNone, true, nil
	}
	return BlockSSEC, true, nil
}

// encryptionRead is GetBucketEncryption's document: the stored rule or the
// SSE-S3 default, with the AES256 default, BucketKeyEnabled false and
// BlockedEncryptionTypes filled in (s3-ssec-object-lock.md 2, 4.2;
// testdata/parity/s3/R06/01-get-get-bucket-encryption.json,
// 02-get-encryption-arn.json; switch s3.encryption.unblocked-read for
// NONE and for a rule put without a default).
func encryptionRead(b *Bucket) *s3api.ServerSideEncryptionConfiguration {
	var rule s3api.ServerSideEncryptionRule
	if b.Encryption != nil && len(b.Encryption.Rules) > 0 {
		rule = b.Encryption.Rules[0]
	}
	if rule.ApplyServerSideEncryptionByDefault == nil {
		rule.ApplyServerSideEncryptionByDefault = &s3api.ServerSideEncryptionByDefault{SSEAlgorithm: s3api.ServerSideEncryptionAes256}
	}
	if rule.BucketKeyEnabled == nil {
		rule.BucketKeyEnabled = new(false)
	}
	blocked := s3api.EncryptionType(blockedEncryptionTexts[b.BlockedEncryption])
	rule.BlockedEncryptionTypes = &s3api.BlockedEncryptionTypes{EncryptionType: []s3api.EncryptionType{blocked}}
	return &s3api.ServerSideEncryptionConfiguration{Rules: []s3api.ServerSideEncryptionRule{rule}}
}
