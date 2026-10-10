package admin

import (
	"errors"
	"io"
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
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBytes))
	if err != nil {
		h.fail(w, r, &admin.Error{Code: admin.InvalidRequest, Message: "the clock op could not be read: " + err.Error()})
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
			h.fail(w, r, &admin.Error{Code: admin.Conflict, Message: err.Error(), Hint: "reset the clock with POST /reset {\"clock\": true}"})
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

// moveError explains a refused advance or set.
func moveError(field string, err error) *admin.Error {
	if back, ok := errors.AsType[*clock.BackwardsError](err); ok {
		return &admin.Error{
			Code: admin.InvalidRequest, Field: field, Message: back.Error(),
			Hint: "emulator time only moves forward; reset the clock with POST /reset {\"clock\": true} or load a snapshot to go back",
		}
	}
	return &admin.Error{Code: admin.InvalidRequest, Field: field, Message: err.Error()}
}
