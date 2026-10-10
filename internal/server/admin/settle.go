package admin

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// defaultSettleTimeout bounds GET /settle without ?timeout=.
const defaultSettleTimeout = 30 * time.Second

// settle waits for the kernel's tracked work to finish. On timeout it
// answers condition_not_met with the work still pending by kind.
func (h *Handler) settle(w http.ResponseWriter, r *http.Request, _ []string) {
	timeout := defaultSettleTimeout
	if s := r.URL.Query().Get("timeout"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			h.fail(w, r, &admin.Error{Code: admin.InvalidRequest, Field: "timeout", Message: fmt.Sprintf("timeout=%q is not a positive duration such as 30s", s)})
			return
		}
		timeout = d
	}
	_, err := h.o.Kernel.Settle(r.Context(), timeout)
	var notSettled *kernel.SettleError
	switch {
	case err == nil:
		h.ok(w, r, admin.Settle{Settled: true})
	case r.Context().Err() != nil:
		// The client went away, which Settle reports as a SettleError too;
		// nobody is left to answer.
		h.log.LogAttrs(r.Context(), slog.LevelDebug, "settle not answered; the client went away", slog.String("error", err.Error()))
	case errors.As(err, &notSettled):
		pending := map[string]int{}
		for kind, n := range notSettled.Pending {
			pending[kind.String()] = n
		}
		h.fail(w, r, &admin.Error{
			Code:    admin.ConditionNotMet,
			Message: fmt.Sprintf("not settled after %s: %s still pending", timeout, notSettled.Pending),
			Hint:    "raise ?timeout=, or advance the clock if the work waits on emulator time",
			Pending: pending,
		})
	default:
		h.fail(w, r, internal("waiting for the emulator to settle", err))
	}
}
