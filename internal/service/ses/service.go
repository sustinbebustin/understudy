package ses

import (
	"log/slog"
	"sync/atomic"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Service is SES API v1 and the owner of the store both APIs share: the full
// kernel.Stateful (export, import, reset, Rearm, resources), one state cell
// per account and Region.
type Service struct {
	opts  Options
	ids   *ids.Source
	cells *store.Partitioned[aws.Scope, state]
	// timers is the kernel's timer table, whose scheduler Rearm arms, and
	// expiry the handled kind of TimerExpiry.
	timers *kernel.Timers
	expiry kernel.HandledKind
	log    *slog.Logger
	// seq is the number of the latest message record (record.Seq).
	seq atomic.Uint64
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

// New binds the SES v1 operations understudy implements. Every other v1
// operation stays unbound and answers 501.
func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{
		opts:   o,
		ids:    d.IDs,
		cells:  store.NewPartitioned(d.Kernel.Gate(), codec(), newState),
		timers: d.Kernel.Timers(),
		log:    d.Log,
	}
	s.expiry = s.timers.Handle(TimerExpiry, s.onExpiry)
	b := sesapi.NewBinder(d.Binder)
	s.bindIdentities(b)
	s.bindSends(b)
	s.bindLink(b)
	return s
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

// Export serializes every non-empty account and Region to one section.
func (s *Service) Export() ([]store.Section, error) {
	return s.cells.Export(store.ScopeKeys(), stateVersion)
}

// Import loads sections written by Export of this schema version. Records
// made after it number past the loaded ones, so they still read after
// them.
func (s *Service) Import(ss []store.Section) error {
	if err := s.cells.Import(store.ScopeKeys(), stateVersion, ss); err != nil {
		return err
	}
	for _, scope := range s.Scopes() {
		if cell, ok := s.cells.Lookup(scope); ok {
			loaded := store.Read(cell, func(st *state) uint64 { return st.maxSeq() })
			for cur := s.seq.Load(); loaded > cur && !s.seq.CompareAndSwap(cur, loaded); cur = s.seq.Load() {
			}
		}
	}
	return nil
}

// Reset drops the scopes rs covers.
func (s *Service) Reset(rs kernel.ResetScope) {
	s.cells.Reset(func(k aws.Scope) bool { return !rs.Covers(k) })
}

// Resources lists the identities of scope in creation order, each with
// its v1 verification status.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *state) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.Identities))
		for i := range st.Identities {
			id := &st.Identities[i]
			out = append(out, kernel.Resource{ARN: identityARN(scope, id.Name), Summary: string(id.Status.v1())})
		}
		return out
	})
}

// Scopes returns the accounts and Regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }

// Switches lists the SES switches whose behavior is implemented, with the
// value in effect.
func (s *Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{
		messageIDSwitch(s.opts.MessageID).Entry(),
		unknownIdentitySwitch(s.opts.UnknownIdentity).Entry(),
	}
}

// messageIDSwitch is switch ses.message-id.format at value v. Ops are the
// operations that mint a MessageId so far: VerifyEmailIdentity, for its
// verification email.
func messageIDSwitch(v MessageIDFormat) fidelity.Switch[MessageIDFormat] {
	return fidelity.Switch[MessageIDFormat]{
		Name:    "ses.message-id.format",
		Value:   v,
		Label:   evidence.Inferred,
		Ref:     "docs/research/ses.md#47-messageid",
		Record:  "SES-07",
		Ops:     []string{"ses.VerifyEmailIdentity"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: SES-07 in the recording queue (ses.md 12) settles it"},
	}
}

// unknownIdentitySwitch is switch ses.verification.unknown-identity at
// value v.
func unknownIdentitySwitch(v UnknownIdentity) fidelity.Switch[UnknownIdentity] {
	return fidelity.Switch[UnknownIdentity]{
		Name:    "ses.verification.unknown-identity",
		Value:   v,
		Label:   evidence.Inferred,
		Ref:     "docs/research/ses.md#53-v1-identity-operations",
		Record:  "SES-08",
		Ops:     []string{"ses.GetIdentityVerificationAttributes"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: SES-08 in the recording queue (ses.md 12) settles it"},
	}
}

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
