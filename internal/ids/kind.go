package ids

import (
	"fmt"
	"strings"
)

// Kind names one generator stream. A seeded Source derives an independent
// stream per kind, so a new call site of one kind never shifts the ids of
// another.
type Kind uint8

// The generator kinds: one per identifier type, plus the fault-injection and
// delivery-jitter streams that draw numbers rather than identifiers.
const (
	KindRequestID Kind = iota
	KindMessageID
	KindReceiptHandle
	KindSNSMessageID
	KindLambdaRequestID
	KindESMID
	KindUploadID
	KindVersionID
	KindETag
	KindKMSKeyID
	KindIAMID
	KindAccessKeyID
	KindSecretAccessKey
	KindSessionToken
	KindDeliveryID
	KindFaultStream
	KindJitter
	// KindEventID is EventBridge's: PutEvents entry ids and the ids of
	// scheduled events. Declared last, so existing kinds keep their values.
	KindEventID
)

const kindCount = int(KindEventID) + 1

// String returns the kind's stable name, which also seeds its stream and
// keys it in snapshot manifests.
func (k Kind) String() string {
	switch k {
	case KindRequestID:
		return "request-id"
	case KindMessageID:
		return "sqs-message-id"
	case KindReceiptHandle:
		return "sqs-receipt-handle"
	case KindSNSMessageID:
		return "sns-message-id"
	case KindLambdaRequestID:
		return "lambda-request-id"
	case KindESMID:
		return "lambda-esm-id"
	case KindUploadID:
		return "s3-upload-id"
	case KindVersionID:
		return "s3-version-id"
	case KindETag:
		return "s3-etag"
	case KindKMSKeyID:
		return "kms-key-id"
	case KindIAMID:
		return "iam-id"
	case KindAccessKeyID:
		return "access-key-id"
	case KindSecretAccessKey:
		return "secret-access-key"
	case KindSessionToken:
		return "session-token"
	case KindDeliveryID:
		return "delivery-id"
	case KindFaultStream:
		return "fault-stream"
	case KindJitter:
		return "jitter"
	case KindEventID:
		return "events-event-id"
	}
	return fmt.Sprintf("kind(%d)", uint8(k))
}

// MarshalText returns the kind's name, so a map keyed by Kind marshals with
// readable keys.
func (k Kind) MarshalText() ([]byte, error) {
	if !k.valid() {
		return nil, &UnknownKindError{Name: k.String()}
	}
	return []byte(k.String()), nil
}

// UnmarshalText parses a kind name.
func (k *Kind) UnmarshalText(b []byte) error {
	for c := range Kind(kindCount) {
		if c.String() == string(b) {
			*k = c
			return nil
		}
	}
	return &UnknownKindError{Name: string(b)}
}

func (k Kind) valid() bool { return int(k) < kindCount }

// knownKinds lists every kind name, comma separated, for error messages.
func knownKinds() string {
	names := make([]string, 0, kindCount)
	for k := range Kind(kindCount) {
		names = append(names, k.String())
	}
	return strings.Join(names, ", ")
}
