package sfn

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of State in snapshot sections.
const stateVersion = 1

// State is the Step Functions state of one account and region. It holds
// nothing yet.
type State struct{}

func newState(aws.Scope) State { return State{} }

// IsZero reports a scope without state machines or executions, which
// snapshots skip.
func (*State) IsZero() bool { return true }

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*State)(nil)

// stateCodec is the snapshot codec of State.
func stateCodec() store.Codec {
	c := jsonx.NewCodec()
	return store.Codec{Marshal: c.Marshal, Unmarshal: c.Unmarshal}
}
