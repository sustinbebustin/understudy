package logs

import (
	"maps"
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of State in snapshot sections.
const stateVersion = 1

// State is the CloudWatch Logs state of one account and region. Records are
// values replaced on change: an event slice is never written in place, so a
// slice read out of a cell never aliases a later write.
type State struct {
	Groups map[string]Group `json:"groups"`
	// LastSeq is the sequence number of the latest ingested event; every
	// event of the scope has a distinct one, in ingestion order.
	LastSeq uint64 `json:"lastSeq,omitempty"`
}

func newState(aws.Scope) State { return State{Groups: map[string]Group{}} }

// IsZero reports a scope without log groups, which snapshots skip.
func (s *State) IsZero() bool { return len(s.Groups) == 0 }

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*State)(nil)

// Group is one log group.
type Group struct {
	Name    string        `json:"name"`
	Created clock.Instant `json:"created"`
	// RetentionDays is 0 when events never expire.
	RetentionDays      int32             `json:"retentionDays,omitempty"`
	KMSKeyID           string            `json:"kmsKeyId,omitempty"`
	Class              string            `json:"class"`
	DeletionProtection bool              `json:"deletionProtection,omitempty"`
	Tags               map[string]string `json:"tags,omitempty"`
	Streams            map[string]Stream `json:"streams,omitempty"`
}

// Stream is one log stream.
type Stream struct {
	Name    string        `json:"name"`
	Created clock.Instant `json:"created"`
	// Events are ordered by position: timestamp, then ingestion.
	Events []Event `json:"events,omitempty"`
	// LastIngestion is when the latest PutLogEvents stored an event; zero
	// before the first.
	LastIngestion clock.Instant `json:"lastIngestion"`
	// LastSeq is the sequence number of the latest event stored in the
	// stream, which its sequence token carries.
	LastSeq uint64 `json:"lastSeq,omitempty"`
}

// Event is one stored log event.
type Event struct {
	// Timestamp is the event's own time, in milliseconds since the epoch.
	Timestamp int64  `json:"timestamp"`
	Message   string `json:"message"`
	// Ingestion is when the event was stored, in emulator milliseconds.
	Ingestion int64 `json:"ingestion"`
	// Seq orders events of equal timestamp by ingestion and makes the
	// event's position unique in its scope.
	Seq uint64 `json:"seq"`
}

// pos is the event's position in its stream and in a filtered listing.
func (e *Event) pos() position { return position{ts: e.Timestamp, seq: e.Seq} }

// retention returns the group's retention period; ok is false when its
// events never expire.
func (g *Group) retention() (time.Duration, bool) {
	if g.RetentionDays == 0 {
		return 0, false
	}
	return time.Duration(g.RetentionDays) * 24 * time.Hour, true
}

// fixedRetention reports whether the group's class fixes its retention, so
// PutRetentionPolicy and DeleteRetentionPolicy cannot change it.
func (g *Group) fixedRetention() bool { return g.Class == string(logsapi.LogGroupClassDelivery) }

// streamNames returns the group's stream names in order.
func (g *Group) streamNames() []string { return slices.Sorted(maps.Keys(g.Streams)) }

// oldestEvent returns the smallest event timestamp of the group; ok is
// false when it holds no event.
func (g *Group) oldestEvent() (int64, bool) {
	var (
		oldest int64
		found  bool
	)
	for _, st := range g.Streams {
		if len(st.Events) == 0 {
			continue
		}
		if ts := st.Events[0].Timestamp; !found || ts < oldest {
			oldest, found = ts, true
		}
	}
	return oldest, found
}

// storedBytes is the size of the group's events: their UTF-8 message bytes.
func (g *Group) storedBytes() int64 {
	var n int64
	for _, st := range g.Streams {
		for i := range st.Events {
			n += int64(len(st.Events[i].Message))
		}
	}
	return n
}

// millis is i in milliseconds since the epoch, the unit of every Logs
// timestamp on the wire.
func millis(i clock.Instant) int64 { return i.Wire().UnixMilli() }

// instantOf is the emulator instant of a millisecond timestamp.
func instantOf(ms int64) clock.Instant { return clock.FromWire(time.UnixMilli(ms).UTC()) }
