// Package awsjson is the codec for the awsJson1_0 and awsJson1_1 protocols
// (ARCHITECTURE.md 6.2): SQS, DynamoDB and CloudWatch speak 1.0; Logs,
// Secrets Manager, KMS and EventBridge speak 1.1. It is built on jsonx only.
//
// A Codec{Version: V10} or Codec{Version: V11} serves the edge in four
// steps. Claim decides from X-Amz-Target, the method and the Content-Type
// essence whether a request is this version's and which operation it
// names. Decode reads the body into the generated input through a
// smithy.Deserializer. Encode serializes the handler's output through a
// smithy.Serializer into a Response with the status the pipeline chose,
// and EncodeError renders an *awserr.Error; the edge writes either.
// awsJson binds nothing to headers or the path, so the codec has no
// separate head step.
//
// Input is read leniently, as AWS's front end reads it: an empty body is
// {}, unknown members (including __type in a union) and null members are
// absent, null entries of dense lists and maps are skipped, duplicate
// members keep the last non-null value, Content-Type parameters such as
// charset are ignored, and a gzip Content-Encoding is decompressed up to
// the operation's body cap. Two duplicate cases are deliberate: a later
// null does not clear an earlier value, since null means absent, and a
// union that repeats its member is refused with a text naming the
// repeated member, since the first value has already been handed to the
// input. Strings keep invalid UTF-8 and lone surrogates for the validators
// (see jsonx). A value that does not fit its member is
// SerializationException with the text Coral sends, as dynalite records it
// from real DynamoDB (error-matrix.md 5.5 T2; third-party evidence):
// "STRING_VALUE cannot be converted to Integer", "Start of structure or
// map found where not expected", "Unrecognized collection type class
// <class>" and so on; a body that is not JSON gets no message (T6).
// Following the same source, a fractional number for an integer member is
// floored rather than refused, and the strings TRUE, 1, YES, FALSE, 0 and
// NO (any case) are accepted for a boolean. understudy approximates the
// Java package of a modeled shape in those texts with its Smithy
// namespace (Coral's adds the API version), and its texts for conditions
// dynalite does not cover (unions, numeric range, timestamp parsing, a
// body that is not an object) are its own.
//
// Output is exact: members in model order, integers as plain digits,
// timestamps as epoch seconds with at most millisecond precision and no
// exponent (date-time and http-date strings where a timestampFormat trait
// asks), blobs as padded standard base64, NaN and the infinities as the
// strings NaN, Infinity and -Infinity, {} for any modeled output with no
// member set, and an empty body for an operation whose output is
// smithy.api#Unit (SQS DeleteMessage answers Content-Length: 0, sqs.md
// 2.1). Errors carry __type always, x-amzn-query-error on every error of a
// query-compatible service, and the message under the shape's own spelling
// (wire-json-query.md 5 and 6).
//
// Decisions the architecture leaves open:
//
//   - The edge may not import a codec, and a codec may not import the edge
//     (which imports the kernel), so this package has its own Claim and
//     Response types. The method set (Protocol, Claim, Decode, Encode,
//     EncodeError) is the contract, and the composition root adapts it to
//     edge.Codec (internal/server/codecs.go).
//
//   - A body that does not decode is answered with the service's
//     malformedRequestError (registry.Service.MalformedRequestError) when
//     its overlay sets one, else SerializationException, which is what
//     every awsJson service in phase 1 sends; Lambda's
//     InvalidRequestContentException belongs to restJson1.
//
//   - registry.Operation does not carry the requestCompression trait, so
//     gzip is accepted on every operation, not only CloudWatch
//     PutMetricData; the decompressed body is capped at MaxRequestBytes(op),
//     the operation's own cap or DefaultMaxRequestBytes, so a small
//     compressed body cannot expand without bound. Any other
//     Content-Encoding is the malformed-request answer.
//
//   - The status is the identity's Status, except that a query-compatible
//     service answering a request that sent x-amzn-query-mode: true uses
//     QueryModeStatus when the identity sets it, as awserr.Identity
//     documents (CloudWatch's 404).
//
//   - A query-compatible service sends x-amzn-query-error on every error,
//     not only on shapes with awsQueryError: modeled shapes without the
//     trait carry their shape name as the code (Identity.QueryCode), and
//     generic errors the catalogue's Query spelling. This follows
//     PHASE1-PLAN.md T11 and error-matrix.md 3, which records the header on
//     SQS's unmodeled errors (boto3 derives Code from it), as
//     ARCHITECTURE.md 6.2 and wire-json-query.md Implication 12 state.
//     Smithy's QueryCompatibleAwsJson10NoCustomCodeError response test
//     sets no forbidHeaders, so the header stays corpus-compliant.
//
//   - Claim accepts application/json as well as the version's media type,
//     as DynamoDB does (error-matrix.md 5.5 T6), and declines any other
//     Content-Type, the other awsJson version's included (T7): the edge
//     answers a declined request signed for a JSON service with that
//     service's own error (ARCHITECTURE.md 4.1 rule 8). A service speaks
//     one awsJson version, so V10 and V11 never both claim a request.
package awsjson
