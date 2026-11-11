package codegen

import (
	"encoding/json"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// ServiceIR is the typed intermediate representation of one service: the
// emitters read nothing else.
type ServiceIR struct {
	ID        ref.ServiceID
	Package   string
	ModelFile string // base name of the model file, for the header
	Origin    string // the pinned upstream, for the header

	Shape            smithy.ShapeID
	SigningName      string
	IAMPrefix        string
	ARNNamespace     string
	Protocols        []ref.ProtocolID
	JSONTargetPrefix string
	QueryVersion     string
	XMLNamespace     string
	QueryCompatible  bool
	// ResponseCRC32 is dev.understudy#responseCrc32: every encoded answer
	// carries x-amz-crc32.
	ResponseCRC32 bool
	// XMLDeclaration is dev.understudy#xmlDeclaration: every awsQuery body
	// opens with the XML declaration.
	XMLDeclaration bool
	// RefuseDuplicateMembers is dev.understudy#refuseDuplicateMembers: a
	// request body repeating a member name is refused.
	RefuseDuplicateMembers bool
	// QueryErrorEnvelope is dev.understudy#queryErrorEnvelope, the zero
	// value without one.
	QueryErrorEnvelope registry.QueryErrorEnvelope
	// UnknownAnswer is dev.understudy#unknownOperationAnswer, the zero
	// value without one.
	UnknownAnswer registry.UnknownOperationAnswer

	Operations []*OperationIR  // sorted by name; Ordinal is the index
	Shapes     []*ShapeIR      // every data shape, sorted by id; synthetic unit IO shapes and per-use streaming IO copies last
	Errors     []*ErrorIR      // sorted by Go name
	Patterns   []*PatternVarIR // the distinct patterns input members check, by rendering
	// Routes is the REST dispatch table, ordered as Match ranks it; empty for
	// RPC services.
	Routes []RouteIR
	// RawBinding adds a <Op>Raw binder method per modeled operation.
	RawBinding bool
	// Wiring is the config's, its bound operations checked against
	// Operations.
	Wiring Wiring
	// SerdeBoth emits both serde directions for every shape: a rawBinding
	// service encodes inputs and decodes outputs for its passthrough, and
	// the protocol compliance harness drives a corpus service both ways.
	SerdeBoth bool
	// Cases are a corpus service's compliance cases, nil for a real
	// service.
	Cases *CasesIR
	// PathStyle is dev.understudy#validationPathStyle: whether a
	// collection's element constraints are reported per element or rolled
	// up at the collection (Lambda).
	PathStyle PathStyle
	// InputRoot is dev.understudy#validationInputRoot: a Coral message
	// names a member not bound to the URI under the input structure,
	// <operation>Input.<member> (API Gateway), and a URI label bare.
	InputRoot bool

	Throttle         *ErrorIR
	UnknownJSON      *ErrorIR
	UnknownQuery     *ErrorIR
	MalformedRequest *ErrorIR
	// Denial is dev.understudy#denialError, nil when the overlay names none.
	Denial *DenialIR
	// Vocabularies are the generic vocabularies of the protocol families
	// the service speaks (overlays/common.json), JSON first.
	Vocabularies []VocabularyIR

	// Validation are the service-level validationError rules, tried in
	// order. A rule with no constraint filter matches every violation, so
	// it is the catch-all and belongs last.
	Validation []RuleIR
}

// DenialIR is the identity an authorization refusal answers with and the
// weakest label of the overlay evidence covering it
// (iam-enforcement-surfaces.md 12.3).
type DenialIR struct {
	Error *ErrorIR
	Label evidence.Label
}

// OperationIR is one operation.
type OperationIR struct {
	Name        string
	ID          smithy.ShapeID
	Ordinal     int
	Kind        registry.OperationKind
	Input       *ShapeIR
	Output      *ShapeIR
	Errors      []*ErrorIR // the operation's then the service's modeled errors in model order, then validation identities
	SuccessCode int
	// MaxRequestBytes is the overlay's dev.understudy#maxRequestBytes, or 0
	// when the overlay sets none and the edge applies the protocol default
	// (ARCHITECTURE.md 4, step 1 admit).
	MaxRequestBytes int64
	Readonly        bool
	Idempotent      bool
	HTTP            *registry.HTTPBinding
	Validation      []RuleIR // operation-level rules
	Paths           []PathIR // every member path of the input, for validation identity selection
	Examples        []ExampleIR
	// IAM are the operation's IAM actions, the primary first.
	IAM []registry.IAMAction
	// NotConfigured is the S3 answer while the operation's subresource is
	// unset (dev.understudy#notConfigured), nil when the overlay sets none.
	NotConfigured NotConfigured
	// Checksum names the request checksum members of an operation with
	// aws.protocols#httpChecksum, nil without the trait.
	Checksum *ChecksumIR
}

// ChecksumIR is an operation's aws.protocols#httpChecksum: the input member
// naming the algorithm. The checksum value headers it selects may arrive as
// aws-chunked trailers.
type ChecksumIR struct {
	AlgorithmMember string
}

// HasIO reports whether the operation has generated input and output types:
// every operation but a raw one, whose handler owns the HTTP exchange.
func (o *OperationIR) HasIO() bool { return o.Kind != registry.Raw }

// ExampleIR is one smithy.api#examples entry whose input the generated test
// validates.
type ExampleIR struct {
	Title string
	Input json.RawMessage
}

// Role says which direction a shape travels.
type Role uint8

// Role bits.
const (
	RoleInput Role = 1 << iota
	RoleOutput
)

// ShapeIR is one data shape.
type ShapeIR struct {
	ID   smithy.ShapeID
	Type smithy.ShapeType
	// GoName is the Go type name of a structure, union, enum or intEnum,
	// and the helper-function stem of a list or map.
	GoName    string
	SchemaVar string
	Members   []*MemberIR
	Enum      []EnumValueIR
	// EnumOrder is dev.understudy#enumOrder: the values in the order an
	// enum violation lists them, nil for the model's order.
	EnumOrder []string

	TimestampFormat smithy.TimestampFormat
	MediaType       string
	Sensitive       bool
	Sparse          bool
	// Streaming is @streaming: a blob streamed as smithy.Body or
	// smithy.BodySource, or an event stream union, framed by its handler
	// and streamed the same way.
	Streaming    bool
	XMLName      string
	XMLNamespace *smithy.XMLNamespace
	Error        *ErrorIR // set on error structures
	Role         Role
	// IO is the operation whose input or output this shape is, renamed to
	// <Op>Input or <Op>Output.
	IO *OperationIR
	// Top says whether the shape is some operation's input, output or both,
	// shared or not: such a type has exported Serialize, Deserialize and
	// Validate methods.
	Top Role
	// Unit marks a synthetic per-operation empty structure for a
	// smithy.api#Unit input or output.
	Unit bool
	// Tracked counts the required members whose presence Deserialize
	// records, so Validate can tell absent from zero.
	Tracked int
}

// IsStruct reports whether the shape is a structure or a unit.
func (s *ShapeIR) IsStruct() bool { return s.Type == smithy.TypeStructure || s.Type == smithy.TypeUnit }

// EnumValueIR is one enum member.
type EnumValueIR struct {
	Name   string // Go constant suffix
	Value  string // wire value (string enums)
	Number int64  // intEnum value
}

// MemberIR is one member with its effective traits (member over target).
type MemberIR struct {
	Name   string
	GoName string
	Index  int
	Var    string // the smithy.Member variable
	Owner  *ShapeIR
	Target *ShapeIR

	Required bool
	Default  json.RawMessage // nil when absent or null
	// DefaultKind says how the Go field holds Default.
	DefaultKind     DefaultKind
	JSONName        string
	XMLName         string
	XMLFlattened    bool
	XMLAttribute    bool
	XMLNamespace    *smithy.XMLNamespace
	TimestampFormat smithy.TimestampFormat
	Sensitive       bool
	HTTP            smithy.Location // nil when the member has no HTTP binding
	// QueryKeyAlias is the overlay's queryKeyAlias, or nil without one.
	QueryKeyAlias *smithy.QueryKeyAlias
	// Streaming marks a member whose target is @streaming: a smithy.Body on
	// input, a smithy.BodySource on output (ARCHITECTURE.md 3.2).
	Streaming bool

	Length     *Bounds
	Range      *Bounds
	Pattern    *PatternIR
	EnumPolicy EnumPolicy

	AlwaysEmit bool
	Empty      EmptyCollection
	// Value: the Go field is a plain value rather than a pointer (scalars and
	// structures only; collections, unions and documents use nil for absent,
	// and a DefaultPointer member is never one).
	Value bool
	// Bit is the presence bit of a tracked required member, else -1.
	Bit   int
	Rules []RuleIR // member-level validationError rules
	// rulesRaw holds the member's validationError until the error
	// identities exist to resolve it against.
	rulesRaw json.RawMessage
}

// DefaultKind is how a structure member's Go field holds its modeled
// default, which the serializer writes for a member the handler leaves
// unset and the deserializer fills in for an absent one
// (smithy-protocol-tests.md 10.4, 14.2 to 14.4).
type DefaultKind uint8

// DefaultKind values.
const (
	// DefaultNone: no default applies. The member models none, models
	// @default(null), is not a structure member, or streams, which keeps a
	// stream as it is.
	DefaultNone DefaultKind = iota
	// DefaultZero: the default is the Go zero value of a plain field
	// (numeric 0, false, "" or intEnum 0), so unset and default agree.
	DefaultZero
	// DefaultPointer: any other scalar default, every string enum and
	// every timestamp default (Go's zero time is not the epoch). The field
	// is a pointer and nil is unset.
	DefaultPointer
	// DefaultNil: a list, map, document, blob or big integer, whose Go type
	// has its own nil for unset.
	DefaultNil
)

// Bounds is a length or range trait, decimal text as the model wrote it.
type Bounds struct {
	Min, Max string // empty when unbounded
}

// PatternIR is a pattern trait after the regex stage.
type PatternIR struct {
	Source    string
	RE2       string // empty when Validator renders it
	Echo      string
	Validator *PatternValidator
	Var       string // the PatternVarIR name of an input member's pattern
}

// PatternVarIR is one distinct rendering input members check, declared once
// as a package variable (or one per validator part) named Name.
type PatternVarIR struct {
	Name      string
	Sources   []string // every distinct model pattern with this rendering, sorted
	RE2       string   // empty when Validator renders it
	Validator *PatternValidator
}

// ErrorIR is an error shape's identity.
type ErrorIR struct {
	Shape *ShapeIR
	// HTTPError is the model's httpError, 0 when the shape has none.
	HTTPError       int
	JSONType        string
	QueryCode       string
	Status          int
	QueryModeStatus int
	QueryErrorWhen  awserr.QueryErrorWhen
	Fault           awserr.Fault
	MessageKey      awserr.MessageKey
	Retryable       bool
	Messages        map[ref.ProtocolID]string
	// Generic marks shapes outside the service namespace (the generic
	// vocabulary of overlays/common.json).
	Generic bool
}

// VocabularyIR is one protocol family's generic vocabulary, in the order
// the family's defaults shape lists it.
type VocabularyIR struct {
	Family awserr.Family
	Errors []*ErrorIR
}

// RuleIR is a resolved validationError rule.
type RuleIR struct {
	Error         *ErrorIR // nil for a rule that only sets a policy
	Style         ValidationStyle
	Kinds         ConstraintKind
	Reason        string
	EnumPolicy    EnumPolicy
	PatternPolicy PatternPolicy
	Echo          awserr.CoralEcho
}

// PathIR is one member path of an operation input: lowerCamel segments with
// "*" for a list index or map key.
type PathIR struct {
	Segments []string
	Param    string
	Rules    []RuleIR
}
