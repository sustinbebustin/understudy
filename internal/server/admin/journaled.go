package admin

import (
	"context"
	"log/slog"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
)

// The admin handler journals each call that changed state, once it has done
// its work, so a consumer of the stream sees resets, snapshot loads, fault
// edits and clock moves (ADR 0007, amendment 2026-10-26).
// Each event has no cause, no links and the zero scope. A call that changed
// nothing, refused or read-only, journals nothing; a reset, snapshot load or
// fault rule addition that fails after it changed state is journaled before
// its error is answered.

// journalClock records the clock changing mode or moving by op.
func (h *Handler) journalClock(ctx context.Context, op journal.ClockOp, from, to clock.Mode) {
	h.journalAdminEvent(ctx, journal.ClockChanged{From: from, To: to, Op: op})
}

// journalAction records the admin call op.
func (h *Handler) journalAction(ctx context.Context, op journal.AdminOp) {
	h.journalAdminEvent(ctx, journal.AdminAction{Op: op})
}

// journalAdminEvent appends d. The call it records has already done its
// work, so a refusal is logged rather than answered.
func (h *Handler) journalAdminEvent(ctx context.Context, d journal.Detail) {
	if _, err := h.o.Kernel.Journal().Append(journal.Event{Detail: d}); err != nil {
		h.log.LogAttrs(ctx, slog.LevelWarn, "the journal refused an admin event; the call it records still took effect",
			slog.String("kind", journal.KindOf(d).String()), slog.String("error", err.Error()))
	}
}
