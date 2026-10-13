package edge

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// maxWarnings bounds the problems warnings remembers: clients choose some
// keys, such as the signing name of a service understudy does not emulate.
const maxWarnings = 1024

// warnings remembers the problems the edge has warned about, so a client
// that retries one does not flood the log. Past maxWarnings keys, a new
// problem is warned about every time it recurs rather than never.
type warnings struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// first reports whether key has not been warned about before, and
// remembers it.
func (w *warnings) first(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.seen[key]; ok {
		return false
	}
	if len(w.seen) < maxWarnings {
		w.seen[key] = struct{}{}
	}
	return true
}

// noteUnimplemented warns, once per operation, that a client called an
// operation understudy models but does not implement, so the developer
// watching the server sees what the client got 501 for.
func (h *Handler) noteUnimplemented(ctx context.Context, x *exchange, e *awserr.Error) {
	u, ok := e.Origin.(awserr.Unimplemented)
	if !ok || !h.warned.first("unimplemented "+u.Op.String()) {
		return
	}
	h.log.LogAttrs(ctx, slog.LevelWarn, "a client called an operation understudy does not implement",
		slog.String("operation", u.Op.String()), slog.String("request_id", string(x.id)))
}

// noteUnknown warns, once per operation, that a client called an operation
// the service's vendored Smithy model does not define: the wire answer is
// Coral's message-less UnknownOperationException, so only this line tells
// the developer that the client is newer than understudy's models or the
// name is misspelled.
func (h *Handler) noteUnknown(ctx context.Context, x *exchange, op ref.OperationID) {
	if !h.warned.first("unknown " + op.String()) {
		return
	}
	h.log.LogAttrs(ctx, slog.LevelWarn,
		"a client called an operation that is not in understudy's vendored Smithy model; the client may be newer than the model, or the name misspelled. Run understudy coverage to list the operations understudy serves",
		slog.String("operation", op.String()), slog.String("request_id", string(x.id)))
}

// logRequest writes the one Debug line per request: method, what the
// request was routed to, status, latency and request id. Credentials and
// bodies are never logged.
func (h *Handler) logRequest(ctx context.Context, x *exchange, routed string, status int, latency time.Duration) {
	h.log.LogAttrs(ctx, slog.LevelDebug, "request",
		slog.String("method", x.r.Method), slog.String("operation", routed), slog.Int("status", status),
		slog.Duration("latency", latency), slog.String("request_id", string(x.id)))
}

// routed names what a route sends a request to, for the request log.
func routed(rt route) string {
	switch rt := rt.(type) {
	case toAdmin:
		return "admin"
	case toOperation:
		return rt.op.ID.String()
	case toUnknown:
		return string(rt.svc.ID) + " unknown operation " + strconv.Quote(rt.name)
	case toRaw:
		return rt.op.ID.String()
	case toLink:
		return rt.op.ID.String()
	case toService:
		return string(rt.svc.ID) + " (no operation named)"
	case notEmulated:
		return rt.signingName + " (not emulated)"
	case unclaimed:
		return "unclaimed"
	case toLocalStack:
		return "unclaimed (LocalStack path)"
	}
	return "unrouted"
}

// statusOf is the status a response was sent with: net/http sends 200
// when a handler writes nothing.
func statusOf(d *datedWriter) int {
	if d.status == 0 {
		return http.StatusOK
	}
	return d.status
}
