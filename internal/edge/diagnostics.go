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
	"github.com/sustinbebustin/understudy/internal/registry"
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
func (h *Handler) noteUnknown(ctx context.Context, x *exchange, src registry.ModelSource, op ref.OperationID) {
	if !h.warned.first("unknown " + op.String()) {
		return
	}
	h.log.LogAttrs(ctx, slog.LevelWarn,
		"a client called an operation that is not in understudy's vendored Smithy model; the client may be newer than the model, or the name misspelled. Run understudy coverage to list the operations understudy serves",
		slog.String("operation", op.String()), slog.String("model_source", src.String()),
		slog.String("request_id", string(x.id)))
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
	case legacyQuery:
		if rt.action == "" {
			return string(rt.svc.ID) + " (legacy Query form body, not implemented)"
		}
		return string(rt.svc.ID) + "." + rt.action + " (legacy Query, not implemented)"
	case notEmulated:
		return rt.signingName + " (not emulated)"
	case misscoped:
		return string(rt.svc.ID) + " (credential scope refused)"
	case unclaimed:
		return "unclaimed"
	case toLocalStack:
		return "unclaimed (LocalStack path)"
	case toExecuteAPI:
		return "execute-api (not implemented)"
	case executeAPIClaimedTwice:
		return "execute-api (API id held twice)"
	case kinesisCBOR:
		return "kinesis CBOR (not implemented)"
	case http2Preface:
		return "HTTP/2 preface (not implemented)"
	}
	return "unrouted"
}

// routedService is the service a route sends a request to, "" for a route
// that names none. Its request ids take the form that service's answers
// show (ids.RequestID.ForService). The S3 Control tag operations rule 3
// claims are S3's raw operations, but S3 Control answers them, so they are
// s3control.
func routedService(rt route) ref.ServiceID {
	switch rt := rt.(type) {
	case toOperation:
		return rt.op.ID.Service
	case toUnknown:
		return rt.svc.ID
	case toRaw:
		return "s3control"
	case toLink:
		return rt.op.ID.Service
	case toService:
		return rt.svc.ID
	case legacyQuery:
		return rt.svc.ID
	case misscoped:
		return rt.svc.ID
	case toAdmin, notEmulated, unclaimed, toLocalStack, toExecuteAPI, executeAPIClaimedTwice, kinesisCBOR, http2Preface:
	}
	return ""
}

// statusOf is the status a response was sent with: net/http sends 200
// when a handler writes nothing.
func statusOf(d *datedWriter) int {
	if d.status == 0 {
		return http.StatusOK
	}
	return d.status
}
