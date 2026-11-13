package ids

import (
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"slices"
	"strings"
	"uuid"
)

// Identifier types. Each is minted by the Source method of the same name in
// the format AWS uses, from that type's own stream.
type (
	// RequestID is the x-amzn-RequestId of a response: a UUID, in the
	// form of the answering service (ForService).
	RequestID string
	// MessageID is an SQS message id: a UUID.
	MessageID string
	// ReceiptHandle is an SQS receipt handle: opaque, base64-looking and
	// starting with AQEB, as real handles do.
	ReceiptHandle string
	// SNSMessageID is an SNS message id: a version 5 UUID.
	SNSMessageID string
	// LambdaRequestID is a Lambda invocation request id: a UUID.
	LambdaRequestID string
	// ESMID is a Lambda event source mapping UUID.
	ESMID string
	// UploadID is an S3 multipart upload id: an opaque URL-safe token.
	UploadID string
	// VersionID is an S3 object version id: 32 characters from [A-Za-z0-9._].
	VersionID string
	// ETag is the random entity tag of an SSE-KMS object: 32 lowercase hex
	// characters, deliberately not the MD5 of the content, and unquoted.
	ETag string
	// KMSKeyID is a KMS key id: a lowercase UUID.
	KMSKeyID string
	// IAMID is an IAM unique id: a four-letter prefix (AROA role, ANPA
	// managed policy, AIDA user, AIPA instance profile) and 17 base32 characters.
	IAMID string
	// AccessKeyID is an access key id: AKIA (long-term) or ASIA (temporary)
	// and 16 base32 characters that encode the owning account.
	AccessKeyID string
	// SecretAccessKey is a 40-character secret access key.
	SecretAccessKey string
	// SessionToken is an opaque STS session token.
	SessionToken string
	// DeliveryID identifies one cross-service delivery in the outbox: a UUID.
	DeliveryID string
)

const (
	base32Alphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	base64Alphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	base64URLAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	versionAlphabet   = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._"

	// Receipt handles are AQEB and the base64 of this many bytes: 412
	// characters on a standard queue, 304 on a FIFO queue, as AWS's
	// (sqs.md 7.2).
	receiptHandlePrefix  = "AQEB"
	standardReceiptBytes = 304
	fifoReceiptBytes     = 224

	uploadIDLen     = 64  // AWS upload ids run past 60 characters
	versionIDLen    = 32  // s3.md: 32 characters from [A-Za-z0-9._]
	iamIDLen        = 17  // after the prefix; 21 characters in all
	secretKeyLen    = 40  // iam-sts-kms.md 2.1
	sessionTokenLen = 192 // opaque; AWS says to assume no size
)

// RequestID mints a request id.
func (s *Source) RequestID() RequestID { return RequestID(s.UUID(KindRequestID).String()) }

// versionNibble is the index of the version digit in a UUID's text form.
const versionNibble = 14

// Version5 is the request id with the version digit of a version 5 UUID,
// the form of nearly every x-amzn-RequestId SQS and SNS answer with
// (testdata/parity/sqs, testdata/parity/sns). The draw behind it does not
// change, so a seeded run keeps its stream.
func (r RequestID) Version5() RequestID {
	if len(r) <= versionNibble {
		return r
	}
	return r[:versionNibble] + "5" + r[versionNibble+1:]
}

// crockford is Crockford's base32: digits and capitals without I, L, O
// and U.
var crockford = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ") //nolint:gochecknoglobals // a constant value; Go has no struct constants.

// S3Form is the request id in S3's form: 16 characters of Crockford's
// base32, as every x-amz-request-id under testdata/parity/s3 is. They
// encode ten bytes of the UUID that its version and variant bits leave
// wholly random, bytes 0-5 and 10-13, so the draw behind it does not change
// and a seeded run keeps its stream. A request id that is not a UUID is
// returned as it is.
func (r RequestID) S3Form() RequestID {
	b, err := hex.DecodeString(strings.ReplaceAll(string(r), "-", ""))
	if err != nil || len(b) != 16 {
		return r
	}
	// Ten bytes are 80 bits, 16 characters with no padding.
	return RequestID(crockford.EncodeToString(slices.Concat(b[0:6], b[10:14])))
}

// ForService is the request id as the service named service answers with
// it: version 5 for SQS and SNS, S3's form for S3 and S3 Control
// (testdata/parity/s3/R12/12-lifecycle-expiration-0.json,
// R06/06-s3control-list-missing.json), version 4, as minted, for every
// other service.
func (r RequestID) ForService(service string) RequestID {
	switch service {
	case "sqs", "sns":
		return r.Version5()
	case "s3", "s3control":
		return r.S3Form()
	}
	return r
}

// MessageID mints an SQS message id.
func (s *Source) MessageID() MessageID { return MessageID(s.UUID(KindMessageID).String()) }

// ReceiptHandle mints a receipt handle of a standard SQS queue.
func (s *Source) ReceiptHandle() ReceiptHandle {
	return s.receiptHandle(standardReceiptBytes)
}

// FIFOReceiptHandle mints a receipt handle of a FIFO SQS queue.
func (s *Source) FIFOReceiptHandle() ReceiptHandle {
	return s.receiptHandle(fifoReceiptBytes)
}

// receiptHandle mints AQEB and the base64 of n random bytes. AQEB is
// itself the base64 of three bytes, so the handle is the padded base64 of
// one blob, as AWS's are.
func (s *Source) receiptHandle(n uint) ReceiptHandle {
	return ReceiptHandle(receiptHandlePrefix + base64.StdEncoding.EncodeToString(s.Draw(KindReceiptHandle, n)))
}

// ParseReceiptHandle accepts s when it has a form ReceiptHandle or
// FIFOReceiptHandle mints, on any queue, and reports false otherwise. A
// handle of that form may still name no message.
func ParseReceiptHandle(s string) (ReceiptHandle, bool) {
	rest, ok := strings.CutPrefix(s, receiptHandlePrefix)
	if !ok || (len(rest) != base64.StdEncoding.EncodedLen(standardReceiptBytes) && len(rest) != base64.StdEncoding.EncodedLen(fifoReceiptBytes)) {
		return "", false
	}
	// Strict refuses non-zero padding bits. The decoder skips line breaks,
	// but each one shortens the decoded blob, which the size check refuses.
	b, err := base64.StdEncoding.Strict().DecodeString(rest)
	if err != nil || (len(b) != standardReceiptBytes && len(b) != fifoReceiptBytes) {
		return "", false
	}
	return ReceiptHandle(s), true
}

// SNSMessageID mints an SNS message id: a version 5 UUID, as AWS's are
// (sqs/R16/01-sqs-enveloped.json), drawn like any other from the kind's
// stream.
func (s *Source) SNSMessageID() SNSMessageID {
	return SNSMessageID(RequestID(s.UUID(KindSNSMessageID).String()).Version5())
}

// LambdaRequestID mints a Lambda invocation request id.
func (s *Source) LambdaRequestID() LambdaRequestID {
	return LambdaRequestID(s.UUID(KindLambdaRequestID).String())
}

// ESMID mints an event source mapping UUID.
func (s *Source) ESMID() ESMID { return ESMID(s.UUID(KindESMID).String()) }

// UploadID mints an S3 multipart upload id.
func (s *Source) UploadID() UploadID {
	return UploadID(s.chars(KindUploadID, base64URLAlphabet, uploadIDLen))
}

// VersionID mints an S3 object version id.
func (s *Source) VersionID() VersionID {
	return VersionID(s.chars(KindVersionID, versionAlphabet, versionIDLen))
}

// ETag mints the entity tag of an SSE-KMS or SSE-C object or part, which
// is not the MD5 of its bytes.
func (s *Source) ETag() ETag { return ETag(hex.EncodeToString(s.Draw(KindETag, 16))) }

// SSECSalt draws the 16-byte salt of the HMAC S3 keeps of an SSE-C
// customer key, one per object version or upload.
func (s *Source) SSECSalt() []byte { return s.Draw(KindSSECSalt, 16) }

// LambdaRecordKey mints the UUID that ends the S3 object key of an
// asynchronous invocation record (lambda-destinations-layers.md 4.5).
func (s *Source) LambdaRecordKey() uuid.UUID { return s.UUID(KindLambdaRecordKey) }

// StreamEventID mints a DynamoDB stream record's eventID: 32 lower-case hex
// digits, the form of the documentation's example (dynamodb-streams.md 4.5).
func (s *Source) StreamEventID() string { return hex.EncodeToString(s.Draw(KindStreamEventID, 16)) }

// StreamIteratorKey draws the 32-byte HMAC key that signs shard iterators.
func (s *Source) StreamIteratorKey() []byte { return s.Draw(KindStreamIteratorKey, 32) }

// KMSKeyID mints a KMS key id.
func (s *Source) KMSKeyID() KMSKeyID { return KMSKeyID(s.UUID(KindKMSKeyID).String()) }

// RoleID mints an IAM role id (AROA).
func (s *Source) RoleID() IAMID { return s.iamID("AROA") }

// PolicyID mints an IAM managed policy id (ANPA).
func (s *Source) PolicyID() IAMID { return s.iamID("ANPA") }

// UserID mints an IAM user id (AIDA).
func (s *Source) UserID() IAMID { return s.iamID("AIDA") }

// InstanceProfileID mints an IAM instance profile id (AIPA).
func (s *Source) InstanceProfileID() IAMID { return s.iamID("AIPA") }

// SecretAccessKey mints a secret access key.
func (s *Source) SecretAccessKey() SecretAccessKey {
	return SecretAccessKey(s.chars(KindSecretAccessKey, base64Alphabet, secretKeyLen))
}

// SessionToken mints an opaque session token.
func (s *Source) SessionToken() SessionToken {
	return SessionToken(s.chars(KindSessionToken, base64Alphabet, sessionTokenLen))
}

// DeliveryID mints an outbox delivery id.
func (s *Source) DeliveryID() DeliveryID { return DeliveryID(s.UUID(KindDeliveryID).String()) }

// LogValue keeps the secret out of logs.
func (SecretAccessKey) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

// LogValue keeps the token out of logs.
func (SessionToken) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

func (s *Source) iamID(prefix string) IAMID {
	return IAMID(prefix + s.chars(KindIAMID, base32Alphabet, iamIDLen))
}

// chars draws n characters uniformly from alphabet, whose length is a power
// of two no larger than 256, one byte per character.
func (s *Source) chars(k Kind, alphabet string, n uint) string {
	b := s.Draw(k, n)
	for i, r := range b {
		b[i] = alphabet[int(r)%len(alphabet)]
	}
	return string(b)
}
