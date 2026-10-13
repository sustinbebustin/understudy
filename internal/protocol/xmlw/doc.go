// Package xmlw writes the XML responses of the awsQuery and restXml codecs and
// reads XML request bodies.
//
// Writer streams one document with explicit calls (Start, StartNS, Attr,
// AttrNS, Text, End, Empty, Declaration) and no reflection, so the codec that
// walks a Smithy shape decides every element, namespace and attribute, and
// equal call sequences produce equal bytes. StartXSIType writes the S3 ACL
// Grantee form that encoding/xml cannot produce.
//
// Character data and attribute values share one escape table, the one the AWS
// SDKs use (smithy-go encoding/xml/escape.go): & < > as entities, quotes as
// &#34; and &#39;, tab, LF and CR as numeric references (so CR and attribute
// whitespace survive parsing), and NEL U+0085 and LINE SEPARATOR U+2028 as
// numeric references. Where the SDK replaces characters outside the XML 1.0
// Char production with U+FFFD, this writer emits a numeric reference for every
// control character, and does the same for U+FFFE and U+FFFF, so the code
// point is not lost silently. Whether S3 writes
// such characters the same way is UNVERIFIED. Strict XML 1.0 parsers, Decode
// among them, reject those references; a listing that may carry such keys
// should use encoding-type=url. Bytes that are not valid UTF-8 are each
// replaced with U+FFFD. The output carries no indentation.
//
// Decode parses a request body with encoding/xml into a tree of Nodes named by
// local name, ignoring namespaces, because SDKs send S3 bodies both with and
// without the S3 namespace. It enforces a byte limit (TooLargeError) and
// MaxDepth, and reports every other defect as MalformedXMLError, the S3
// MalformedXML condition.
package xmlw
