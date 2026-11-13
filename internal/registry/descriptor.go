package registry

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Service describes one AWS service. Generated packages hold one immutable
// Service each; nothing changes it after generation.
type Service struct {
	ID          ref.ServiceID
	Shape       smithy.ShapeID
	SigningName string // the SigV4 credential-scope name: "monitoring" for CloudWatch
	IAMPrefix   string // the IAM action prefix: "cloudwatch" for CloudWatch
	// ARNNamespace is the service part of the service's ARNs.
	ARNNamespace string
	// Protocols are the model's protocols plus extras from
	// codegen/services (SQS speaks awsQuery too).
	Protocols []ref.ProtocolID
	// JSONTargetPrefix is the X-Amz-Target prefix: AmazonSQS, TrentService.
	JSONTargetPrefix string
	// QueryVersion is the awsQuery Version parameter, set when Protocols
	// holds awsquery.
	QueryVersion    string
	XMLNamespace    string
	QueryCompatible bool // awsQueryCompatible: JSON errors carry x-amzn-query-error
	// ResponseCRC32 is the overlay's dev.understudy#responseCrc32: every
	// answer the awsJson codec encodes, success and error, carries
	// x-amz-crc32, the CRC-32 (IEEE) of its body in decimal.
	ResponseCRC32 bool
	// XMLDeclaration is the overlay's dev.understudy#xmlDeclaration: every
	// body the awsQuery codec encodes, success and error, opens with
	// <?xml version="1.0"?> and no newline after it.
	XMLDeclaration bool
	// RefuseDuplicateMembers is the overlay's
	// dev.understudy#refuseDuplicateMembers: the awsJson codec refuses a
	// request body whose objects repeat a member name, at any depth,
	// instead of keeping the last value.
	RefuseDuplicateMembers bool
	// QueryErrorEnvelope is the overlay's dev.understudy#queryErrorEnvelope:
	// how the awsQuery ErrorResponse differs from Smithy's example. The zero
	// value is Smithy's example.
	QueryErrorEnvelope QueryErrorEnvelope
	// MalformedRequestError is the overlay's dev.understudy#malformedRequestError,
	// the identity for a body that does not decode (Lambda
	// InvalidRequestContentException). It is the zero Identity when the
	// overlay sets none; the codec then answers with its protocol's default.
	MalformedRequestError awserr.Identity
	// ThrottleError is the overlay's dev.understudy#throttleError, the
	// identity a throttling fault rule injects (SQS RequestThrottled). It is
	// the zero Identity on a hand-built descriptor, which then throttles with
	// the generic ThrottlingException.
	ThrottleError awserr.Identity
	// UnknownOperationError returns the overlay's
	// dev.understudy#unknownOperationError for protocol family f, the
	// identity for a request naming no operation of the service, or false
	// for a family the overlay does not name. Generated descriptors set it
	// to their package's UnknownOperationError when the overlay names one;
	// it is nil otherwise, and the edge then answers the family's generic
	// identity.
	UnknownOperationError func(f awserr.Family) (awserr.Identity, bool)
	// UnknownOperationAnswer is the overlay's
	// dev.understudy#unknownOperationAnswer: the form the edge answers an
	// unknown operation in. The zero value is UnknownOperationError's
	// identity in the protocol's error envelope.
	UnknownOperationAnswer UnknownOperationAnswer
	// ModelSource names the vendored model the descriptor was generated
	// from, as the generated file headers do: "api-models-aws@1cfdfb4" and
	// "sqs-2012-11-05.json". It is zero on a hand-built descriptor.
	ModelSource ModelSource
	// Denial is the overlay's dev.understudy#denialError, the error an
	// authorization refusal answers with; zero when the overlay names none.
	Denial Denial
	// NewErrorMembers returns a new, empty value of one of the service's
	// error shapes, to decode a fault rule's members into, or false for a
	// shape the service generates no decoder for. Generated descriptors set
	// it to their package's NewErrorMembers; it is nil on a hand-built
	// descriptor, whose errors then take no members.
	NewErrorMembers func(smithy.ShapeID) (awserr.DecodableMembers, bool)
	// GenericErrors returns the service's generic, protocol-level
	// identities over protocol family f: overlays/common.json's vocabulary
	// with the service overlay's corrections (DynamoDB's
	// AccessDeniedException is 400), nil for a family it does not speak.
	// Generated descriptors set it to their package's GenericErrors; it is
	// nil on a hand-built descriptor, whose fault rules then read
	// awserr.Catalogue.
	GenericErrors func(awserr.Family) []awserr.Identity
	// Operations are every operation, sorted by name; an operation's Ordinal
	// is its index.
	Operations []*Operation
	// Routes is the REST dispatch table, nil for RPC services.
	Routes *RouteTable
}

// Denial is the error a service answers an authorization refusal with, and
// the weakest evidence behind it (iam-enforcement-surfaces.md 12.3).
type Denial struct {
	Identity awserr.Identity
	// Label is the weakest label, in evidence.Labels order, among the overlay
	// evidence items covering dev.understudy#denialError.
	Label evidence.Label
}

// ModelSource is the vendored model a service was generated from.
type ModelSource struct {
	// Origin is the pinned upstream and its short commit:
	// "api-models-aws@1cfdfb4".
	Origin string
	// File is the model file's base name: "sqs-2012-11-05.json".
	File string
}

// String is the source as the generated file headers spell it:
// "api-models-aws@1cfdfb4 sqs-2012-11-05.json". It is empty for the zero
// value.
func (m ModelSource) String() string {
	if m == (ModelSource{}) {
		return ""
	}
	return m.Origin + " " + m.File
}

// QueryErrorEnvelope is how a service's awsQuery ErrorResponse differs from
// Smithy's example, which declares no namespace and sends no Detail
// (error-matrix.md 3). The two differences are independent.
type QueryErrorEnvelope struct {
	// Namespace declares the service's XMLNamespace on ErrorResponse.
	Namespace bool
	// Detail adds an empty <Detail/> after the message.
	Detail bool
}

// UnknownOperationAnswer is the form a service answers a request naming
// none of its operations in, where AWS departs from the identity in the
// protocol's error envelope.
type UnknownOperationAnswer uint8

// UnknownOperationAnswer values.
const (
	// UnknownOperationInEnvelope is the unknown-operation identity in the
	// protocol's error envelope, the service's own namespace included.
	UnknownOperationInEnvelope UnknownOperationAnswer = iota
	// UnknownOperationAWSFault is the awsQuery identity in Coral's AWSFault
	// envelope: xmlns http://webservices.amazon.com/AWSFault/2005-15-09, no
	// Detail, and "Could not find operation <Action> for version <Version>"
	// (SNS and SES v1; error-matrix.md 3, ses.md 3.1).
	UnknownOperationAWSFault
	// UnknownOperationNotFound is Coral's 404 for a REST path no route
	// matches: the body <UnknownOperationException/> and a newline, with no
	// Content-Type (SES v2; ses.md 3.2).
	UnknownOperationNotFound
)

// Operation finds the operation of s named name.
func (s *Service) Operation(name string) (*Operation, bool) {
	i := sort.Search(len(s.Operations), func(i int) bool { return s.Operations[i].ID.Name >= name })
	if i < len(s.Operations) && s.Operations[i].ID.Name == name {
		return s.Operations[i], true
	}
	return nil, false
}

// OperationKind says how an operation is reached.
type OperationKind uint8

// OperationKind values.
const (
	// Modeled is an operation of the model, claimed by the edge.
	Modeled OperationKind = iota + 1
	// Internal is a typed operation an overlay adds with
	// dev.understudy#internalOperation; the edge never claims it and the
	// access recorder skips it.
	Internal
	// Raw is a synthetic operation from rawOperations whose handler owns the
	// HTTP body (function URLs, SNS links), claimed by a prefix or host rule.
	Raw
)

// String returns "modeled", "internal" or "raw".
func (k OperationKind) String() string {
	switch k {
	case Modeled:
		return "modeled"
	case Internal:
		return "internal"
	case Raw:
		return "raw"
	default:
		return fmt.Sprintf("OperationKind(%d)", uint8(k))
	}
}

// Operation describes one operation. It is immutable; bindings live in each
// kernel, indexed by Ordinal.
type Operation struct {
	Service *Service
	ID      ref.OperationID
	Ordinal int
	Kind    OperationKind
	Shape   smithy.ShapeID
	Input   *smithy.Schema
	Output  *smithy.Schema
	// Errors are the modeled and overlay errors, for fault-rule validation.
	Errors []awserr.Identity
	// IAM lists the actions the call needs, the primary action first.
	IAM []IAMAction
	// HTTP is the http trait, set for REST services only.
	HTTP *HTTPBinding
	// AdditionalRoutes are the routes a codegen/services additionalRoutes
	// entry serves the operation at beside HTTP, each also a row of the
	// service's RouteTable (ARCHITECTURE.md 5.3).
	AdditionalRoutes []AdditionalRoute
	// SuccessCode is dev.understudy#successCode, else the http trait's code,
	// else 200.
	SuccessCode int
	// MaxRequestBytes is the overlay's request body cap. 0 means the overlay
	// sets none, and the edge and codec apply the protocol default
	// (ARCHITECTURE.md 4 step 1; awsjson.MaxRequestBytes for awsJson).
	MaxRequestBytes int64
	Readonly        bool
	Idempotent      bool
}

// AdditionalRoute is a route an operation is served at beside its http
// trait: a binding an older model gave it that AWS still answers.
// QueryMembers bind query parameters onto input members the http trait reads
// from the body; the request's other members bind as the http trait binds
// them.
type AdditionalRoute struct {
	Binding      HTTPBinding
	QueryMembers []QueryMember
}

// MatchAdditionalRoute returns the additional route of o a request came
// by: the most specific of o's routes that accepts it, as RouteTable.Match
// picks the row the edge dispatched on, when it is not the http trait. An
// additional route precedes the http trait, so it wins a tie, as in the
// service's RouteTable. path is the escaped request path.
func (o *Operation) MatchAdditionalRoute(method, path string, query url.Values, header http.Header) (*AdditionalRoute, bool) {
	if len(o.AdditionalRoutes) == 0 {
		return nil, false
	}
	segs := pathSegments(path)
	var best *AdditionalRoute
	var bestRow *Route
	for i := range o.AdditionalRoutes {
		row := &Route{HTTPBinding: o.AdditionalRoutes[i].Binding}
		if row.accepts(method, segs, query, header) && (bestRow == nil || CompareSpecificity(row, bestRow) > 0) {
			best, bestRow = &o.AdditionalRoutes[i], row
		}
	}
	if best == nil {
		return nil, false
	}
	if o.HTTP != nil {
		if modeled := (&Route{HTTPBinding: *o.HTTP}); modeled.accepts(method, segs, query, header) && CompareSpecificity(modeled, bestRow) > 0 {
			return nil, false
		}
	}
	return best, true
}

// QueryMember binds the query parameter Param onto the input member Member.
type QueryMember struct {
	Param  string
	Member string
}

// IAMAction is one IAM action, such as sqs:SendMessage.
type IAMAction struct {
	Prefix string // the service's IAM prefix: "sqs"
	Name   string // "SendMessage"
}

// String formats the action as prefix:Name.
func (a IAMAction) String() string { return a.Prefix + ":" + a.Name }
