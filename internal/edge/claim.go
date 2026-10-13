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

// toRaw is a raw operation claimed by S3 Control's account host (rule 3).
// errors renders the errors its handler returns; nil means the codec of the
// operation's service.
type toRaw struct {
	op     *registry.Operation
	errors Codec
}

// toLink is a raw operation serving a link understudy hands out for a
// browser to open from any page, as AWS serves its own: an SNS link or a
// Lambda code link (rule 1), or a function URL by path (rule 1) or by host
// (rule 2). The edge's browser defences never refuse one.
type toLink struct{ op *registry.Operation }

// toService is a request signed for a registered service that no rule
// claimed (rule 8): that service answers it, never S3, as its own front end
// would (ADR 0013).
type toService struct {
	svc     *registry.Service
	reasons []reason
}

// notEmulated is a request signed for a service understudy does not
// emulate (rule 8).
type notEmulated struct {
	signingName string
	reasons     []reason
}

// unclaimed is a request no rule claimed (rule 9), or one rule 5 rejected.
type unclaimed struct{ reasons []reason }

// toLocalStack is a path under LocalStack's reserved prefix (rule 1), such
// as a migrated health check's /_localstack/health. reasons are the rules
// passed over before it, for the Unclaimed journal event.
type toLocalStack struct{ reasons []reason }

func (toAdmin) isRoute()      {}
func (toOperation) isRoute()  {}
func (toUnknown) isRoute()    {}
func (toRaw) isRoute()        {}
func (toLink) isRoute()       {}
func (toService) isRoute()    {}
func (notEmulated) isRoute()  {}
func (unclaimed) isRoute()    {}
func (toLocalStack) isRoute() {}

// reason is why one claim rule passed over a request: its number in
// ARCHITECTURE.md 4.1, which the journal records, and the words a client
// is shown.
type reason struct {
	rule int
	text string
}

// journaled renders reasons as the Unclaimed journal event lists them,
// each numbered by its rule.
func journaled(reasons []reason) []string {
	out := make([]string, len(reasons))
	for i, r := range reasons {
		out[i] = fmt.Sprintf("rule %d: %s", r.rule, r.text)
	}
	return out
}

// adminPrefix is reserved for understudy: the admin API, SNS links and the
// function URL path fallback. Bucket names cannot start with "_", so it
// never shadows S3.
const adminPrefix = "/_understudy/"

// localStackSegment is the first path segment of LocalStack's reserved
// prefix. No bucket name starts with "_", so claiming it never shadows S3.
const localStackSegment = "_localstack"

// claimer runs the claim rules for one request.
type claimer struct {
	h    *Handler
	r    *http.Request
	path string // escaped path as sent: RequestURI up to the query
	host string // Host without the port, lowercase
	auth auth.Auth
	// parseErr is why auth is nil; nil when the credentials were read.
	parseErr auth.ParseError
	reasons  []reason
}

func (c *claimer) decline(rule int, format string, args ...any) {
	c.reasons = append(c.reasons, reason{rule: rule, text: fmt.Sprintf(format, args...)})
}

// claim runs rules 1 to 3, then the rules that read credentials. a is the
// parsed credentials, nil when they cannot be read, and parseErr says why.
func (h *Handler) claim(r *http.Request, a auth.Auth, parseErr auth.ParseError) route {
	c := &claimer{h: h, r: r, path: requestPath(r), host: hostname(r.Host), auth: a, parseErr: parseErr}
	if rt, ok := c.snsLink(); ok {
		return rt
	}
	if rt, ok := c.functionURLPath(); ok {
		return rt
	}
	if rt, ok := c.lambdaCodeLink(); ok {
		return rt
	}
	switch {
	case !strings.HasPrefix(c.path, adminPrefix):
		c.decline(1, "the path is not under %s", adminPrefix)
	case h.opts.Admin != nil:
		return toAdmin{}
	default:
		c.decline(1, "the path is under %s, but no admin handler is configured", adminPrefix)
	}
	if first, _, _ := strings.Cut(strings.TrimPrefix(c.path, "/"), "/"); first == localStackSegment {
		c.decline(1, "%s is under LocalStack's reserved /%s/ prefix, which understudy does not serve", c.path, localStackSegment)
		return toLocalStack{reasons: c.reasons}
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

// snsLinkPrefix is where the links SNS writes into notifications live
// (SigningCertURL, UnsubscribeURL, SubscribeURL): the reserved prefix, then
// the raw SNS operation that serves the link.
const snsLinkPrefix = adminPrefix + "sns/"

// snsLink is rule 1's SNS part: on the main listener, /_understudy/sns/<Op>
// goes to SNS's raw operation <Op>. The links carry no credentials; the
// operations serve them as AWS serves its own (ARCHITECTURE.md 4.1). Any
// other request goes on to the admin part of rule 1, which says why it
// declines.
func (c *claimer) snsLink() (route, bool) {
	rest, ok := strings.CutPrefix(c.path, snsLinkPrefix)
	if !ok || c.h.opts.Listener != ref.ListenerMain {
		return nil, false
	}
	name, _, _ := strings.Cut(rest, "/")
	op, ok := c.h.opts.Registry.Operation(ref.OperationID{Service: "sns", Name: name})
	if !ok || op.Kind != registry.Raw {
		c.decline(1, "%s names no raw SNS link operation (SigningCert, UnsubscribeLink, ConfirmSubscriptionLink)", c.path)
		return unclaimed{reasons: c.reasons}, true
	}
	return toLink{op: op}, true
}

// functionURLPathPrefix is the function URL path fallback,
// /_understudy/lambda-url/<url-id>/<path>, for clients that cannot resolve
// a function URL host.
const functionURLPathPrefix = adminPrefix + "lambda-url/"

// functionURLPath is rule 1's function URL part: on the main listener, the
// path fallback goes to Lambda's raw InvokeFunctionUrl operation, which
// resolves the url id from the path as it does from the host (rule 2).
// Any other request goes on to the admin part of rule 1.
func (c *claimer) functionURLPath() (route, bool) {
	if !strings.HasPrefix(c.path, functionURLPathPrefix) || c.h.opts.Listener != ref.ListenerMain {
		return nil, false
	}
	op, ok := c.h.opts.Registry.Operation(ref.OperationID{Service: "lambda", Name: "InvokeFunctionUrl"})
	if !ok || op.Kind != registry.Raw {
		c.decline(1, "%s is the function URL path fallback, but no raw lambda.InvokeFunctionUrl operation is registered", c.path)
		return unclaimed{reasons: c.reasons}, true
	}
	return toLink{op: op}, true
}

// lambdaCodePrefix is where GetFunction's Code.Location links live.
const lambdaCodePrefix = adminPrefix + "lambda-code/"

// lambdaCodeLink is rule 1's Lambda code part: on the main listener, a
// Code.Location link goes to Lambda's raw DownloadFunctionCode operation,
// which carries no credentials, as a presigned URL's signature is its own.
// Any other request goes on to the admin part of rule 1.
func (c *claimer) lambdaCodeLink() (route, bool) {
	if !strings.HasPrefix(c.path, lambdaCodePrefix) || c.h.opts.Listener != ref.ListenerMain {
		return nil, false
	}
	op, ok := c.h.opts.Registry.Operation(ref.OperationID{Service: "lambda", Name: "DownloadFunctionCode"})
	if !ok || op.Kind != registry.Raw {
		c.decline(1, "%s is a Lambda code link, but no raw lambda.DownloadFunctionCode operation is registered", c.path)
		return unclaimed{reasons: c.reasons}, true
	}
	return toLink{op: op}, true
}

// functionURLHost matches <url-id>.lambda-url.<region>.<base>.
var functionURLHost = regexp.MustCompile(`^[a-z0-9]+\.lambda-url\.[a-z0-9-]+\.`)

// functionURL is rule 2: a function URL host goes to Lambda's raw
// InvokeFunctionUrl operation.
func (c *claimer) functionURL() (route, bool) {
	if !functionURLHost.MatchString(c.host) {
		c.decline(2, "host %q is not a Lambda function URL host", c.host)
		return nil, false
	}
	op, ok := c.h.opts.Registry.Operation(ref.OperationID{Service: "lambda", Name: "InvokeFunctionUrl"})
	if !ok || op.Kind != registry.Raw {
		c.decline(2, "host %q names a Lambda function URL, but no raw lambda.InvokeFunctionUrl operation is registered", c.host)
		return nil, false
	}
	return toLink{op: op}, true
}

// accountHost matches <12 digits>.<base>, the S3 Control authority.
var accountHost = regexp.MustCompile(`^\d{12}\.`)

// s3ControlTag names the raw S3 operation that serves S3 Control tagging
// at /v20180820/tags/{ResourceArn+} for method (s3.md 4.2.9).
func s3ControlTag(method string) (string, bool) {
	switch method {
	case http.MethodGet:
		return "ListTagsForResource", true
	case http.MethodPost:
		return "TagResource", true
	case http.MethodDelete:
		return "UntagResource", true
	}
	return "", false
}

// s3Control is rule 3: S3 Control tagging on <account>.<base> with a
// /v20180820/tags/ path goes to S3's raw tag operations, whose errors
// Options.S3Control renders. understudy serves no other S3 Control
// operation, so any other /v20180820/ request on that host is unclaimed.
func (c *claimer) s3Control() (route, bool) {
	if !accountHost.MatchString(c.host) || !strings.HasPrefix(c.path, "/v20180820/") {
		c.decline(3, "not an S3 Control request (<account>.<base> host and a /v20180820/ path)")
		return nil, false
	}
	name, ok := s3ControlTag(c.r.Method)
	if !strings.HasPrefix(c.path, "/v20180820/tags/") || !ok {
		c.decline(3, "%s %s is an S3 Control request, but understudy serves only its tag operations: GET, POST and DELETE /v20180820/tags/{ResourceArn}", c.r.Method, c.path)
		return unclaimed{reasons: c.reasons}, true
	}
	op, ok := c.h.opts.Registry.Operation(ref.OperationID{Service: "s3", Name: name})
	if !ok || op.Kind != registry.Raw {
		c.decline(3, "%s %s is S3 Control's %s, but no raw s3.%s operation is registered", c.r.Method, c.path, name, name)
		return unclaimed{reasons: c.reasons}, true
	}
	return toRaw{op: op, errors: c.h.opts.S3Control}, true
}

// smithyProtocolPath matches rpcv2Cbor's /service/<Name>/operation/<Op>,
// after any path prefix, which the protocol allows and the codec ignores.
var smithyProtocolPath = regexp.MustCompile(`^(?:/.*)?/service/[^/]+/operation/[^/]+$`)

// rpcv2cbor is rule 5. A request with both Smithy-Protocol and X-Amz-Target
// is ambiguous and is rejected outright.
func (c *claimer) rpcv2cbor() (route, bool) {
	if !smithyProtocolHeader(c.r) || !smithyProtocolPath.MatchString(c.path) {
		c.decline(5, "no Smithy-Protocol: rpc-v2-cbor header with a /service/<name>/operation/<op> path")
		return nil, false
	}
	if c.r.Header.Get("X-Amz-Target") != "" {
		c.decline(5, "the request carries both Smithy-Protocol: rpc-v2-cbor and X-Amz-Target, so its protocol is ambiguous; send one")
		return unclaimed{reasons: c.reasons}, true
	}
	return c.codecClaim(ref.RPCv2CBOR, 5)
}

// awsJSON is rule 6: X-Amz-Target names the service and operation, cross-
// checked against the signing name.
func (c *claimer) awsJSON() (route, bool) {
	if c.r.Header.Get("X-Amz-Target") == "" {
		c.decline(6, "no X-Amz-Target header")
		return nil, false
	}
	for _, p := range []ref.ProtocolID{ref.AWSJSON10, ref.AWSJSON11} {
		if rt, ok := c.codecClaim(p, 6); ok {
			return rt, true
		}
	}
	return nil, false
}

// awsQuery is rule 7: a form body, or a GET with Action.
func (c *claimer) awsQuery() (route, bool) {
	if !queryShaped(c.r) {
		c.decline(7, "neither a form body nor a GET with Action")
		return nil, false
	}
	return c.codecClaim(ref.AWSQuery, 7)
}

// smithyProtocolHeader reports whether r names rpcv2Cbor in its
// Smithy-Protocol header.
func smithyProtocolHeader(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Smithy-Protocol"), "rpc-v2-cbor")
}

// queryShaped reports whether r has awsQuery's shape: a form body, or a GET
// with Action.
func queryShaped(r *http.Request) bool {
	if mediaType(r.Header.Get("Content-Type")) == "application/x-www-form-urlencoded" {
		return true
	}
	return r.Method == http.MethodGet && r.URL.Query().Has("Action")
}

// codecClaim asks the codec registered for p and checks the claimed service
// against the signing name.
func (c *claimer) codecClaim(p ref.ProtocolID, rule int) (route, bool) {
	codec, ok := c.h.opts.Codecs[p]
	if !ok {
		c.decline(rule, "no %s codec is registered", p)
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
		c.decline(rule, "the %s codec declined: %s", p, cl.Reason)
		return nil, false
	}
	if signed, ok := signingName(c.auth); ok && signed != svc.SigningName {
		c.decline(rule, "the request names %s service %s but is signed for %q", p, svc.ID, signed)
		return nil, false
	}
	return rt, true
}

// datePath matches the /YYYY-MM-DD/ prefix of Lambda's REST paths.
var datePath = regexp.MustCompile(`^/\d{4}-\d{2}-\d{2}/`)

// rest is rule 8: Lambda's and S3's route tables, and the rule that a
// request signed for another service is that service's to answer, never
// S3's.
func (c *claimer) rest() (route, bool) {
	signed, isSigned := signingName(c.auth)
	_, presigned := c.auth.(auth.SigV4Query)
	_, sigV2Header := c.auth.(auth.SigV2Header)
	_, sigV2Query := c.auth.(auth.SigV2Query)
	switch {
	case signed == "lambda" && !presigned:
		return c.restService("lambda", ref.RESTJSON1, "Lambda", c.r, c.path)
	case signed == "s3" || presigned || sigV2Header || sigV2Query:
		return c.s3("S3")
	case isSigned:
		return c.signedElsewhere(signed)
	case refusedForS3(c.parseErr):
		return c.s3("S3, whose credentials S3 alone refuses")
	case c.auth == nil && !unknownScheme(c.parseErr):
		c.decline(8, "the request's credentials cannot be read, so it cannot be routed by signing name")
		return nil, false
	}
	if _, virtual := c.virtualHostBucket(); !virtual && datePath.MatchString(c.path) && !c.bucketExists() {
		if rt, ok := c.unsignedLambda(); ok {
			return rt, true
		}
	}
	return c.s3("unsigned S3")
}

// refusedForS3 reports whether e is a parse failure of credentials only S3
// takes, which S3 answers in its own words (DEFERRED.md "S3 auth"), so the
// request reaches S3 although no signing name could be read: a
// header-signed request scoped to s3 without x-amz-content-sha256 or with
// an unknown region, and every presigned or SigV2 credential, as rule 8
// sends any presigned query credential or SigV2 request to S3. A conflict
// always involves a query credential.
func refusedForS3(e auth.ParseError) bool {
	switch e := e.(type) {
	case auth.Invalid:
		return e.Problem == auth.MissingPayloadHash || e.Mechanism != auth.HeaderV4
	case auth.UnknownRegion:
		return e.Mechanism != auth.HeaderV4 || e.Service == "s3"
	case auth.MissingQueryParameters, auth.Conflict:
		return true
	case auth.UnknownScheme, nil:
	}
	return false
}

// unknownScheme reports an Authorization header in no AWS scheme: the
// request carries no AWS credentials, so it is routed as unsigned, and the
// service it reaches answers the header in its own words.
func unknownScheme(e auth.ParseError) bool {
	_, ok := e.(auth.UnknownScheme)
	return ok
}

// unsignedLambda claims an unsigned date path for Lambda only when it
// matches a Lambda route; anything else is S3 path-style.
func (c *claimer) unsignedLambda() (route, bool) {
	const what = "unsigned Lambda path"
	svc, codec, ok := c.restTable("lambda", ref.RESTJSON1, what)
	if !ok {
		return nil, false
	}
	rt, ok := svc.Routes.Match(c.r.Method, c.path, c.r.URL.Query(), c.r.Header)
	if !ok {
		c.decline(8, "%s: %s %s matches no Lambda route", what, c.r.Method, c.path)
		return nil, false
	}
	return toOperation{codec: codec, op: rt.Operation, req: c.r}, true
}

// s3 matches the request against S3's route table in its path-style form:
// a virtual-hosted request (ADR 0013) has its bucket label put in front of
// the path, so both addressings reach the same route and the S3 codec
// binds the bucket one way. what says which rule 8 case sent it.
func (c *claimer) s3(what string) (route, bool) {
	bucket, virtual := c.virtualHostBucket()
	if !virtual {
		return c.restService("s3", ref.RESTXML, what, c.r, c.path)
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
	return c.restService("s3", ref.RESTXML, what+" (virtual-hosted bucket "+bucket+")", r, path)
}

// signedElsewhere answers a request signed for a service no rule claimed:
// a registered service answers with its own error, never S3's (ADR 0013),
// and any other signing name names a service understudy does not emulate.
func (c *claimer) signedElsewhere(signed string) (route, bool) {
	svc, ok := c.h.opts.Registry.BySigningName(signed)
	if !ok {
		c.decline(8, "the request is signed for %q, which names no registered service", signed)
		return notEmulated{signingName: signed, reasons: c.reasons}, true
	}
	c.decline(8, "the request is signed for %s, which no protocol rule claimed", svc.ID)
	return toService{svc: svc, reasons: c.reasons}, true
}

// restService matches r, whose escaped path is path, against a REST
// service's route table when that service and its codec are registered; a
// request no route matches is the service's unknown operation.
func (c *claimer) restService(id ref.ServiceID, p ref.ProtocolID, what string, r *http.Request, path string) (route, bool) {
	svc, codec, ok := c.restTable(id, p, what)
	if !ok {
		return nil, false
	}
	if rt, ok := svc.Routes.Match(r.Method, path, r.URL.Query(), r.Header); ok {
		return toOperation{codec: codec, op: rt.Operation, req: r}, true
	}
	return toUnknown{codec: codec, svc: svc, req: r}, true
}

// restTable returns a REST service and its codec, declining rule 8 for the
// case what names when either is not registered.
func (c *claimer) restTable(id ref.ServiceID, p ref.ProtocolID, what string) (*registry.Service, Codec, bool) {
	svc, ok := c.h.opts.Registry.Service(id)
	if !ok || svc.Routes == nil {
		c.decline(8, "%s: no %s route table is registered", what, id)
		return nil, nil, false
	}
	codec, ok := c.h.opts.Codecs[p]
	if !ok {
		c.decline(8, "%s: no %s codec is registered", what, p)
		return nil, nil, false
	}
	return svc, codec, true
}

// virtualHostBucket returns the bucket label of a virtual-hosted S3 host:
// the label in front of the longest base domain, when it is a DNS-valid
// bucket name (ADR 0013). The base domains are the configured ones and
// their regional forms, s3.<region>.<domain> (regionalBase); a host that
// is a regional base itself is path style.
func (c *claimer) virtualHostBucket() (string, bool) {
	best := ""
	consider := func(base string) {
		if strings.HasSuffix(c.host, "."+base) && len(base) > len(best) {
			best = base
		}
	}
	for _, base := range c.h.opts.S3BaseDomains {
		regional, ok := c.regionalBase(base)
		if ok && c.host == regional {
			return "", false
		}
		consider(base)
		if ok {
			consider(regional)
		}
	}
	if best == "" {
		return "", false
	}
	bucket := strings.TrimSuffix(c.host, "."+best)
	return bucket, dnsBucket.MatchString(bucket)
}

// regionalBase is the regional S3 base domain the host is or ends in for
// the configured base: s3.<region>.<domain>, where domain is base without
// a leading "s3." label and region is a name Regions accepts. Both
// localhost and s3.localhost give s3.us-west-2.localhost.
func (c *claimer) regionalBase(base string) (string, bool) {
	domain := strings.TrimPrefix(base, "s3.")
	front, ok := strings.CutSuffix(c.host, "."+domain)
	if !ok {
		return "", false
	}
	labels := strings.Split(front, ".")
	if len(labels) < 2 || labels[len(labels)-2] != "s3" {
		return "", false
	}
	region := labels[len(labels)-1]
	if _, err := c.h.opts.Regions.Parse(region); err != nil {
		return "", false
	}
	return "s3." + region + "." + domain, true
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
