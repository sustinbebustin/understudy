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
// references, under the gate's exclusive side. A service's Export, Import
// and Reset delegate to its store.Partitioned; Rearm and Resources are its
// own.
type Stateful interface {
	Name() ref.ServiceID
	SchemaVersion() int
	Export() ([]store.Section, error)
	// Import refuses a section whose version differs from SchemaVersion,
	// unless the service declares it reads that older version
	// (snapshot.OlderReader).
	Import(ss []store.Section) error
	Reset(scope ResetScope)
	// Rearm rebuilds the timers state implies; timers are never saved.
	Rearm(now clock.Instant)
	Resources(scope aws.Scope) []Resource
}

// RearmEach arms on sched the timers that timersOf lists for the state of
// every scope p holds a cell for. It is the body of a Stateful.Rearm for a
// service whose timers all follow from its state, since timers are never
// saved (ADR 0009).
//
// Each scope's timers are listed and armed inside store.Read, under the
// cell's lock, where Write arms. A concurrent write to the cell therefore
// either commits first and is read here, or commits after and its own arm
// or disarm wins. The helper lives in kernel because store may not import
// clock.
func RearmEach[S any](sched *clock.Scheduler, p *store.Partitioned[aws.Scope, S], timersOf func(scope aws.Scope, st *S) []clock.Armed) {
	for _, scope := range p.Keys(store.ScopeKeys()) {
		cell, ok := p.Lookup(scope)
		if !ok {
			continue
		}
		store.Read(cell, func(st *S) struct{} {
			for _, a := range timersOf(scope, st) {
				sched.Arm(a.Key, a.Due)
			}
			return struct{}{}
		})
	}
}

// Late is implemented by services whose data is not cell state (DynamoDB
// items). It runs after the cut, outside the gate and outside the pipeline,
// so it never matches a fault rule.
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
