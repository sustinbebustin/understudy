package admin

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/snapshot"
)

// Options configures New. Kernel, Faults, Access, Snapshots, Blobs,
// ClockStart and Uptime are required; a nil hook makes its endpoint answer
// unavailable.
type Options struct {
	Kernel    *kernel.Kernel
	Faults    *fault.Store
	Access    *access.Recorder
	Snapshots *snapshot.Coordinator
	// Blobs is the blob store the services keep bodies in: a reset
	// releases the references the dropped state held, as a snapshot load
	// does for the state it replaces.
	Blobs BlobReleaser
	// Services are every Stateful the emulator wires, the outbox included:
	// reset, resources and snapshot load reach state through them.
	Services []Service
	// Late are the services whose data is not cell state.
	Late []snapshot.Late
	// Regions is the region table reset and resources parse names with.
	Regions aws.Regions
	// ClockStart is how serve started the clock, which a reset with Clock
	// returns to: the instant and the kind of mode.
	ClockStart func() (clock.Instant, clock.Mode)
	// DefaultAccount is the account of an admin call that names none; the
	// zero value is 000000000000.
	DefaultAccount aws.AccountID
	// DataDir is the directory the snapshot path form is rooted in, the
	// snapshots directory under the persistence directory; nil without
	// persistence.
	DataDir *os.Root
	// MaxSnapshotBytes caps an archive PUT /snapshots reads; 0 means
	// DefaultMaxSnapshotBytes.
	MaxSnapshotBytes int64
	// Token, when set, is the bearer token every call on the main listener
	// must carry.
	Token string
	// Version and ModelPin are reported by the health endpoint.
	Version  string
	ModelPin string
	// Uptime reports how long the server has run.
	Uptime func() time.Duration

	Drainer       Drainer
	Pollers       PollerRearmer
	HotReloader   HotReloader
	ImagePuller   ImagePuller
	TTLSweeper    TTLSweeper
	Subscriptions SubscriptionTokens
	// Components reports Docker and DynamoDB Local for health; nil leaves
	// them out.
	Components Components

	// Log receives handler diagnostics; nil discards them.
	Log *slog.Logger
}

// DefaultMaxSnapshotBytes caps an uploaded archive when Options sets none.
// snapshot.Read holds the archive in memory.
const DefaultMaxSnapshotBytes int64 = 1 << 30

// maxJSONBytes caps a JSON request body.
const maxJSONBytes = 1 << 20

// OptionsError reports Options New cannot use.
type OptionsError struct {
	Field string
}

func (e *OptionsError) Error() string {
	return "admin: Options." + e.Field + " is required; internal/server/wire.go shows how it is built"
}

// BlobReleaser drops one reference to a stored blob, deleting it with the
// last one. *blob.Store implements it.
type BlobReleaser interface {
	Release(ref blob.Ref) error
}

// Handler serves the admin API under /_understudy/v1/.
type Handler struct {
	o   Options
	log *slog.Logger
	// listener is the main listener's bound address while it accepts, and
	// nil before and after: health is ready exactly while it is set.
	listener atomic.Pointer[netip.AddrPort]
	// stateOps serializes reset, snapshot save and snapshot load, each held
	// from its drain or cut to its last late step, so none runs in another's
	// gap between the gate and the late data.
	stateOps sync.Mutex
}

// New returns the admin handler; it answers health with 503 until
// SetReady.
func New(o Options) (*Handler, error) {
	switch {
	case o.Kernel == nil:
		return nil, &OptionsError{Field: "Kernel"}
	case o.Faults == nil:
		return nil, &OptionsError{Field: "Faults"}
	case o.Access == nil:
		return nil, &OptionsError{Field: "Access"}
	case o.Snapshots == nil:
		return nil, &OptionsError{Field: "Snapshots"}
	case o.Blobs == nil:
		return nil, &OptionsError{Field: "Blobs"}
	case o.ClockStart == nil:
		return nil, &OptionsError{Field: "ClockStart"}
	case o.Uptime == nil:
		return nil, &OptionsError{Field: "Uptime"}
	}
	if o.DefaultAccount.IsZero() {
		o.DefaultAccount = aws.DefaultAccount()
	}
	if o.MaxSnapshotBytes <= 0 {
		o.MaxSnapshotBytes = DefaultMaxSnapshotBytes
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Handler{o: o, log: log}, nil
}

// SetReady reports that the main listener accepts on listener, which
// health then reports as ready and names.
func (h *Handler) SetReady(listener netip.AddrPort) { h.listener.Store(&listener) }

// SetNotReady reports that the main listener no longer accepts.
func (h *Handler) SetNotReady() { h.listener.Store(nil) }

// ServeHTTP serves the main listener: every call needs the bearer token
// when one is configured.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.o.Token != "" && !h.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="understudy"`)
		h.fail(w, r, &admin.Error{
			Code:    admin.Unauthorized,
			Message: "this listener requires the admin token as Authorization: Bearer <token>",
			Hint:    "send Authorization: Bearer <token> with the token serve was configured with (admin.Options.Token for Go clients)",
		})
		return
	}
	h.route(w, r, routes())
}

// Container returns the container listener's handler: GET health and GET
// clock only, without a token, so function code can read emulator time.
func (h *Handler) Container() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.route(w, r, containerRoutes())
	})
}

// authorized reports whether r carries the token. The auth-scheme name is
// case-insensitive (RFC 7235 2.1).
func (h *Handler) authorized(r *http.Request) bool {
	scheme, got, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	return ok && strings.EqualFold(scheme, "Bearer") && subtle.ConstantTimeCompare([]byte(got), []byte(h.o.Token)) == 1
}

// route is one admin endpoint: a method and the path segments after
// /_understudy/v1/, where "*" captures one segment, unescaped.
type route struct {
	method string
	path   []string
	// params are the query parameters the endpoint reads. route refuses
	// any other before the endpoint runs, so a misplaced filter such as
	// POST /reset?services=sqs fails instead of resetting everything.
	params []string
	// waits marks a GET whose answer waits on later events (a stream,
	// settle). HEAD is refused there rather than served as GET, since it
	// would block and then send no body.
	waits bool
	serve func(h *Handler, w http.ResponseWriter, r *http.Request, args []string)
}

func routes() []route {
	traceParams := []string{admin.ParamRequestID, admin.ParamCanonical}
	return []route{
		{method: http.MethodGet, path: []string{"health"}, serve: (*Handler).health},
		{method: http.MethodPost, path: []string{"reset"}, serve: (*Handler).reset},
		{method: http.MethodGet, path: []string{"clock"}, serve: (*Handler).clockState},
		{method: http.MethodPost, path: []string{"clock"}, serve: (*Handler).clockChange},
		{method: http.MethodGet, path: []string{"faults"}, serve: (*Handler).faultList},
		{method: http.MethodDelete, path: []string{"faults"}, serve: (*Handler).faultClear},
		{method: http.MethodPut, path: []string{"faults", "*"}, serve: (*Handler).faultPut},
		{method: http.MethodDelete, path: []string{"faults", "*"}, serve: (*Handler).faultDelete},
		{method: http.MethodGet, path: []string{"journal", "events"}, params: append(filterParams(), admin.ParamLimit, admin.ParamCursor), serve: (*Handler).journalEvents},
		{method: http.MethodGet, path: []string{"journal", "stream"}, params: append(filterParams(), admin.ParamAfter), waits: true, serve: (*Handler).journalStream},
		{method: http.MethodGet, path: []string{"journal", "traces"}, params: traceParams, serve: (*Handler).journalTrace},
		{method: http.MethodGet, path: []string{"journal", "traces", "*"}, params: traceParams, serve: (*Handler).journalTrace},
		{method: http.MethodPost, path: []string{"snapshots"}, params: []string{pGzip, pJournal}, serve: (*Handler).snapshotSave},
		{method: http.MethodPut, path: []string{"snapshots"}, serve: (*Handler).snapshotLoad},
		{method: http.MethodGet, path: []string{"resources"}, params: []string{"service", "account", "region"}, serve: (*Handler).resources},
		{method: http.MethodGet, path: []string{"coverage"}, params: []string{"format"}, serve: (*Handler).coverage},
		{method: http.MethodGet, path: []string{"iam", "usage"}, params: []string{"principal", "service"}, serve: (*Handler).iamUsage},
		{method: http.MethodGet, path: []string{"iam", "policy"}, params: []string{"principal"}, serve: (*Handler).iamPolicy},
		{method: http.MethodGet, path: []string{"settle"}, params: []string{"timeout"}, waits: true, serve: (*Handler).settle},
		{method: http.MethodPost, path: []string{"lambda", "functions", "*", "hot-reload"}, serve: (*Handler).hotReload},
		{method: http.MethodPost, path: []string{"lambda", "images", "pull"}, serve: (*Handler).imagePull},
		{method: http.MethodPost, path: []string{"dynamodb", "ttl-sweep"}, serve: (*Handler).ttlSweep},
		{method: http.MethodGet, path: []string{"sns", "subscriptions", "*", "token"}, serve: (*Handler).subscriptionToken},
	}
}

func containerRoutes() []route {
	return []route{
		{method: http.MethodGet, path: []string{"health"}, serve: (*Handler).health},
		{method: http.MethodGet, path: []string{"clock"}, serve: (*Handler).clockState},
	}
}

// route dispatches on the escaped path, as the edge does: a percent-encoded
// spelling of a literal segment is not that segment (go-toolchain.md 5).
func (h *Handler) route(w http.ResponseWriter, r *http.Request, table []route) {
	escaped := r.URL.EscapedPath()
	rest, ok := strings.CutPrefix(escaped, admin.Prefix+"/")
	if !ok {
		h.notFound(w, r, escaped)
		return
	}
	segments := strings.Split(rest, "/")
	var allowed []string
	for _, rt := range table {
		args, ok := match(rt.path, segments)
		if !ok {
			continue
		}
		if rt.method == r.Method || (rt.method == http.MethodGet && r.Method == http.MethodHead && !rt.waits) {
			if err := checkParams(r, rt.params); err != nil {
				h.fail(w, r, err)
				return
			}
			rt.serve(h, w, r, args)
			return
		}
		allowed = append(allowed, rt.method)
	}
	if len(allowed) == 0 {
		h.notFound(w, r, escaped)
		return
	}
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	h.failStatus(w, r, http.StatusMethodNotAllowed, &admin.Error{
		Code:    admin.InvalidRequest,
		Message: r.Method + " " + escaped + " is not supported",
		Hint:    "use " + strings.Join(allowed, " or ") + " " + escaped,
	})
}

func match(pattern, segments []string) ([]string, bool) {
	if len(pattern) != len(segments) {
		return nil, false
	}
	var args []string
	for i, p := range pattern {
		if p != "*" {
			if segments[i] != p {
				return nil, false
			}
			continue
		}
		arg, err := url.PathUnescape(segments[i])
		if err != nil || arg == "" {
			return nil, false
		}
		args = append(args, arg)
	}
	return args, true
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request, path string) {
	h.fail(w, r, &admin.Error{
		Code:    admin.NotFound,
		Message: path + " is not an admin endpoint of this build",
		Hint:    "the admin API lives under " + admin.Prefix + "/; GET " + admin.Prefix + "/health is the readiness probe",
	})
}

// encodeFailure answers a response that did not encode.
const encodeFailure = `{"error":{"code":"internal","message":"the response did not encode; the server log has the cause"}}`

// reply writes v as JSON with status.
func (h *Handler) reply(w http.ResponseWriter, r *http.Request, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		h.log.LogAttrs(r.Context(), slog.LevelError, "admin response did not encode",
			slog.String("path", r.URL.Path), slog.String("error", err.Error()))
		status = http.StatusInternalServerError
		data = []byte(encodeFailure)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(append(data, '\n')); err != nil {
		h.log.LogAttrs(r.Context(), slog.LevelDebug, "admin response not written; the client went away", slog.String("error", err.Error()))
	}
}

// ok writes v with status 200.
func (h *Handler) ok(w http.ResponseWriter, r *http.Request, v any) {
	h.reply(w, r, http.StatusOK, v)
}

// noContent answers 204.
func (*Handler) noContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// fail writes e in the error envelope with its code's status.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, e *admin.Error) {
	h.failStatus(w, r, e.Code.HTTPStatus(), e)
}

func (h *Handler) failStatus(w http.ResponseWriter, r *http.Request, status int, e *admin.Error) {
	if e.Code == admin.Internal {
		h.log.LogAttrs(r.Context(), slog.LevelError, "admin call failed", slog.String("path", r.URL.Path), slog.String("error", e.Message))
	}
	h.reply(w, r, status, admin.Envelope{Error: e})
}

// internal wraps an unexpected failure of op.
func internal(op string, err error) *admin.Error {
	return &admin.Error{Code: admin.Internal, Message: op + ": " + err.Error()}
}

// readJSON decodes the request body strictly into v. An empty body leaves v
// at its zero value when empty is allowed.
func readJSON(w http.ResponseWriter, r *http.Request, v any, empty bool) *admin.Error {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBytes))
	if err != nil {
		if tooBig, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return &admin.Error{Code: admin.InvalidRequest, Message: fmt.Sprintf("the request body is larger than %d bytes", tooBig.Limit)}
		}
		return &admin.Error{Code: admin.InvalidRequest, Message: "the request body could not be read: " + err.Error()}
	}
	if strings.TrimSpace(string(data)) == "" {
		if empty {
			return nil
		}
		return &admin.Error{Code: admin.InvalidRequest, Message: "the request needs a JSON body"}
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &admin.Error{Code: admin.InvalidRequest, Message: "the request body is not the expected JSON: " + err.Error()}
	}
	if dec.More() {
		return &admin.Error{Code: admin.InvalidRequest, Message: "the request body holds more than one JSON value"}
	}
	return nil
}

// checkParams refuses a query parameter of r that known does not list,
// before the endpoint changes anything.
func checkParams(r *http.Request, known []string) *admin.Error {
	keys := slices.Sorted(maps.Keys(r.URL.Query()))
	for _, k := range keys {
		if slices.Contains(known, k) {
			continue
		}
		e := &admin.Error{
			Code: admin.InvalidRequest, Field: k,
			Message: fmt.Sprintf("%q is not a parameter of %s %s; nothing was changed", k, r.Method, r.URL.Path),
			Known:   known,
		}
		if len(known) == 0 {
			e.Hint = r.Method + " " + r.URL.Path + " takes no query parameters; options go in the JSON body where it has one"
		}
		return e
	}
	return nil
}
