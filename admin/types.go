package admin

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration is a time.Duration written as a Go duration string ("90s",
// "1h30m"), the form the config file and the CLI use.
type Duration time.Duration

// MarshalText writes the Go duration string.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

// UnmarshalText reads a Go duration string.
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("admin: %q is not a duration such as 90s or 1h30m: %w", text, err)
	}
	*d = Duration(v)
	return nil
}

// Health is GET /health. Until the emulator is ready the server answers 503
// with only Ready false and Waiting; afterwards 200 with the rest.
type Health struct {
	Ready bool `json:"ready"`
	// Waiting names the components still starting, such as "listener".
	Waiting  []string                 `json:"waiting,omitempty"`
	Version  string                   `json:"version,omitempty"`
	Uptime   Duration                 `json:"uptime,omitempty"`
	Services map[string]ServiceStatus `json:"services,omitempty"`
	// Docker and DynamoDBLocal are the state of the components Lambda and
	// DynamoDB run on; absent until the services that use them report it.
	Docker        ServiceStatus `json:"docker,omitzero"`
	DynamoDBLocal ServiceStatus `json:"dynamodbLocal,omitzero"`
	// Journal is absent while the event journal records normally, and
	// degraded, with the reason, once its file has failed or it has
	// stopped recording.
	Journal ServiceStatus `json:"journal,omitzero"`
	// Model is the vendored Smithy model pin the build was generated from.
	Model string `json:"model,omitempty"`
}

// ServiceState is whether a wired service, or a component one runs on,
// serves requests.
type ServiceState string

// ServiceState values.
const (
	Enabled  ServiceState = "enabled"
	Disabled ServiceState = "disabled"
	Degraded ServiceState = "degraded"
)

// ServiceStatus is one service's state; Reason says why it is disabled or
// degraded.
type ServiceStatus struct {
	Status ServiceState `json:"status"`
	Reason string       `json:"reason,omitempty"`
}

// ResetRequest is the body of POST /reset. Empty lists select everything,
// so the zero request drops all service state; the clock and the fault rules
// are kept unless Clock or Faults asks for them too.
type ResetRequest struct {
	Services []string `json:"services,omitempty"`
	Accounts []string `json:"accounts,omitempty"`
	Regions  []string `json:"regions,omitempty"`
	// Clock also returns the clock to how serve started it.
	Clock bool `json:"clock,omitempty"`
	// Faults also removes every fault rule.
	Faults bool `json:"faults,omitempty"`
}

// ResetResult names the services whose state the reset dropped.
type ResetResult struct {
	Services []string `json:"services"`
}

// ClockMode is how emulator time relates to wall time.
type ClockMode string

// ClockMode values.
const (
	Running ClockMode = "running"
	Frozen  ClockMode = "frozen"
	Scaled  ClockMode = "scaled"
)

// ClockState is GET /clock.
type ClockState struct {
	// Now is emulator time.
	Now  time.Time `json:"now"`
	Mode ClockMode `json:"mode"`
	// Factor is the speed of a Scaled clock relative to wall time.
	Factor float64 `json:"factor,omitempty"`
	// Wall is the server's wall time when it read the clock.
	Wall time.Time `json:"wall"`
}

// Timer is one timer an advance fired.
type Timer struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// ClockResult answers POST /clock: the clock after the change and, for
// advance and set, the timers fired on the way, in firing order.
type ClockResult struct {
	Clock ClockState `json:"clock"`
	Fired []Timer    `json:"fired,omitempty"`
}

// ClockOp is a change POST /clock makes. Its JSON form is
// {"op": "freeze"|"resume"|"advance"|"set"|"scale", "by": "90s", "to":
// "<RFC 3339>", "factor": 60}.
//
//sumtype:decl
type ClockOp interface {
	isClockOp()
}

// Freeze stops emulator time.
type Freeze struct{}

// Resume runs emulator time with wall time again from where it is.
type Resume struct{}

// Advance moves emulator time forward by By, firing the timers due on the
// way.
type Advance struct{ By time.Duration }

// Set moves emulator time forward to To; an earlier To is refused, since
// only reset and snapshot load go back.
type Set struct{ To time.Time }

// Scale runs emulator time Factor times faster than wall time.
type Scale struct{ Factor float64 }

// Clock op names.
const (
	opFreeze  = "freeze"
	opResume  = "resume"
	opAdvance = "advance"
	opSet     = "set"
	opScale   = "scale"
)

type clockOpJSON struct {
	Op     string     `json:"op"`
	By     *Duration  `json:"by,omitempty"`
	To     *time.Time `json:"to,omitempty"`
	Factor *float64   `json:"factor,omitempty"`
}

// MarshalJSON writes {"op": "freeze"}.
func (Freeze) MarshalJSON() ([]byte, error) { return json.Marshal(clockOpJSON{Op: opFreeze}) }

// MarshalJSON writes {"op": "resume"}.
func (Resume) MarshalJSON() ([]byte, error) { return json.Marshal(clockOpJSON{Op: opResume}) }

// MarshalJSON writes {"op": "advance", "by": "<duration>"}.
func (a Advance) MarshalJSON() ([]byte, error) {
	by := Duration(a.By)
	return json.Marshal(clockOpJSON{Op: opAdvance, By: &by})
}

// MarshalJSON writes {"op": "set", "to": "<RFC 3339>"}.
func (s Set) MarshalJSON() ([]byte, error) {
	return json.Marshal(clockOpJSON{Op: opSet, To: &s.To})
}

// MarshalJSON writes {"op": "scale", "factor": <factor>}.
func (s Scale) MarshalJSON() ([]byte, error) {
	return json.Marshal(clockOpJSON{Op: opScale, Factor: &s.Factor})
}

func (Freeze) isClockOp()  {}
func (Resume) isClockOp()  {}
func (Advance) isClockOp() {}
func (Set) isClockOp()     {}
func (Scale) isClockOp()   {}

// ParseClockOp reads the JSON form of a clock op, requiring exactly the
// members its op takes. A refused op is an *Error with code
// invalid_request, naming the member at fault.
func ParseClockOp(data []byte) (ClockOp, error) {
	var w clockOpJSON
	if err := decodeStrict(data, &w); err != nil {
		return nil, &Error{Code: InvalidRequest, Message: "the clock op is not valid JSON: " + err.Error(), Hint: `send {"op": "advance", "by": "90s"}`}
	}
	members := func(field string, by, to, factor bool) error {
		if (w.By != nil) != by || (w.To != nil) != to || (w.Factor != nil) != factor {
			return &Error{
				Code: InvalidRequest, Field: field,
				Message: fmt.Sprintf("op %q takes %s", w.Op, describeMembers(by, to, factor)),
				Hint:    `send {"op": "freeze"}, {"op": "resume"}, {"op": "advance", "by": "90s"}, {"op": "set", "to": "2026-12-31T23:59:00Z"} or {"op": "scale", "factor": 60}`,
			}
		}
		return nil
	}
	switch w.Op {
	case opFreeze:
		if err := members("op", false, false, false); err != nil {
			return nil, err
		}
		return Freeze{}, nil
	case opResume:
		if err := members("op", false, false, false); err != nil {
			return nil, err
		}
		return Resume{}, nil
	case opAdvance:
		if err := members("by", true, false, false); err != nil {
			return nil, err
		}
		return Advance{By: time.Duration(*w.By)}, nil
	case opSet:
		if err := members("to", false, true, false); err != nil {
			return nil, err
		}
		return Set{To: *w.To}, nil
	case opScale:
		if err := members("factor", false, false, true); err != nil {
			return nil, err
		}
		return Scale{Factor: *w.Factor}, nil
	default:
		return nil, &Error{
			Code: InvalidRequest, Field: "op",
			Message: fmt.Sprintf("%q is not a clock op", w.Op),
			Known:   []string{opFreeze, opResume, opAdvance, opSet, opScale},
		}
	}
}

func describeMembers(by, to, factor bool) string {
	switch {
	case by:
		return `"by", a duration such as "90s", and nothing else`
	case to:
		return `"to", an RFC 3339 time, and nothing else`
	case factor:
		return `"factor", a number above 0 and at most 10000, and nothing else`
	default:
		return "no other members"
	}
}
