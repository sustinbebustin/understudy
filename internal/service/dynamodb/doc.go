// Package dynamodb implements DynamoDB as a proxy to an understudy-managed
// DynamoDB Local container (ADR 0014, ARCHITECTURE.md 15).
//
// Every operation Local serves is bound with kernel.BindRaw: the edge
// decodes and validates the typed input, so the journal, fault rules, the
// access recorder and Coral-grammar validation run first; the handler then
// forwards the call to Local and writes Local's answer back. Local
// partitions tables by the credential scope, so the forwarded credential
// is rewritten to <account>/<date>/<region>/dynamodb/aws4_request with the
// caller's account and a region from understudy's region table. Local's
// answers pass through byte for byte, except table descriptions, whose
// ARNs Local mints in account 000000000000 and region ddblocal, and error
// bodies, whose message member and texts are AWS's; a rewritten body gets
// a recomputed x-amz-crc32. Operations Local lacks, and those the shims
// own (TTL, tags, continuous backups), stay unbound, so they answer 501
// before Local sees them: in particular a TTL specification never reaches
// Local, whose sweeper runs on the wall clock. Local resolves table names
// case-insensitively; the shim's table registry, the service's snapshot
// state, holds the exact names, so a CreateTable whose name differs from
// an existing table's only in case is refused with ResourceInUseException,
// and a call naming a table the scope lacks while it holds a case twin is
// answered ResourceNotFoundException before Local can act on the twin.
// Inside PartiQL statements the names stay Local's to resolve.
//
// A client request is forwarded as its own bytes (FromClient), {} for an
// empty body, so Local decodes exactly what the client sent (ADR 0014
// amendment: byte-exact passthrough for every operation). An internal
// caller's input (Synthesized) is encoded through the awsJson codec,
// members in model order. The edge buffers a client body for every
// operation bound with kernel.BindRaw and hands it over as the FromClient
// wire (TestEdgeForwardsTheClientsBytes).
//
// Decisions the architecture leaves open:
//
//   - Local is sent a fixed header set (Content-Type, X-Amz-Target,
//     X-Amz-Date, the rewritten Authorization, Accept-Encoding: identity)
//     and a signature of zeros, which Local never checks. The client's
//     other headers, a session token included, name nothing Local reads.
//   - A client request without credentials is refused in the handler with
//     MissingAuthenticationTokenException. Coral checks credentials before
//     the body, so an unsigned request whose body does not decode gets the
//     SerializationException the edge gives it instead.
//   - Local's own error answers are raw responses like its successes, so
//     the journal and the access recorder count them as completed calls;
//     only understudy's errors (validation, faults, the licence gate) are
//     recorded as failures.
//   - Every answer carries understudy's request id, the one the journal
//     records, instead of Local's, and an x-amz-crc32 header, which Local
//     omits on errors and AWS sends on them too.
//   - ResourceNotFoundException's text names the table on DescribeTable,
//     DeleteTable and UpdateTable only, as real DynamoDB does per
//     dynalite's recorded suite; the item operations answer "Requested
//     resource not found".
//   - Until the licence is accepted the answer is the 501 NotImplemented
//     identity unbound operations use, carrying the licence notice. A
//     Local that cannot start is InternalServerError naming the cause; a
//     start still running after the call's bounded wait (startWait) is a
//     retryable InternalServerError saying so, and goes on in the
//     background.
package dynamodb
