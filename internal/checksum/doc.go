// Package checksum implements the ten S3 flexible checksum algorithms,
// including in-repo ports of XXHASH64, XXHASH3 and XXHASH128 written from the
// public xxHash specification, and CRC combination for multipart objects.
//
// The wire form of every algorithm is standard padded base64 of the
// big-endian digest; the xxHash variants use seed 0 and the default secret,
// and XXHASH128 is high64 then low64, which is what the AWS CRT behind
// botocore, the Java v2 SDK and the C++ SDK computes
// (s3-checksum-residuals-and-record-backlog.md 1.1).
//
// A Hash computes a Sum; a Sum knows its algorithm, so Composite and Combine
// cannot mix algorithms and Sum.String is the wire encoding. Verify compares a
// received x-amz-checksum-* value with a computed Sum and tells a malformed
// value (S3 InvalidRequest) from a wrong one (S3 BadDigest). It lives here
// rather than in awschunked, which imports nothing in the repository.
//
// The package states what each algorithm can do (Combinable is the
// FULL_OBJECT side of the S3 multipart matrix); enforcing which multipart
// checksum type a request may use is the S3 service's job.
package checksum
