package ref

import (
	"fmt"
	"net/netip"
)

// ServiceID names a service in the registry: "sqs", "s3", "lambda".
type ServiceID string

// OperationID names one operation of one service.
type OperationID struct {
	Service ServiceID
	Name    string // the model's operation name: "SendMessage"
}

// String formats the id as service.Operation.
func (o OperationID) String() string { return string(o.Service) + "." + o.Name }

// RuleID names a fault injection rule.
type RuleID string

// EventID numbers a journal event; 0 is no event.
type EventID uint64

// Cause links a journal event to its causal tree.
type Cause struct {
	Parent EventID // 0 for a root event
	Trace  EventID // the root of the causal tree
}

// ProtocolID names a wire protocol; codecs register under it.
type ProtocolID string

// The protocols understudy speaks. Raw is a handler that owns the HTTP body
// (function URLs, SNS links).
const (
	AWSJSON10 ProtocolID = "awsjson1_0"
	AWSJSON11 ProtocolID = "awsjson1_1"
	AWSQuery  ProtocolID = "awsquery"
	RESTJSON1 ProtocolID = "restjson1"
	RESTXML   ProtocolID = "restxml"
	RPCv2CBOR ProtocolID = "rpcv2cbor"
	Raw       ProtocolID = "raw"
)

// ListenerKind says which listener accepted a request.
type ListenerKind uint8

// ListenerKind values.
const (
	ListenerMain      ListenerKind = iota + 1 // the configured listen address
	ListenerContainer                         // the listener Lambda containers call
)

// String returns "main" or "container".
func (k ListenerKind) String() string {
	switch k {
	case ListenerMain:
		return "main"
	case ListenerContainer:
		return "container"
	default:
		return fmt.Sprintf("ListenerKind(%d)", uint8(k))
	}
}

// ListenerKindError reports text that names no listener kind.
type ListenerKindError struct {
	Text string
}

func (e *ListenerKindError) Error() string {
	return fmt.Sprintf("%q names no listener kind; known names are main and container", e.Text)
}

// ParseListenerKind returns the ListenerKind whose name is s.
func ParseListenerKind(s string) (ListenerKind, error) {
	for k := ListenerMain; k.valid(); k++ {
		if k.String() == s {
			return k, nil
		}
	}
	return 0, &ListenerKindError{Text: s}
}

// MarshalText encodes the name.
func (k ListenerKind) MarshalText() ([]byte, error) {
	if !k.valid() {
		return nil, &ListenerKindError{Text: k.String()}
	}
	return []byte(k.String()), nil
}

// UnmarshalText decodes a name.
func (k *ListenerKind) UnmarshalText(text []byte) error {
	parsed, err := ParseListenerKind(string(text))
	if err != nil {
		return err
	}
	*k = parsed
	return nil
}

// valid reports whether k is one of the declared values.
func (k ListenerKind) valid() bool { return k >= ListenerMain && k <= ListenerContainer }

// Origin says who started a call.
//
//sumtype:decl
type Origin interface{ isOrigin() }

// External is a request that arrived over HTTP.
type External struct {
	Protocol   ProtocolID
	Listener   ListenerKind
	RemoteAddr netip.AddrPort
	UserAgent  string
}

// Internal is a call the emulator made itself.
type Internal struct{ Via Via }

func (External) isOrigin() {}
func (Internal) isOrigin() {}
