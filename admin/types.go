package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
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
	// Topology is how containers reach understudy; absent when the server
	// runs no containers.
	Topology Topology `json:"topology,omitempty"`
	// Listener is the main listener's bound address, such as 0.0.0.0:4566;
	// absent until it accepts.
	Listener string `json:"listener,omitempty"`
	// Clock is the emulator clock: its mode, emulator time and, when
	// scaled, its factor, as GET /clock reports it; absent until ready.
	Clock ClockState `json:"clock,omitzero"`
}

// Topology names how Lambda and DynamoDB Local containers reach
// understudy (ARCHITECTURE.md 14.5).
type Topology string

// Topology values. TopologyHost is reported while nothing has been
// detected and the server does not run in a container: detection waits
// for the first container.
const (
	TopologyLinuxBridge    Topology = "linux-bridge"
	TopologyDockerDesktop  Topology = "docker-desktop"
	TopologyRootlessDocker Topology = "rootless-docker"
	TopologyPodman         Topology = "podman"
	TopologyRootlessPodman Topology = "rootless-podman"
	TopologyInContainer    Topology = "in-container"
	// TopologyRemoteDaemon is a daemon on another host reached over TCP,
	// such as a docker:dind CI service.
	TopologyRemoteDaemon Topology = "remote-daemon"
	TopologyHost         Topology = "host"
)

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

// ResetResult names the services whose state the reset dropped, and what
// else the request asked to reset.
type ResetResult struct {
	Services []string `json:"services"`
	// Clock is the mode the clock returned to when the request set Clock;
	// empty when the clock was kept.
	Clock ClockMode `json:"clock,omitempty"`
	// Faults is how many fault rules the reset removed: 0 when the request
	// kept them or there were none.
	Faults int `json:"faults"`
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

// clockOpHint shows every clock op's JSON form.
const clockOpHint = `send {"op": "freeze"}, {"op": "resume"}, {"op": "advance", "by": "90s"}, {"op": "set", "to": "2026-12-31T23:59:00Z"} or {"op": "scale", "factor": 60}`

// ParseClockOp reads the JSON form of a clock op, requiring exactly the
// members its op takes. A refused op is an *Error with code
// invalid_request, naming the member at fault.
func ParseClockOp(data []byte) (ClockOp, error) {
	w, err := decodeClockOp(data)
	if err != nil {
		return nil, err
	}
	members := func(field string, by, to, factor bool) error {
		if (w.By != nil) != by || (w.To != nil) != to || (w.Factor != nil) != factor {
			return &Error{
				Code: InvalidRequest, Field: field,
				Message: fmt.Sprintf("op %q takes %s", w.Op, describeMembers(by, to, factor)),
				Hint:    clockOpHint,
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

// decodeClockOp reads a clock op's members one at a time, so a JSON syntax
// error, a member no op takes and a member holding the wrong kind of value
// are each reported as what they are, the last two naming the member.
func decodeClockOp(data []byte) (clockOpJSON, error) {
	var raw map[string]json.RawMessage
	if err := decodeStrict(data, &raw); err != nil {
		if typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
			return clockOpJSON{}, &Error{Code: InvalidRequest, Message: "the clock op is a JSON " + typeErr.Value + ", not an object", Hint: clockOpHint}
		}
		return clockOpJSON{}, &Error{Code: InvalidRequest, Message: "the clock op is not valid JSON: " + err.Error(), Hint: clockOpHint}
	}
	for _, name := range slices.Sorted(maps.Keys(raw)) {
		switch name {
		case "op", "by", "to", "factor":
		default:
			return clockOpJSON{}, &Error{
				Code: InvalidRequest, Field: name,
				Message: fmt.Sprintf("%q is not a clock op member; the members are op, by, to and factor", name),
				Hint:    clockOpHint,
			}
		}
	}
	var w clockOpJSON
	if err := decodeClockMember(raw, "op", &w.Op, "a clock op", clockOpHint); err != nil {
		return clockOpJSON{}, err
	}
	if err := decodeClockMember(raw, "by", &w.By, `a duration such as "90s" or "1h30m"`, "a duration is a number with a unit: ns, us, ms, s, m or h"); err != nil {
		return clockOpJSON{}, err
	}
	if err := decodeClockMember(raw, "to", &w.To, `an RFC 3339 time such as "2026-12-31T23:59:00Z"`, clockOpHint); err != nil {
		return clockOpJSON{}, err
	}
	if err := decodeClockMember(raw, "factor", &w.Factor, "a number such as 60", clockOpHint); err != nil {
		return clockOpJSON{}, err
	}
	return w, nil
}

// decodeClockMember decodes the clock op member name into into, leaving it
// as it is when the member is absent. A value that does not decode is
// refused as not being want, quoting the value as sent.
func decodeClockMember[T any](raw map[string]json.RawMessage, name string, into *T, want, hint string) error {
	data, ok := raw[name]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(data, into); err != nil {
		return &Error{Code: InvalidRequest, Field: name, Message: fmt.Sprintf("%s is not %s", data, want), Hint: hint}
	}
	return nil
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
