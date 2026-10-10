package fault

import (
	"fmt"
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Rule is one declarative fault: which calls it matches, at which hook point,
// what it does to them and when it fires.
type Rule struct {
	// ID names the rule; Store.Add mints one when it is empty.
	ID      ref.RuleID
	Match   Match
	Phase   Phase
	Action  Action
	Trigger Trigger
}

// Match selects calls. Every empty dimension matches anything; a call
// matches when it matches every dimension.
type Match struct {
	Services []ref.ServiceID
	// Operations apply to the Request phase only.
	Operations []ref.OperationID
	// Mechanisms apply to the Delivery phase only.
	Mechanisms []delivery.Mechanism
	// Resource matches when any ARN the call touches matches it; a call that
	// touches nothing never matches a non-zero glob.
	Resource ARNGlob
	Accounts []aws.AccountID
	Regions  []aws.Region
	Origin   OriginMatch
}

// matches reports whether the call in is selected by every dimension.
func (m Match) matches(in MatchInput) bool {
	return within(m.Services, in.Service) &&
		within(m.Operations, in.Op) &&
		within(m.Mechanisms, in.Mechanism) &&
		within(m.Accounts, in.Scope.Account) &&
		within(m.Regions, in.Scope.Region) &&
		m.Origin.matches(in.Origin) &&
		m.touches(in.Touched)
}

// clone returns m with its own copies of the dimension slices, so a rule the
// store validated and holds cannot change underneath it.
func (m Match) clone() Match {
	m.Services = slices.Clone(m.Services)
	m.Operations = slices.Clone(m.Operations)
	m.Mechanisms = slices.Clone(m.Mechanisms)
	m.Accounts = slices.Clone(m.Accounts)
	m.Regions = slices.Clone(m.Regions)
	return m
}

func (m Match) touches(arns []aws.ARN) bool {
	if m.Resource.IsZero() {
		return true
	}
	return slices.ContainsFunc(arns, func(a aws.ARN) bool { return m.Resource.Match(a.String()) })
}

// within reports whether v is in set; the empty set holds everything.
func within[T comparable](set []T, v T) bool {
	return len(set) == 0 || slices.Contains(set, v)
}

// Phase is the hook point a rule runs at.
type Phase uint8

// Phase values.
const (
	// Request runs for every call, internal ones included: pipeline step 8
	// before the handler and step 10 after it, and the DynamoDB proxy before
	// forwarding.
	Request Phase = iota + 1
	// Delivery runs in the delivery engine, once per attempt.
	Delivery
)

// String returns the wire name, "request" or "delivery".
func (p Phase) String() string {
	switch p {
	case Request:
		return "request"
	case Delivery:
		return "delivery"
	default:
		return fmt.Sprintf("Phase(%d)", uint8(p))
	}
}

// MarshalText encodes the wire name.
func (p Phase) MarshalText() ([]byte, error) {
	if !p.valid() {
		return nil, &EnumError{Enum: "phase", Text: p.String(), Known: "request and delivery"}
	}
	return []byte(p.String()), nil
}

// UnmarshalText decodes a wire name.
func (p *Phase) UnmarshalText(text []byte) error {
	for c := Request; c.valid(); c++ {
		if c.String() == string(text) {
			*p = c
			return nil
		}
	}
	return &EnumError{Enum: "phase", Text: string(text), Known: "request and delivery"}
}

func (p Phase) valid() bool { return p == Request || p == Delivery }

// OriginMatch selects calls by where they came from. Calls with Origin
// Internal{Via: Snapshot} never match any rule, whatever the OriginMatch.
type OriginMatch uint8

// OriginMatch values. The zero value is Any, so an empty Match matches every
// origin.
const (
	Any          OriginMatch = iota // external and internal calls
	ExternalOnly                    // requests that arrived over HTTP
	InternalOnly                    // calls the emulator made itself
)

// String returns the wire name: "any", "external" or "internal".
func (o OriginMatch) String() string {
	switch o {
	case Any:
		return "any"
	case ExternalOnly:
		return "external"
	case InternalOnly:
		return "internal"
	default:
		return fmt.Sprintf("OriginMatch(%d)", uint8(o))
	}
}

// MarshalText encodes the wire name.
func (o OriginMatch) MarshalText() ([]byte, error) {
	if !o.valid() {
		return nil, &EnumError{Enum: "origin", Text: o.String(), Known: "any, external and internal"}
	}
	return []byte(o.String()), nil
}

// UnmarshalText decodes a wire name.
func (o *OriginMatch) UnmarshalText(text []byte) error {
	for c := Any; c.valid(); c++ {
		if c.String() == string(text) {
			*o = c
			return nil
		}
	}
	return &EnumError{Enum: "origin", Text: string(text), Known: "any, external and internal"}
}

func (o OriginMatch) valid() bool { return o <= InternalOnly }

// matches reports whether a call from origin o is selected.
func (o OriginMatch) matches(origin ref.Origin) bool {
	_, external := origin.(ref.External)
	_, internal := origin.(ref.Internal)
	switch o {
	case Any:
		return true
	case ExternalOnly:
		return external
	case InternalOnly:
		return internal
	default:
		return false
	}
}

// EnumError reports text that names no value of one of the package's
// enumerations.
type EnumError struct {
	Enum  string // "phase" or "origin"
	Text  string
	Known string
}

// Error names the rejected text and lists the known names.
func (e *EnumError) Error() string {
	return fmt.Sprintf("fault: %q names no %s; known names are %s", e.Text, e.Enum, e.Known)
}

// Action is what a rule does to a matched call.
//
//sumtype:decl
type Action interface{ isAction() }

// Error fails the call before the handler with Shape, which must be one of
// the errors the matched operations really send.
type Error struct {
	Shape smithy.ShapeID `json:"shape"`
	// Message is the error text; empty means a message naming the rule. A
	// shape with no message member takes none, and Add rejects one.
	Message string `json:"message,omitempty"`
}

// Throttle fails the call before the handler with the service's own
// throttling error (the overlay's throttleError).
type Throttle struct{}

// Latency delays the call before the handler by Wall of wall time, so a
// frozen emulator clock cannot hang it. Its JSON form is in json.go.
type Latency struct{ Wall time.Duration }

// AfterCommit lets the handler's side effect happen, then sends the client
// Shape instead of the result.
type AfterCommit struct {
	Shape smithy.ShapeID `json:"shape"`
}

// StatusOKError sends Shape in a 200 response after the handler ran, as S3
// does for CompleteMultipartUpload, CopyObject and UploadPartCopy.
type StatusOKError struct {
	Shape smithy.ShapeID `json:"shape"`
}

// DropDelivery discards a delivery attempt: the target never receives it.
type DropDelivery struct{}

func (Error) isAction()         {}
func (Throttle) isAction()      {}
func (Latency) isAction()       {}
func (AfterCommit) isAction()   {}
func (StatusOKError) isAction() {}
func (DropDelivery) isAction()  {}

// Trigger says which matched calls a rule fires on.
//
//sumtype:decl
type Trigger interface{ isTrigger() }

// Always fires on every matched call and never expires.
type Always struct{}

// NextN fires on the next N matched calls, then the rule is removed. Every
// SDK retry is a call, so it counts as a hit.
type NextN struct {
	N int `json:"n"`
}

// Probability fires on each matched call with probability P, drawn from the
// rule's own seeded stream.
type Probability struct {
	P float64 `json:"p"`
}

func (Always) isTrigger()      {}
func (NextN) isTrigger()       {}
func (Probability) isTrigger() {}
