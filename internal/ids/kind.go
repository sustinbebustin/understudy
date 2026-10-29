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
	// scheduled events.
	KindEventID
	// KindSSECSalt salts the HMAC S3 keeps of an SSE-C customer key
	// (s3-ssec-object-lock.md 3.11).
	KindSSECSalt
	// KindLambdaRecordKey is the UUID that ends the S3 object key of an
	// asynchronous invocation record (lambda-destinations-layers.md 4.5).
	KindLambdaRecordKey
	// KindAPIGatewayAPIID is API Gateway's API ids, which HTTP and REST
	// APIs draw alike, so the execute-api namespace is one stream.
	KindAPIGatewayAPIID
	// KindAPIGatewayChildID is the ids of what an API Gateway API holds:
	// routes, integrations and deployments.
	KindAPIGatewayChildID
	// KindStreamEventID is a DynamoDB stream record's eventID
	// (dynamodb-streams.md 4.5).
	KindStreamEventID
	// KindStreamIteratorKey is the HMAC key that signs shard iterators
	// (dynamodb-streams.md 11.1).
	KindStreamIteratorKey
	// KindSFNExecutionName names a Step Functions execution started without
	// a name: a v4 UUID (stepfunctions-api.md 6.1).
	KindSFNExecutionName
	// KindSFNRevisionID is the revisionId an effective UpdateStateMachine
	// mints: a v4 UUID (stepfunctions-api.md 5.3).
	KindSFNRevisionID
	// KindSFNUUID is a States.UUID value: a v4 UUID (stepfunctions-asl.md
	// 5.2).
	KindSFNUUID
	// KindSFNMathRandom draws an unseeded States.MathRandom value
	// (stepfunctions-asl.md 5.2).
	KindSFNMathRandom
	// KindSFNJitter seeds each Step Functions execution's FULL retry jitter
	// (stepfunctions-asl.md 8.3), apart from delivery jitter (KindJitter),
	// so EventBridge deliveries never shift an execution's retry delays
	// (ADR 0018).
	KindSFNJitter
	// KindSESMessageID is the random part of an SES MessageId (ses.md 4.7).
	KindSESMessageID
	// KindSESDomainToken is an SES domain verification token: 32 bytes
	// encoded as 44 base64 characters (ses.md 5.3).
	KindSESDomainToken
	// KindSESDkimToken is the Easy DKIM tokens of an SES v2 domain identity,
	// whose format is unrecorded (ses.md 5.4, SES-13).
	KindSESDkimToken
	// KindSESLinkToken is the token of an SES verification link (ses.md
	// 11.3).
	KindSESLinkToken

	// endKind follows the last kind and is no kind: kindCount counts up to
	// it, so a new kind is one line above it plus its String case.
	endKind
)

const kindCount = int(endKind)

// Kinds returns every generator kind in value order, KindRequestID first.
func Kinds() []Kind {
	all := make([]Kind, 0, kindCount)
	for k := range endKind {
		all = append(all, k)
	}
	return all
}

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
	case KindSSECSalt:
		return "s3-ssec-salt"
	case KindLambdaRecordKey:
		return "lambda-record-key"
	case KindAPIGatewayAPIID:
		return "apigateway-api-id"
	case KindAPIGatewayChildID:
		return "apigateway-child-id"
	case KindStreamEventID:
		return "dynamodb-stream-event-id"
	case KindStreamIteratorKey:
		return "shard-iterator-key"
	case KindSFNExecutionName:
		return "sfn-execution-name"
	case KindSFNRevisionID:
		return "sfn-revision-id"
	case KindSFNUUID:
		return "sfn-uuid"
	case KindSFNMathRandom:
		return "sfn-math-random"
	case KindSFNJitter:
		return "sfn-jitter"
	case KindSESMessageID:
		return "ses-message-id"
	case KindSESDomainToken:
		return "ses-domain-token"
	case KindSESDkimToken:
		return "ses-dkim-token"
	case KindSESLinkToken:
		return "ses-link-token"
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
