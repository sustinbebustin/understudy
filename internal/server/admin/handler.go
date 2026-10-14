package admin

import (
	"bytes"
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
	"reflect"
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
	// MaxSnapshotBytes caps an archive PUT /snapshots reads and, counted
	// decompressed as snapshot.Read counts it, one POST /snapshots writes,
	// so a save never writes an archive a load with the same cap refuses;
	// 0 means DefaultMaxSnapshotBytes.
	MaxSnapshotBytes int64
	// TempDir is the directory a snapshot download is spooled in before it
	// is sent: the run's temporary directory, or tmp/ under the data
	// directory with persistence, which the next start removes when a crash
	// leaves a spooled archive behind. Empty means os.TempDir(), which
	// nothing sweeps.
	TempDir string
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

// DefaultMaxSnapshotBytes caps an archive, uploaded or saved, when Options
// sets none. snapshot.Read holds the archive in memory, and one load reads
// at a time, so this also bounds the memory loads hold.
const DefaultMaxSnapshotBytes int64 = 1 << 30

// maxJSONBytes caps a JSON request body.
const maxJSONBytes = 1 << 20

// OptionsError reports a required Options field New was given unset.
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
	// loading admits one snapshot load at a time, from before it reads its
	// archive to its answer: snapshot.Read holds a whole archive in memory,
	// so concurrent loads would each hold up to MaxSnapshotBytes.
	loading sync.Mutex
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

// ServeHTTP serves the main listener: a call a web page made is refused,
// and every call needs the bearer token when one is configured.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if aerr := refuseBrowser(r); aerr != nil {
		h.failStatus(w, r, http.StatusForbidden, aerr)
		return
	}
	if h.o.Token != "" && !h.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="understudy"`)
		h.fail(w, r, unauthorized(r))
		return
	}
	h.route(w, r, routes())
}

// unauthorized is the refusal of r for its token: one that sent none is
// told the header it needs, one whose token is wrong that it was rejected.
func unauthorized(r *http.Request) *admin.Error {
	const hint = "send Authorization: Bearer <token> with the admin token serve was started with (UNDERSTUDY_ADMIN_TOKEN)"
	if r.Header.Get("Authorization") == "" {
		return &admin.Error{Code: admin.Unauthorized, Message: "this listener requires the admin token, and the call sent no Authorization header", Hint: hint}
	}
	return &admin.Error{Code: admin.Unauthorized, Message: "this listener rejected the call's Authorization header: it is not Bearer with the admin token", Hint: hint}
}

// Container returns the container listener's handler: GET health and GET
// clock only, without a token, so function code can read emulator time.
func (h *Handler) Container() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.route(w, r, containerRoutes())
	})
}

// refuseBrowser refuses a call a web page made. Without a token any page
// the developer opens can reach a loopback listener: a bodyless POST /reset
// is a CORS simple request, which runs without a preflight, and a page on a
// hostname rebound to 127.0.0.1, or served by the emulator itself (an S3
// object, a function URL), is same-origin. Browsers set Origin and
// Sec-Fetch-Site themselves and page scripts cannot drop them, while the
// CLI, admin.Client and HTTP tools send neither, and no admin endpoint
// serves a page. Sec-Fetch-Site "none" is a navigation the user started,
// such as a GET URL typed into the address bar, and passes.
func refuseBrowser(r *http.Request) *admin.Error {
	site := r.Header.Get("Sec-Fetch-Site")
	origin := r.Header.Values("Origin")
	if (site == "" || site == "none") && len(origin) == 0 {
		return nil
	}
	return &admin.Error{
		Code:    admin.InvalidRequest,
		Message: fmt.Sprintf("the admin API does not answer calls a web page makes (Origin %q, Sec-Fetch-Site %q); nothing was changed", strings.Join(origin, ", "), site),
		Hint:    "call it with the understudy CLI, admin.Client or an HTTP client such as curl, which send neither header",
	}
}

// authorized reports whether r carries the token. The auth-scheme name is
// case-insensitive (RFC 7235 2.1).
func (h *Handler) authorized(r *http.Request) bool {
	scheme, got, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	return ok && strings.EqualFold(scheme, "Bearer") && subtle.ConstantTimeCompare([]byte(got), []byte(h.o.Token)) == 1
}

// route is one admin endpoint: a method and the path segments after
// /_understudy/v1/, where a "{name}" segment captures one segment,
// unescaped, and names it in the route list a 404 answers.
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
		{method: http.MethodPut, path: []string{"faults", "{id}"}, serve: (*Handler).faultPut},
		{method: http.MethodDelete, path: []string{"faults", "{id}"}, serve: (*Handler).faultDelete},
		{method: http.MethodGet, path: []string{"journal", "events"}, params: append(filterParams(), admin.ParamLimit, admin.ParamCursor), serve: (*Handler).journalEvents},
		{method: http.MethodGet, path: []string{"journal", "stream"}, params: append(filterParams(), admin.ParamAfter), waits: true, serve: (*Handler).journalStream},
		{method: http.MethodGet, path: []string{"journal", "traces"}, params: traceParams, serve: (*Handler).journalTrace},
		{method: http.MethodGet, path: []string{"journal", "traces", "{event}"}, params: traceParams, serve: (*Handler).journalTrace},
		{method: http.MethodPost, path: []string{"snapshots"}, params: []string{pGzip, pJournal}, serve: (*Handler).snapshotSave},
		{method: http.MethodPut, path: []string{"snapshots"}, serve: (*Handler).snapshotLoad},
		{method: http.MethodGet, path: []string{"resources"}, params: []string{"service", "account", "region"}, serve: (*Handler).resources},
		{method: http.MethodGet, path: []string{"coverage"}, params: []string{"format"}, serve: (*Handler).coverage},
		{method: http.MethodGet, path: []string{"iam", "usage"}, params: []string{"principal", "service"}, serve: (*Handler).iamUsage},
		{method: http.MethodGet, path: []string{"iam", "policy"}, params: []string{"principal"}, serve: (*Handler).iamPolicy},
		{method: http.MethodGet, path: []string{"settle"}, params: []string{"timeout"}, waits: true, serve: (*Handler).settle},
		{method: http.MethodPost, path: []string{"lambda", "functions", "{function}", "hot-reload"}, serve: (*Handler).hotReload},
		{method: http.MethodPost, path: []string{"lambda", "images", "pull"}, serve: (*Handler).imagePull},
		{method: http.MethodPost, path: []string{"dynamodb", "ttl-sweep"}, serve: (*Handler).ttlSweep},
		{method: http.MethodGet, path: []string{"sns", "subscriptions", "{arn}", "token"}, serve: (*Handler).subscriptionToken},
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
		h.notFound(w, r, escaped, table)
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
		h.notFound(w, r, escaped, table)
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
		if !strings.HasPrefix(p, "{") {
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

// notFound answers a path no route of table matches, listing the routes.
func (h *Handler) notFound(w http.ResponseWriter, r *http.Request, path string, table []route) {
	endpoints := make([]string, len(table))
	for i, rt := range table {
		endpoints[i] = rt.method + " /" + strings.Join(rt.path, "/")
	}
	h.fail(w, r, &admin.Error{
		Code:    admin.NotFound,
		Message: path + " is not an admin endpoint of this build",
		Hint:    "the endpoints under " + admin.Prefix + " are " + strings.Join(endpoints, ", "),
	})
}

// encodeFailure answers a response that did not encode.
const encodeFailure = `{"error":{"code":"internal","message":"the response did not encode; the server log has the cause"}}` + "\n"

// reply writes v as JSON with status. It does not escape <, > and &, so a
// hint such as "Bearer <token>" reads as written.
func (h *Handler) reply(w http.ResponseWriter, r *http.Request, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		h.log.LogAttrs(r.Context(), slog.LevelError, "admin response did not encode",
			slog.String("path", r.URL.Path), slog.String("error", err.Error()))
		status = http.StatusInternalServerError
		buf.Reset()
		buf.WriteString(encodeFailure)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(buf.Bytes()); err != nil {
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
	data, aerr := readBody(w, r, "request body")
	if aerr != nil {
		return aerr
	}
	if len(data) == 0 {
		if empty {
			return nil
		}
		return &admin.Error{Code: admin.InvalidRequest, Message: "the request needs a JSON body"}
	}
	if aerr := checkMembers(data, v); aerr != nil {
		return aerr
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &admin.Error{Code: admin.InvalidRequest, Message: "the request body is not the expected JSON: " + err.Error()}
	}
	if dec.More() {
		return &admin.Error{Code: admin.InvalidRequest, Message: "the request body holds more than one JSON value"}
	}
	return nil
}

// checkMembers refuses a member of the JSON object data that has no field
// in the struct v points to, naming the members it has, so a near miss
// such as "service" for "services" says what to send. Names match without
// regard to case, as encoding/json matches them. A body that is not an
// object is left for the decoder to refuse.
func checkMembers(data []byte, v any) *admin.Error {
	t := reflect.TypeOf(v)
	if t == nil || t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct {
		return nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err == nil {
		known := jsonNames(t.Elem())
		for _, name := range slices.Sorted(maps.Keys(members)) {
			if !slices.ContainsFunc(known, func(k string) bool { return strings.EqualFold(k, name) }) {
				return &admin.Error{
					Code: admin.InvalidRequest, Field: name, Known: known,
					Message: fmt.Sprintf("the request body has no member %q; nothing was changed", name),
				}
			}
		}
	}
	return nil
}

// jsonNames returns the JSON member names of struct type t's exported
// fields, as encoding/json names them. The admin request types embed no
// structs, so embedded fields are named like any other.
func jsonNames(t reflect.Type) []string {
	var names []string
	for f := range t.Fields() {
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = f.Name
		}
		names = append(names, name)
	}
	return names
}

// readBody reads a JSON request body of at most maxJSONBytes, trimmed of
// surrounding space; what names it in errors. A body that is not empty must
// be declared application/json: a page can send a text/plain or form body
// cross-site without a preflight, but not a JSON one.
func readBody(w http.ResponseWriter, r *http.Request, what string) ([]byte, *admin.Error) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBytes))
	if err != nil {
		if tooBig, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return nil, &admin.Error{Code: admin.InvalidRequest, Message: fmt.Sprintf("the %s is larger than %d bytes; nothing was changed", what, tooBig.Limit)}
		}
		return nil, &admin.Error{Code: admin.InvalidRequest, Message: "the " + what + " could not be read: " + err.Error()}
	}
	data = bytes.TrimSpace(data)
	if len(data) > 0 && !isJSON(r) {
		return nil, &admin.Error{
			Code:    admin.InvalidRequest,
			Message: fmt.Sprintf("the %s is sent as Content-Type %q, but the admin API reads only application/json; nothing was changed", what, r.Header.Get("Content-Type")),
			Hint:    "send the body with Content-Type: application/json (curl --json does)",
		}
	}
	return data, nil
}

// checkParams refuses a query string that does not parse, and a query
// parameter of r that known does not list, before the endpoint changes
// anything. URL.Query drops a pair it cannot parse, such as resource=%ZZ,
// so without the parse check a malformed filter would read as no filter.
func checkParams(r *http.Request, known []string) *admin.Error {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return &admin.Error{
			Code:    admin.InvalidRequest,
			Message: fmt.Sprintf("the query string of %s %s is malformed: %v; nothing was changed", r.Method, r.URL.Path, err),
			Hint:    "percent-encode each parameter value and separate parameters with & (not ;)",
		}
	}
	keys := slices.Sorted(maps.Keys(query))
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
