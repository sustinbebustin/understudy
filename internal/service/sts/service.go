package sts

import (
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// schemaVersion is the snapshot schema version of STS, which writes no
// sections.
const schemaVersion = 1

// Service is AWS STS. It keeps no state: the identity store holds the
// credentials it mints.
type Service struct {
	k        *kernel.Kernel
	clock    *clock.Clock
	identity *identity.Store
	opts     Options
}

// The service takes part in snapshots and resets, with nothing to save.
var _ kernel.Stateful = (*Service)(nil)

// New binds the STS operations understudy implements. Every other STS
// operation stays unbound and answers 501.
func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{k: d.Kernel, clock: d.Clock, identity: d.Identity, opts: o}
	b := stsapi.NewBinder(d.Binder)
	b.GetCallerIdentity(s.getCallerIdentity, registry.Full{})
	b.AssumeRole(s.assumeRole, registry.Partial{Note: "the role's trust policy and session policies are not evaluated; session policies, tags and source identity are validated but not enforced; session tag errors answer ValidationError, not InvalidParameterValue; MFA is accepted and not checked; the account root user may assume roles, which AWS refuses, because unminted keys resolve to root (ADR 0012)"})
	b.GetSessionToken(s.getSessionToken, registry.Partial{Note: "MFA is accepted and not checked; credentials from an earlier GetSessionToken are not refused"})
	return s
}

// Switches lists the behaviors chosen without AWS-recorded evidence.
func (s *Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{s.roleCheck().Entry()}
}

// Name returns "sts".
func (*Service) Name() ref.ServiceID { return "sts" }

// SchemaVersion returns the snapshot schema version.
func (*Service) SchemaVersion() int { return schemaVersion }

// Export writes no sections: STS keeps no state.
func (*Service) Export() ([]store.Section, error) { return nil, nil }

// Import accepts only the empty set of sections Export writes.
func (*Service) Import(ss []store.Section) error {
	if len(ss) > 0 {
		return &sectionsError{paths: sectionPaths(ss)}
	}
	return nil
}

// Reset does nothing: STS keeps no state. The sessions it minted live in
// the identity store, which offers no way to revoke a reset account's
// temporary keys, so they outlive the reset.
func (*Service) Reset(kernel.ResetScope) {}

// Rearm does nothing: STS arms no timers.
func (*Service) Rearm(clock.Instant) {}

// Resources lists nothing: STS holds no resources.
func (*Service) Resources(aws.Scope) []kernel.Resource { return nil }

// Scopes returns nothing: STS keeps no state in any account or region.
func (*Service) Scopes() []aws.Scope { return nil }

// sectionsError is a snapshot that holds STS sections, which no build of
// STS writes.
type sectionsError struct{ paths []string }

func (e *sectionsError) Error() string {
	return fmt.Sprintf("sts: the snapshot holds %d STS sections (%s), but STS keeps no state and writes none; the snapshot was edited or written by another program, and none of its STS sections were loaded", len(e.paths), strings.Join(e.paths, ", "))
}

func sectionPaths(ss []store.Section) []string {
	out := make([]string, len(ss))
	for i := range ss {
		out[i] = ss[i].Path
	}
	return out
}

// roleCheck is the iam.assumeRole switch as configured.
func (s *Service) roleCheck() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "iam.assumeRole",
		Value: s.opts.AssumeRole.String(),
		Label: s.opts.AssumeRole.label(),
		Ref:   "docs/design/ARCHITECTURE.md#7-identity-auth-accounts-and-regions",
	}
}
