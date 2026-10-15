package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// reset drops the state a ResetRequest selects. The drain runs first,
// outside the gate; late data, which lives outside the cells, is dropped
// next; the cells reset together under the gate's exclusive side, and the
// blob references the dropped cells held are released once it is free;
// then the scheduler is cleared and every service rearms from the state it
// kept, so no timer of the dropped state survives it. The whole reset holds
// the state lock, so no snapshot save or load interleaves with it.
func (h *Handler) reset(w http.ResponseWriter, r *http.Request, _ []string) {
	var in admin.ResetRequest
	if err := readJSON(w, r, &in, true); err != nil {
		h.fail(w, r, err)
		return
	}
	fields := resetFields()
	selected, err := h.selectServices(in.Services, fields)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	scope, err := h.parseScope(in.Accounts, in.Regions, fields)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.checkLateScope(selected, scope); err != nil {
		h.fail(w, r, err)
		return
	}
	res, aerr := h.resetState(r.Context(), in, selected, scope)
	if aerr != nil {
		h.fail(w, r, aerr)
		return
	}
	h.ok(w, r, res)
}

// resetState runs the reset in from the drain to the late steps, returning
// the services it reset, the clock mode it returned to and the number of
// fault rules it removed. It returns only once the drain is lifted, so the
// answer never reaches a caller whose next invocation the drain would
// still refuse.
func (h *Handler) resetState(ctx context.Context, in admin.ResetRequest, selected []ref.ServiceID, scope kernel.ResetScope) (admin.ResetResult, *admin.Error) {
	h.stateOps.Lock()
	defer h.stateOps.Unlock()
	undrain, aerr := h.drain(ctx, selected, scope)
	if aerr != nil {
		return admin.ResetResult{}, aerr
	}
	defer undrain()
	if err := h.resetLate(ctx, selected, scope); err != nil {
		return admin.ResetResult{}, err
	}
	names, dropped, cellErr := h.resetCells(selected, scope)
	releaseErr := h.releaseBlobs(dropped)
	if cellErr != nil {
		h.rebuildTimers()
		if releaseErr != nil {
			cellErr.Message += "; also, " + releaseErr.Message
		}
		return admin.ResetResult{}, cellErr
	}
	res := admin.ResetResult{Services: names}
	var clockErr *admin.Error
	if in.Clock {
		res.Clock, clockErr = h.restartClock(ctx)
	} else {
		h.rebuildTimers()
	}
	// The fault rules and the access record do not depend on the clock,
	// so they are reset as asked even when restoring the clock failed.
	if in.Faults {
		res.Faults = h.o.Faults.Clear()
	}
	if len(in.Services) == 0 && whole(scope) {
		h.o.Access.Reset()
	}
	if aerr := afterReset(names, clockErr, releaseErr); aerr != nil {
		return admin.ResetResult{}, aerr
	}
	return res, nil
}

// afterReset reports the steps that failed once the selected state was
// reset: restoring the clock, releasing the dropped state's blobs, or
// both. nil when neither failed.
func afterReset(names []string, clockErr, releaseErr *admin.Error) *admin.Error {
	if clockErr == nil {
		return releaseErr
	}
	msg := fmt.Sprintf("the state of %v was reset, but %s; the clock was not restored and every service's timers were rebuilt", names, clockErr.Message)
	if releaseErr != nil {
		msg += "; also, " + releaseErr.Message
	}
	return &admin.Error{Code: admin.Internal, Message: msg, Hint: "set the clock with 'understudy clock set TIME' (POST " + admin.Prefix + "/clock)"}
}

// resetCells resets the cells of the selected services under the gate's
// exclusive side. It returns the services reset and the blob references
// the dropped state held: each service's references before its reset less
// those it still holds after, as a multiset, since a scoped reset keeps the
// references of the scopes it leaves. On failure it returns what it reset
// and collected so far, for the caller to release.
func (h *Handler) resetCells(selected []ref.ServiceID, scope kernel.ResetScope) (names, dropped []string, aerr *admin.Error) {
	release := h.o.Kernel.Gate().Exclusive()
	defer release()
	names = []string{}
	for _, s := range h.o.Services {
		if !slices.Contains(selected, s.Name()) {
			continue
		}
		before, err := blobRefs(s)
		if err != nil {
			return names, dropped, internal(fmt.Sprintf("exporting %s to find the blobs its state holds; %s was not reset, the services reset before it were %v", s.Name(), s.Name(), names), err)
		}
		s.Reset(scope)
		names = append(names, string(s.Name()))
		after, err := blobRefs(s)
		if err != nil {
			return names, dropped, internal(fmt.Sprintf("exporting %s after its reset to find the blobs its dropped state held; the services reset were %v, and the blobs only %s's dropped state used stay stored until the server stops", s.Name(), names, s.Name()), err)
		}
		dropped = append(dropped, lost(before, after)...)
	}
	return names, dropped, nil
}

// blobRefs lists the blob references s's state holds, one entry per
// reference.
func blobRefs(s kernel.Stateful) ([]string, error) {
	sections, err := s.Export()
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, sec := range sections {
		refs = append(refs, sec.Blobs...)
	}
	return refs, nil
}

// lost is the multiset difference before - after.
func lost(before, after []string) []string {
	held := map[string]int{}
	for _, h := range after {
		held[h]++
	}
	var out []string
	for _, h := range before {
		if held[h] > 0 {
			held[h]--
			continue
		}
		out = append(out, h)
	}
	return out
}

// releaseBlobs drops one reference per entry of refs, outside the gate,
// since the last release deletes the blob's file.
func (h *Handler) releaseBlobs(refs []string) *admin.Error {
	var errs []error
	for _, s := range refs {
		r, err := blob.ParseRef(s)
		if err == nil {
			err = h.o.Blobs.Release(r)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("blob %s: %w", s, err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return &admin.Error{
		Code:    admin.Internal,
		Message: fmt.Sprintf("the state was reset, but releasing %d of the %d blob references it held failed, so those blob bodies stay stored until the server stops: %v", len(errs), len(refs), errors.Join(errs...)),
		Hint:    "the emulator state is as the reset left it; the server log has the cause",
	}
}

// scopeFields are the names an endpoint reads its service, account and
// region selection under, which its errors name: reset's body members or
// resources' query parameters. none says what selecting no service does
// at that endpoint, for the hint of an unknown service.
type scopeFields struct{ services, accounts, regions, none string }

// resetFields are POST /reset's body members.
func resetFields() scopeFields {
	return scopeFields{services: "services", accounts: "accounts", regions: "regions", none: "selecting no service resets every known one"}
}

// resourcesFields are GET /resources' query parameters.
func resourcesFields() scopeFields {
	return scopeFields{services: "service", accounts: "account", regions: "region", none: "selecting no service lists what every known one holds"}
}

// selectServices resolves service names to the Stateful and Late names they
// select; none selects every one. fields name the selection in errors.
func (h *Handler) selectServices(names []string, fields scopeFields) ([]ref.ServiceID, *admin.Error) {
	known := h.stateful()
	if len(names) == 0 {
		return known, nil
	}
	selected := make([]ref.ServiceID, 0, len(names))
	for _, n := range names {
		if !slices.Contains(known, ref.ServiceID(n)) {
			return nil, h.unknownService(n, fields, known)
		}
		selected = append(selected, ref.ServiceID(n))
	}
	return selected, nil
}

// stateful names every Stateful and Late service, each once.
func (h *Handler) stateful() []ref.ServiceID {
	var known []ref.ServiceID
	for _, s := range h.o.Services {
		known = append(known, s.Name())
	}
	for _, l := range h.o.Late {
		if !slices.Contains(known, l.Name()) {
			known = append(known, l.Name())
		}
	}
	return known
}

// unknownService refuses a service name that holds no state, listing those
// that do. Names the registry does not serve, such as the outbox, hold
// understudy's own state rather than an AWS service's; the hint says so,
// since the list would otherwise show them as services, and says what
// selecting none does at the endpoint fields belong to.
func (h *Handler) unknownService(name string, fields scopeFields, known []ref.ServiceID) *admin.Error {
	served := h.serviceNames()
	list := make([]string, len(known))
	var own []string
	for i, k := range known {
		list[i] = string(k)
		if !slices.Contains(served, string(k)) {
			own = append(own, string(k))
		}
	}
	e := &admin.Error{
		Code:    admin.InvalidRequest,
		Field:   fields.services,
		Message: fmt.Sprintf("%s names %q, which is not a service this build holds state in; it takes the names listed as known", fields.services, name),
		Known:   list,
		Hint:    fields.none,
	}
	switch len(own) {
	case 0:
	case 1:
		e.Hint = own[0] + " holds understudy's own state, such as deliveries and invocations still pending, rather than an AWS service's; " + fields.none
	default:
		e.Hint = strings.Join(own, ", ") + " hold understudy's own state, such as deliveries and invocations still pending, rather than an AWS service's; " + fields.none
	}
	return e
}

// parseScope parses the accounts and regions a reset or resource listing
// selects; fields names them in errors.
func (h *Handler) parseScope(accounts, regions []string, fields scopeFields) (kernel.ResetScope, *admin.Error) {
	var rs kernel.ResetScope
	for _, a := range accounts {
		id, err := aws.ParseAccountID(a)
		if err != nil {
			return rs, &admin.Error{Code: admin.InvalidRequest, Field: fields.accounts, Message: err.Error()}
		}
		rs.Accounts = append(rs.Accounts, id)
	}
	for _, name := range regions {
		reg, err := h.o.Regions.Parse(name)
		if err != nil {
			return rs, &admin.Error{Code: admin.InvalidRequest, Field: fields.regions, Message: err.Error()}
		}
		rs.Regions = append(rs.Regions, reg)
	}
	return rs, nil
}

// drain opens the bracket a reset or snapshot load replaces state in: it
// stops the drainer's environments and pollers in scope when its service
// is selected, and returns undrain, which reconciles the pollers with the
// state left behind and so lifts the drain's block on invocations. The
// caller defers undrain once it has rearmed the services, or replaced
// nothing; the pollers follow the state whether or not the drain ran. A
// drain that fails runs undrain itself, since it may have stopped part of
// scope.
func (h *Handler) drain(ctx context.Context, selected []ref.ServiceID, scope kernel.ResetScope) (undrain func(), aerr *admin.Error) {
	if h.o.Drainer == nil || !slices.Contains(selected, h.o.Drainer.Name()) {
		return h.rearmPollers, nil
	}
	if err := h.o.Drainer.Drain(ctx, scope); err != nil {
		h.rearmPollers()
		return nil, internal("draining "+string(h.o.Drainer.Name()), err)
	}
	return h.rearmPollers, nil
}

// whole reports whether scope selects every account and region.
func whole(scope kernel.ResetScope) bool {
	return len(scope.Accounts) == 0 && len(scope.Regions) == 0
}

// checkLateScope refuses a reset by account or region that selects a Late
// service unable to drop part of its data, before anything is dropped:
// ImportLate replaces a service's late data whole, so without a
// LateResetter the reset would keep that data and still answer success.
func (h *Handler) checkLateScope(selected []ref.ServiceID, scope kernel.ResetScope) *admin.Error {
	if whole(scope) {
		return nil
	}
	var wholeOnly []string
	for _, l := range h.o.Late {
		if _, ok := l.(LateResetter); !ok && slices.Contains(selected, l.Name()) {
			wholeOnly = append(wholeOnly, string(l.Name()))
		}
	}
	if len(wholeOnly) == 0 {
		return nil
	}
	names := strings.Join(wholeOnly, ", ")
	return &admin.Error{
		Code: admin.Unavailable, Field: resetFields().services,
		Message: fmt.Sprintf("this build cannot reset the data of %s by account or region: it lives outside the emulator's cells and is dropped only whole; nothing was reset", names),
		Hint:    "reset without accounts and regions, or name the services to reset in \"services\" and leave " + names + " out",
	}
}

// resetLate drops the late data scope covers in the selected Late services:
// through LateResetter when the service implements it, otherwise, for a
// reset of every account and region, by replacing its data with nothing.
// checkLateScope has refused every other case.
func (h *Handler) resetLate(ctx context.Context, selected []ref.ServiceID, scope kernel.ResetScope) *admin.Error {
	for _, l := range h.o.Late {
		if !slices.Contains(selected, l.Name()) {
			continue
		}
		var err error
		switch lr, ok := l.(LateResetter); {
		case ok:
			err = lr.ResetLate(ctx, scope)
		case whole(scope):
			err = l.ImportLate(ctx, nil)
		default:
			continue
		}
		if err != nil {
			return internal("resetting the data of "+string(l.Name()), err)
		}
	}
	return nil
}

// restartClock returns the clock to how serve started it, and returns the
// mode it now runs in: the scheduler is cleared, the clock restored, and
// every service rearms its timers from the state it kept.
func (h *Handler) restartClock(ctx context.Context) (admin.ClockMode, *admin.Error) {
	k := h.o.Kernel
	at, mode := h.o.ClockStart()
	k.Timers().Scheduler().Clear()
	change, err := k.Clock().Restore(at, mode)
	if err != nil {
		h.rearm()
		return "", internal("restoring the clock", err)
	}
	h.journalClock(ctx, change.From, change.To)
	h.rearm()
	return modeName(change.To), nil
}

// rebuildTimers clears the scheduler and rearms every service from its
// state, dropping the timers of state that is gone.
func (h *Handler) rebuildTimers() {
	h.o.Kernel.Timers().Scheduler().Clear()
	h.rearm()
}

// rearm rebuilds every service's timers from its state. The pollers follow
// when the drain bracket closes.
func (h *Handler) rearm() {
	now := h.o.Kernel.Clock().Now()
	for _, s := range h.o.Services {
		s.Rearm(now)
	}
}

// rearmPollers reconciles the pollers with the state every service has
// rearmed from; drain hands it out as the bracket's undrain.
func (h *Handler) rearmPollers() {
	if h.o.Pollers != nil {
		h.o.Pollers.RearmPollers()
	}
}

// journalClock records a clock mode change.
func (h *Handler) journalClock(ctx context.Context, from, to clock.Mode) {
	if _, err := h.o.Kernel.Journal().Append(journal.Event{Detail: journal.ClockChanged{From: from, To: to}}); err != nil {
		h.log.LogAttrs(ctx, slog.LevelWarn, "the journal refused a clock change", slog.String("error", err.Error()))
	}
}
