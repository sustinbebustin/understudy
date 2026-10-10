package registry

import (
	"fmt"
	"sort"

	"github.com/sustinbebustin/understudy/internal/awserr"
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
	// MalformedRequestError is the overlay's dev.understudy#malformedRequestError,
	// the identity for a body that does not decode (Lambda
	// InvalidRequestContentException). It is the zero Identity when the
	// overlay sets none; the codec then answers with its protocol's default.
	MalformedRequestError awserr.Identity
	// Operations are every operation, sorted by name; an operation's Ordinal
	// is its index.
	Operations []*Operation
	// Routes is the REST dispatch table, nil for RPC services.
	Routes *RouteTable
}

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

// IAMAction is one IAM action, such as sqs:SendMessage.
type IAMAction struct {
	Prefix string // the service's IAM prefix: "sqs"
	Name   string // "SendMessage"
}

// String formats the action as prefix:Name.
func (a IAMAction) String() string { return a.Prefix + ":" + a.Name }
