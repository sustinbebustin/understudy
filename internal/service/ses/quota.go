package ses

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// bindQuota binds GetSendQuota, which names no resource.
func (s *Service) bindQuota(b sesapi.Binder) {
	b.GetSendQuota(s.getSendQuota,
		registry.Partial{Note: "Max24HourSend and MaxSendRate from the sandbox or production setting; " +
			"SentLast24Hours counts accepted recipients on the emulator clock, mailbox simulator excluded; " +
			"the send rate is not enforced; production values -1 and 14 are unrecorded (SES-26)"},
		kernel.WithoutSecondaryActions(), refusedConditionKeys())
}

// getSendQuota answers the caller's scope's quota: the sandbox values
// (recorded, ses.md 6.3) or, with ses.productionAccess, the production ones
// (UNVERIFIED, SES-26), and the recipients counted in the last 24 hours.
func (s *Service) getSendQuota(_ context.Context, c *kernel.Call, _ *sesapi.GetSendQuotaInput) (*sesapi.GetSendQuotaOutput, *awserr.Error) {
	out := &sesapi.GetSendQuotaOutput{Max24HourSend: SandboxMax24HourSend, MaxSendRate: SandboxMaxSendRate}
	if s.opts.ProductionAccess.allows(c.Scope()) {
		out.Max24HourSend, out.MaxSendRate = ProductionMax24HourSend, ProductionMaxSendRate
	}
	if cell, ok := s.cells.Lookup(c.Scope()); ok {
		out.SentLast24Hours = float64(store.Read(cell, func(st *state) int { return st.sentLast24Hours(c.Admitted()) }))
	}
	return out, nil
}
