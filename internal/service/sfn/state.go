package sfn

import (
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of State in snapshot sections. Before
// 1.0 a section of another version is refused, not migrated (ADR 0009).
const stateVersion = 2

// State is the Step Functions state of one account and region. Records are
// values replaced on change. An execution's Events only grow by append
// under the cell lock, so every read clones the slices it returns rather
// than alias a later write.
type State struct {
	// Machines are the state machines by name.
	Machines map[string]Machine `json:"machines"`
	// Executions are the executions by executionKey: "<machine>:<name>".
	Executions map[string]Execution `json:"executions"`
}

func newState(aws.Scope) State {
	return State{Machines: map[string]Machine{}, Executions: map[string]Execution{}}
}

// IsZero reports a scope without state machines or executions, which
// snapshots skip.
func (s *State) IsZero() bool { return len(s.Machines) == 0 && len(s.Executions) == 0 }

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*State)(nil)

// Machine is one Standard state machine.
type Machine struct {
	Name string `json:"name"`
	// Definition is the definition exactly as sent: Terraform's empty
	// second plan reads it back byte for byte (stepfunctions-api.md 11.2).
	Definition string        `json:"definition"`
	RoleArn    string        `json:"roleArn"`
	Created    clock.Instant `json:"created"`
	// Updated is the last effective UpdateStateMachine; zero before one.
	Updated clock.Instant `json:"updated,omitzero"`
	// RevisionID is the UUID the last effective update minted; "" before
	// one (stepfunctions-api.md 5.3).
	RevisionID string  `json:"revisionId,omitempty"`
	Logging    Logging `json:"logging"`
	Tracing    bool    `json:"tracing,omitempty"`
	Tags       []Tag   `json:"tags,omitempty"`
	// Deleting is set by DeleteStateMachine while executions still run; the
	// last of them to end removes the machine (stepfunctions-api.md 5.4).
	Deleting bool `json:"deleting,omitempty"`
}

// sameConfiguration reports whether m and o have the same definition,
// logging and tracing: what CreateStateMachine's idempotency compares
// besides the name (stepfunctions-api.md 5.1).
func (m *Machine) sameConfiguration(o *Machine) bool {
	return m.Definition == o.Definition && m.Logging.equal(o.Logging) && m.Tracing == o.Tracing
}

// lastChange is when the machine was created or last effectively updated.
func (m *Machine) lastChange() clock.Instant {
	if m.Updated.IsZero() {
		return m.Created
	}
	return m.Updated
}

// Logging is a stored loggingConfiguration; the zero value is AWS's default
// {level: OFF, includeExecutionData: false} (stepfunctions-api.md 5.2).
type Logging struct {
	// Level is "" for the default OFF.
	Level                string `json:"level,omitempty"`
	IncludeExecutionData bool   `json:"includeExecutionData,omitempty"`
	// Destinations are the log group ARNs as sent, each naming a log group
	// that existed when the configuration was set. Nil is destinations not
	// sent; empty is "destinations": [], which AWS echoes as sent
	// (stepfunctions-api.md 5.1), so the tag is omitzero, not omitempty.
	Destinations []string `json:"destinations,omitzero"`
}

func (l Logging) equal(o Logging) bool {
	return l.Level == o.Level && l.IncludeExecutionData == o.IncludeExecutionData && slices.Equal(l.Destinations, o.Destinations)
}

// Tag is one resource tag; tags keep the order they were added in.
type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Execution is one execution: what it was started with, the machine as it
// ran, the engine's state and the history it wrote.
type Execution struct {
	Machine string `json:"machine"`
	Name    string `json:"name"`
	// Input is the input exactly as sent, "{}" when it was omitted
	// (stepfunctions-api.md 6.1).
	Input       string        `json:"input"`
	TraceHeader string        `json:"traceHeader,omitempty"`
	Started     clock.Instant `json:"started"`
	// Stopped is when the execution ended; zero while it runs.
	Stopped clock.Instant `json:"stopped,omitzero"`
	// Run is the machine as the execution started it: running executions
	// keep the definition and role they started with
	// (stepfunctions-api.md 5.3).
	Run    Machine         `json:"run"`
	Engine asl.Execution   `json:"engine"`
	Events []HistoryRecord `json:"events,omitempty"`
}

// HistoryRecord is one stored history event; Details is the JSON text of
// the engine's details, "" for a type without any.
type HistoryRecord struct {
	ID        int64     `json:"id"`
	Previous  int64     `json:"previous,omitempty"`
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Details   string    `json:"details,omitempty"`
}

// stateCodec is the snapshot codec of State.
func stateCodec() store.Codec {
	c := jsonx.NewCodec()
	return store.Codec{Marshal: c.Marshal, Unmarshal: c.Unmarshal}
}
