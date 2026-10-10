// Package form encodes and decodes application/x-www-form-urlencoded bodies
// with numeric index ordering, as the awsQuery protocol requires.
//
// Parse reads a body or a query string into Values, a tree of key segments
// split on '.', so a codec walks Attribute.1.Name as nested children rather
// than matching flat strings. Clients encode differently
// (wire-json-query.md 7.1): Go v2 and botocore send spaces as '+', JS v3 as
// %20, and Go v2 sorts keys as strings, so member.10 arrives before
// member.2. Parse decodes '+' and %XX in keys and values alike, and
// Node.Indexed returns indexed children in numeric order whatever the
// arrival order. A key sent with the empty value (ListArg=, the empty-list
// sentinel of every SDK) is present with the value "".
//
// Two behaviors are understudy's choice, for want of a recording of what
// AWS does: a repeated key keeps its first value, as a Java servlet's
// getParameter reads it, and an index segment is any run of ASCII digits,
// 0 and leading zeros included, so nothing a client sends is silently
// dropped.
//
// Parse bounds what a client's form can cost: a key of more than
// MaxKeySegments segments, or a form of more than MaxNodes keys and key
// prefixes, is refused with a *ParseError, as is a bad escape. The error
// names the pair by its offset and raw key, never by its value, which may
// be a secret. Lookup
// reads one key's value without building the tree, for a codec that only
// needs to route the request.
//
// Escape and Encode write RFC 3986 percent-encoding (a space as %20, '+'
// as %2B), the form IAM returns policy documents in (iam-sts-kms.md 3.3).
package form
