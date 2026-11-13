package ses

import (
	"log/slog"
	"sync/atomic"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/blob"
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
	// blobs holds raw messages; a record holds one reference per blob.
	blobs *blob.Store
	// timers is the kernel's timer table, whose scheduler Rearm arms, and
	// expiry the handled kind of TimerExpiry.
	timers *kernel.Timers
	expiry kernel.HandledKind
	// bindings lists the operations ses.denial.identity governs.
	bindings *kernel.Bindings
	log      *slog.Logger
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
		opts:     o,
		ids:      d.IDs,
		cells:    store.NewPartitioned(d.Kernel.Gate(), codec(), newState),
		blobs:    d.Blob,
		timers:   d.Kernel.Timers(),
		bindings: d.Kernel.Bindings(),
		log:      d.Log,
	}
	s.expiry = s.timers.Handle(TimerExpiry, s.onExpiry)
	b := sesapi.NewBinder(d.Binder)
	s.bindIdentities(b)
	s.bindSends(b)
	s.bindSendRaw(b)
	s.bindSendTemplated(b)
	s.bindQuota(b)
	s.bindAccountSending(b)
	s.bindTemplates(b)
	s.bindConfigurationSets(b)
	s.bindLink(b)
	return s
}

// NewV2 binds the SES v2 operations understudy implements over svc's store.
// Every other v2 operation stays unbound and answers 501.
func NewV2(d kernel.ServiceDeps, svc *Service) *V2 {
	v := &V2{svc: svc}
	b := sesv2api.NewBinder(d.Binder)
	v.bindV2(b)
	v.bindV2Templates(b)
	v.bindV2ConfigurationSets(b)
	v.bindV2Send(b)
	return v
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

// Reset drops the scopes rs covers. As in the other services that hold
// blobs, the raw messages of dropped records are not released here, where
// the gate is held and releasing may delete files: they stay until the next
// snapshot load or --persist startup prunes unreferenced blobs.
func (s *Service) Reset(rs kernel.ResetScope) {
	s.cells.Reset(func(k aws.Scope) bool { return !rs.Covers(k) })
}

// Resources lists the identities of scope in creation order, each with
// its v1 verification status, then the templates in creation order, each
// summarized "template", then the configuration sets in creation order,
// each summarized "configuration-set".
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *state) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.Identities)+len(st.Templates)+len(st.ConfigurationSets))
		for i := range st.Identities {
			id := &st.Identities[i]
			out = append(out, kernel.Resource{ARN: identityARN(scope, id.Name), Summary: string(id.Status.v1())})
		}
		for i := range st.Templates {
			out = append(out, kernel.Resource{ARN: templateARN(scope, st.Templates[i].Name), Summary: "template"})
		}
		for i := range st.ConfigurationSets {
			out = append(out, kernel.Resource{ARN: configurationSetARN(scope, st.ConfigurationSets[i].Name), Summary: "configuration-set"})
		}
		return out
	})
}

// Scopes returns the accounts and Regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }

// Switches lists the SES switches whose behavior is implemented, with the
// value in effect, the condition-key derivations and the denial identity
// (iam-enforcement-surfaces.md 12.5, 12.8).
func (s *Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{
		storeSwitch(s.opts.Store).Entry(),
		messageIDSwitch(s.opts.MessageID).Entry(),
		unverifiedTextSwitch(s.opts.UnverifiedText).Entry(),
		unknownIdentitySwitch(s.opts.UnknownIdentity).Entry(),
		rawEnvelopeSwitch(s.opts.RawEnvelope).Entry(),
		apiVersionV1Switch(s.bindings.AuthorizedOps(sesapi.Service())),
		addressFormSwitch(),
		denialSwitch(s.bindings.AuthorizedOps(sesapi.Service())),
	}
}

// storeSwitch is switch ses.store.shared-v1-v2 at value v. Ops are the v1
// operations that read or write identities and configuration sets so far;
// a switch lists only operations of the service that declares it, so the
// v2 identity, configuration-set and tag operations it governs as well are
// not listed. SES-12 recorded "shared": each API read the identity and the
// template the other created (ses.md 2.3). That configuration sets are
// shared as well is inferred (ses.md 11.4); the label is the recorded
// one, since a switch carries one.
func storeSwitch(v StoreSharing) fidelity.Switch[StoreSharing] {
	return fidelity.Switch[StoreSharing]{
		Name:   "ses.store.shared-v1-v2",
		Value:  v,
		Label:  evidence.AWSRecorded,
		Ref:    "docs/research/ses.md#23-decision",
		Record: "SES-12",
		Ops: []string{
			"ses.VerifyEmailIdentity", "ses.VerifyDomainIdentity", "ses.GetIdentityVerificationAttributes",
			"ses.GetIdentityNotificationAttributes", "ses.ListIdentities", "ses.DeleteIdentity",
			"ses.SendEmail", "ses.SendRawEmail", "ses.SendTemplatedEmail",
			"ses.CreateConfigurationSet", "ses.DescribeConfigurationSet", "ses.DeleteConfigurationSet", "ses.ListConfigurationSets",
		},
		Settles: fidelity.NoScenario{Reason: "ses/SES-12 is recorded; every step crossing the APIs has a v2 REST request, which the replayer does not route"},
	}
}

// messageIDSwitch is switch ses.message-id.format at value v. Ops are the
// v1 operations that mint a MessageId so far: the sends, and
// VerifyEmailIdentity for its verification email. It governs v2 SendEmail
// and CreateEmailIdentity's verification email too, which, being another
// service's operations, it does not list.
func messageIDSwitch(v MessageIDFormat) fidelity.Switch[MessageIDFormat] {
	return fidelity.Switch[MessageIDFormat]{
		Name:    "ses.message-id.format",
		Value:   v,
		Label:   evidence.Inferred,
		Ref:     "docs/research/ses.md#47-messageid",
		Record:  "SES-07",
		Ops:     []string{"ses.SendEmail", "ses.SendRawEmail", "ses.SendTemplatedEmail", "ses.VerifyEmailIdentity"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: SES-07 (ses.md 12) sends from a verified address, which the 2026-11-03 session did not have"},
	}
}

// unverifiedTextSwitch is switch ses.error.unverified.text at value v. It
// governs v2 SendEmail too, which, being another service's operation, it
// does not list. The text is recorded (SES-01), and so are the two lists
// SES-03 recorded, which send_test.go pins. The order, Java HashSet
// iteration, is a model fitted to those two samples (ses.md 6.1): an
// arbitrary order matches both with probability 1/48, too likely to
// settle it, and ties within a bucket, sets over 12 identities, display
// names and case are unrecorded. So the switch stays inferred, and its
// coverage row pending retirement, until a larger list is recorded.
func unverifiedTextSwitch(v UnverifiedText) fidelity.Switch[UnverifiedText] {
	return fidelity.Switch[UnverifiedText]{
		Name:    "ses.error.unverified.text",
		Value:   v,
		Label:   evidence.Inferred,
		Ref:     "docs/research/ses.md#61-sandbox-rules-per-send",
		Record:  "SES-01, SES-03",
		Ops:     []string{"ses.SendEmail", "ses.SendRawEmail", "ses.SendTemplatedEmail"},
		Settles: fidelity.SettledBy{Step: fidelity.StepRef{Scenario: "ses/SES-03", Step: "02-both"}},
	}
}

// rawEnvelopeSwitch is switch ses.raw.envelope at value v: which
// recipients a raw send's sandbox check and counter use (ses.md 4.2). It
// governs v2 SendEmail's Raw content too, which, being another service's
// operation, it does not list.
func rawEnvelopeSwitch(v RawEnvelope) fidelity.Switch[RawEnvelope] {
	return fidelity.Switch[RawEnvelope]{
		Name:    "ses.raw.envelope",
		Value:   v,
		Label:   evidence.Inferred,
		Ref:     "docs/research/ses.md#42-v1-sendrawemail",
		Record:  "SES-20",
		Ops:     []string{"ses.SendRawEmail"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: SES-20 steps 01, 02 and 04 (ses.md 12) send from a verified address, which the 2026-11-03 session did not have"},
	}
}

// unknownIdentitySwitch is switch ses.verification.unknown-identity at
// value v. SES-08 recorded "omitted" (ses.md 5.3).
func unknownIdentitySwitch(v UnknownIdentity) fidelity.Switch[UnknownIdentity] {
	return fidelity.Switch[UnknownIdentity]{
		Name:    "ses.verification.unknown-identity",
		Value:   v,
		Label:   evidence.AWSRecorded,
		Ref:     "docs/research/ses.md#53-v1-identity-operations",
		Record:  "SES-08",
		Ops:     []string{"ses.GetIdentityVerificationAttributes"},
		Settles: fidelity.SettledBy{Step: fidelity.StepRef{Scenario: "ses/SES-08", Step: "01-mixed"}},
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
