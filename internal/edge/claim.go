package edge

import (
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"regexp"
	"slices"
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
// Lambda code link (rule 1), a function URL by path (rule 1) or by host
// (rule 2), or the execute-api path fallback (rule 1). The edge's browser
// defences never refuse one. policy is cspSandbox for the path fallbacks,
// a user's page on understudy's origin (ADR 0020).
type toLink struct {
	op     *registry.Operation
	policy csp
}

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

// notEmulatedTarget is an operation of a service understudy does not
// emulate that X-Amz-Target names (rule 6).
type notEmulatedTarget struct {
	target    unemulatedTarget
	operation string
	reasons   []reason
}

// unclaimed is a request no rule claimed (rule 9), or one rule 5 rejected.
type unclaimed struct{ reasons []reason }

// toLocalStack is a path under LocalStack's reserved prefix (rule 1), such
// as a migrated health check's /_localstack/health. reasons are the rules
// passed over before it, for the Unclaimed journal event.
type toLocalStack struct{ reasons []reason }

// toExecuteAPI is API Gateway's execute-api data plane, by host (rule 2),
// or by the path fallback (rule 1) when no raw apigatewayv2.InvokeApi is
// registered: it is claimed so S3 never answers it, and answered 501. reasons end with
// the claiming rule's, for the Unclaimed journal event. policy is
// cspSandbox for the path fallback, which an integration controls on
// understudy's origin (ADR 0020).
type toExecuteAPI struct {
	reasons []reason
	policy  csp
}

func (toAdmin) isRoute()           {}
func (toOperation) isRoute()       {}
func (toUnknown) isRoute()         {}
func (toRaw) isRoute()             {}
func (toLink) isRoute()            {}
func (toService) isRoute()         {}
func (notEmulated) isRoute()       {}
func (notEmulatedTarget) isRoute() {}
func (unclaimed) isRoute()         {}
func (toLocalStack) isRoute()      {}
func (toExecuteAPI) isRoute()      {}

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

// adminPrefix is reserved for understudy: the admin API, SNS links, Lambda
// code links and the function URL and execute-api path fallbacks. Bucket
// names cannot start with "_", so it never shadows S3.
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
	if rt, ok := c.executeAPIPath(); ok {
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
	if rt, ok := c.executeAPI(); ok {
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
// resolves the url id from the path as it does from the host (rule 2),
// sandboxed, since the function's page shares understudy's origin (ADR
// 0020). Any other request goes on to the admin part of rule 1.
func (c *claimer) functionURLPath() (route, bool) {
	if !strings.HasPrefix(c.path, functionURLPathPrefix) || c.h.opts.Listener != ref.ListenerMain {
		return nil, false
	}
	op, ok := c.h.opts.Registry.Operation(ref.OperationID{Service: "lambda", Name: "InvokeFunctionUrl"})
	if !ok || op.Kind != registry.Raw {
		c.decline(1, "%s is the function URL path fallback, but no raw lambda.InvokeFunctionUrl operation is registered", c.path)
		return unclaimed{reasons: c.reasons}, true
	}
	return toLink{op: op, policy: cspSandbox}, true
}

// executeAPIPathPrefix is the execute-api path fallback,
// /_understudy/execute-api/<api-id>/<path>, for clients that cannot resolve
// an execute-api host (apigateway-http.md 11.3).
const executeAPIPathPrefix = adminPrefix + "execute-api/"

// executeAPIPath is rule 1's execute-api part: on the main listener, the
// path fallback is API Gateway's data plane, whatever follows the prefix
// and whatever the credentials, sandboxed, since its pages share
// understudy's origin (ADR 0020). It goes to the raw apigatewayv2.InvokeApi
// operation, which resolves the API id from the path, when the registry
// holds one; else it is answered 501. Any other request goes on to the
// admin part of rule 1.
func (c *claimer) executeAPIPath() (route, bool) {
	if !strings.HasPrefix(c.path, executeAPIPathPrefix) || c.h.opts.Listener != ref.ListenerMain {
		return nil, false
	}
	if op, ok := c.h.opts.Registry.Operation(ref.OperationID{Service: "apigatewayv2", Name: "InvokeApi"}); ok && op.Kind == registry.Raw {
		return toLink{op: op, policy: cspSandbox}, true
	}
	c.decline(1, "%s is the API Gateway execute-api path fallback, and understudy does not implement API Gateway invoke", c.path)
	return toExecuteAPI{reasons: c.reasons, policy: cspSandbox}, true
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

// executeAPIHost matches <api-id>.execute-api.<rest>; isExecuteAPIHost
// checks that rest is <region>.<domain> or <domain>.
var executeAPIHost = regexp.MustCompile(`^[a-z0-9]+\.execute-api\.(.+)$`)

// isExecuteAPIHost reports whether the host is
// <api-id>.execute-api.<region>.<domain> or <api-id>.execute-api.<domain>,
// domain being a configured base domain without a leading "s3." label, as
// for regionalBase. Host detection is configuration on the base domains
// (apigateway-http.md 11.3), so the claim ends at one: a bucket named
// x.execute-api.y is S3's on its regional virtual host
// x.execute-api.y.s3.<region>.<domain>, as on AWS.
func (c *claimer) isExecuteAPIHost() bool {
	m := executeAPIHost.FindStringSubmatch(c.host)
	if m == nil {
		return false
	}
	rest := m[1]
	for _, base := range c.h.opts.S3BaseDomains {
		domain := strings.TrimPrefix(base, "s3.")
		if rest == domain {
			return true
		}
		region, ok := strings.CutSuffix(rest, "."+domain)
		if !ok || strings.Contains(region, ".") {
			continue
		}
		if _, err := c.h.opts.Regions.Parse(region); err == nil {
			return true
		}
	}
	return false
}

// executeAPI is rule 2's execute-api part: an execute-api host is API
// Gateway's data plane whatever the credentials, never an S3
// virtual-hosted bucket, as AWS serves those hosts apart from S3
// (apigateway-http.md 4.1, ADR 0013). A bucket of that name stays
// reachable path style.
func (c *claimer) executeAPI() (route, bool) {
	if !c.isExecuteAPIHost() {
		return nil, false
	}
	c.decline(2, "host %q is an API Gateway execute-api host, and understudy does not implement API Gateway invoke", c.host)
	return toExecuteAPI{reasons: c.reasons}, true
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

// awsJSON is rule 6: X-Amz-Target names the service and operation,
// cross-checked against the signing name.
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
	return c.unemulatedJSON()
}

// unemulatedTarget is a service understudy does not emulate whose awsJson
// requests carry the signing name of one it does, so rule 8 would answer
// them as that service's unknown operation.
type unemulatedTarget struct {
	// name is the service as its SDKs name it (sdkId).
	name string
	// coverage is the emulated service whose coverage says what understudy
	// does with this one.
	coverage     ref.ServiceID
	protocol     ref.ProtocolID
	signingNames []string
	operations   []string
}

// unemulated returns the service an X-Amz-Target prefix names among those
// understudy does not emulate. DynamoDB Streams is signed for dynamodb
// (dynamodb-streams.md 3.1, 3.2;
// testdata/parity/dynamodb/R11/07-streams-signed-dynamodb.json); its
// endpoint prefix streams.dynamodb, which AWS refuses, gets the same 501.
func unemulated(prefix string) (unemulatedTarget, bool) {
	if prefix != "DynamoDBStreams_20120810" {
		return unemulatedTarget{}, false
	}
	return unemulatedTarget{
		name: "DynamoDB Streams", coverage: "dynamodb", protocol: ref.AWSJSON10,
		signingNames: []string{"dynamodb", "streams.dynamodb"},
		operations:   []string{"DescribeStream", "GetRecords", "GetShardIterator", "ListStreams"},
	}, true
}

// unemulatedJSON is rule 6's last part: an awsJson request naming an
// operation of a service understudy does not emulate (unemulated), signed
// for it or unsigned, is that service's 501, so the signing name's service
// never answers it as its own unknown operation. A target naming no
// operation of it, or a request in another shape, goes on to rule 8.
func (c *claimer) unemulatedJSON() (route, bool) {
	prefix, name, _ := strings.Cut(c.r.Header.Get("X-Amz-Target"), ".")
	t, ok := unemulated(prefix)
	if !ok || !slices.Contains(t.operations, name) {
		return nil, false
	}
	if mt := mediaType(c.r.Header.Get("Content-Type")); c.r.Method != http.MethodPost || (mt != jsonMediaType(t.protocol) && mt != "application/json") {
		c.decline(6, "X-Amz-Target names %s.%s, but the request is not a POST in %s", t.name, name, t.protocol)
		return nil, false
	}
	if signed, ok := signingName(c.auth); ok && !slices.Contains(t.signingNames, signed) {
		c.decline(6, "X-Amz-Target names %s.%s but the request is signed for %q", t.name, name, signed)
		return nil, false
	}
	c.decline(6, "X-Amz-Target names %s.%s, and understudy does not emulate %s", t.name, name, t.name)
	return notEmulatedTarget{target: t, operation: name, reasons: c.reasons}, true
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
// S3's: the route tables of the REST services that share its signing name,
// else the first service of the name (ADR 0019).
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
// takes, which S3 answers in its own words (differences.md "S3 auth"), so the
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

// unknownScheme reports whether e is an Authorization header in no AWS
// scheme: the request carries no AWS credentials, so it is routed as
// unsigned, and the service it reaches answers the header in its own words.
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

// signedElsewhere answers a request signed for a service no earlier rule
// claimed. When REST services share the signing name, their route tables
// are tried in registration order and the first match wins; a request none
// matches is the first one's unknown operation, as a single REST service
// answers it (ADR 0019). Otherwise the first registered service of the name
// answers with its own error, never S3's (ADR 0013), and a name no service
// holds names a service understudy does not emulate.
func (c *claimer) signedElsewhere(signed string) (route, bool) {
	candidates := c.h.opts.Registry.BySigningName(signed)
	if len(candidates) == 0 {
		c.decline(8, "the request is signed for %q, which names no registered service", signed)
		return notEmulated{signingName: signed, reasons: c.reasons}, true
	}
	if rt, ok := c.restCandidates(candidates); ok {
		return rt, true
	}
	svc := candidates[0]
	c.decline(8, "the request is signed for %s, which no protocol rule claimed", svc.ID)
	return toService{svc: svc, reasons: c.reasons}, true
}

// restCandidates matches the request against the route table of each REST
// service among candidates, in order; false when none is a REST service
// with a route table. A candidate whose codec is not registered is
// skipped; with every one skipped the request is unclaimed (rule 9).
func (c *claimer) restCandidates(candidates []*registry.Service) (route, bool) {
	var unknown route
	found := false
	for _, svc := range candidates {
		p, isREST := restProtocol(svc)
		if !isREST || svc.Routes == nil {
			continue
		}
		found = true
		codec, ok := c.h.opts.Codecs[p]
		if !ok {
			c.decline(8, "%s: no %s codec is registered", svc.ID, p)
			continue
		}
		if matched, ok := svc.Routes.Match(c.r.Method, c.path, c.r.URL.Query(), c.r.Header); ok {
			return toOperation{codec: codec, op: matched.Operation, req: c.r}, true
		}
		if unknown == nil {
			unknown = toUnknown{codec: codec, svc: svc, req: c.r}
		}
	}
	switch {
	case !found:
		return nil, false
	case unknown == nil:
		return unclaimed{reasons: c.reasons}, true
	}
	return unknown, true
}

// restProtocol is the first of a service's protocols that is restJson1 or
// restXml, the one its route table is decoded in.
func restProtocol(svc *registry.Service) (ref.ProtocolID, bool) {
	for _, p := range svc.Protocols {
		if p == ref.RESTJSON1 || p == ref.RESTXML {
			return p, true
		}
	}
	return "", false
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
