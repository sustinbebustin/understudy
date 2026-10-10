package smithy

import "github.com/sustinbebustin/understudy/internal/protocol/smithy/document"

// Schema describes one shape for the codecs. Generated packages hold one
// immutable Schema per shape; nothing changes it after generation.
type Schema struct {
	ID   ShapeID
	Type ShapeType
	// Members are the members of a structure or union in model order. A list
	// has one member, "member"; a map has two, "key" then "value".
	Members []*Member
	// XMLName renames the root element of a structure (S3's
	// ListObjectsV2Output is ListBucketResult); members targeting the shape
	// keep their own names.
	XMLName      string
	XMLNamespace *XMLNamespace
	// TimestampFormat is the timestampFormat trait on a timestamp shape.
	TimestampFormat TimestampFormat
	MediaType       string // mediaType on a string or blob shape
	Sensitive       bool   // never written to logs or the journal
	Streaming       bool   // a streaming blob (Body) or event stream union
	Sparse          bool   // a list or map whose values may be null
	// Error is set on error structures.
	Error *ErrorTrait
}

// Member describes one member of a structure, union, list or map. Its traits
// are the effective traits: the member's own merged over its target's.
type Member struct {
	Name   string
	Index  int // position in the parent's Members
	Target *Schema
	// Required is the required trait; servers honor it even when the member
	// is also clientOptional.
	Required bool
	// Default is the default trait value, or nil when the member has none.
	// document.Null is an explicit null default, which removes a default the
	// target shape declares.
	Default         document.Value
	JSONName        string
	XMLName         string
	XMLFlattened    bool
	XMLAttribute    bool
	XMLNamespace    *XMLNamespace
	TimestampFormat TimestampFormat
	Sensitive       bool
	// HTTP is the member's HTTP binding trait, or nil when it has none; an
	// unbound top-level member of a REST operation travels in the body.
	HTTP Location
}

// XMLNamespace is the xmlNamespace trait.
type XMLNamespace struct {
	URI    string
	Prefix string // empty for the default namespace
}

// TimestampFormat is the timestampFormat trait.
type TimestampFormat uint8

// TimestampFormat values. TimestampProtocolDefault means the trait is absent
// and the protocol's default for the member's location applies.
const (
	TimestampProtocolDefault TimestampFormat = iota
	DateTime                                 // RFC 3339, 2019-12-16T23:48:18Z
	HTTPDate                                 // IMF-fixdate, Tue, 29 Apr 2014 18:30:38 GMT
	EpochSeconds                             // seconds since the epoch, fractional milliseconds allowed
)

// ErrorTrait is the error and httpError traits of an error structure.
type ErrorTrait struct {
	Fault ErrorFault
	// HTTPError is the httpError status, or 0 when the shape has none and the
	// fault's default (400 client, 500 server) applies.
	HTTPError int
}

// ErrorFault is the value of the error trait.
type ErrorFault uint8

// ErrorFault values.
const (
	ClientFault ErrorFault = iota + 1 // "client": the caller's mistake
	ServerFault                       // "server": the service's failure
)

// Location is where an HTTP binding trait puts a top-level member.
//
//sumtype:decl
type Location interface{ isLocation() }

// HTTPLabel binds the member to the URI label of the same name.
type HTTPLabel struct{}

// HTTPQuery binds the member to the query parameter Name.
type HTTPQuery struct{ Name string }

// HTTPQueryParams binds a map member to every query parameter no HTTPQuery
// member claims.
type HTTPQueryParams struct{}

// HTTPHeader binds the member to the header Name.
type HTTPHeader struct {
	Name string
	// Trailer is set when the value may instead arrive as an aws-chunked
	// trailer (S3 x-amz-checksum-*), so it is known only after the body EOF.
	Trailer bool
}

// HTTPPrefixHeaders binds a map member to every header starting with Prefix.
type HTTPPrefixHeaders struct{ Prefix string }

// HTTPPayload binds the member to the whole body.
type HTTPPayload struct{}

// HTTPResponseCode binds an output member to the response status.
type HTTPResponseCode struct{}

func (HTTPLabel) isLocation()         {}
func (HTTPQuery) isLocation()         {}
func (HTTPQueryParams) isLocation()   {}
func (HTTPHeader) isLocation()        {}
func (HTTPPrefixHeaders) isLocation() {}
func (HTTPPayload) isLocation()       {}
func (HTTPResponseCode) isLocation()  {}
