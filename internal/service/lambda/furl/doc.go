// Package furl serves Lambda function URLs using the payload format version
// 2.0 (lambda-events.md 7): it resolves the URL a request names (Resolve),
// builds the request event (Event), infers the HTTP response from the
// function's output (Infer), applies a URL's CORS configuration (CORS) and
// words the answers Lambda's front end gives itself (BadGateway,
// UnknownURL, Forbidden, Denied). Event and Infer fill in a function URL's
// context around the payload format 2.0 builders of internal/httpevent,
// which API Gateway HTTP APIs share. It is pure: the lambda package finds
// the URL configuration, authorizes the request and runs the function.
//
// Decisions the research leaves open:
//
//   - Resolve reads a url id from its host,
//     <url-id>.lambda-url.<region>.<base>, or from the path fallback
//     /_understudy/lambda-url/<url-id>/<path>. The fallback names no region
//     and reports the host form under localhost as domainName. The edge
//     routes both to InvokeFunctionUrl, the fallback on the main listener
//     only (ARCHITECTURE.md 4.1 rule 1).
//   - requestContext.accountId is "anonymous" for an AuthType NONE URL, as
//     LocalStack reports it, and the signer's account for AWS_IAM, which
//     also fills authorizer.iam. pathParameters, stageVariables and
//     requestContext.authentication are left out, as the Lambda guide
//     allows.
//   - Lambda's front end adds host, x-forwarded-for, x-forwarded-proto,
//     x-forwarded-port and x-amzn-trace-id: the client's trace, or else one
//     derived from the request id and the admission time, which the
//     function's runtime also runs under. The TLS headers AWS adds are not
//     reported.
//   - Under sigv4 verify, an AWS_IAM request whose credential is not
//     scoped to lambda, or to the region its host names, is refused with
//     403 InvalidSignatureException in the words of AWS's SigV4 front end
//     ("Credential should be scoped to correct service: 'lambda'. ").
//     That wording is UNVERIFIED for function URLs.
//   - An unknown url id answers 403 {"Message":null}. A NONE URL whose
//     resource policy does not allow the request answers the recorded
//     Forbidden help message with x-amzn-ErrorType AccessDeniedException
//     (lambda-events.md 7.2), not the {"Message":null} of an unknown url id.
//   - The event's text-or-base64 body, its omitted keys, the bare JSON
//     string output (recorded against AWS; the guide's example keeps the
//     quotes), malformed outputs and the dropped framing headers are
//     httpevent's rules, listed in its package doc. A malformed output is
//     answered with 502; the length sent is the body's, as AWS's front end
//     frames its own response.
//   - A CORS preflight is answered with 200; configured CORS headers
//     replace any the function set, and a request without Origin gets
//     none. The preflight is answered before the auth type applies, since
//     browsers never sign preflights: an AWS_IAM URL with CORS would
//     otherwise refuse every browser preflight. AWS's order is UNVERIFIED.
//   - An AuthType NONE URL runs only when its resource policy allows both
//     public lambda:InvokeFunctionUrl with lambda:FunctionUrlAuthType NONE
//     and public lambda:InvokeFunction with lambda:InvokedViaFunctionUrl,
//     as AWS requires since October 2025 (lambda-api.md 12 item 11); either
//     statement alone answers 403.
package furl
