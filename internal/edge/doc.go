// Package edge is the front door of the emulator: body caps, request ids,
// credential parsing, identity resolution, claiming a request for a service,
// codec dispatch and diagnostics for requests no service claims.
//
// Handler runs pipeline steps 1 to 5 and 13 of ARCHITECTURE.md 4 around
// kernel.Dispatch, claiming in the order of ARCHITECTURE.md 4.1. Dispatch
// reads the request target exactly as sent (RequestURI, else EscapedPath):
// no ServeMux, so %2F, "." and ".." reach the codecs and the admin handler
// unchanged. A rule whose codec or route table is not registered declines
// with a reason; every rule that declines records why, and a request no rule
// claims gets an AWS-shaped 400 listing the reasons, in the envelope of the
// protocol its shape names, and an Unclaimed journal event.
//
// Decisions the architecture leaves open:
//
//   - Codecs encode into a Response the edge writes, errors included
//     (EncodeError), instead of writing themselves, so the one
//     Date header, the S3 request id headers and a failed write are handled
//     in one place. Response bodies are readers, so streaming outputs never
//     sit in memory whole.
//   - A request signed for a registered service that no protocol rule
//     claimed is answered as that service's Coral front end does, never by
//     S3 (ADR 0013): a POST with a JSON body or none is the service's JSON
//     unknownOperationError without a message (error-matrix.md 5.5 T5);
//     anything else is 404 "<UnknownOperationException/>\n" with no
//     Content-Type but with the usual x-amzn-RequestId, as dynalite records
//     DynamoDB sending (T7). It is journaled as Unclaimed, since no
//     operation was named.
//   - A request signed for a service understudy does not emulate (ec2,
//     rds, kinesis) is 501 NotImplemented, outside every SDK's retry set,
//     naming the service and the emulated ones. Since no codec claimed it,
//     its envelope is the one its shape names: rpcv2Cbor by Smithy-Protocol,
//     awsJson by X-Amz-Target, awsQuery by a form or a GET with Action,
//     restJson1 by a JSON body, else restXml, whose body a JSON client still
//     keeps as the message. Client-facing reasons drop the ARCHITECTURE.md
//     4.1 rule numbers, which the journal keeps.
//   - A path under /_localstack/, LocalStack's reserved prefix, is claimed
//     under rule 1 before S3, which would answer it as the bucket
//     "_localstack" that no bucket name can be: 404 in the admin API's JSON
//     error envelope naming GET /_understudy/v1/health and understudy
//     health --wait, so a migrated health check says what to call instead.
//     It is journaled as Unclaimed. A bare /health is a valid bucket path
//     and stays S3's.
//   - Each request is logged at Debug (method, operation, status, latency,
//     request id). A not-emulated service, an unclaimed request, a call to
//     an unimplemented operation and a call to an operation the vendored
//     model does not define are also logged at Warn, once per service,
//     reason or operation, so the server's terminal shows why a client
//     failed; the last is the only pointer to understudy coverage, since its
//     wire answer is Coral's message-less unknownOperationError. net/http's
//     own diagnostics go to the same logger at Warn. Credentials and bodies
//     are never logged.
//   - Connections are bounded in time, never in total request length, so
//     long polls, journal streams and slow uploads still work: headers
//     within 30 s, a pause of at most a minute inside a body, and two
//     minutes idle between requests.
//   - A target or form request whose service differs from the SigV4 signing
//     name is declined by its rule, so rule 8 answers for the signed
//     service.
//   - Options carries what the descriptors do not: the services' overlay
//     unknownOperationError (UnknownOperationError), the protocol whose
//     envelope answers an unclaimed request (Unclaimed) and the handler of
//     the /_understudy/ prefix (Admin). The container listener passes only
//     GET health and clock to it.
//   - Credentials are parsed, never verified: the edge has no verifier
//     hook, and serve refuses the sigv4: verify setting rather than ignore
//     it (cli.unappliedSettings). Credentials that cannot be read reject a claimed request with the protocol
//     family's auth error, except that an operation the service does not
//     model is rejected as unknown first, in Coral's check order. Rule 8
//     routes by signing name, which unreadable credentials do not yield; a
//     header-signed request without x-amz-content-sha256 is refused only
//     when scoped to s3, so rule 8 sends that failure to S3 to answer.
//   - The Date header is stamped when the response is sent, on every path
//     (codec answers, the admin handler, raw HTTP handlers), so a long poll
//     answers with a current Date.
//   - A body is capped twice: a declared Content-Length past the
//     operation's cap is refused before reading, and every body is read
//     through a counting reader that fails past it, so chunked bodies are
//     never buffered beyond the cap.
//   - A web page must not drive the emulator. Before anything else the
//     edge refuses, with a plain-text 403, a Host the listener does not
//     answer (Options.Hosts): under LocalHosts only an IP literal,
//     localhost and names under it, and the S3 base domains, which defeats
//     DNS rebinding, since a rebound page still sends its own name; under
//     NetworkHosts, for an exposed listener, any Host from a client that
//     is not a browser and the LocalHosts names from one. An IP
//     literal is safe because no page can make a browser send another
//     site's address as Host. The admin API proper (not the SNS, function
//     URL and Lambda code links under the same prefix, which browsers open
//     cross-site by design) also refuses a call marked Sec-Fetch-Site:
//     cross-site or carrying an Origin that is not loopback, which defeats
//     CSRF through requests that need no CORS preflight. The AWS APIs, under
//     Options.Pages, refuse a page (its Origin, else its Referer unless
//     Sec-Fetch-Site is none, since a browser sends no Fetch Metadata to a
//     host it does not deem trustworthy) that is not on a loopback origin
//     or that is on a port understudy serves (the call's, or any listener's
//     in Options.Served), and a same-origin or cross-site browser call that
//     names no page. A page on localhost still drives S3 or Lambda from the
//     browser, while a site the developer visits cannot create a function
//     with a text/plain POST, and a page understudy served itself (an
//     uploaded S3 object, a function URL response) cannot read secrets or
//     objects with same-origin requests, which no signature check stops.
//   - x-amz-id-2 on S3 answers is the base64 SHA-256 of the request id: S3
//     clients log the host id and never interpret it.
package edge
