package admin

import (
	"errors"
	"net/http"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/clock"
)

func (h *Handler) clockState(w http.ResponseWriter, r *http.Request, _ []string) {
	h.ok(w, r, h.state())
}

// state reads the clock.
func (h *Handler) state() admin.ClockState {
	c := h.o.Kernel.Clock()
	s := admin.ClockState{Now: c.Now().Wire(), Wall: c.Wall().UTC()}
	switch m := c.Mode().(type) {
	case clock.Running:
		s.Mode = admin.Running
	case clock.Frozen:
		s.Mode = admin.Frozen
	case clock.Scaled:
		s.Mode, s.Factor = admin.Scaled, m.Factor
	}
	return s
}

// clockChange applies one ClockOp. Mode changes are journaled as
// ClockChanged; advances answer the timers they fired.
func (h *Handler) clockChange(w http.ResponseWriter, r *http.Request, _ []string) {
	data, aerr := readBody(w, r, "clock op")
	if aerr != nil {
		h.fail(w, r, aerr)
		return
	}
	op, err := admin.ParseClockOp(data)
	if err != nil {
		aerr, ok := errors.AsType[*admin.Error](err)
		if !ok {
			aerr = internal("reading the clock op", err)
		}
		h.fail(w, r, aerr)
		return
	}
	c := h.o.Kernel.Clock()
	var fired []clock.TimerKey
	switch op := op.(type) {
	case admin.Freeze:
		m := c.Freeze()
		h.journalClock(r.Context(), m.From, m.To)
	case admin.Resume:
		m, err := c.Resume()
		if err != nil {
			h.fail(w, r, &admin.Error{Code: admin.Conflict, Message: err.Error(), Hint: resetClockHint})
			return
		}
		h.journalClock(r.Context(), m.From, m.To)
	case admin.Scale:
		m, err := c.Scale(op.Factor)
		if err != nil {
			h.fail(w, r, &admin.Error{Code: admin.InvalidRequest, Field: "factor", Message: err.Error()})
			return
		}
		h.journalClock(r.Context(), m.From, m.To)
	case admin.Advance:
		adv, err := c.Advance(op.By)
		if err != nil {
			h.fail(w, r, moveError("by", err))
			return
		}
		fired = adv.Fired
	case admin.Set:
		adv, err := c.AdvanceTo(clock.FromWire(op.To))
		if err != nil {
			h.fail(w, r, moveError("to", err))
			return
		}
		fired = adv.Fired
	}
	out := admin.ClockResult{Clock: h.state()}
	for _, k := range fired {
		out.Fired = append(out.Fired, admin.Timer{Kind: string(k.Kind), ID: k.ID})
	}
	h.ok(w, r, out)
}

// resetClockHint names the ways back to an earlier emulator time, for the
// CLI and for HTTP clients.
const resetClockHint = "'understudy reset --clock' (POST " + admin.Prefix + "/reset with {\"clock\": true}) returns the clock to how serve started it; " +
	"'understudy snapshot load FILE' (PUT " + admin.Prefix + "/snapshots) returns it to a saved time"

// moveError explains a refused advance or set. The message already says
// the clock only moves forward; the hint names the commands that go back.
func moveError(field string, err error) *admin.Error {
	if back, ok := errors.AsType[*clock.BackwardsError](err); ok {
		return &admin.Error{Code: admin.InvalidRequest, Field: field, Message: back.Error(), Hint: resetClockHint}
	}
	return &admin.Error{Code: admin.InvalidRequest, Field: field, Message: err.Error()}
}
