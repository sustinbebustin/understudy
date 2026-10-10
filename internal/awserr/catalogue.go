package awserr

import (
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// The generic catalogue: the unmodeled, protocol-level errors of
// error-matrix.md section 3. Shapes live in the com.amazon.coral.service
// namespace AWS uses for them (DynamoDB sends that __type; awsJson1_1
// clients strip namespaces), except NotImplemented, which is understudy's
// own code for an unbound operation (ADR 0003). A status recorded against a
// real service (error-matrix.md 5.5, DYN) wins over AWS's CommonErrors pages;
// where nothing is recorded, the majority of the pages wins. Services whose
// real status differs override it in their overlay.

const coralService = "com.amazon.coral.service#"

func coral(name, queryCode string, status int, fault Fault, key MessageKey) Identity {
	return Identity{
		Shape:      smithy.ShapeID(coralService + name),
		JSONType:   coralService + name,
		QueryCode:  queryCode,
		Status:     status,
		Fault:      fault,
		MessageKey: key,
	}
}

// SerializationException is a JSON-family request body that does not decode
// against the input shape: 400, message under "Message" as DynamoDB sends it.
func SerializationException() Identity {
	return coral("SerializationException", "SerializationException", 400, Sender, MessageUpper)
}

// UnknownOperationException is a JSON-family request naming no operation of
// the service: 400, as real DynamoDB answers a missing target, a bad target or
// a bad operation name (error-matrix.md 5.5 T5) and ARCHITECTURE.md 6.3
// specifies. The JSON CommonErrors template's 404 is Lambda's status, which
// its overlay's unknownOperationError carries.
func UnknownOperationException() Identity {
	return coral("UnknownOperationException", "UnknownOperationException", 400, Sender, MessageLower)
}

// InvalidAction is a Query-family request naming no operation of the
// service: 400, "The action X is not valid for this endpoint.".
func InvalidAction() Identity {
	return coral("InvalidAction", "InvalidAction", 400, Sender, MessageLower)
}

// The auth errors differ per family in name and status, so each family has
// its own constructors. JSON-family statuses are what real DynamoDB sends
// (error-matrix.md 5.5 T8, 400 for all of them, matching the DynamoDB
// developer guide); Query-family statuses are the Query CommonErrors template
// of the SNS, IAM and STS pages (error-matrix.md 3).

// MissingAuthenticationTokenException is a JSON-family request that carries
// no credentials: 400.
func MissingAuthenticationTokenException() Identity {
	return coral("MissingAuthenticationTokenException", "MissingAuthenticationToken", 400, Sender, MessageLower)
}

// IncompleteSignatureException is a JSON-family request with a malformed
// signature: 400.
func IncompleteSignatureException() Identity {
	return coral("IncompleteSignatureException", "IncompleteSignature", 400, Sender, MessageLower)
}

// InvalidSignatureException is a JSON-family request whose signature does not
// verify or has expired: 400.
func InvalidSignatureException() Identity {
	return coral("InvalidSignatureException", "SignatureDoesNotMatch", 400, Sender, MessageLower)
}

// MissingAuthenticationToken is a Query-family request that carries no
// credentials: 403.
func MissingAuthenticationToken() Identity {
	return coral("MissingAuthenticationToken", "MissingAuthenticationToken", 403, Sender, MessageLower)
}

// IncompleteSignature is a Query-family request with a malformed signature:
// 400.
func IncompleteSignature() Identity {
	return coral("IncompleteSignature", "IncompleteSignature", 400, Sender, MessageLower)
}

// SignatureDoesNotMatch is a Query-family request whose signature does not
// verify: 403.
func SignatureDoesNotMatch() Identity {
	return coral("SignatureDoesNotMatch", "SignatureDoesNotMatch", 403, Sender, MessageLower)
}

// AccessDeniedException is a request the caller may not make: 403.
func AccessDeniedException() Identity {
	return coral("AccessDeniedException", "AccessDeniedException", 403, Sender, MessageLower)
}

// InternalFailure is an emulator bug: 500.
func InternalFailure() Identity {
	return coral("InternalFailure", "InternalFailure", 500, Receiver, MessageLower)
}

// NotImplemented is an operation in the model that no handler is bound to:
// 501, outside every SDK's retry set, so clients fail fast with the message.
func NotImplemented() Identity {
	return Identity{
		Shape:      "dev.understudy#NotImplemented",
		JSONType:   "NotImplemented",
		QueryCode:  "NotImplemented",
		Status:     501,
		Fault:      Receiver,
		MessageKey: MessageLower,
	}
}

// RequestEntityTooLarge is a request body above the operation's cap: 413.
func RequestEntityTooLarge() Identity {
	return coral("RequestEntityTooLargeException", "RequestEntityTooLarge", 413, Sender, MessageLower)
}

// ThrottlingException is the generic throttle: 400 and retryable. Services
// with their own throttling shape name it in the overlay's throttleError.
func ThrottlingException() Identity {
	id := coral("ThrottlingException", "ThrottlingException", 400, Sender, MessageLower)
	id.Retryable = true
	return id
}

// RequestAbortedException is a request the client abandoned: 400.
func RequestAbortedException() Identity {
	return coral("RequestAbortedException", "RequestAborted", 400, Sender, MessageLower)
}

// Family is a protocol family's generic error vocabulary.
type Family uint8

// Family values. S3 (restXml) has its own vocabulary, which its overlay
// carries in full (error-matrix.md H8), so it is no family here.
const (
	JSONFamily  Family = iota + 1 // awsJson1_0, awsJson1_1, restJson1, rpcv2Cbor
	QueryFamily                   // awsQuery
)

// FamilyOf returns the family of protocol p, or false for restXml and raw.
func FamilyOf(p ref.ProtocolID) (Family, bool) {
	switch p {
	case ref.AWSJSON10, ref.AWSJSON11, ref.RESTJSON1, ref.RPCv2CBOR:
		return JSONFamily, true
	case ref.AWSQuery:
		return QueryFamily, true
	case ref.RESTXML, ref.Raw:
		return 0, false
	default:
		return 0, false
	}
}

// Catalogue lists the generic identities services of family f send, for
// validating fault rules against what a service really returns.
func Catalogue(f Family) []Identity {
	shared := []Identity{
		AccessDeniedException(),
		InternalFailure(),
		NotImplemented(),
		RequestEntityTooLarge(),
		ThrottlingException(),
	}
	switch f {
	case JSONFamily:
		return append([]Identity{
			SerializationException(),
			UnknownOperationException(),
			MissingAuthenticationTokenException(),
			IncompleteSignatureException(),
			InvalidSignatureException(),
		}, shared...)
	case QueryFamily:
		return append([]Identity{
			InvalidAction(),
			MissingAuthenticationToken(),
			IncompleteSignature(),
			SignatureDoesNotMatch(),
		}, shared...)
	default:
		return nil
	}
}
