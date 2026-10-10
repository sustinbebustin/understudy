package kernel

import (
	"context"
	"slices"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Stateful is implemented by every service with state, and by the outbox.
// Export does no I/O: it serializes to in-memory sections and pins blob
// references, under the gate's exclusive side. store.Partitioned implements
// Export, Import and Reset; services add Rearm and Resources.
type Stateful interface {
	Name() ref.ServiceID
	SchemaVersion() int
	Export() ([]store.Section, error)
	// Import refuses a section whose version differs from SchemaVersion.
	Import(ss []store.Section) error
	Reset(scope ResetScope)
	// Rearm rebuilds the timers state implies; timers are never saved.
	Rearm(now clock.Instant)
	Resources(scope aws.Scope) []Resource
}

// Late is implemented by services whose data is not cell state (DynamoDB
// items). It runs after the cut, outside the gate, through the pipeline with
// Origin Internal{Via: Snapshot}.
type Late interface {
	ExportLate(ctx context.Context) ([]store.Section, error)
	ImportLate(ctx context.Context, ss []store.Section) error
}

// ResetScope selects the state a reset drops: the cells of the listed
// accounts and regions. An empty list selects every value, so the zero
// ResetScope drops everything. Which services reset is the caller's choice
// of Stateful values.
type ResetScope struct {
	Accounts []aws.AccountID
	Regions  []aws.Region
}

// Covers reports whether a cell of scope s is dropped. A cell keyed by
// account only (IAM, STS) passes a zero region.
func (r ResetScope) Covers(s aws.Scope) bool {
	if len(r.Accounts) > 0 && !slices.Contains(r.Accounts, s.Account) {
		return false
	}
	if len(r.Regions) > 0 && !s.Region.IsZero() && !slices.Contains(r.Regions, s.Region) {
		return false
	}
	return true
}

// Resource is one resource a service holds, for GET /_understudy/v1/resources.
type Resource struct {
	ARN aws.ARN
	// Summary is a short state description: "3 messages", "ACTIVE".
	Summary string
}
