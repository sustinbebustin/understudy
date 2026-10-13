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
//   - Credentials are always parsed and, with Options.Verify (sigv4
//     verify), verified after the claim and before decode (step 4a, verify
//     .go): for a claimed operation and for raw routes and links that carry
//     credentials, but not for an operation the service does not model,
//     which is rejected as unknown first, in Coral's check order. A refusal
//     is dispatched as kernel.Rejected through the claimed codec, so it is
//     journaled as a failed call; a request that verifies carries its
//     credentials on the call (kernel.Call.VerifiedAuth). Credentials that
//     cannot be read reject a claimed request the same way, in either mode,
//     raw routes and links included, so their handlers never run; only an
//     Authorization header in no AWS scheme (Bearer) reaches a link, as
//     unsigned, since a function URL's app may read one of its own, as AWS
//     passes it to an AuthType NONE function.
//     Both answer in the claimed service's words: Options.ServiceAuth for
//     S3, whose identities no protocol family holds, else the protocol
//     family's. Rule 8 routes by signing name, which unreadable
//     credentials do not yield, so it sends the failures only S3 can
//     raise to S3 to answer: a presigned or SigV2 credential, a conflict
//     (which always involves a query credential), and a header scoped to
//     s3 that lacks x-amz-content-sha256 or names an unknown region. An
//     Authorization header in no AWS scheme is routed as unsigned.
//   - A key verifies only when it has a secret: one understudy minted, a
//     static key from accounts.keys, the dummy key test or a 12-digit key,
//     whose secret is test (ADR 0012 pre-tag review amendment). Temporary
//     credentials must send their session token and be unexpired on the
//     wall clock, as STS stamps Expiration; presigned expiry runs on the
//     emulator clock. A SigV4 URL's lifetime counts from its X-Amz-Date
//     placed on the emulator timeline by the ClockTimeline the edge keeps
//     of every clock mode it saw (before each request, and as it answers
//     each admin call, holding an answer that changed the clock until the
//     next whole wall second, since X-Amz-Date has whole-second precision),
//     so a URL signed after `clock advance` lives its full lifetime and an
//     advance past it expires the URL, frozen or not. A SigV2 URL
//     carries only its Expires, which is read as an emulator instant, so it
//     expires as intended only while emulator time read wall time when it
//     was signed. A body is read whole within the operation's cap to hash
//     it, then handed to the codec again, unless the operation streams its
//     input and its service's ServiceAuth says it checks the sent payload
//     hash itself (ServiceChecksPayload: S3 PutObject and UploadPart),
//     whose bodies stay streamed; every other S3 body is bound to the
//     signature by the edge. The key is resolved once per request, for
//     both the secret and the caller. Neither credentials nor tokens are
//     logged.
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
