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
// a recomputed x-amz-crc32. Operations Local lacks stay unbound, so they
// answer 501 before Local sees them. Local resolves table names
// case-insensitively; the shim's table registry, the service's snapshot
// state, holds the exact names, so a CreateTable whose name differs from
// an existing table's only in case is refused with ResourceInUseException,
// and a call naming a table the scope lacks while it holds a case twin is
// answered ResourceNotFoundException before Local can act on the twin.
// Inside PartiQL statements the names stay Local's to resolve.
//
// The shims (ARCHITECTURE.md 15) keep, per account, region and table, what
// Local drops or answers wrongly: a Table record holds the tags, seeded
// from CreateTable.Tags, the TTL specification, point-in-time recovery,
// the table class, KMS encryption, warm throughput, the TableId and the
// emulator-clock instants of creation and of the last switch to
// PAY_PER_REQUEST. The tag, TTL and continuous-backup operations are
// answered from it and never reach Local; in particular a TTL
// specification never does, so Local's wall-clock sweeper stays off. Every
// table description Local writes is completed from it (describe.go): the
// WarmThroughput Terraform waits on, of the table and of each global
// secondary index, TableId, TableClassSummary, SSEDescription, the
// timestamps, CREATING on CreateTable and DELETING on DeleteTable. An
// UpdateTable of only the members the shims own is answered with Local's
// description instead of Local's "Nothing to update". The TTL sweep runs on
// the emulator clock (ttl.go), and snapshot export, import and reset reach
// Local table by table (snapshot.go, reset.go).
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
//     only understudy's errors (validation, faults, a disabled Local) are
//     recorded as failures.
//   - Every answer carries understudy's request id, the one the journal
//     records, instead of Local's, and an x-amz-crc32 header, which Local
//     omits on errors and AWS sends on them too.
//   - ResourceNotFoundException's text names the table on DescribeTable,
//     DeleteTable and UpdateTable only, as real DynamoDB does per
//     dynalite's recorded suite; the item operations answer "Requested
//     resource not found".
//   - While DynamoDB is turned off or Docker is unavailable the answer is
//     the 501 NotImplemented identity unbound operations use, carrying the
//     reason (DisabledError). A Local that cannot start is
//     InternalServerError naming the cause; a
//     start still running after the call's bounded wait (startWait) is a
//     retryable InternalServerError saying so, and goes on in the
//     background.
//   - A TTL sweep is armed for the earliest expiry the shim knows, so an
//     item is deleted at the instant its TTL attribute names; AWS deletes
//     within days. An expiry more than five calendar years before the
//     sweep's instant never expires, as AWS documents. An UpdateItem or a
//     transaction's update is followed by a read of the item's TTL
//     attribute, sent to Local directly as part of the write, so it
//     matches no fault rule and is not journaled; PartiQL INSERT and
//     UPDATE, whose keys the shim cannot know, sweep their scope's tables
//     at once. The due instants live beside the scheduler, not in state,
//     and Rearm sweeps every table with TTL enabled at the instant it
//     runs. The sweep's calls run as kernel.InvocationRunning work, a
//     poller cycle's kind.
//   - The kernel has no call without a parent, so snapshot export, import
//     and reset reach Local directly, not through the pipeline: they never
//     match fault rules, as the architecture requires. The admin sweep
//     (SweepTTL) instead arms the tables' sweep timers now and waits for
//     their sweeps, which run through the pipeline.
//   - NOT IMPLEMENTED: the summary journal event per export, import and
//     reset that ARCHITECTURE.md 13 and the ADR 0014 amendment ask for.
//     The journal's Detail kinds are a closed set without one, and
//     kernel.Invoke refuses a nil parent, so this package writes none.
//     Once the journal has such a kind, ExportLate, ImportLate and
//     ResetLate should each emit one.
//   - A TableId is derived from the request id of the CreateTable, so
//     seeded runs mint the same ids without a stream of their own.
//   - A KMS key is resolved with a DescribeKey forwarded to KMS as the
//     caller, and described by the key ARN KMS answers; no key is the AWS
//     managed key behind alias/aws/dynamodb, and a key KMS does not find
//     is refused with ValidationException. A server that binds no KMS
//     describes the ARN of the id, alias or ARN the request names instead.
//   - A table Local holds that the shim does not record (kept under
//     --persist without the archive) is adopted by the first tag, TTL,
//     continuous-backup or UpdateTable operation naming it, with Local's
//     CreationDateTime and last switch to PAY_PER_REQUEST and a TableId
//     derived from its path; DescribeTable alone adopts nothing. Snapshot save exports it by Local's own table list, so the
//     load, which deletes every table of the scope, brings it back.
//   - Tags follow AWS's limits: at most 50 per table, and no key starting
//     with aws: in any letter case.
//   - Enabling point-in-time recovery without RecoveryPeriodInDays sets 35
//     days; LatestRestorableDateTime trails now by 5 minutes, both never
//     before the instant recovery was enabled.
//   - Stream labels stay Local's, as its stream ARNs and records carry
//     them; a re-created table gets a new one (ADR 0014 amendment), which
//     the coverage notes of CreateTable, DescribeTable and UpdateTable
//     list.
//   - The texts of TableNotFoundException ("Table not found: <name>") and of
//     a tag operation's ResourceNotFoundException ("Requested resource not
//     found: ResourcArn: <arn> not found", AWS's spelling as remembered),
//     of the tag limits and of an unknown KMS key are UNVERIFIED against
//     AWS, as are Local's TTL refusals reused here, except enabling a
//     different attribute: "TimeToLive is active on a different
//     AttributeName: current AttributeName is <attr>" is AWS's text, as
//     celery records it from the service (t/unit/backends/test_dynamodb.py
//     L314-L320; celery/backends/dynamodb.py L374-L380).
package dynamodb
