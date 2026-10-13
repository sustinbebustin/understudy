package lambda

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/service/lambda/furl"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
	"github.com/sustinbebustin/understudy/internal/store"
)

const urlNote = "BUFFERED only: a RESPONSE_STREAM URL answers buffered; AWS_IAM requires a SigV4 signature, verified under sigv4 verify, and IAM policies are not enforced"

// functionURL is the URL a function URL configuration answers on:
// http://<url-id>.lambda-url.<region>.localhost:<port>/, on the port the
// client addressed (ARCHITECTURE.md 14.7). The edge routes the host form
// to InvokeFunctionUrl, which BindFunctionURLs binds.
func functionURL(e kernel.Endpoint, scope aws.Scope, id string) string {
	host := id + ".lambda-url." + scope.Region.String() + ".localhost"
	if _, port, err := net.SplitHostPort(e.Host); err == nil && port != "" {
		host = net.JoinHostPort(host, port)
	}
	return "http://" + host + "/"
}

// SigV4Mode is the sigv4 switch as it applies to function URLs with
// AuthType AWS_IAM.
type SigV4Mode uint8

// SigV4Mode values. The zero value is SigV4Parse.
const (
	// SigV4Parse requires a SigV4 signature but does not check it, as
	// understudy parses every request's credentials without verifying
	// them by default.
	SigV4Parse SigV4Mode = iota
	// SigV4Verify also checks the signature against its key's secret; a
	// key with none is refused. A call the edge already verified (sigv4
	// verify in the server, kernel.Call.VerifiedAuth) is not checked
	// again, so this mode matters only to a Lambda served behind an edge
	// that does not verify. Its presigned check reads X-Amz-Date as an
	// emulator instant (auth.VerifyPresigned), right only while the
	// emulator clock has not moved away from wall time; the edge places
	// the signing on the emulator timeline instead.
	SigV4Verify
)

// FunctionURLOptions configure the function URL data plane. The zero value
// parses signatures without verifying them, against AWS's region table.
type FunctionURLOptions struct {
	SigV4 SigV4Mode
	// Regions is the credential-scope region table, as the edge's.
	Regions aws.Regions
}

// BindFunctionURLs binds InvokeFunctionUrl, the function URL data plane,
// to x: the raw operation the edge routes function URL requests to, so
// each request is journaled, faultable and recorded as
// lambda:InvokeFunctionUrl. d is the Lambda service's dependencies, as
// NewExecution took them.
func (x *Execution) BindFunctionURLs(d kernel.ServiceDeps, o FunctionURLOptions) {
	u := &urlServer{x: x, identity: d.Identity, opts: o}
	lambdaapi.NewBinder(d.Binder).InvokeFunctionUrl(u.serve, partial(urlNote))
}

// urlServer serves function URLs for an Execution.
type urlServer struct {
	x        *Execution
	identity *identity.Store
	opts     FunctionURLOptions
}

// served is a function URL configuration as serving it needs it, copied
// out of the state.
type served struct {
	ref      functionRef
	authType lambdaapi.FunctionUrlAuthType
	cors     *furl.CORS
	// policy is the stored resource policy document of ref's qualifier;
	// empty when there is none. authorize parses it, so a corrupt one is
	// reported rather than read as no policy.
	policy string
}

// serve is InvokeFunctionUrl (lambda-events.md 7): it finds the URL the
// request names, applies its auth type and CORS configuration, invokes the
// function synchronously with the payload 2.0 event and answers with the
// response inferred from its output. Refusals AWS words itself are
// written here; failures of the emulator and throttles are returned for
// the edge to render in Lambda's error format.
func (u *urlServer) serve(ctx context.Context, c *kernel.Call, r *http.Request, w http.ResponseWriter) *awserr.Error {
	request := ids.LambdaRequestID(c.ID())
	w.Header().Set("X-Amzn-Requestid", string(request))
	addr, ok := furl.Resolve(r.Host, escapedPath(r))
	if !ok {
		furl.UnknownURL().Write(w)
		return nil
	}
	var s served
	switch found := u.x.svc.findURL(addr).(type) {
	case urlLive:
		s = found.served
	case urlRetired:
		furl.Forbidden().Write(w)
		return nil
	case urlUnknown:
		furl.UnknownURL().Write(w)
		return nil
	}
	touch(c, s.ref.qualifiedARN())
	// Browsers never sign a preflight, so it is answered before the auth
	// type applies (furl/doc.go); it never runs the function.
	if s.cors != nil {
		if resp, ok := s.cors.Preflight(r); ok {
			resp.Write(w)
			return nil
		}
	}
	body, err := readBody(r)
	if err != nil {
		return err
	}
	verdict, err := u.authorize(c, r, s, addr, body)
	if err != nil {
		return err
	}
	var caller furl.Caller
	switch v := verdict.(type) {
	case refused:
		v.resp.Write(w)
		return nil
	case authorized:
		caller = v.caller
	}
	resp, err := u.invoke(ctx, c, r, request, s, addr, caller, body)
	if err != nil {
		return err
	}
	if s.cors != nil {
		s.cors.Decorate(&resp, r.Header.Get("Origin"))
	}
	resp.Write(w)
	return nil
}

// invoke runs the function for a request with its payload 2.0 event and
// infers the response: a function error or a malformed response is 502.
// request is the invocation's request id.
func (u *urlServer) invoke(ctx context.Context, c *kernel.Call, r *http.Request, request ids.LambdaRequestID, s served, addr furl.Address, caller furl.Caller, body []byte) (furl.Response, *awserr.Error) {
	// Lambda's front end always adds x-amzn-trace-id: the client's trace,
	// or else one derived as runIn derives the invocation's, at
	// admission. The function runs under the same trace.
	trace := c.Trace()
	if trace == "" {
		trace = traceID(request, c.Admitted().Wire().Unix())
	}
	payload, perr := furl.Event(eventRequest(r, addr, body), furl.Context{
		URLID:      addr.ID,
		DomainName: addr.Domain(s.ref.scope.Region.String()),
		RequestID:  string(request),
		At:         c.Admitted().Wire(),
		TraceID:    trace,
		Caller:     caller,
	})
	if perr != nil {
		return furl.Response{}, errService("Lambda could not encode the function URL request event for " + s.ref.qualifiedARN() + ": " + perr.Error())
	}
	if len(payload) > maxSyncRequest {
		return furl.Response{}, errPayloadTooLarge(maxSyncRequest)
	}
	t, err := u.x.resolve(s.ref)
	if err != nil {
		return furl.Response{}, err
	}
	res, err := u.x.runSync(outliveCaller(ctx), c, t, syncRun{payload: payload, trace: trace})
	if err != nil {
		return furl.Response{}, err
	}
	success, ok := res.outcome.(runtimeapi.Success)
	if !ok {
		return furl.BadGateway(), nil
	}
	resp, ierr := furl.Infer(success.Payload)
	if ierr != nil {
		u.x.k.Log().LogAttrs(ctx, slog.LevelWarn, "function URL answered 502: the function's response is malformed",
			slog.String("function", s.ref.qualifiedARN()), slog.String("problem", ierr.Error()))
		return furl.BadGateway(), nil
	}
	return resp, nil
}

// authorization is authorize's verdict on a request.
//
//sumtype:decl
type authorization interface{ isAuthorization() }

// authorized lets the request run the function as caller.
type authorized struct{ caller furl.Caller }

// refused answers the request with resp, a 403, without running the
// function.
type refused struct{ resp furl.Response }

func (authorized) isAuthorization() {}
func (refused) isAuthorization()    {}

// authorize applies the URL's auth type (lambda-events.md 7.2). NONE
// evaluates the resource policy (public); AWS_IAM requires a SigV4
// signature, checked under SigV4Verify. The error is a URL configuration
// understudy stored but cannot apply: an unknown auth type or a resource
// policy it cannot parse.
func (u *urlServer) authorize(c *kernel.Call, r *http.Request, s served, addr furl.Address, body []byte) (authorization, *awserr.Error) {
	switch s.authType {
	case lambdaapi.FunctionUrlAuthTypeNone:
		return public(s)
	case lambdaapi.FunctionUrlAuthTypeAwsIam:
		return u.signed(c, r, addr, body), nil
	}
	return nil, errService("Lambda cannot apply the function URL of " + s.ref.qualifiedARN() + ": its stored AuthType " + strconv.Quote(string(s.authType)) +
		" is neither NONE nor AWS_IAM. This is an understudy bug; the function did not run. UpdateFunctionUrlConfig with a valid AuthType rewrites it.")
}

// public authorizes a request to an AuthType NONE URL. Its resource policy
// must allow the public principal both lambda:InvokeFunctionUrl and
// lambda:InvokeFunction, in a request made through the URL: with
// lambda:FunctionUrlAuthType NONE and lambda:InvokedViaFunctionUrl true.
// That is the rule AWS applies since October 2025 (lambda-api.md finding
// 7); the recorded fixtures, which add only the first statement, predate
// it, and Terraform's aws_lambda_function_url adds both. The error is a
// stored policy that cannot be parsed, reported rather than read as no
// policy.
func public(s served) (authorization, *awserr.Error) {
	var doc policydoc.Document
	if s.policy != "" {
		parsed, perr := policydoc.Parse([]byte(s.policy))
		if perr != nil {
			return nil, errService("Lambda could not read the stored resource policy of " + s.ref.qualifiedARN() + ": " + perr.Error() +
				". This is an understudy bug; the function did not run. GetPolicy returns the stored document, and RemovePermission and AddPermission rewrite it.")
		}
		doc = parsed
	}
	keys := map[string]string{
		"lambda:FunctionUrlAuthType":   string(lambdaapi.FunctionUrlAuthTypeNone),
		"lambda:InvokedViaFunctionUrl": "true",
	}
	for _, action := range []string{"lambda:InvokeFunctionUrl", "lambda:InvokeFunction"} {
		q := policydoc.Query{Principal: policydoc.Anonymous{}, Action: action, Resource: s.ref.qualifiedARN(), Context: keys}
		if _, ok := policydoc.Allows(doc, q).(policydoc.Allowed); !ok {
			return refused{furl.Forbidden()}, nil
		}
	}
	return authorized{furl.Public{}}, nil
}

// sigV4 is the SigV4 signature an AWS_IAM request carries, in its
// Authorization header or presigned in its query: its credential, and
// check, which verifies the signature as its form requires.
type sigV4 struct {
	cred  auth.Credential
	check func(req auth.Normalized, secret string, clk *clock.Clock, v auth.Vocabulary) *awserr.Error
}

// signed authorizes a request to an AuthType AWS_IAM URL: it must carry a
// SigV4 signature, which is verified under SigV4Verify.
func (u *urlServer) signed(c *kernel.Call, r *http.Request, addr furl.Address, body []byte) authorization {
	a, perr := auth.Parse(r, u.opts.Regions)
	if perr != nil {
		return refused{furl.Forbidden()}
	}
	var sig sigV4
	switch a := a.(type) {
	case auth.SigV4Header:
		sig = sigV4{cred: a.Credential, check: func(req auth.Normalized, secret string, clk *clock.Clock, v auth.Vocabulary) *awserr.Error {
			return auth.Verify(a, req, secret, clk.Wall(), v)
		}}
	case auth.SigV4Query:
		sig = sigV4{cred: a.Credential, check: func(req auth.Normalized, secret string, clk *clock.Clock, v auth.Vocabulary) *awserr.Error {
			return auth.VerifyPresigned(a, req, secret, clk.Wall(), clk.Now(), v)
		}}
	case auth.SigV2Header, auth.SigV2Query, auth.Anonymous:
		return refused{furl.Forbidden()}
	}
	if _, verified := c.VerifiedAuth(); verified {
		// The edge verified the signature, the session token and a
		// presigned URL's expiry on the emulator timeline (sigv4 verify);
		// only the scope's service and region are the URL's to check.
		if refusal := scoped(sig.cred, addr); refusal != nil {
			return refused{*refusal}
		}
	} else if u.opts.SigV4 == SigV4Verify {
		if refusal := u.verify(r, sig, addr, body); refusal != nil {
			return refused{*refusal}
		}
	}
	return authorized{iamCaller(c.Principal(), sig.cred.Key)}
}

// verify checks a SigV4 request's credential scope against the URL, then
// its signature against the secret of its key, and a temporary key's
// session token. A key with no secret (identity.SecretOf) is refused, as
// the edge refuses it.
func (u *urlServer) verify(r *http.Request, sig sigV4, addr furl.Address, body []byte) *furl.Response {
	if refusal := scoped(sig.cred, addr); refusal != nil {
		return refusal
	}
	_, res := u.identity.Resolve(sig.cred.Key)
	secret, ok := identity.SecretOf(res)
	if !ok {
		return new(furl.Denied("UnrecognizedClientException", "The security token included in the request is invalid."))
	}
	if minted, ok := res.(identity.Minted); ok {
		if t, ok := minted.Lifetime.(identity.Temporary); ok && sessionToken(r) != string(t.SessionToken) {
			return new(furl.Denied("UnrecognizedClientException", "The security token included in the request is invalid."))
		}
	}
	v, ok := auth.VocabularyFor(awserr.JSONFamily)
	if !ok {
		// auth defines a vocabulary for every family; refuse rather than
		// verify with empty error identities.
		return new(furl.Denied("AccessDeniedException", "understudy could not verify this signature: auth has no JSON family vocabulary. This is an understudy bug; the function did not run. Serving with sigv4 parse skips verification."))
	}
	req := auth.Normalize(r, auth.Hashed{SHA256: sha256.Sum256(body)})
	if err := sig.check(req, string(secret), u.x.k.Clock(), v); err != nil {
		return new(furl.Denied(err.ID.Shape.Name(), err.Message))
	}
	return nil
}

// scoped refuses a SigV4 credential not scoped to lambda or, for the host
// form, to another region than the URL's; the path fallback names no
// region. The wording follows AWS's SigV4 front end and is UNVERIFIED for
// function URLs (furl/doc.go).
func scoped(cred auth.Credential, addr furl.Address) *furl.Response {
	if cred.Service != "lambda" {
		return new(furl.Denied("InvalidSignatureException", "Credential should be scoped to correct service: 'lambda'. "))
	}
	if addr.Region != "" && cred.Region.String() != addr.Region {
		return new(furl.Denied("InvalidSignatureException", "Credential should be scoped to a valid region, not '"+cred.Region.String()+"'. "))
	}
	return nil
}

// sessionToken is the X-Amz-Security-Token a request carries, in its
// header or, presigned, its query.
func sessionToken(r *http.Request) string {
	if t := r.Header.Get("X-Amz-Security-Token"); t != "" {
		return t
	}
	return r.URL.Query().Get("X-Amz-Security-Token")
}

// iamCaller is the signer of an AWS_IAM request, as the event reports it
// under requestContext.authorizer.iam.
func iamCaller(p identity.Principal, key string) furl.IAMCaller {
	account, _ := identity.AccountOf(p)
	c := furl.IAMCaller{AccessKey: key, AccountID: account.String(), UserARN: identity.Render(p)}
	switch p := p.(type) {
	case identity.User:
		c.ID = p.ID
	case identity.AssumedRole:
		c.ID = p.Role.ID + ":" + p.Session
	case identity.Root:
		c.ID = account.String()
	case identity.ServicePrincipal, identity.Anonymous:
	}
	return c
}

// urlLookup is what a url id names.
//
//sumtype:decl
type urlLookup interface{ isURLLookup() }

// urlLive is a function URL configuration that exists.
type urlLive struct{ served served }

// urlRetired is the id of a deleted function URL configuration.
type urlRetired struct{}

// urlUnknown is an id no function URL configuration ever had.
type urlUnknown struct{}

func (urlLive) isURLLookup()    {}
func (urlRetired) isURLLookup() {}
func (urlUnknown) isURLLookup() {}

// findURL finds the function URL configuration addr names, in any scope
// of the region addr's host names, or of any region for the path fallback.
func (s *Service) findURL(addr furl.Address) urlLookup {
	var found urlLookup = urlUnknown{}
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		if addr.Region != "" && scope.Region.String() != addr.Region {
			continue
		}
		cell, ok := s.cells.Lookup(scope)
		if !ok {
			continue
		}
		switch got := store.Read(cell, func(st *State) urlLookup { return st.urlByID(scope, addr.ID) }).(type) {
		case urlLive:
			return got
		case urlRetired:
			found = got
		case urlUnknown:
		}
	}
	return found
}

// urlByID finds the URL configuration whose id is id, copied so it shares
// nothing with the state, or the deleted one that had it.
func (st *State) urlByID(scope aws.Scope, id string) urlLookup {
	if st.RetiredURLs[id] {
		return urlRetired{}
	}
	for name := range st.Functions {
		f := st.Functions[name]
		for qualifier, u := range f.URLs {
			if u.ID != id {
				continue
			}
			s := served{
				ref:      functionRef{scope: scope, name: name, qualifier: qualifier},
				authType: lambdaapi.FunctionUrlAuthType(u.AuthType),
				cors:     u.Cors.forURL(),
			}
			if p, ok := f.Policies[qualifier]; ok {
				s.policy = p.document()
			}
			return urlLive{served: s}
		}
	}
	return urlUnknown{}
}

// forURL is the CORS configuration as the URL applies it, sharing nothing
// with c; nil when none is configured.
func (c *Cors) forURL() *furl.CORS {
	if c == nil {
		return nil
	}
	out := &furl.CORS{
		AllowCredentials: c.AllowCredentials != nil && *c.AllowCredentials,
		AllowHeaders:     slices.Clone(c.AllowHeaders), AllowMethods: slices.Clone(c.AllowMethods),
		AllowOrigins: slices.Clone(c.AllowOrigins), ExposeHeaders: slices.Clone(c.ExposeHeaders),
	}
	if c.MaxAge != nil {
		out.MaxAge = new(*c.MaxAge)
	}
	return out
}

// readBody reads a request's body within the synchronous invocation limit.
func readBody(r *http.Request) ([]byte, *awserr.Error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSyncRequest+1))
	if err != nil {
		return nil, awserr.RequestAborted()
	}
	if len(body) > maxSyncRequest {
		return nil, errPayloadTooLarge(maxSyncRequest)
	}
	return body, nil
}

// eventRequest is r as the request event reports it.
func eventRequest(r *http.Request, addr furl.Address, body []byte) furl.Request {
	scheme, port := "http", "80"
	if r.TLS != nil {
		scheme, port = "https", "443"
	}
	if _, p, err := net.SplitHostPort(r.Host); err == nil && p != "" {
		port = p
	}
	source := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		source = host
	}
	return furl.Request{
		Method: r.Method, RawPath: addr.RawPath, RawQuery: r.URL.RawQuery, Protocol: r.Proto,
		Host: r.Host, Header: r.Header, SourceIP: source, Scheme: scheme, Port: port, Body: body,
	}
}

// escapedPath is the escaped path exactly as the client sent it: the
// request target up to its query.
func escapedPath(r *http.Request) string {
	if strings.HasPrefix(r.RequestURI, "/") {
		path, _, _ := strings.Cut(r.RequestURI, "?")
		return path
	}
	return r.URL.EscapedPath()
}
