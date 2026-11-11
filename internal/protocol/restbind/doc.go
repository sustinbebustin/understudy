// Package restbind binds Smithy HTTP traits for the REST protocols
// (ARCHITECTURE.md 6.1, wire-rest.md 1): labels, query strings, query-params
// maps, headers, prefix headers, payloads, the response status and
// trailer-bound members. The restJson1 and restXml codecs put it in front of
// their body documents, so each sees only the members its body carries.
//
// Decode runs a generated input's Deserialize once over a merged
// smithy.Deserializer: head-bound members first, read as text from the
// request, then the payload member, then the document's unbound members
// through the codec's Document. Encode runs a generated output's Serialize
// through a serializer that sends each top-level member where its binding
// says: headers, the status, the payload or the codec's DocumentWriter.
//
// Reading is lenient where clients differ and exact where Smithy is:
// labels are split on the escaped path before they are percent-decoded,
// once, so %2F stays inside its label, a greedy label keeps its slashes and
// a trailing slash, and "." and ".." are kept verbatim (wire-rest.md 1.2,
// 3.3); a query list repeats its key and a scalar takes the first value;
// header lists parse quoted and bare elements and http-date lists pair
// their commas back up; timestamps default to http-date in headers and
// date-time in labels and queries, and accept the three HTTP date forms;
// a header string with a mediaType trait is base64. Writing uses the same
// text forms; header lists are joined with ", " and quote only the
// elements that need it.
//
// Decisions the architecture leaves open:
//
//   - Head and body bind in one Decode, not in the edge's separate
//     DecodeHead and DecodeBody steps: a generated Deserialize resets its
//     required-member tracking each time it runs, so two passes would
//     report the first pass's required members missing. The adapters bind
//     nothing in DecodeHead.
//   - A trailer-bound member (smithy.HTTPHeader.Trailer) that the head
//     carries binds as any header. One the head does not carry is left
//     unset. The *Body that a @streaming payload binds to answers it
//     through Trailer: nothing until the handler has read the body to
//     EOF, then the trailer, which an aws-chunked decoder supplies.
//   - Whether an output whose members are all bound still sends a
//     document is the codec's rule, passed to Encode: restJson1 servers
//     send {} (RestJsonGreetingWithErrors, RestJsonHttpResponseCode),
//     restXml sends nothing (RestXmlHttpResponseCode), and nothing for an
//     output with no members either (EmptyInputAndEmptyOutput). Either
//     way a 1xx, 204 or 304 status sends no body.
//   - Prefix-header keys are the lowercase remainder of the header name:
//     net/http canonicalizes names, the corpus expects lowercase keys, and
//     S3 stores user metadata keys lowercase. Repeated lines of one header
//     join with commas.
//   - A body is read only when the input has a document or a payload
//     member, so S3 GETs read nothing. A body of only whitespace is no
//     document (the restJson1 empty-body cases), but it is a blob or
//     string payload: Lambda's Invoke passes " \n" on as its bytes.
//   - Query strings split on '&' only and reject an invalid
//     percent-escape as a *BindError, where url.ParseQuery would drop the
//     pair and bind the value as absent.
//   - The text forms of scalars (ParseInt, ParseFloat, FormatTimestamp and
//     the rest) live here because restXml bodies use the same text as
//     headers; both codecs call them.
package restbind
