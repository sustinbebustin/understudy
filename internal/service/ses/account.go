package ses

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// bindAccountSending binds the v1 account sending operations, which name
// no resource and use their primary action alone.
func (s *Service) bindAccountSending(b sesapi.Binder) {
	b.GetAccountSendingEnabled(s.getAccountSendingEnabled, registry.Full{},
		kernel.WithoutSecondaryActions(), refusedConditionKeys())
	b.UpdateAccountSendingEnabled(s.updateAccountSendingEnabled, registry.Full{},
		kernel.WithoutSecondaryActions(), refusedConditionKeys())
}

// sendingEnabled is whether account sending is enabled in scope: it is
// unless UpdateAccountSendingEnabled paused it.
func (s *Service) sendingEnabled(scope aws.Scope) bool {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return true
	}
	return store.Read(cell, func(st *state) bool { return !st.SendingPaused })
}

func (s *Service) getAccountSendingEnabled(_ context.Context, c *kernel.Call, _ *sesapi.GetAccountSendingEnabledInput) (*sesapi.GetAccountSendingEnabledOutput, *awserr.Error) {
	return &sesapi.GetAccountSendingEnabledOutput{Enabled: s.sendingEnabled(c.Scope())}, nil
}

// updateAccountSendingEnabled pauses or resumes sending for the caller's
// account and Region. While paused, every v1 and v2 send fails at
// CheckAccountSending.
func (s *Service) updateAccountSendingEnabled(_ context.Context, c *kernel.Call, in *sesapi.UpdateAccountSendingEnabledInput) (*sesapi.UpdateAccountSendingEnabledOutput, *awserr.Error) {
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *state, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		st.SendingPaused = !in.Enabled
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesapi.UpdateAccountSendingEnabledOutput{}, nil
}
