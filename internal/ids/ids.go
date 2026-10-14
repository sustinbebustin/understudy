package ids

import (
	"encoding/hex"
	"log/slog"
	"strings"
)

// Identifier types. Each is minted by the Source method of the same name in
// the format AWS uses, from that type's own stream.
type (
	// RequestID is the x-amzn-RequestId of a response: a UUID.
	RequestID string
	// MessageID is an SQS message id: a UUID.
	MessageID string
	// ReceiptHandle is an SQS receipt handle: opaque, base64-looking and
	// starting with AQEB, as real handles do.
	ReceiptHandle string
	// SNSMessageID is an SNS message id: a UUID.
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

	receiptHandleLen = 32  // after the AQEB prefix
	uploadIDLen      = 64  // AWS upload ids run past 60 characters
	versionIDLen     = 32  // s3.md: 32 characters from [A-Za-z0-9._]
	iamIDLen         = 17  // after the prefix; 21 characters in all
	secretKeyLen     = 40  // iam-sts-kms.md 2.1
	sessionTokenLen  = 192 // opaque; AWS says to assume no size
)

// RequestID mints a request id.
func (s *Source) RequestID() RequestID { return RequestID(s.UUID(KindRequestID).String()) }

// MessageID mints an SQS message id.
func (s *Source) MessageID() MessageID { return MessageID(s.UUID(KindMessageID).String()) }

// ReceiptHandle mints an SQS receipt handle.
func (s *Source) ReceiptHandle() ReceiptHandle {
	return ReceiptHandle("AQEB" + s.chars(KindReceiptHandle, base64Alphabet, receiptHandleLen))
}

// ParseReceiptHandle accepts s when it has the form ReceiptHandle mints, AQEB
// and 32 base64 characters, and reports false otherwise. A handle of that
// form may still name no message.
func ParseReceiptHandle(s string) (ReceiptHandle, bool) {
	rest, ok := strings.CutPrefix(s, "AQEB")
	if !ok || len(rest) != receiptHandleLen {
		return "", false
	}
	for i := range len(rest) {
		if strings.IndexByte(base64Alphabet, rest[i]) < 0 {
			return "", false
		}
	}
	return ReceiptHandle(s), true
}

// SNSMessageID mints an SNS message id.
func (s *Source) SNSMessageID() SNSMessageID {
	return SNSMessageID(s.UUID(KindSNSMessageID).String())
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

// ETag mints the entity tag of an SSE-KMS object.
func (s *Source) ETag() ETag { return ETag(hex.EncodeToString(s.Draw(KindETag, 16))) }

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
