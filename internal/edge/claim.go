package edge

import (
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// route is where the claim rules of ARCHITECTURE.md 4.1 send a request.
//
//sumtype:decl
type route interface{ isRoute() }

// toAdmin is the /_understudy/ prefix (rule 1).
type toAdmin struct{}

// toOperation is a modeled operation a codec or route table claimed. req is
// the request the codec decodes: the client's, or the path-style form of a
// virtual-hosted S3 request.
type toOperation struct {
	codec Codec
	op    *registry.Operation
	req   *http.Request
}

// toUnknown is a request of a service's protocol naming no operation of it.
type toUnknown struct {
	codec Codec
	svc   *registry.Service
	name  string
	req   *http.Request
}

// toRaw is a raw operation claimed by host (rule 2).
type toRaw struct{ op *registry.Operation }

// toService is a request signed for a service that no rule claimed (rule 8,
// F7): that service answers it, never S3.
type toService struct {
	svc     *registry.Service
	reasons []string
}

// unclaimed is a request no rule claimed (rule 9), or one rule 5 rejected.
type unclaimed struct{ reasons []string }

func (toAdmin) isRoute()     {}
func (toOperation) isRoute() {}
func (toUnknown) isRoute()   {}
func (toRaw) isRoute()       {}
func (toService) isRoute()   {}
func (unclaimed) isRoute()   {}

// adminPrefix is reserved for understudy: the admin API, SNS links and the
// function URL path fallback. Bucket names cannot start with "_", so it
// never shadows S3.
const adminPrefix = "/_understudy/"

// claimer runs the claim rules for one request.
type claimer struct {
	h    *Handler
	r    *http.Request
	path string // escaped path as sent: RequestURI up to the query
	host string // Host without the port, lowercase
	auth auth.Auth
	// parseErr is why auth is nil; nil when the credentials were read.
	parseErr auth.ParseError
	reasons  []string
}

func (c *claimer) decline(format string, args ...any) {
	c.reasons = append(c.reasons, fmt.Sprintf(format, args...))
}

// claim runs rules 1 to 3, then the rules that read credentials. a is the
// parsed credentials, nil when they cannot be read, and parseErr says why.
func (h *Handler) claim(r *http.Request, a auth.Auth, parseErr auth.ParseError) route {
	c := &claimer{h: h, r: r, path: requestPath(r), host: hostname(r.Host), auth: a, parseErr: parseErr}
	switch {
	case !strings.HasPrefix(c.path, adminPrefix):
		c.decline("rule 1: the path is not under %s", adminPrefix)
	case h.opts.Admin != nil:
		return toAdmin{}
	default:
		c.decline("rule 1: the path is under %s, but no admin handler is configured", adminPrefix)
	}
	if rt, ok := c.functionURL(); ok {
		return rt
	}
	if rt, ok := c.s3Control(); ok {
		return rt
	}
	for _, rule := range []func() (route, bool){c.rpcv2cbor, c.awsJSON, c.awsQuery, c.rest} {
		if rt, ok := rule(); ok {
			return rt
		}
	}
	return unclaimed{reasons: c.reasons}
}

// functionURLHost matches <url-id>.lambda-url.<region>.<base>.
var functionURLHost = regexp.MustCompile(`^[a-z0-9]+\.lambda-url\.[a-z0-9-]+\.`)

// functionURL is rule 2: a function URL host goes to Lambda's raw
// InvokeFunctionUrl operation.
func (c *claimer) functionURL() (route, bool) {
	if !functionURLHost.MatchString(c.host) {
		c.decline("rule 2: host %q is not a Lambda function URL host", c.host)
		return nil, false
	}
	op, ok := c.h.opts.Registry.Operation(ref.OperationID{Service: "lambda", Name: "InvokeFunctionUrl"})
	if !ok || op.Kind != registry.Raw {
		c.decline("rule 2: host %q names a Lambda function URL, but no raw lambda.InvokeFunctionUrl operation is registered", c.host)
		return nil, false
	}
	return toRaw{op: op}, true
}

// accountHost matches <12 digits>.<base>, the S3 Control authority.
var accountHost = regexp.MustCompile(`^\d{12}\.`)

// s3Control is rule 3: S3 Control tagging on <account>.<base> with a
// /v20180820/ path, served by S3's route table.
func (c *claimer) s3Control() (route, bool) {
	if !accountHost.MatchString(c.host) || !strings.HasPrefix(c.path, "/v20180820/") {
		c.decline("rule 3: not an S3 Control request (<account>.<base> host and a /v20180820/ path)")
		return nil, false
	}
	return c.restService("s3", ref.RESTXML, "rule 3: S3 Control", c.r, c.path)
}

// smithyProtocolPath matches rpcv2Cbor's /service/<Name>/operation/<Op>.
var smithyProtocolPath = regexp.MustCompile(`^/service/[^/]+/operation/[^/]+$`)

// rpcv2cbor is rule 5. A request with both Smithy-Protocol and X-Amz-Target
// is ambiguous and is rejected outright.
func (c *claimer) rpcv2cbor() (route, bool) {
	if !strings.EqualFold(c.r.Header.Get("Smithy-Protocol"), "rpc-v2-cbor") || !smithyProtocolPath.MatchString(c.path) {
		c.decline("rule 5: no Smithy-Protocol: rpc-v2-cbor header with a /service/<name>/operation/<op> path")
		return nil, false
	}
	if c.r.Header.Get("X-Amz-Target") != "" {
		c.decline("rule 5: the request carries both Smithy-Protocol: rpc-v2-cbor and X-Amz-Target, so its protocol is ambiguous; send one")
		return unclaimed{reasons: c.reasons}, true
	}
	return c.codecClaim(ref.RPCv2CBOR, "rule 5")
}

// awsJSON is rule 6: X-Amz-Target names the service and operation, cross-
// checked against the signing name.
func (c *claimer) awsJSON() (route, bool) {
	if c.r.Header.Get("X-Amz-Target") == "" {
		c.decline("rule 6: no X-Amz-Target header")
		return nil, false
	}
	for _, p := range []ref.ProtocolID{ref.AWSJSON10, ref.AWSJSON11} {
		if rt, ok := c.codecClaim(p, "rule 6"); ok {
			return rt, true
		}
	}
	return nil, false
}

// awsQuery is rule 7: a form body, or a GET with Action.
func (c *claimer) awsQuery() (route, bool) {
	form := mediaType(c.r.Header.Get("Content-Type")) == "application/x-www-form-urlencoded"
	if !form && (c.r.Method != http.MethodGet || !c.r.URL.Query().Has("Action")) {
		c.decline("rule 7: neither a form body nor a GET with Action")
		return nil, false
	}
	return c.codecClaim(ref.AWSQuery, "rule 7")
}

// codecClaim asks the codec registered for p and checks the claimed service
// against the signing name.
func (c *claimer) codecClaim(p ref.ProtocolID, rule string) (route, bool) {
	codec, ok := c.h.opts.Codecs[p]
	if !ok {
		c.decline("%s: no %s codec is registered", rule, p)
		return nil, false
	}
	var svc *registry.Service
	var rt route
	switch cl := codec.Claim(c.r, c.h.opts.Registry).(type) {
	case Claimed:
		svc, rt = cl.Op.Service, toOperation{codec: codec, op: cl.Op, req: c.r}
	case UnknownOperation:
		svc, rt = cl.Service, toUnknown{codec: codec, svc: cl.Service, name: cl.Name, req: c.r}
	case Declined:
		c.decline("%s (%s): %s", rule, p, cl.Reason)
		return nil, false
	}
	if signed, ok := signingName(c.auth); ok && signed != svc.SigningName {
		c.decline("%s (%s): the request names service %s but is signed for %q", rule, p, svc.ID, signed)
		return nil, false
	}
	return rt, true
}

// datePath matches the /YYYY-MM-DD/ prefix of Lambda's REST paths.
var datePath = regexp.MustCompile(`^/\d{4}-\d{2}-\d{2}/`)

// rest is rule 8: Lambda's and S3's route tables, and the F7 rule that a
// request signed for another service is that service's to answer.
func (c *claimer) rest() (route, bool) {
	signed, isSigned := signingName(c.auth)
	_, presigned := c.auth.(auth.SigV4Query)
	_, sigV2Header := c.auth.(auth.SigV2Header)
	_, sigV2Query := c.auth.(auth.SigV2Query)
	switch {
	case signed == "lambda" && !presigned:
		return c.restService("lambda", ref.RESTJSON1, "rule 8: Lambda", c.r, c.path)
	case signed == "s3" || presigned || sigV2Header || sigV2Query:
		return c.s3("rule 8: S3")
	case isSigned:
		return c.signedElsewhere(signed)
	case refusedForS3(c.parseErr):
		return c.s3("rule 8: S3, whose credentials S3 alone refuses")
	case c.auth == nil:
		c.decline("rule 8: the request's credentials cannot be read, so it cannot be routed by signing name")
		return nil, false
	}
	if _, virtual := c.virtualHostBucket(); !virtual && datePath.MatchString(c.path) && !c.bucketExists() {
		if rt, ok := c.unsignedLambda(); ok {
			return rt, true
		}
	}
	return c.s3("rule 8: unsigned S3")
}

// refusedForS3 reports whether e is a parse failure auth raises only for a
// request signed for S3: a header-signed request without
// x-amz-content-sha256. S3 answers it with its own error, so the request
// must reach S3 although its signing name could not be returned.
func refusedForS3(e auth.ParseError) bool {
	invalid, ok := e.(auth.Invalid)
	return ok && invalid.Problem == auth.MissingPayloadHash
}

// unsignedLambda claims an unsigned date path for Lambda only when it
// matches a Lambda route; anything else is S3 path-style.
func (c *claimer) unsignedLambda() (route, bool) {
	const rule = "rule 8: unsigned Lambda path"
	svc, codec, ok := c.restTable("lambda", ref.RESTJSON1, rule)
	if !ok {
		return nil, false
	}
	rt, ok := svc.Routes.Match(c.r.Method, c.path, c.r.URL.Query(), c.r.Header)
	if !ok {
		c.decline("%s: %s %s matches no Lambda route", rule, c.r.Method, c.path)
		return nil, false
	}
	return toOperation{codec: codec, op: rt.Operation, req: c.r}, true
}

// s3 matches the request against S3's route table in its path-style form:
// a virtual-hosted request (ADR 0013) has its bucket label put in front of
// the path, so both addressings reach the same route and the S3 codec
// binds the bucket one way.
func (c *claimer) s3(rule string) (route, bool) {
	bucket, virtual := c.virtualHostBucket()
	if !virtual {
		return c.restService("s3", ref.RESTXML, rule, c.r, c.path)
	}
	path := "/" + bucket + c.path
	r := c.r.WithContext(c.r.Context())
	u := *c.r.URL
	u.Path = "/" + bucket + u.Path
	if u.RawPath != "" {
		u.RawPath = "/" + bucket + u.RawPath
	}
	r.URL = &u
	if c.r.RequestURI != "" {
		r.RequestURI = path
		if u.RawQuery != "" {
			r.RequestURI += "?" + u.RawQuery
		}
	}
	return c.restService("s3", ref.RESTXML, rule+" (virtual-hosted bucket "+bucket+")", r, path)
}

// signedElsewhere answers a request signed for a service no rule claimed
// with that service's own error (F7).
func (c *claimer) signedElsewhere(signed string) (route, bool) {
	svc, ok := c.h.opts.Registry.BySigningName(signed)
	if !ok {
		c.decline("rule 8: the request is signed for %q, which names no registered service", signed)
		return nil, false
	}
	c.decline("rule 8: the request is signed for %s, which no protocol rule claimed", svc.ID)
	return toService{svc: svc, reasons: c.reasons}, true
}

// restService matches r, whose escaped path is path, against a REST
// service's route table when that service and its codec are registered; a
// request no route matches is the service's unknown operation.
func (c *claimer) restService(id ref.ServiceID, p ref.ProtocolID, rule string, r *http.Request, path string) (route, bool) {
	svc, codec, ok := c.restTable(id, p, rule)
	if !ok {
		return nil, false
	}
	if rt, ok := svc.Routes.Match(r.Method, path, r.URL.Query(), r.Header); ok {
		return toOperation{codec: codec, op: rt.Operation, req: r}, true
	}
	return toUnknown{codec: codec, svc: svc, req: r}, true
}

// restTable returns a REST service and its codec, declining with rule's
// reason when either is not registered.
func (c *claimer) restTable(id ref.ServiceID, p ref.ProtocolID, rule string) (*registry.Service, Codec, bool) {
	svc, ok := c.h.opts.Registry.Service(id)
	if !ok || svc.Routes == nil {
		c.decline("%s: no %s route table is registered", rule, id)
		return nil, nil, false
	}
	codec, ok := c.h.opts.Codecs[p]
	if !ok {
		c.decline("%s: no %s codec is registered", rule, p)
		return nil, nil, false
	}
	return svc, codec, true
}

// virtualHostBucket returns the bucket label of a virtual-hosted S3 host:
// the label in front of the longest configured base domain, when it is a
// DNS-valid bucket name (ADR 0013).
func (c *claimer) virtualHostBucket() (string, bool) {
	best := ""
	for _, base := range c.h.opts.S3BaseDomains {
		if strings.HasSuffix(c.host, "."+base) && len(base) > len(best) {
			best = base
		}
	}
	if best == "" {
		return "", false
	}
	bucket := strings.TrimSuffix(c.host, "."+best)
	return bucket, dnsBucket.MatchString(bucket)
}

// dnsBucket is a DNS-compatible bucket name, dots allowed.
var dnsBucket = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// bucketExists reports whether the first path segment names a bucket, the
// one routing input that reads state (wire.go injects it).
func (c *claimer) bucketExists() bool {
	if c.h.opts.BucketExists == nil {
		return false
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(c.path, "/"), "/")
	return c.h.opts.BucketExists(first)
}

// signingName is the SigV4 credential scope's service; SigV2 and anonymous
// requests have none.
func signingName(a auth.Auth) (string, bool) {
	switch a := a.(type) {
	case auth.SigV4Header:
		return a.Credential.Service, true
	case auth.SigV4Query:
		return a.Credential.Service, true
	case auth.SigV2Header, auth.SigV2Query, auth.Anonymous, nil:
	}
	return "", false
}

// requestPath is the escaped path exactly as the client sent it: the
// request target up to its query. AWS dispatch never cleans it, so %2F,
// "." and ".." segments reach the codecs unchanged (go-toolchain.md 5).
func requestPath(r *http.Request) string {
	if r.RequestURI == "" {
		return r.URL.EscapedPath()
	}
	target := r.RequestURI
	if rest, ok := strings.CutPrefix(target, "http://"); ok {
		target = rest[strings.IndexByte(rest+"/", '/'):]
	} else if rest, ok := strings.CutPrefix(target, "https://"); ok {
		target = rest[strings.IndexByte(rest+"/", '/'):]
	}
	path, _, _ := strings.Cut(target, "?")
	return path
}

// hostname is the Host header without its port, lowercase.
func hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}

// mediaType is a Content-Type's essence, parameters such as charset
// ignored; a malformed parameter does not change the essence.
func mediaType(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil && !errors.Is(err, mime.ErrInvalidMediaParameter) {
		return ""
	}
	return mt
}
