package admin

import (
	"encoding/json"
	"net/url"
	"strconv"
	"time"
)

// JournalEvent is one journal record in its NDJSON form: the envelope every
// kind shares, with the kind-specific payload in Detail as the server wrote
// it (ARCHITECTURE.md 10).
type JournalEvent struct {
	ID    uint64   `json:"id"`
	Cause Cause    `json:"cause"`
	Links []uint64 `json:"links,omitempty"`
	// At is the event's emulator time; Wall the wall time it was recorded.
	At    time.Time `json:"at"`
	Wall  time.Time `json:"wall"`
	Scope Scope     `json:"scope"`
	// Kind names the variant of Detail: "api-call", "delivery",
	// "invocation", "timer-fired", "state-change", "fault-injected",
	// "clock-changed", "advisory", "unclaimed" or "gap".
	Kind   string          `json:"kind"`
	Detail json.RawMessage `json:"detail"`
}

// Cause is an event's parent and the root of its causal tree; zero means
// none.
type Cause struct {
	Parent uint64 `json:"parent,omitempty"`
	Trace  uint64 `json:"trace,omitempty"`
}

// Scope is where an event happened.
type Scope struct {
	Partition string `json:"partition"`
	Account   string `json:"account"`
	Region    string `json:"region"`
}

// JournalQuery selects journal events; zero fields select everything.
type JournalQuery struct {
	// Trace selects the events of one causal tree by its root event id.
	Trace uint64
	// RequestID selects the API call with this request id, the key an SDK
	// error or a log line carries.
	RequestID string
	Service   string
	// Operation is "service.Operation", such as "sqs.SendMessage".
	Operation string
	// Resource is an ARN glob; * matches any run of characters.
	Resource string
	// Kind is an event kind, such as "api-call".
	Kind string
	// Outcome is "ok", "error" or "pending".
	Outcome   string
	Principal string
	// From and To bound emulator time, [From, To); zero is open.
	From, To time.Time
	// FromID and ToID bound event ids, [FromID, ToID); zero is open.
	FromID, ToID uint64
	// Limit is the page size of a query; 0 means the server's default.
	// Streams ignore it.
	Limit int
	// Cursor is JournalPage.Next of the previous page. Streams ignore it.
	Cursor uint64
}

// JournalPage is one page of GET /journal/events, in ascending id order.
type JournalPage struct {
	Events []JournalEvent `json:"events"`
	// Next is the Cursor of the next page, or 0 when there is none.
	Next uint64 `json:"next,omitempty"`
}

// Query parameter names of the journal endpoints, shared by the client and
// the server's handlers. ParamAfter is the stream's resume point and
// ParamCanonical asks a trace for its normalized form; the rest are
// JournalQuery's members.
const (
	ParamTrace     = "trace"
	ParamRequestID = "request_id"
	ParamService   = "service"
	ParamOperation = "operation"
	ParamResource  = "resource"
	ParamKind      = "kind"
	ParamOutcome   = "outcome"
	ParamPrincipal = "principal"
	ParamFrom      = "from"
	ParamTo        = "to"
	ParamFromID    = "from_id"
	ParamToID      = "to_id"
	ParamLimit     = "limit"
	ParamCursor    = "cursor"
	ParamAfter     = "after"
	ParamCanonical = "canonical"
)

// Values encodes q as the query parameters the journal endpoints read.
func (q JournalQuery) Values() url.Values {
	v := url.Values{}
	set := func(k, s string) {
		if s != "" {
			v.Set(k, s)
		}
	}
	setID := func(k string, n uint64) {
		if n != 0 {
			v.Set(k, strconv.FormatUint(n, 10))
		}
	}
	setTime := func(k string, t time.Time) {
		if !t.IsZero() {
			v.Set(k, t.UTC().Format(time.RFC3339Nano))
		}
	}
	setID(ParamTrace, q.Trace)
	set(ParamRequestID, q.RequestID)
	set(ParamService, q.Service)
	set(ParamOperation, q.Operation)
	set(ParamResource, q.Resource)
	set(ParamKind, q.Kind)
	set(ParamOutcome, q.Outcome)
	set(ParamPrincipal, q.Principal)
	setTime(ParamFrom, q.From)
	setTime(ParamTo, q.To)
	setID(ParamFromID, q.FromID)
	setID(ParamToID, q.ToID)
	if q.Limit != 0 {
		v.Set(ParamLimit, strconv.Itoa(q.Limit))
	}
	setID(ParamCursor, q.Cursor)
	return v
}

// Trace is a causal tree: an event and the events it caused, children in id
// order.
type Trace struct {
	Event    JournalEvent `json:"event"`
	Children []Trace      `json:"children,omitempty"`
}

// CanonicalTrace is the normalized tree two runs of one workload agree on
// (ARCHITECTURE.md 12): ids replaced by tree positions, wall-clock values
// dropped, children sorted by kind, operation and resource.
type CanonicalTrace struct {
	// Position is the path from the root: "0", "0.2".
	Position string       `json:"position"`
	Event    JournalEvent `json:"event"`
	// Links are the positions of linked events, "-" for one outside the
	// tree.
	Links    []string         `json:"links,omitempty"`
	Children []CanonicalTrace `json:"children,omitempty"`
}

// TraceKey names the tree to fetch: by an event id in it, or by the request
// id of an API call in it.
//
//sumtype:decl
type TraceKey interface {
	isTraceKey()
}

// ByEvent names the tree holding an event id, such as a trace's root.
type ByEvent struct{ ID uint64 }

// ByRequestID names the tree holding the API call with a request id.
type ByRequestID struct{ RequestID string }

func (ByEvent) isTraceKey()     {}
func (ByRequestID) isTraceKey() {}
