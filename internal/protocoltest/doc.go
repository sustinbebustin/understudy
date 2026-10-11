// Package protocoltest is the Smithy protocol compliance harness
// (ARCHITECTURE.md 5.6, 6.4): the checked-in baseline that ratchets pass
// counts, the skip list that says why each excluded case is excluded, the
// check that holds a corpus run to both, and the media-type-aware body and
// header comparators the run uses.
//
// The corpus test (corpus_test.go) serves every synthetic service of
// internal/protocoltest/gen the way the emulator serves a real one: a
// kernel with every operation bound to an echo handler, behind the edge
// with every codec internal/server registers, on a loopback HTTP server. A
// request case passes when the handler is entered once with an input equal
// to the case's params; a response case when the handler's answer is
// encoded with the case's status, headers and body; a malformed case when
// the handler is never entered and the response has the case's status and
// x-amzn-errortype. Every case must be either recorded as passing in
// baseline.json or skipped in skips.json; go test -run TestCorpus
// -update-baseline (just protocoltest-baseline) rewrites the baseline.
//
// Decisions the architecture leaves open:
//
//   - skips.json also records known failures, category gap: cases
//     understudy fails because a part it builds on cannot yet express what
//     they assert (an error member holding a structure, modeled defaults, a
//     null set in a sparse map). They are not skips: the coverage report
//     counts them as failed and lists them with their owning package, and
//     the ratchet accepts them until the fix lands (ARCHITECTURE.md 6.4,
//     docs/plan/DEFERRED.md wave 6).
//   - The baseline records each suite's case count beside its passing ids,
//     so the coverage report states passed, skipped and failed counts
//     without running the corpus, and a corpus update that adds or drops
//     cases fails the check until the baseline is rewritten. A skipped case
//     that passes fails it too, so skips cannot outlive their reason.
//   - The edge claims every RPC protocol and the S3 slice, but its REST rule
//     knows only the lambda and s3 route tables (ARCHITECTURE.md 4.1 rule
//     8), so the synthetic RestJson and RestXml services are matched
//     against their own route tables by the bench and then decoded,
//     dispatched and encoded through the same codecs and kernel calls.
//   - A response case gets the operation's first request case that is not
//     skipped, which a server is known to accept, else the smallest request
//     the protocol's framing and the operation's binding allow.
//   - Values a server mints are not held to a client's fixture: request id
//     headers are compared for presence, a RequestId element's text is not
//     compared, and an awsQuery ResponseMetadata element the expected body
//     lacks is ignored, since AWS sends one on every response.
//   - XML siblings of one name compare in order, except a map's entries,
//     which compare as a multiset since a Go map has no order. The output
//     schema names the map entry elements: entry for a wrapped map, the
//     member's name for a flattened one.
//   - Five server-runnable cases are not generated: customizations of
//     Machine Learning, API Gateway and Glacier, and an operation no
//     corpus service binds. UngeneratedCases lists them, so the coverage
//     report reconciles with the counts of ARCHITECTURE.md 6.4.
//   - A case body is base64 when its media type, or failing that its
//     Content-Type header, is application/cbor. A streaming input member is
//     compared by its bytes, an absent stream reading as an empty one.
//   - The tests live in package protocoltest_test, since they import
//     internal/server for its codecs and internal/server imports this
//     package through internal/coverage.
package protocoltest
