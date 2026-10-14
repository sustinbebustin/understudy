// Package restjson is the codec for the restJson1 protocol (ARCHITECTURE.md
// 6.2): Lambda. restbind splits each request and response by its HTTP
// bindings; this package reads and writes the JSON document that carries
// the unbound members (or a structure payload), built on jsonx, and renders
// errors.
//
// Input is read leniently in shape and strictly in type, as the Smithy
// malformed-request corpus asks: an empty body is no members, unknown and
// null members are absent, null entries of dense lists and maps are
// skipped, a body member whose name an HTTP-bound member also has is
// ignored, members are matched under their jsonName; but a string is never
// a number or boolean and a fraction is never an integer. Output is exact:
// model order, jsonName, plain-digit integers, epoch-seconds timestamps
// with at most millisecond precision unless a timestampFormat trait says
// otherwise, padded base64 blobs, NaN and the infinities as strings, and
// {} for a structure output without a payload and no body member set, even
// one whose members are all bound to headers or the status, as Smithy asks
// of servers (RestJsonGreetingWithErrors, RestJsonHttpResponseCode). A
// Unit output, and a 1xx, 204 or 304 status (Lambda's DeleteFunction),
// send no body.
//
// Errors carry X-Amzn-Errortype with the bare shape name and a body
// without __type or code: the error's fields first (Lambda's Type and
// Reason, which handlers set) and then the message under the shape's own
// spelling, message or Message (wire-rest.md 4.2, lambda-api.md 2.1).
//
// Decisions the architecture leaves open:
//
//   - The edge may not import a codec, and a codec may not import the edge,
//     so this package has its own Response type; the composition root
//     adapts Decode, Encode and EncodeError to edge.Codec. The edge routes
//     REST requests through registry.RouteTable itself, so the codec has
//     no Claim. Decode binds head and body in one call, because a generated
//     Deserialize resets its required-member tracking each time it runs:
//     the adapter's DecodeHead binds nothing and DecodeBody calls Decode.
//   - A body that does not decode, and a header or query value that does
//     not parse, are the service's malformedRequestError (Lambda's
//     InvalidRequestContentException, "The request body could not be
//     parsed as JSON, or a request header is invalid"), else
//     SerializationException, which the synthetic RestJson service's
//     overlay names. A body's message starts "Could not parse request body
//     into json: " as Lambda's does; the rest is understudy's own text,
//     since Lambda's is a Jackson parser message. A header or query value's
//     message names its location instead.
//   - The 415 and 406 identities are in neither the descriptors nor the
//     generic catalogue, so Codec.MediaTypeErrors supplies them per
//     service; a service without them is not checked. When checked, a
//     non-empty body must have the Content-Type the operation reads
//     (application/json for a document or structure payload, the payload's
//     mediaType, text/plain for a string payload, none when the input has
//     no body) and an Accept header must admit what it writes. A blob
//     payload without a mediaType trait is never checked, in either
//     direction: Lambda's Invoke payload is the one such member, and SDKs
//     send it as application/octet-stream or with no Content-Type
//     (wire-rest.md 4.4, 8). Every corpus media-type case uses a payload
//     with a mediaType trait, so all of them still hold.
//   - Errors the codec raises itself (malformed request, 415, 406) take
//     Codec.ClientFaultFields, since no handler sees them to add a
//     service's fields: Lambda's recorded InvalidRequestContentException
//     carries Type User (error-matrix.md 5.6 T6).
//   - The error body writes fields before the message. Lambda's error
//     shapes declare Type before Message, and the recordings show Type
//     first; JSON clients read the keys in any order.
package restjson
