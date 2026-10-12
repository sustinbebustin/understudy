// Package rpcv2cbor is the codec for the Smithy RPC v2 CBOR protocol
// (ARCHITECTURE.md 6.2, wire-cbor.md): in phase 1, CloudWatch as
// aws-sdk-go-v2, and so the Terraform provider, speaks it. It is built on
// the cbor package only.
//
// A Codec serves the edge in four steps, as the awsjson codec does. Claim
// decides from the Smithy-Protocol header and the
// /service/{Service}/operation/{Op} path whether a request is rpcv2Cbor and
// which operation it names. Decode reads the body into the generated input
// through a smithy.Deserializer. Encode serializes the handler's output
// through a smithy.Serializer into a Response with the status the pipeline
// chose, and EncodeError renders an *awserr.Error; the edge writes either.
// HTTP binding traits are ignored on this protocol, so there is no head
// step.
//
// Input is read leniently (wire-cbor.md 3.2 and 3.6): an empty body, a0
// and bfff all mean no member is set; maps, lists and strings come in
// either length form, nested in each other; integers in any argument
// width, range checked against the member; floats and doubles from any
// number, half precision included; timestamps from tag 1 around any
// number; unknown members are skipped whatever they hold (__type in a
// union among them), null and undefined members are absent, dense
// collections skip null entries, and a gzip Content-Encoding is
// decompressed up to the operation's body cap. A body that does not decode
// is the service's malformedRequestError, else SerializationException,
// with a message that names the member and byte offset.
//
// Output is exact: definite lengths, minimal integer widths, float as
// single and double as double, timestamps as tag 1 around a double, and
// Smithy-Protocol: rpc-v2-cbor on every response. An operation whose
// output is smithy.api#Unit answers with no body and no Content-Type,
// since one member panics the Go client (G11, G22). An error body is never
// empty: __type is the error's absolute shape id, then the message and
// the error's extra fields (G17 to G21); a query-compatible service adds
// x-amzn-query-error.
//
// Decisions the architecture leaves open:
//
//   - The edge may not import a codec, so this package has its own Claim
//     and Response types, as awsjson has; the composition root adapts
//     them to edge.Codec. EncodeError takes no request: nothing on this
//     protocol depends on it.
//
//   - Claim has a fourth answer, Rejected, for a request the
//     Smithy-Protocol header and path make rpcv2Cbor but that breaks the
//     protocol: it carries X-Amz-Target or X-Amzn-Target (which the
//     specification says must be rejected), is not a POST, or sends a
//     Content-Type other than application/cbor. Its Err is the answer the
//     protocol calls for: the generic UnknownOperationException (400), the
//     identity the edge answers an unroutable request with (ARCHITECTURE.md
//     4.1 rules 5 and 9), with a message naming the rule, rather than
//     smithy-rs's empty 404 or 405, since the Go client cannot read an
//     empty error body (wire-cbor.md 3.9). The edge does not yet answer
//     with it: edge.Codec has no rejecting claim, so the composition root
//     maps Rejected to Declined with Err's message as the reason, and the
//     later claim rules, then rule 9's unclaimed answer, take the request
//     (edge rule 5 answers one with X-Amz-Target as unclaimed before
//     asking the codec).
//     A request without Content-Type is accepted, an empty body being
//     legal; Accept is never checked.
//
//   - Claim honors any path prefix before /service/ and both service
//     forms (the shape name and the dotted absolute id), as the
//     specification requires; a Smithy-Protocol value other than
//     rpc-v2-cbor, or a service segment that names no rpcv2Cbor service,
//     is Declined, so the later claim rules and rule 9 answer it.
//
//   - The status is the identity's Status, except that a query-compatible
//     service answering a request that sent x-amzn-query-mode: true uses
//     QueryModeStatus when the identity sets it, as the awsjson codec does:
//     AWS answers CloudWatch's ResourceNotFound 400 by default and 404 in
//     query mode on both wires (error-matrix.md 5.13), and the Go client
//     always sends the header (wire-cbor.md 3.2). The corpus case
//     QueryCompatibleRpcV2CborCustomCodeError, which expects 400 for a
//     shape whose code is 402, is a response case with no request, so no
//     query mode; its awsJson1_0 twin expects the same 400.
//
//   - A query-compatible service sends x-amzn-query-error on every error,
//     with Identity.QueryCode, as the awsjson codec does; for a shape
//     without awsQueryError that is the shape name, which is the code the
//     Go client derives without the header (wire-cbor.md 5.2), and the
//     corpus forbids neither form.
//
//   - The message goes under the shape's own spelling (Message or
//     message) when the shape is in the service's namespace, so typed Go
//     errors read it (G18, G20). A shape from any other namespace, the
//     generic catalogue and understudy's NotImplemented, is an unknown
//     type to the client, which reads the message from message, so it goes
//     there whatever the identity's MessageKey (wire-cbor.md 3.9 and 5.2).
//
//   - The default body cap is the awsjson codec's 16 MiB, so CloudWatch
//     takes the same bodies over either protocol; CloudWatch's own
//     request limits on this protocol are unrecorded (wire-cbor.md U8).
//
//   - Documents, bigDecimal values and bigInteger values beyond 64 bits
//     have no form in the subset: a request member of those types is a
//     malformed request, and an output that holds one is InternalFailure.
//     No CloudWatch shape uses them.
package rpcv2cbor
