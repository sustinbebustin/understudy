// Package awschunked is a streaming decoder for aws-chunked request bodies in
// all four modes; it resolves trailers at end of stream.
//
// Behavior a caller relies on (sigv4-checksums-endpoints.md 3; s3.md 6.3):
//
//   - A Reader never buffers a chunk. In signed modes it hands the chain
//     check the SHA-256 of each chunk once the chunk's data is read, so data
//     reaches the caller before its signature is checked; the caller commits
//     only after io.EOF, which is when BadDigest, IncompleteBody and
//     signature failures become handler outcomes.
//   - x-amz-decoded-content-length is optional (Length): botocore omits it
//     for bodies of unknown length. When declared, a body that ends short is
//     an IncompleteBodyError, and a chunk header that announces more is a
//     DecodedLengthExceededError; the S3 code for the larger case is not
//     recorded.
//   - In every mode, signed or not, each data chunk but the last must hold at
//     least 8,192 bytes, as the S3 user guide requires for unsigned trailer
//     bodies (sigv4-checksums-endpoints.md 3.2) and signed ones (3.3); a
//     smaller one is a ChunkTooSmallError, raised at the next non-zero chunk
//     header. Real S3 rejects small unsigned chunks too: the AWS JS SDK's e2e
//     test sends 8-byte chunks on the unsigned trailer path and expects S3's
//     InvalidChunkSizeError, which is why that SDK buffers to 8 KiB.
//   - Chunk header lines end with CRLF. A trailer field line ends with CRLF
//     or LF CRLF, the form the S3 user guide shows. One trailer field is
//     allowed and it must be the one x-amz-trailer declared. Bytes after the
//     closing empty line are malformed.
//
// Trailer checksums are checked by the caller with checksum.Verify against
// what it computed over the decoded bytes.
package awschunked
