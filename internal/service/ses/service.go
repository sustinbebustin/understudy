package ses

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the version of the "ses" snapshot section.
const stateVersion = 1

// Service is SES API v1 and the owner of the store both APIs share: the full
// kernel.Stateful (export, import, reset, Rearm, resources).
type Service struct {
	opts Options
}

// V2 is SES API v2: a view of the Service store that exports and lists
// nothing, and whose Reset resets that store.
type V2 struct {
	svc *Service
}

// The service and its v2 view are the snapshot and reset views of the
// store.
var (
	_ kernel.Stateful = (*Service)(nil)
	_ kernel.Stateful = (*V2)(nil)
)

// New binds the SES v1 operations understudy implements; not implemented
// yet, so every v1 operation stays unbound and answers 501.
func New(_ kernel.ServiceDeps, o Options) *Service {
	return &Service{opts: o}
}

// NewV2 binds the SES v2 operations understudy implements over svc's store;
// not implemented yet, so every v2 operation stays unbound and answers 501.
func NewV2(_ kernel.ServiceDeps, svc *Service) *V2 {
	return &V2{svc: svc}
}

// Name returns "ses".
func (*Service) Name() ref.ServiceID { return "ses" }

// SchemaVersion returns the version of the "ses" section.
func (*Service) SchemaVersion() int { return stateVersion }

// Export serializes the store; not implemented yet, so it writes no
// section.
func (*Service) Export() ([]store.Section, error) { return nil, nil }

// Import loads sections written by Export; not implemented yet, so it
// accepts none and changes nothing.
func (*Service) Import([]store.Section) error { return nil }

// Reset drops the scopes rs covers, sent messages and recipient counters
// included; not implemented yet, so there is nothing to drop.
func (*Service) Reset(kernel.ResetScope) {}

// Rearm rebuilds the verification expiry timers from state; not
// implemented yet, so it arms none.
func (*Service) Rearm(clock.Instant) {}

// Resources lists the identities and templates of scope; not implemented
// yet, so it lists nothing.
func (*Service) Resources(aws.Scope) []kernel.Resource { return nil }

// Scopes lists the scopes the store holds a cell for; not implemented yet,
// so it lists none.
func (*Service) Scopes() []aws.Scope { return nil }

// Switches lists the SES switches in effect; not implemented yet, so it
// lists none.
func (*Service) Switches() []fidelity.Entry { return nil }

// Name returns "sesv2".
func (*V2) Name() ref.ServiceID { return "sesv2" }

// SchemaVersion is 1: the view writes no section, so it never changes.
func (*V2) SchemaVersion() int { return 1 }

// Export returns no section: the store is in the ses section.
func (*V2) Export() ([]store.Section, error) { return nil, nil }

// Import accepts the no sections Export writes.
func (*V2) Import([]store.Section) error { return nil }

// Reset resets the shared store, so a reset of sesv2 clears what both APIs
// made.
func (v *V2) Reset(rs kernel.ResetScope) { v.svc.Reset(rs) }

// Rearm has no timers to arm: Service arms the store's.
func (*V2) Rearm(clock.Instant) {}

// Resources lists nothing: Service lists the store's resources.
func (*V2) Resources(aws.Scope) []kernel.Resource { return nil }

// Scopes returns the store's scopes.
func (v *V2) Scopes() []aws.Scope { return v.svc.Scopes() }

// MessageIDRegionCode is the two hex digits after "01" in a timestamped
// MessageId: us-east-1 "00", us-west-2 "01", eu-west-1 "02" (captured,
// ses.md 4.7), any other Region "00" (UNVERIFIED, SES-07).
func MessageIDRegionCode(r aws.Region) string {
	switch r.String() {
	case "us-west-2":
		return "01"
	case "eu-west-1":
		return "02"
	}
	return "00"
}
