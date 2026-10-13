package admin

import (
	"bufio"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// filterParams are the parameters both journal endpoints read.
func filterParams() []string {
	return []string{admin.ParamTrace, admin.ParamRequestID, admin.ParamService, admin.ParamOperation, admin.ParamResource, admin.ParamKind, admin.ParamOutcome, admin.ParamPrincipal, admin.ParamFrom, admin.ParamTo, admin.ParamFromID, admin.ParamToID}
}

// page is GET /journal/events with the journal's events as written.
type page struct {
	Events []journal.Event `json:"events"`
	Next   ref.EventID     `json:"next,omitempty"`
}

func (h *Handler) journalEvents(w http.ResponseWriter, r *http.Request, _ []string) {
	v := r.URL.Query()
	f, err := h.filter(v)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if f.Limit, err = limitParam(v, admin.ParamLimit); err != nil {
		h.fail(w, r, err)
		return
	}
	if f.Cursor, err = idParam(v, admin.ParamCursor); err != nil {
		h.fail(w, r, err)
		return
	}
	p := h.o.Kernel.Journal().Query(f)
	h.ok(w, r, page{Events: nonNil(p.Events), Next: p.Next})
}

// journalStream writes the events the filter selects as NDJSON, flushing
// each, until the client leaves or the server shuts down. after resumes
// behind an event id; a consumer too slow for the emulator gets a Gap
// event in place of what it missed.
func (h *Handler) journalStream(w http.ResponseWriter, r *http.Request, _ []string) {
	v := r.URL.Query()
	f, err := h.filter(v)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	after, err := idParam(v, admin.ParamAfter)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ctx := r.Context()
	events := h.o.Kernel.Journal().Stream(ctx, f, after)
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", admin.NDJSON)
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		h.log.LogAttrs(ctx, slog.LevelDebug, "journal stream not flushed; the client went away", slog.String("error", err.Error()))
		return
	}
	bw := bufio.NewWriter(w)
	for e := range events {
		err := journal.EncodeNDJSON(bw, e)
		if err == nil {
			err = bw.Flush()
		}
		if err == nil {
			err = rc.Flush()
		}
		if err != nil {
			h.log.LogAttrs(ctx, slog.LevelDebug, "journal stream ended; the client went away", slog.String("error", err.Error()))
			return
		}
	}
}

// tree is a journal.Tree in its JSON form.
type tree struct {
	Event    journal.Event `json:"event"`
	Children []tree        `json:"children,omitempty"`
}

func wireTree(t *journal.Tree) tree {
	out := tree{Event: t.Event}
	for _, c := range t.Children {
		out.Children = append(out.Children, wireTree(c))
	}
	return out
}

// journalTrace returns the tree holding the event id in the path or the
// API call with request_id; canonical=1 normalizes it.
func (h *Handler) journalTrace(w http.ResponseWriter, r *http.Request, args []string) {
	v := r.URL.Query()
	canonical, aerr := boolParam(v, admin.ParamCanonical)
	if aerr != nil {
		h.fail(w, r, aerr)
		return
	}
	j := h.o.Kernel.Journal()
	var t *journal.Tree
	var found bool
	var key string
	switch rid := v.Get(admin.ParamRequestID); {
	case len(args) == 1 && rid == "":
		id, err := strconv.ParseUint(args[0], 10, 64)
		if err != nil || id == 0 {
			h.fail(w, r, &admin.Error{Code: admin.InvalidRequest, Message: fmt.Sprintf("%q is not an event id", args[0]), Hint: "pass a positive event id, or ?request_id= without one"})
			return
		}
		t, found = j.Trace(ref.EventID(id))
		key = "event " + args[0]
	case len(args) == 0 && rid != "":
		t, found = j.TraceByRequestID(ids.RequestID(rid))
		key = "request id " + rid
	default:
		h.fail(w, r, &admin.Error{Code: admin.InvalidRequest, Message: "a trace is named by an event id in the path or by request_id, exactly one", Hint: "GET " + admin.Prefix + "/journal/traces/<event id> or " + admin.Prefix + "/journal/traces?request_id=<id>"})
		return
	}
	if !found {
		h.fail(w, r, &admin.Error{Code: admin.NotFound, Message: "no retained journal event has " + key, Hint: "the journal is a bounded ring; 'understudy journal' (GET " + admin.Prefix + "/journal/events) shows what it holds"})
		return
	}
	if canonical {
		h.ok(w, r, t.Canonical())
		return
	}
	h.ok(w, r, wireTree(t))
}

// filter reads the journal filter parameters.
func (h *Handler) filter(v url.Values) (journal.Filter, *admin.Error) {
	var f journal.Filter
	var err *admin.Error
	if f.Trace, err = idParam(v, admin.ParamTrace); err != nil {
		return f, err
	}
	f.RequestID = ids.RequestID(v.Get(admin.ParamRequestID))
	if name := v.Get(admin.ParamService); name != "" {
		svc, ok := h.o.Kernel.Registry().Service(ref.ServiceID(name))
		if !ok {
			return f, &admin.Error{Code: admin.InvalidRequest, Field: admin.ParamService, Message: "service " + name + " is not wired in this build", Known: h.serviceNames()}
		}
		f.Service = journal.Service{ID: svc.ID, ARNNamespace: svc.ARNNamespace}
	}
	if name := v.Get(admin.ParamOperation); name != "" {
		svc, op, ok := strings.Cut(name, ".")
		id := ref.OperationID{Service: ref.ServiceID(svc), Name: op}
		if _, known := h.o.Kernel.Registry().Operation(id); !ok || !known {
			return f, &admin.Error{Code: admin.InvalidRequest, Field: admin.ParamOperation, Message: fmt.Sprintf("%q is not an operation of this build; the form is service.Operation, such as sqs.SendMessage", name)}
		}
		f.Operation = id
	}
	f.ResourceGlob = v.Get(admin.ParamResource)
	if name := v.Get(admin.ParamKind); name != "" {
		k, perr := journal.ParseKind(name)
		if perr != nil {
			return f, &admin.Error{Code: admin.InvalidRequest, Field: admin.ParamKind, Message: perr.Error()}
		}
		f.Kind = k
	}
	if name := v.Get(admin.ParamOutcome); name != "" {
		res, perr := journal.ParseResult(name)
		if perr != nil {
			return f, &admin.Error{Code: admin.InvalidRequest, Field: admin.ParamOutcome, Message: perr.Error()}
		}
		f.Outcome = res
	}
	f.Principal = v.Get(admin.ParamPrincipal)
	if f.EmuRange.From, err = instantParam(v, admin.ParamFrom); err != nil {
		return f, err
	}
	if f.EmuRange.To, err = instantParam(v, admin.ParamTo); err != nil {
		return f, err
	}
	if f.IDRange.From, err = idParam(v, admin.ParamFromID); err != nil {
		return f, err
	}
	if f.IDRange.To, err = idParam(v, admin.ParamToID); err != nil {
		return f, err
	}
	return f, nil
}

func (h *Handler) serviceNames() []string {
	services := h.o.Kernel.Registry().Services()
	out := make([]string, 0, len(services))
	for _, s := range services {
		out = append(out, string(s.ID))
	}
	return out
}

// idParam reads an event id parameter; 0 when it is absent. Event ids
// start at 1, and 0 is how journal.Filter spells an unset field, so an
// explicit 0, such as an unset trace id a script interpolated, is refused
// rather than read as no filter.
func idParam(v url.Values, name string) (ref.EventID, *admin.Error) {
	s := v.Get(name)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n == 0 {
		return 0, &admin.Error{
			Code: admin.InvalidRequest, Field: name,
			Message: fmt.Sprintf("%s=%q is not an event id", name, s),
			Hint:    "event ids are positive integers; leave " + name + " out rather than pass 0",
		}
	}
	return ref.EventID(n), nil
}

// limitParam reads a page size; 0, journal.Filter's default, when it is
// absent. An explicit 0 is refused, as it reads like "no events" or "no
// limit" and means neither.
func limitParam(v url.Values, name string) (int, *admin.Error) {
	s := v.Get(name)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, &admin.Error{
			Code: admin.InvalidRequest, Field: name,
			Message: fmt.Sprintf("%s=%q is not a page size", name, s),
			Hint:    fmt.Sprintf("pass a positive count, or leave %s out for pages of %d", name, journal.DefaultLimit),
		}
	}
	return n, nil
}

func boolParam(v url.Values, name string) (bool, *admin.Error) {
	switch s := v.Get(name); s {
	case "", "0", "false":
		return false, nil
	case "1", "true":
		return true, nil
	default:
		return false, &admin.Error{Code: admin.InvalidRequest, Field: name, Message: fmt.Sprintf("%s=%q is not 1 or 0", name, s)}
	}
}

func instantParam(v url.Values, name string) (clock.Instant, *admin.Error) {
	s := v.Get(name)
	if s == "" {
		return clock.Instant{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return clock.Instant{}, &admin.Error{Code: admin.InvalidRequest, Field: name, Message: fmt.Sprintf("%s=%q is not an RFC 3339 time", name, s)}
	}
	return clock.FromWire(t), nil
}

// nonNil keeps an empty list a JSON array.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
