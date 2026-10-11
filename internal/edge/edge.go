package edge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Options configures a Handler. Kernel and Registry are required.
type Options struct {
	Kernel   *kernel.Kernel
	Registry *registry.Registry
	// Identity resolves access keys to principals; nil makes every caller
	// the default account's root.
	Identity *identity.Store
	// Regions is the table credential-scope regions are validated against.
	Regions aws.Regions
	// Codecs are the wire protocols, by the id each registers under.
	Codecs map[ref.ProtocolID]Codec
	// BucketExists tells an unsigned Lambda date path from an S3 path-style
	// request naming a bucket (rule 8); nil means no bucket exists.
	BucketExists func(name string) bool
	// S3BaseDomains are the hosts virtual-hosted S3 addresses end in.
	S3BaseDomains []string
	// S3Control renders the errors of the S3 Control tag operations rule 3
	// claims: restXml with wrapped errors (<ErrorResponse><Error>), which
	// the SDKs' s3control clients read, unlike S3's bare <Error>. nil
	// renders them with S3's codec.
	S3Control Codec
	// Listener says which listener the handler serves.
	Listener ref.ListenerKind
	// MaxHeaderBytes bounds request headers; 0 means net/http's default.
	MaxHeaderBytes int
	// Admin serves the /_understudy/ prefix (rule 1).
	Admin http.Handler
	// Unclaimed is the protocol whose error envelope answers a request no
	// rule claimed; without a codec for it the answer is plain text.
	Unclaimed ref.ProtocolID
	// UnknownOperationError is a service's overlay unknownOperationError for
	// a protocol family (the generated UnknownOperationError of its api
	// package), which the descriptors do not carry; nil or false means the
	// generic identity.
	UnknownOperationError func(svc ref.ServiceID, f awserr.Family) (awserr.Identity, bool)
	// Log receives edge diagnostics; nil discards them.
	Log *slog.Logger
}

// Handler is the front door: pipeline steps 1 to 5 and 13 of
// ARCHITECTURE.md 4 around kernel.Dispatch.
type Handler struct {
	opts Options
	log  *slog.Logger
}

// OptionsError reports Options New cannot use.
type OptionsError struct{ Field string }

func (e *OptionsError) Error() string {
	return "edge: Options." + e.Field + " is required"
}

// New returns the front door over o.Kernel.
func New(o Options) (*Handler, error) {
	switch {
	case o.Kernel == nil:
		return nil, &OptionsError{Field: "Kernel"}
	case o.Registry == nil:
		return nil, &OptionsError{Field: "Registry"}
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Handler{opts: o, log: log}, nil
}

// readHeaderTimeout bounds how long a client may take to send its headers.
const readHeaderTimeout = 30 * time.Second

// Server returns an http.Server serving h with the header limits; base
// gives each connection its context, so shutdown can end long polls.
func (h *Handler) Server(base func(net.Listener) context.Context) *http.Server {
	return &http.Server{
		Handler:           h,
		MaxHeaderBytes:    h.opts.MaxHeaderBytes,
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       base,
	}
}

// exchange is one request in flight at the edge.
type exchange struct {
	w        http.ResponseWriter
	r        *http.Request
	id       ids.RequestID
	admitted clock.Instant
	// protocol is the claimed request's protocol, empty until claimed;
	// restXml answers carry S3's request id headers.
	protocol ref.ProtocolID
}

// ServeHTTP admits a request (step 1), parses its credentials (2), resolves
// the caller (3), claims it (4), decodes it (5), dispatches it to the
// kernel and writes the answer (13).
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	k := h.opts.Kernel
	ctx := r.Context()
	x := &exchange{w: &datedWriter{ResponseWriter: w, wall: k.Clock().Wall}, r: r, id: k.IDs().RequestID(), admitted: k.Clock().Now()}
	a, parseErr := auth.Parse(r, h.opts.Regions)
	switch rt := h.claim(r, a, parseErr).(type) {
	case toAdmin:
		h.serveAdmin(x)
	case toOperation:
		x.r = rt.req
		h.dispatch(ctx, x, rt.codec, rt.op.ID, a, parseErr, func() kernel.Decoded { return h.decode(x, rt.codec, rt.op) })
	case toUnknown:
		// Coral rejects an unknown operation before it reads the
		// credentials (ARCHITECTURE.md 3.3), so a parse error is moot here.
		x.r = rt.req
		id := ref.OperationID{Service: rt.svc.ID, Name: rt.name}
		h.dispatch(ctx, x, rt.codec, id, a, nil, func() kernel.Decoded {
			return kernel.Rejected{Err: h.unknownOperation(rt.svc, rt.codec.Protocol(), rt.name)}
		})
	case toRaw:
		h.dispatchRaw(ctx, x, rt, a)
	case toService:
		h.answerForService(ctx, x, rt.svc, rt.reasons)
	case unclaimed:
		h.unclaimed(ctx, x, rt.reasons)
	}
}

// serveAdmin hands the /_understudy/ prefix to the admin handler. The
// container listener serves only health and clock from it.
func (h *Handler) serveAdmin(x *exchange) {
	stampRequestID(x)
	if h.opts.Listener == ref.ListenerContainer {
		path := requestPath(x.r)
		if x.r.Method != http.MethodGet || (path != "/_understudy/v1/health" && path != "/_understudy/v1/clock") {
			http.Error(x.w, "the container listener serves only GET /_understudy/v1/health and GET /_understudy/v1/clock", http.StatusNotFound)
			return
		}
	}
	h.opts.Admin.ServeHTTP(x.w, x.r)
}

// dispatch builds the call of a claimed request and runs it through the
// kernel. Unreadable credentials reject it with the protocol family's auth
// error, which Coral checks before the body.
func (h *Handler) dispatch(ctx context.Context, x *exchange, codec Codec, op ref.OperationID, a auth.Auth, parseErr auth.ParseError, decode func() kernel.Decoded) {
	x.protocol = codec.Protocol()
	c := h.call(ctx, x, op, codec.Protocol(), a)
	var d kernel.Decoded
	if parseErr != nil {
		d = kernel.Rejected{Err: authError(parseErr, codec.Protocol())}
	} else {
		d = decode()
	}
	svc, _ := h.opts.Registry.Service(op.Service)
	switch res := h.opts.Kernel.Dispatch(ctx, c, d).(type) {
	case kernel.Output:
		h.writeOutput(ctx, x, codec, c, svc, res)
	case kernel.Failure:
		h.write(ctx, x, codec.EncodeError(x.r, svc, string(x.id), res.Err))
	case kernel.StatusOKFailure:
		e := *res.Err
		e.ID.Status = http.StatusOK
		h.write(ctx, x, codec.EncodeError(x.r, svc, string(x.id), &e))
	}
}

func (h *Handler) writeOutput(ctx context.Context, x *exchange, codec Codec, c *kernel.Call, svc *registry.Service, out kernel.Output) {
	switch v := out.Out.(type) {
	case *kernel.RawOutput:
		r := v.Response
		h.write(ctx, x, Response{Status: r.Status, Header: r.Header, Body: bytes.NewReader(r.Body)})
	case smithy.Serializable:
		resp, err := codec.Encode(x.r, c.Op(), string(x.id), v, out.Status)
		if err != nil {
			h.write(ctx, x, codec.EncodeError(x.r, svc, string(x.id), err))
			return
		}
		h.write(ctx, x, resp)
	default:
		h.log.LogAttrs(ctx, slog.LevelError, "handler output has no encoding", slog.String("op", c.OperationID().String()))
		h.write(ctx, x, codec.EncodeError(x.r, svc, string(x.id), awserr.Internalf(&OutputError{Op: c.OperationID()})))
	}
}

// OutputError is a handler output the codec cannot serialize.
type OutputError struct{ Op ref.OperationID }

func (e *OutputError) Error() string {
	return "the " + e.Op.String() + " output does not implement smithy.Serializable; return a generated output type"
}

// InputError is a bound input type the codec cannot decode into.
type InputError struct{ Op ref.OperationID }

func (e *InputError) Error() string {
	return "the " + e.Op.String() + " input does not implement smithy.Deserializable; bind the operation with its generated input type"
}

// decode binds the request onto a new input of op (step 5). An unbound
// operation is not decoded: the kernel answers it with 501.
func (h *Handler) decode(x *exchange, codec Codec, op *registry.Operation) kernel.Decoded {
	in, ok := h.opts.Kernel.NewInput(op)
	if !ok {
		return kernel.Ok{}
	}
	des, ok := in.(smithy.Deserializable)
	if !ok {
		return kernel.Rejected{Err: awserr.Internalf(&InputError{Op: op.ID})}
	}
	limit := codec.BodyLimit(op)
	if x.r.ContentLength > limit {
		return kernel.Rejected{Err: tooLarge(op.ID, limit)}
	}
	var wire kernel.Wire
	if h.opts.Kernel.Passthrough(op) {
		client, err := clientRequest(x.r, limit, op.ID)
		if err != nil {
			return kernel.Rejected{Err: err}
		}
		wire = client
	}
	if err := codec.DecodeHead(x.r, op, des); err != nil {
		return kernel.Rejected{Err: err}
	}
	if err := codec.DecodeBody(x.r, op, &requestBody{r: x.r, limit: limit, op: op.ID}, des); err != nil {
		return kernel.Rejected{Err: err}
	}
	return kernel.Ok{In: in, Wire: wire}
}

// clientRequest buffers r's body, within limit, for a passthrough handler
// that forwards the client's exact bytes (ADR 0014), and leaves r reading
// the same bytes for the codec.
func clientRequest(r *http.Request, limit int64, op ref.OperationID) (kernel.FromClient, *awserr.Error) {
	var body []byte
	if r.Body != nil {
		read, err := io.ReadAll(&cappedReader{rc: r.Body, limit: limit, op: op})
		if err != nil {
			if boundary, ok := errors.AsType[*awserr.BoundaryError](err); ok && boundary.Err != nil {
				return kernel.FromClient{}, boundary.Err
			}
			return kernel.FromClient{}, awserr.RequestAborted()
		}
		body = read
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return kernel.FromClient{Request: kernel.RawRequest{
		Method: r.Method, Path: r.URL.EscapedPath(), RawQuery: r.URL.RawQuery, Header: r.Header.Clone(), Body: body,
	}}, nil
}

// dispatchRaw runs a raw operation, whose handler writes the response; the
// route's codec, or else its service's, renders an error it returns.
func (h *Handler) dispatchRaw(ctx context.Context, x *exchange, rt toRaw, a auth.Auth) {
	op := rt.op
	c := h.call(ctx, x, op.ID, ref.Raw, a)
	res := h.opts.Kernel.Dispatch(ctx, c, kernel.Ok{In: kernel.RawExchange{Request: x.r, Writer: x.w}})
	var err *awserr.Error
	switch res := res.(type) {
	case kernel.Output:
		return
	case kernel.Failure:
		err = res.Err
	case kernel.StatusOKFailure:
		err = res.Err
	}
	if rt.errors != nil {
		h.write(ctx, x, rt.errors.EncodeError(x.r, op.Service, string(x.id), err))
		return
	}
	for _, p := range op.Service.Protocols {
		if codec, ok := h.opts.Codecs[p]; ok {
			h.write(ctx, x, codec.EncodeError(x.r, op.Service, string(x.id), err))
			return
		}
	}
	h.write(ctx, x, plainError(err.ID.Status, err.Message))
}

// call builds the kernel call of a request: the caller from its access key
// (step 3), the scope from the key's account and the credential region.
func (h *Handler) call(ctx context.Context, x *exchange, op ref.OperationID, p ref.ProtocolID, a auth.Auth) *kernel.Call {
	principal := h.principal(ctx, a)
	account, ok := identity.AccountOf(principal)
	if !ok {
		account = aws.DefaultAccount()
	}
	region := aws.DefaultRegion()
	var src aws.RegionSource = aws.None{}
	if a != nil {
		src = a.RegionSource()
		if cs, ok := src.(aws.CredentialScope); ok {
			region = cs.Region
		}
	}
	remote, _ := netip.ParseAddrPort(x.r.RemoteAddr) //nolint:errcheck // a malformed remote address is journaled as the zero address
	scheme := "http"
	if x.r.TLS != nil {
		scheme = "https"
	}
	return h.opts.Kernel.NewCall(kernel.ExternalCall{
		RequestID:    x.id,
		Operation:    op,
		Scope:        aws.Scope{Partition: aws.PartitionAWS(), Account: account, Region: region},
		RegionSource: src,
		Principal:    principal,
		Origin:       ref.External{Protocol: p, Listener: h.opts.Listener, RemoteAddr: remote, UserAgent: x.r.UserAgent()},
		Admitted:     x.admitted,
		Endpoint:     kernel.Endpoint{Scheme: scheme, Host: x.r.Host},
		Header:       x.r.Header,
	})
}

// principal resolves the access key (ADR 0012). A real-looking key
// understudy did not mint is read as the default account, with one warning
// per key.
func (h *Handler) principal(ctx context.Context, a auth.Auth) identity.Principal {
	key, ok := auth.AccessKey(a)
	switch {
	case !ok && a != nil:
		return identity.Anonymous{}
	case h.opts.Identity == nil || a == nil:
		return identity.Root{Account: aws.DefaultAccount()}
	}
	p, res := h.opts.Identity.Resolve(key)
	if fb, ok := res.(identity.Fallback); ok && fb.Warn {
		h.log.LogAttrs(ctx, slog.LevelWarn, "an AWS access key understudy did not mint is read as the default account, never decoded into its real account",
			slog.String("account", aws.DefaultAccount().String()))
	}
	return p
}

// write sends a response: the request id headers and one Date header (the
// datedWriter's), then the body, which it closes when the body is an
// io.Closer (a blob-backed stream). A failed write means the client is
// gone; nothing else can be sent.
func (h *Handler) write(ctx context.Context, x *exchange, resp Response) {
	if c, ok := resp.Body.(io.Closer); ok {
		defer func() {
			if err := c.Close(); err != nil {
				h.log.LogAttrs(ctx, slog.LevelWarn, "response body did not close",
					slog.String("request_id", string(x.id)), slog.String("error", err.Error()))
			}
		}()
	}
	hdr := x.w.Header()
	for name, values := range resp.Header {
		if strings.EqualFold(name, "Date") {
			continue
		}
		hdr[name] = slices.Clone(values)
	}
	stampRequestID(x)
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	x.w.WriteHeader(status)
	if resp.Body == nil {
		return
	}
	if _, err := io.Copy(x.w, resp.Body); err != nil && !errors.Is(err, context.Canceled) {
		h.log.LogAttrs(ctx, slog.LevelDebug, "response not fully written; the client went away",
			slog.String("request_id", string(x.id)), slog.String("error", err.Error()))
	}
}

// datedWriter stamps the one Date header with the wall time the response
// is sent (pipeline step 13), as AWS does, on every path: codec answers,
// the admin handler and raw HTTP handlers. A 20 s long poll thus answers
// with a current Date, not its admission time.
type datedWriter struct {
	http.ResponseWriter
	wall    func() time.Time
	stamped bool
}

func (d *datedWriter) WriteHeader(status int) {
	// A 1xx status is interim; the final status carries the Date.
	if !d.stamped && status >= http.StatusOK {
		d.stamped = true
		d.Header().Set("Date", d.wall().UTC().Format(http.TimeFormat))
	}
	d.ResponseWriter.WriteHeader(status)
}

func (d *datedWriter) Write(b []byte) (int, error) {
	if !d.stamped {
		d.WriteHeader(http.StatusOK)
	}
	return d.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the connection's writer, for
// flushing a streamed raw response.
func (d *datedWriter) Unwrap() http.ResponseWriter { return d.ResponseWriter }

// stampRequestID gives the response its request id headers: S3's
// x-amz-request-id and x-amz-id-2 on a restXml answer, otherwise
// x-amzn-RequestId unless the answer carries its own (a proxied DynamoDB
// Local response).
func stampRequestID(x *exchange) {
	hdr := x.w.Header()
	if x.protocol == ref.RESTXML {
		hdr.Set("X-Amz-Request-Id", string(x.id))
		hdr.Set("X-Amz-Id-2", hostID(x.id))
		return
	}
	if hdr.Get("X-Amzn-Requestid") == "" {
		hdr.Set("X-Amzn-Requestid", string(x.id))
	}
}

// hostID is x-amz-id-2: a stable base64 digest of the request id, standing
// for the S3 host id, which clients log but never interpret.
func hostID(id ids.RequestID) string {
	sum := sha256.Sum256([]byte(id))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// authError renders credentials that cannot be read in the protocol
// family's words.
func authError(e auth.ParseError, p ref.ProtocolID) *awserr.Error {
	family, ok := awserr.FamilyOf(p)
	if !ok {
		family = awserr.JSONFamily
	}
	v, _ := auth.VocabularyFor(family)
	return e.AWSError(v)
}

func plainError(status int, message string) Response {
	return Response{
		Status: status,
		Header: http.Header{"Content-Type": {"text/plain; charset=utf-8"}},
		Body:   strings.NewReader(message + "\n"),
	}
}
