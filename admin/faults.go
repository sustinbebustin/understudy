package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// FaultRule is one fault-injection rule in the JSON form the admin API, the
// config file and snapshots share (ARCHITECTURE.md 11):
//
//	{"id": "orders-throttle", "phase": "request",
//	 "match": {"operations": ["sqs.SendMessage"], "resource": "arn:aws:sqs:*:*:orders-*", "origin": "internal"},
//	 "action": {"kind": "throttle"},
//	 "trigger": {"kind": "next-n", "n": 3}}
type FaultRule struct {
	// ID names the rule: 1 to 64 letters, digits, '.', '_' and '-'.
	ID      string     `json:"id,omitempty"`
	Phase   FaultPhase `json:"phase"`
	Match   FaultMatch `json:"match,omitzero"`
	Action  FaultAction
	Trigger FaultTrigger
}

// FaultPhase is the hook point a rule runs at.
type FaultPhase string

// FaultPhase values.
const (
	// RequestPhase runs for every call, internal ones included.
	RequestPhase FaultPhase = "request"
	// DeliveryPhase runs in the delivery engine, once per attempt.
	DeliveryPhase FaultPhase = "delivery"
)

// FaultOrigin selects calls by where they came from; empty means any.
type FaultOrigin string

// FaultOrigin values.
const (
	AnyOrigin      FaultOrigin = "any"
	ExternalOrigin FaultOrigin = "external"
	InternalOrigin FaultOrigin = "internal"
)

// FaultMatch selects calls; every empty dimension matches anything.
type FaultMatch struct {
	Services []string `json:"services,omitempty"`
	// Operations are "service.Operation", such as "sqs.SendMessage".
	Operations []string `json:"operations,omitempty"`
	// Mechanisms are delivery paths, such as "sns-to-sqs".
	Mechanisms []string `json:"mechanisms,omitempty"`
	// Resource is an ARN glob over the ARNs the call touches.
	Resource string      `json:"resource,omitempty"`
	Accounts []string    `json:"accounts,omitempty"`
	Regions  []string    `json:"regions,omitempty"`
	Origin   FaultOrigin `json:"origin,omitempty"`
}

// FaultAction is what a rule does to a matched call. Its JSON form is
// {"kind": "<kind>", ...fields}.
//
//sumtype:decl
type FaultAction interface {
	isFaultAction()
}

// InjectError fails the call before the handler with Shape, one of the
// errors the matched operations really send; an empty Message names the
// rule. Members makes it not comparable: compare two with
// reflect.DeepEqual, or their Shape and Message with == and their Members
// with bytes.Equal, never with == or through FaultAction values.
type InjectError struct {
	Shape   string
	Message string
	// Members sets members of the error beyond its message: a JSON object
	// of the shape's members as awsJson writes them, names as modeled,
	// such as {"CancellationReasons": [{"Code": "TransactionConflict"}]}
	// for DynamoDB's TransactionCanceledException. Empty or JSON null sets
	// none.
	Members json.RawMessage
}

// Throttle fails the call with the service's own throttling error.
type Throttle struct{}

// Latency delays the call by Wall of wall time.
type Latency struct{ Wall time.Duration }

// AfterCommit lets the call's side effect happen, then answers with Shape.
type AfterCommit struct{ Shape string }

// StatusOKError answers 200 with Shape in the body, as S3 does for
// CompleteMultipartUpload, CopyObject and UploadPartCopy.
type StatusOKError struct{ Shape string }

// DropDelivery discards a delivery attempt.
type DropDelivery struct{}

// HTTPStatus answers an HTTP delivery attempt with Status, 100 to 599,
// without contacting the endpoint. Delivery phase only; the rule selects
// the attempts with Mechanisms "sns-to-http".
type HTTPStatus struct{ Status int }

// HTTPTimeout ends an HTTP delivery attempt as a timed-out exchange
// without contacting the endpoint or waiting. Its retry is scheduled from
// the end of the 15-second deadline it stands for, which is later than the
// attempt only on a running or scaled clock. Delivery phase only, as
// HTTPStatus.
type HTTPTimeout struct{}

func (InjectError) isFaultAction()   {}
func (Throttle) isFaultAction()      {}
func (Latency) isFaultAction()       {}
func (AfterCommit) isFaultAction()   {}
func (StatusOKError) isFaultAction() {}
func (DropDelivery) isFaultAction()  {}
func (HTTPStatus) isFaultAction()    {}
func (HTTPTimeout) isFaultAction()   {}

// FaultTrigger says which matched calls a rule fires on. Its JSON form is
// {"kind": "always"}, {"kind": "next-n", "n": 3} or {"kind": "probability",
// "p": 0.1}.
//
//sumtype:decl
type FaultTrigger interface {
	isFaultTrigger()
}

// Always fires on every matched call.
type Always struct{}

// NextN fires on the next N matched calls, then the rule is removed.
type NextN struct{ N int }

// Probability fires on each matched call with probability P.
type Probability struct{ P float64 }

func (Always) isFaultTrigger()      {}
func (NextN) isFaultTrigger()       {}
func (Probability) isFaultTrigger() {}

// Fault is an active rule as GET /faults and PUT /faults/{id} return it:
// the rule, the hits it has taken and, when the rule injects an error the
// AWS SDKs retry, a warning that each retry counts as a hit.
type Fault struct {
	Rule    FaultRule `json:"rule"`
	Hits    int       `json:"hits"`
	Warning string    `json:"warning,omitempty"`
}

// Action and trigger kind names.
const (
	kindError         = "error"
	kindThrottle      = "throttle"
	kindLatency       = "latency"
	kindAfterCommit   = "after-commit"
	kindStatusOKError = "status-ok-error"
	kindDropDelivery  = "drop-delivery"
	kindHTTPStatus    = "http-status"
	kindHTTPTimeout   = "http-timeout"
	kindAlways        = "always"
	kindNextN         = "next-n"
	kindProbability   = "probability"
)

type faultRuleJSON struct {
	ID      string          `json:"id,omitempty"`
	Phase   FaultPhase      `json:"phase"`
	Match   FaultMatch      `json:"match,omitzero"`
	Action  json.RawMessage `json:"action"`
	Trigger json.RawMessage `json:"trigger"`
}

// actionJSON holds every action member; a nil member was absent, so a
// member its kind does not take is refused even when empty.
type actionJSON struct {
	Kind    string          `json:"kind"`
	Shape   *string         `json:"shape,omitempty"`
	Message *string         `json:"message,omitempty"`
	Wall    *string         `json:"wall,omitempty"`
	Members json.RawMessage `json:"members,omitempty"`
	// Status is read by decodeStatus, so a status that is no integer is
	// refused at action.status.
	Status json.RawMessage `json:"status,omitempty"`
}

type triggerJSON struct {
	Kind string   `json:"kind"`
	N    *int     `json:"n,omitempty"`
	P    *float64 `json:"p,omitempty"`
}

// MarshalJSON writes the rule's JSON form.
func (r FaultRule) MarshalJSON() ([]byte, error) {
	action, err := encodeAction(r.Action)
	if err != nil {
		return nil, err
	}
	trigger, err := encodeTrigger(r.Trigger)
	if err != nil {
		return nil, err
	}
	return json.Marshal(faultRuleJSON{ID: r.ID, Phase: r.Phase, Match: r.Match, Action: action, Trigger: trigger})
}

// UnmarshalJSON reads the rule's JSON form strictly: an unknown member or
// kind, or a member the action's or trigger's kind does not take, is an
// error naming it. The server validates the rule further.
func (r *FaultRule) UnmarshalJSON(data []byte) error {
	var w faultRuleJSON
	if err := decodeStrict(data, &w); err != nil {
		return fmt.Errorf("admin: fault rule: %w", err)
	}
	action, err := decodeAction(w.Action)
	if err != nil {
		return err
	}
	trigger, err := decodeTrigger(w.Trigger)
	if err != nil {
		return err
	}
	*r = FaultRule{ID: w.ID, Phase: w.Phase, Match: w.Match, Action: action, Trigger: trigger}
	return nil
}

// FaultFormError reports a fault rule whose action or trigger is missing,
// has an unknown kind, or carries a member its kind does not take.
type FaultFormError struct {
	Field  string
	Reason string
}

func (e *FaultFormError) Error() string {
	return "admin: fault rule " + e.Field + ": " + e.Reason
}

func encodeAction(a FaultAction) ([]byte, error) {
	var w actionJSON
	switch a := a.(type) {
	case InjectError:
		members, err := encodeMembers(a.Members)
		if err != nil {
			return nil, err
		}
		w = actionJSON{Kind: kindError, Shape: nonEmpty(a.Shape), Message: nonEmpty(a.Message), Members: members}
	case Throttle:
		w = actionJSON{Kind: kindThrottle}
	case Latency:
		wall := a.Wall.String()
		w = actionJSON{Kind: kindLatency, Wall: &wall}
	case AfterCommit:
		w = actionJSON{Kind: kindAfterCommit, Shape: nonEmpty(a.Shape)}
	case StatusOKError:
		w = actionJSON{Kind: kindStatusOKError, Shape: nonEmpty(a.Shape)}
	case DropDelivery:
		w = actionJSON{Kind: kindDropDelivery}
	case HTTPStatus:
		w = actionJSON{Kind: kindHTTPStatus, Status: json.RawMessage(strconv.Itoa(a.Status))}
	case HTTPTimeout:
		w = actionJSON{Kind: kindHTTPTimeout}
	case nil:
		return nil, &FaultFormError{Field: "action", Reason: "is missing; set an action such as admin.Throttle{}"}
	}
	return json.Marshal(w)
}

func encodeTrigger(t FaultTrigger) ([]byte, error) {
	var w triggerJSON
	switch t := t.(type) {
	case Always:
		w = triggerJSON{Kind: kindAlways}
	case NextN:
		w = triggerJSON{Kind: kindNextN, N: &t.N}
	case Probability:
		w = triggerJSON{Kind: kindProbability, P: &t.P}
	case nil:
		return nil, &FaultFormError{Field: "trigger", Reason: "is missing; set a trigger such as admin.Always{}"}
	}
	return json.Marshal(w)
}

func decodeAction(data json.RawMessage) (FaultAction, error) {
	if len(data) == 0 {
		return nil, &FaultFormError{Field: "action", Reason: `is missing; add {"kind": "throttle"} or another action`}
	}
	var w actionJSON
	if err := decodeStrict(data, &w); err != nil {
		return nil, &FaultFormError{Field: "action", Reason: err.Error()}
	}
	takes, known := actionTakes(w.Kind)
	if !known {
		return nil, unknownAction(w.Kind)
	}
	present := []member{
		{"shape", w.Shape != nil}, {"message", w.Message != nil}, {"wall", w.Wall != nil}, {"members", w.Members != nil}, {"status", w.Status != nil},
	}
	if err := onlyMembers("action", w.Kind, present, takes); err != nil {
		return nil, err
	}
	switch w.Kind {
	case kindError:
		members, err := decodeMembers(w.Members)
		if err != nil {
			return nil, err
		}
		return InjectError{Shape: valueOf(w.Shape), Message: valueOf(w.Message), Members: members}, nil
	case kindLatency:
		wall, err := time.ParseDuration(valueOf(w.Wall))
		if err != nil {
			return nil, &FaultFormError{Field: "action.wall", Reason: fmt.Sprintf("%q is not a duration such as 250ms or 2s", valueOf(w.Wall))}
		}
		return Latency{Wall: wall}, nil
	case kindAfterCommit:
		return AfterCommit{Shape: valueOf(w.Shape)}, nil
	case kindStatusOKError:
		return StatusOKError{Shape: valueOf(w.Shape)}, nil
	case kindDropDelivery:
		return DropDelivery{}, nil
	case kindHTTPStatus:
		status, err := decodeStatus(w.Status)
		if err != nil {
			return nil, err
		}
		return HTTPStatus{Status: status}, nil
	case kindHTTPTimeout:
		return HTTPTimeout{}, nil
	case kindThrottle:
		return Throttle{}, nil
	}
	return nil, unknownAction(w.Kind)
}

func unknownAction(kind string) *FaultFormError {
	return &FaultFormError{Field: "action.kind", Reason: fmt.Sprintf("%q is not an action; the kinds are error, throttle, latency, after-commit, status-ok-error, drop-delivery, http-status and http-timeout", kind)}
}

// actionTakes returns the members an action of kind takes besides kind,
// and false for a kind that is no action.
func actionTakes(kind string) ([]string, bool) {
	switch kind {
	case kindError:
		return []string{"shape", "message", "members"}, true
	case kindLatency:
		return []string{"wall"}, true
	case kindAfterCommit, kindStatusOKError:
		return []string{"shape"}, true
	case kindHTTPStatus:
		return []string{"status"}, true
	case kindThrottle, kindDropDelivery, kindHTTPTimeout:
		return nil, true
	default:
		return nil, false
	}
}

// decodeStatus reads an http-status action's status: a JSON integer, or 0
// when absent, which the server refuses with the range it takes.
func decodeStatus(raw json.RawMessage) (int, error) {
	if raw == nil {
		return 0, nil
	}
	var status int
	if err := json.Unmarshal(raw, &status); err != nil {
		return 0, &FaultFormError{Field: "action.status", Reason: "is a JSON " + jsonKind(raw) + "; expected an integer HTTP status, 100 to 599"}
	}
	return status, nil
}

func decodeTrigger(data json.RawMessage) (FaultTrigger, error) {
	if len(data) == 0 {
		return nil, &FaultFormError{Field: "trigger", Reason: `is missing; add {"kind": "always"} or another trigger`}
	}
	var w triggerJSON
	if err := decodeStrict(data, &w); err != nil {
		return nil, &FaultFormError{Field: "trigger", Reason: err.Error()}
	}
	only := func(takes ...string) error {
		return onlyMembers("trigger", w.Kind, []member{{"n", w.N != nil}, {"p", w.P != nil}}, takes)
	}
	switch w.Kind {
	case kindAlways:
		if err := only(); err != nil {
			return nil, err
		}
		return Always{}, nil
	case kindNextN:
		if err := only("n"); err != nil {
			return nil, err
		}
		if w.N == nil {
			return nil, &FaultFormError{Field: "trigger.n", Reason: "is missing; next-n takes a count n"}
		}
		return NextN{N: *w.N}, nil
	case kindProbability:
		if err := only("p"); err != nil {
			return nil, err
		}
		if w.P == nil {
			return nil, &FaultFormError{Field: "trigger.p", Reason: "is missing; probability takes a p between 0 and 1"}
		}
		return Probability{P: *w.P}, nil
	default:
		return nil, &FaultFormError{Field: "trigger.kind", Reason: fmt.Sprintf("%q is not a trigger; the kinds are always, next-n and probability", w.Kind)}
	}
}

// encodeMembers checks that an error action's members are one JSON object
// and returns them compact; empty members and null encode as none, as
// decodeMembers reads them.
func encodeMembers(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		return nil, &FaultFormError{Field: "action.members", Reason: "is not a JSON object: " + err.Error()}
	}
	if kind := jsonKind(out.Bytes()); kind != "object" {
		return nil, &FaultFormError{Field: "action.members", Reason: "is not a JSON object: it is a JSON " + kind +
			`; set Members to an object of the error shape's members, such as {"CancellationReasons": [{"Code": "TransactionConflict"}]}`}
	}
	return out.Bytes(), nil
}

// decodeMembers reads an error action's members, compact; null reads as
// none, and any other value than an object is refused.
func decodeMembers(raw json.RawMessage) (json.RawMessage, error) {
	if raw == nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	if kind := jsonKind(raw); kind != "object" {
		return nil, &FaultFormError{Field: "action.members", Reason: "is a JSON " + kind + "; members is an object of the error shape's members"}
	}
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		return nil, &FaultFormError{Field: "action.members", Reason: "is not valid JSON: " + err.Error()}
	}
	return out.Bytes(), nil
}

// jsonKind names the kind of the valid JSON value raw by its first byte:
// "object", "array", "string", "boolean", "null" or "number".
func jsonKind(raw []byte) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "nothing"
	}
	switch raw[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

// member is one optional member of an action or trigger and whether the
// JSON carried it.
type member struct {
	name    string
	present bool
}

// onlyMembers refuses the first present member that kind does not take, as
// fault.Rule's strict decoding does, so a rule the server would refuse is
// not quietly rewritten by dropping the member.
func onlyMembers(field, kind string, members []member, takes []string) error {
	for _, m := range members {
		if !m.present || slices.Contains(takes, m.name) {
			continue
		}
		accepted := "only kind"
		if len(takes) > 0 {
			accepted = "kind and " + strings.Join(takes, " and ")
		}
		return &FaultFormError{Field: field + "." + m.name, Reason: fmt.Sprintf("is not a member of %s %q, which takes %s", field, kind, accepted)}
	}
	return nil
}

// nonEmpty is s as an optional member: nil when empty.
func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// valueOf is an optional member's value, empty when absent.
func valueOf(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
