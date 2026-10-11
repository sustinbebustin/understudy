// Package awsquery is the codec for the awsQuery protocol (ARCHITECTURE.md
// 6.2): SNS, IAM, STS, CloudWatch's legacy clients and SQS's legacy
// clients. It is built on form for requests and xmlw for responses.
//
// The Codec serves the edge in four steps. Claim decides from the method,
// the Content-Type essence and the Action and Version parameters whether a
// request is an awsQuery request and which operation it names (rule 7 of
// ARCHITECTURE.md 4.1). Decode reads the form into the generated input
// through a smithy.Deserializer. Encode serializes the handler's output
// into the <OpResponse><OpResult/><ResponseMetadata/></OpResponse>
// envelope, and EncodeError renders an *awserr.Error as an ErrorResponse;
// the edge writes either. awsQuery binds nothing to headers, so the codec
// has no separate head step.
//
// Input is read leniently (wire-json-query.md 7): '+' and %XX decode in
// keys and values; indexed keys are read in numeric order whatever order
// they arrive in (Go v2 sends member.10 before member.2); ListArg= is an
// empty list; unknown parameters, Action and Version among them, are
// ignored; a map entry without its key or value is skipped; booleans are
// read in any case; timestamps accept fractional seconds and offsets.
// Members are found by their Query key: xmlName, else the member name;
// lists under member.N or the list member's xmlName, flattened lists
// directly under N; maps under entry.N.key and entry.N.value or the key
// and value members' xmlNames, flattened maps under N.key. A legacy SQS
// request may be sent to its queue URL, /<account>/<queue> (sqs.md 2.3):
// the path supplies QueueUrl unless the form names one, and the
// account-scoped CreateQueue and ListQueues are unknown actions there.
//
// Output is exact (wire-json-query.md 8): the Result element whenever the
// operation has an output shape, <OpResult/> when no member is set, the
// envelope with ResponseMetadata alone for smithy.api#Unit, the service's
// xmlNamespace on the root (SQS's legacy namespace comes from its
// overlay), a member's own xmlNamespace on its element (a flattened
// list's items take the list member's, a flattened map's entries the
// structure member's; a namespace on a member's target shape is not
// written, as the corpus asserts), an empty element as <Name/>, lowercase
// booleans, date-time timestamps with millisecond precision, padded base64
// blobs, NaN,
// Infinity and -Infinity for the non-finite floats, flattened collections
// as contiguous repeated elements, and IAM policy documents percent-encoded
// as RFC 3986 asks, '+' as %2B and a space as %20 (iam-sts-kms.md 3.3).
// Whether an empty collection is written at all (SQS omits, IAM emits
// <Roles/>) is the dev.understudy#emptyCollection trait, which the
// generated Serialize methods apply; the codec writes what they hand it.
// Errors are ErrorResponse/Error{Type, Code, Message} and a sibling
// RequestId, with the error's extra fields after Message.
//
// Decisions the architecture leaves open:
//
//   - The edge may not import a codec, so this package has its own Claim
//     and Response types, as awsjson does, and the composition root
//     adapts them to edge.Codec. EncodeError takes no request: nothing in
//     an awsQuery error depends on it.
//
//   - Claim must read a POST body to find Action. It reads at most
//     DefaultMaxRequestBytes and puts the bytes back, as sent, in front of
//     the rest of the body, so the edge's capped read in Decode sees the
//     whole body. A larger body is declined, since its Action is not read.
//     DefaultMaxRequestBytes is awsJson's 16 MiB; no Query cap is
//     recorded. Claim reads Action and Version alone with form.Lookup, so
//     a bad escape elsewhere in the form is claimed and answered by
//     Decode as a malformed request.
//
//   - A body with Content-Encoding gzip is decompressed, by Claim up to
//     DefaultMaxRequestBytes and by Decode up to the operation's cap
//     (wire-json-query.md 3.2: botocore gzips CloudWatch PutMetricData
//     forms of 10240 bytes or more). Any service's body is decompressed,
//     since the trait only permits a client to compress. Another encoding,
//     corrupt gzip and a body that inflates past the cap are declined by
//     Claim and are the malformed-request answer from Decode; no recording
//     shows what AWS sends.
//
//   - An unknown Action is UnknownOperation when Version names exactly one
//     awsQuery service, and is declined otherwise, so the edge answers by
//     signing name. A form without Action is declined: the Query
//     MissingAction code has no identity in the generic catalogue.
//
//   - A form value that does not fit its member is answered with the
//     service's malformedRequestError, else SerializationException, the
//     catalogue's one malformed-input identity; no recording shows what a
//     Query service sends. The message text, "Value v for parameter P is
//     invalid. Reason: ...", is understudy's own, in the form of SQS's
//     InvalidParameterValue texts.
//
//   - The error status is awsQueryError's httpResponseCode, as the Smithy
//     awsQuery specification and ARCHITECTURE.md 6.2 resolve it. The
//     generated identity carries that code as QueryModeStatus when it
//     differs from httpError, so the status is QueryModeStatus when set,
//     else Status (CloudWatch's ResourceNotFound is 404 over Query).
//
//   - The ErrorResponse envelope is per service, from recordings
//     (error-matrix.md 3): CloudWatch declares its namespace and SNS does
//     not; IAM declares it in the recorded fixture iam-sts-kms.md 1.2
//     cites; SQS sends an empty <Detail/>. The architecture places these
//     among the overlay facts, but no dev.understudy trait carries them
//     and registry.Service has no field for them, so errorEnvelopeOf
//     holds them by service id. STS and every other service follow
//     Smithy's example: no namespace, no Detail.
//
//   - Policy documents are recognized by their shape,
//     com.amazonaws.iam#policyDocumentType, the shape botocore decodes;
//     the model gives the encoding no trait.
//
//   - Two members of one structure may share a Query key (SQS
//     ReceiveMessage's AttributeNames and MessageSystemAttributeNames are
//     both AttributeName.N). The overlay's queryKeyAlias, carried as
//     smithy.Member.QueryKeyAlias, says which member receives the values;
//     the decoder leaves a member marked AliasIgnore unset.
//
//   - A null (a sparse collection's) is written as nothing, since XML has
//     no null and an absent element reads as an absent value. Documents
//     and streaming members are refused as generator bugs: awsQuery
//     carries neither.
package awsquery
