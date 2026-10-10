package admin

import (
	"encoding/json"
	"fmt"
	"slices"
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
// rule.
type InjectError struct {
	Shape   string
	Message string
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

func (InjectError) isFaultAction()   {}
func (Throttle) isFaultAction()      {}
func (Latency) isFaultAction()       {}
func (AfterCommit) isFaultAction()   {}
func (StatusOKError) isFaultAction() {}
func (DropDelivery) isFaultAction()  {}

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
	Kind    string  `json:"kind"`
	Shape   *string `json:"shape,omitempty"`
	Message *string `json:"message,omitempty"`
	Wall    *string `json:"wall,omitempty"`
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
		w = actionJSON{Kind: kindError, Shape: nonEmpty(a.Shape), Message: nonEmpty(a.Message)}
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
	only := func(takes ...string) error {
		return onlyMembers("action", w.Kind, []member{{"shape", w.Shape != nil}, {"message", w.Message != nil}, {"wall", w.Wall != nil}}, takes)
	}
	switch w.Kind {
	case kindError:
		if err := only("shape", "message"); err != nil {
			return nil, err
		}
		return InjectError{Shape: valueOf(w.Shape), Message: valueOf(w.Message)}, nil
	case kindThrottle:
		if err := only(); err != nil {
			return nil, err
		}
		return Throttle{}, nil
	case kindLatency:
		if err := only("wall"); err != nil {
			return nil, err
		}
		wall, err := time.ParseDuration(valueOf(w.Wall))
		if err != nil {
			return nil, &FaultFormError{Field: "action.wall", Reason: fmt.Sprintf("%q is not a duration such as 250ms or 2s", valueOf(w.Wall))}
		}
		return Latency{Wall: wall}, nil
	case kindAfterCommit:
		if err := only("shape"); err != nil {
			return nil, err
		}
		return AfterCommit{Shape: valueOf(w.Shape)}, nil
	case kindStatusOKError:
		if err := only("shape"); err != nil {
			return nil, err
		}
		return StatusOKError{Shape: valueOf(w.Shape)}, nil
	case kindDropDelivery:
		if err := only(); err != nil {
			return nil, err
		}
		return DropDelivery{}, nil
	default:
		return nil, &FaultFormError{Field: "action.kind", Reason: fmt.Sprintf("%q is not an action; the kinds are error, throttle, latency, after-commit, status-ok-error and drop-delivery", w.Kind)}
	}
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
