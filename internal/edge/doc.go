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
// claims gets an AWS-shaped 400 listing the reasons and an Unclaimed journal
// event.
//
// Decisions the architecture leaves open:
//
//   - Codecs encode into a Response the edge writes, instead of writing
//     themselves (the plan's WriteError is EncodeError here), so the one
//     Date header, the S3 request id headers and a failed write are handled
//     in one place. Response bodies are readers, so streaming outputs never
//     sit in memory whole.
//   - A request signed for a service that no protocol rule claimed is
//     answered as that service's Coral front end does (F7): a POST with a
//     JSON body or none is the service's JSON unknownOperationError without
//     a message (error-matrix.md 5.5 T5); anything else is 404
//     "<UnknownOperationException/>\n" with no Content-Type but with the
//     usual x-amzn-RequestId, as dynalite records DynamoDB sending (T7). It is
//     journaled as Unclaimed, since no operation was named.
//   - A target or form request whose service differs from the SigV4 signing
//     name is declined by its rule, so rule 8 answers for the signed
//     service.
//   - Options carries what the descriptors do not: the services' overlay
//     unknownOperationError (UnknownOperationError), the protocol whose
//     envelope answers an unclaimed request (Unclaimed) and the handler of
//     the /_understudy/ prefix (Admin). The container listener passes only
//     GET health and clock to it.
//   - Credentials are parsed, never verified: SigV4 verification is opt-in
//     configuration that arrives with the configuration package. Credentials
//     that cannot be read reject a claimed request with the protocol
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
//   - x-amz-id-2 on S3 answers is the base64 SHA-256 of the request id: S3
//     clients log the host id and never interpret it.
package edge
