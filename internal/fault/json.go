package fault

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// The JSON form of a rule, shared by the admin API, the config file and
// snapshots. Action and Trigger follow the jsonx tagged-union convention,
// {"kind": "<variant>", ...fields}; decoding is strict, so an unknown member
// is an error naming it.
//
//	{"id": "orders-throttle", "phase": "request",
//	 "match": {"operations": ["sqs.SendMessage"], "resource": "arn:aws:sqs:*:*:orders-*", "origin": "internal"},
//	 "action": {"kind": "throttle"},
//	 "trigger": {"kind": "next-n", "n": 3}}
type ruleJSON struct {
	ID      ref.RuleID `json:"id,omitempty"`
	Phase   Phase      `json:"phase"`
	Match   matchJSON  `json:"match,omitzero"`
	Action  Action     `json:"action"`
	Trigger Trigger    `json:"trigger"`
}

type matchJSON struct {
	Services   []ref.ServiceID      `json:"services,omitempty"`
	Operations []operationName      `json:"operations,omitempty"`
	Mechanisms []delivery.Mechanism `json:"mechanisms,omitempty"`
	Resource   ARNGlob              `json:"resource,omitzero"`
	Accounts   []aws.AccountID      `json:"accounts,omitempty"`
	Regions    []aws.Region         `json:"regions,omitempty"`
	Origin     OriginMatch          `json:"origin,omitzero"`
}

// operationName is an OperationID in its "service.Operation" text form.
type operationName ref.OperationID

func (o operationName) MarshalText() ([]byte, error) {
	return []byte(ref.OperationID(o).String()), nil
}

func (o *operationName) UnmarshalText(text []byte) error {
	svc, name, ok := strings.Cut(string(text), ".")
	if !ok || svc == "" || name == "" {
		return &OperationNameError{Text: string(text)}
	}
	*o = operationName{Service: ref.ServiceID(svc), Name: name}
	return nil
}

// OperationNameError reports an operation that is not in service.Operation
// form.
type OperationNameError struct {
	Text string
}

// Error names the rejected text and shows the service.Operation form.
func (e *OperationNameError) Error() string {
	return fmt.Sprintf("fault: %q is not an operation; the form is service.Operation, such as sqs.SendMessage", e.Text)
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

// actionKind returns the action's kind name.
func actionKind(a Action) string {
	switch a.(type) {
	case Error:
		return kindError
	case Throttle:
		return kindThrottle
	case Latency:
		return kindLatency
	case AfterCommit:
		return kindAfterCommit
	case StatusOKError:
		return kindStatusOKError
	case DropDelivery:
		return kindDropDelivery
	default:
		return fmt.Sprintf("%T", a)
	}
}

// ruleCodec builds the codec once, on the first rule encoded or decoded.
//
//nolint:gochecknoglobals // a lazily built immutable codec, not mutable state
var ruleCodec = sync.OnceValues(codec)

// codec returns the jsonx codec with the Action and Trigger union tables.
// The tables are static, so an error is a bug in them.
func codec() (jsonx.Codec, error) {
	actions, err := jsonx.NewUnion("fault.Action",
		jsonx.Variant[Action, Error](kindError),
		jsonx.Variant[Action, Throttle](kindThrottle),
		jsonx.Variant[Action, Latency](kindLatency),
		jsonx.Variant[Action, AfterCommit](kindAfterCommit),
		jsonx.Variant[Action, StatusOKError](kindStatusOKError),
		jsonx.Variant[Action, DropDelivery](kindDropDelivery),
	)
	if err != nil {
		return jsonx.Codec{}, err
	}
	triggers, err := jsonx.NewUnion("fault.Trigger",
		jsonx.Variant[Trigger, Always](kindAlways),
		jsonx.Variant[Trigger, NextN](kindNextN),
		jsonx.Variant[Trigger, Probability](kindProbability),
	)
	if err != nil {
		return jsonx.Codec{}, err
	}
	return jsonx.NewCodec(actions, triggers), nil
}

// MarshalJSON encodes the rule in its JSON form.
func (r Rule) MarshalJSON() ([]byte, error) {
	c, err := ruleCodec()
	if err != nil {
		return nil, err
	}
	m := r.Match
	ops := make([]operationName, len(m.Operations))
	for i, op := range m.Operations {
		ops[i] = operationName(op)
	}
	return c.Marshal(ruleJSON{
		ID:    r.ID,
		Phase: r.Phase,
		Match: matchJSON{
			Services: m.Services, Operations: ops, Mechanisms: m.Mechanisms, Resource: m.Resource,
			Accounts: m.Accounts, Regions: m.Regions, Origin: m.Origin,
		},
		Action:  r.Action,
		Trigger: r.Trigger,
	})
}

// UnmarshalJSON decodes the JSON form strictly. It checks the form only;
// Store.Add validates the rule, and reports a missing action or trigger.
func (r *Rule) UnmarshalJSON(data []byte) error {
	c, err := ruleCodec()
	if err != nil {
		return err
	}
	var w ruleJSON
	if err := c.Unmarshal(data, &w); err != nil {
		return parseError(err)
	}
	var ops []ref.OperationID
	for _, op := range w.Match.Operations {
		ops = append(ops, ref.OperationID(op))
	}
	*r = Rule{
		ID:    w.ID,
		Phase: w.Phase,
		Match: Match{
			Services: w.Match.Services, Operations: ops, Mechanisms: w.Match.Mechanisms, Resource: w.Match.Resource,
			Accounts: w.Match.Accounts, Regions: w.Match.Regions, Origin: w.Match.Origin,
		},
		Action:  w.Action,
		Trigger: w.Trigger,
	}
	return nil
}

// RuleParseError is a rule's JSON that does not parse, said in the rule's
// own terms rather than the decoder's Go types: Path is the member at
// fault in dotted form ("match.operations"), empty for the rule itself.
// Err is the decoder's error, which may wrap an *OperationNameError.
type RuleParseError struct {
	Path   string
	Reason string
	Err    error
}

func (e *RuleParseError) Error() string {
	if e.Path == "" {
		return "the rule " + e.Reason
	}
	return e.Path + " " + e.Reason
}

func (e *RuleParseError) Unwrap() error { return e.Err }

// parseError turns the decoder's error into a *RuleParseError.
func parseError(err error) error {
	sem, ok := errors.AsType[*json.SemanticError](err)
	if !ok {
		return &RuleParseError{Reason: "is not valid JSON: " + strings.TrimPrefix(err.Error(), "jsontext: "), Err: err}
	}
	out := &RuleParseError{Path: strings.Join(slices.Collect(sem.JSONPointer.Tokens()), "."), Err: err}
	switch {
	case errors.Is(sem.Err, json.ErrUnknownName):
		out.Reason = "is not a member a rule has"
	case sem.Err != nil:
		out.Reason = "is invalid: " + strings.TrimPrefix(sem.Err.Error(), "fault: ")
	default:
		out.Reason = "is a JSON " + sem.JSONKind.String() + "; expected " + jsonShape(sem.GoType)
	}
	return out
}

// jsonShape says what JSON a rule member of Go type t takes.
func jsonShape(t reflect.Type) string {
	if t == nil {
		return "another JSON type"
	}
	shapes := map[reflect.Kind]string{
		reflect.Slice: "an array", reflect.Struct: "an object", reflect.Interface: "an object",
		reflect.Int: "a number", reflect.Int64: "a number", reflect.Float64: "a number", reflect.String: "a string",
	}
	if shape, ok := shapes[t.Kind()]; ok {
		return shape
	}
	return "another JSON type"
}

// latencyJSON carries Wall as a Go duration string, as the config file
// writes durations; encoding/json/v2 has no representation for
// time.Duration.
type latencyJSON struct {
	Wall string `json:"wall"`
}

// MarshalJSON writes the fields of a Latency action; the union adds the
// kind.
func (l Latency) MarshalJSON() ([]byte, error) {
	return jsonx.Marshal(latencyJSON{Wall: l.Wall.String()})
}

// UnmarshalJSON reads the fields of a Latency action.
func (l *Latency) UnmarshalJSON(data []byte) error {
	var w latencyJSON
	if err := jsonx.Unmarshal(data, &w); err != nil {
		return err
	}
	wall, err := time.ParseDuration(w.Wall)
	if err != nil {
		return fmt.Errorf("fault: latency wall %q is not a duration such as 250ms or 2s: %w", w.Wall, err)
	}
	l.Wall = wall
	return nil
}
