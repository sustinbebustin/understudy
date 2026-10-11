// Package restxml is the codec for the restXml protocol (ARCHITECTURE.md
// 6.2): S3 and S3 Control. restbind splits each request and response by
// its HTTP bindings; this package reads request bodies with xmlw.Decode on
// local names, writes response documents through xmlw, and renders errors.
//
// Request documents match elements by local name, with or without the S3
// namespace (wire-rest.md 2.3): a member under its xmlName, flattened
// lists and maps gathered from repeated elements, attributes for
// xmlAttribute members, unknown elements skipped. Response documents start
// with the XML declaration and a line feed, then a root named by the
// payload member's or shape's xmlName (ListBucketResult) carrying the
// service namespace; members follow in model order, wrapped lists with
// <member> or the list member's xmlName, flattened lists repeated in their
// parent, attributes with their namespace prefix (an ACL Grantee's
// xmlns:xsi and xsi:type), escaped with the SDK table xmlw implements.
//
// Errors on S3 are a bare <Error> root with Code, Message (absent when
// empty), the error's context fields (BucketName, Key, UploadId,
// ArgumentName), RequestId and HostId; a HEAD request gets its status and
// headers only. The 200-with-error path of CompleteMultipartUpload,
// CopyObject and UploadPartCopy needs nothing special here: the pipeline
// sets the status to 200 and the same writer renders the body, its XML
// declaration first, as SDKs require to detect the error (wire-rest.md
// 3.7).
//
// Decisions the architecture leaves open:
//
//   - The descriptors do not carry restXml's noErrorWrapping, S3's
//     millisecond timestamps or S3's InvalidArgument identity, so a
//     per-service Dialect supplies them; S3Dialect is S3's. The zero
//     Dialect is Smithy's restXml, which the synthetic corpus services
//     speak: <ErrorResponse> wrapping, plain date-time, and Content-Type
//     application/xml on an output whose members all bind outside the
//     body.
//   - awserr.Internalf carries the generic InternalFailure, but S3's bug
//     answer is InternalError, "We encountered an internal error. Please
//     try again." (wire-rest.md 3.6, 3.7). Dialect.Internal holds S3's
//     identity, which lives in S3's overlay and so reaches the codec from
//     the composition root, never from a literal here; an Internal origin
//     is rendered with it and keeps its status.
//   - A request document's root must carry the local name the response
//     writer would give it (the payload member's xmlName, else the shape's
//     xmlName, else its name); any other root is MalformedXML, not an
//     empty input (wire-rest.md 2.3).
//   - Namespaces: a list member's, map key's and value's xmlNamespace is
//     declared on each element it names, a flattened map's own member
//     namespace on each entry, and a declaration an ancestor already made
//     is left out (XmlNamespaces, XmlLists, the map namespace cases).
//   - The descriptors do not carry aws.customizations#s3UnwrappedXmlOutput
//     either. An output is written unwrapped when its one unbound member
//     is a scalar whose element name is the root's, which is that trait's
//     shape and holds only for GetBucketLocation in S3; an absent value is
//     an empty root (the us-east-1 answer), written as a start and end tag.
//   - <Code> is the identity's QueryCode (the shape name for modeled S3
//     errors and the code for overlay ones), else the shape name.
//   - encoding-type=url applies when the request asks for it and the
//     output has an EncodingType member, to every member named Key,
//     Prefix, Delimiter, Marker, NextMarker, StartAfter, KeyMarker or
//     NextKeyMarker at any depth: RFC 3986 unreserved characters and "/"
//     stay, every other byte is %XX, so "+" is %2B (wire-rest.md 3.4).
//   - A body that does not parse or fit the input is the service's
//     malformedRequestError with S3's MalformedXML text, else
//     SerializationException; a label, query or header value that does
//     not parse is the dialect's InvalidArgument with ArgumentName and
//     ArgumentValue, else the malformedRequestError.
//   - HostId and x-amz-id-2 are HostID(requestID), the base64 SHA-256 of
//     the request id, the same value the edge stamps on every restXml
//     answer; EncodeError and Encode set the request id pair themselves
//     too, so a response is complete without the edge.
//   - Body caps: an operation's MaxRequestBytes, else 5 GiB for a
//     @streaming payload (S3's largest single PUT, enforced by the edge's
//     counting reader) and 16 MiB for an XML document.
//   - The edge routes REST requests through registry.RouteTable, so the
//     codec has no Claim; the composition root adapts Decode, Encode and
//     EncodeError to edge.Codec, binding nothing in DecodeHead (restbind
//     binds head and body in one pass).
package restxml
