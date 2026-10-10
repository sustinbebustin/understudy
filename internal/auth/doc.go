// Package auth parses SigV4 and SigV2 credentials into a sum type and
// implements the opt-in signature verification, presigning and payload hash.
//
// Parsing always runs; verification runs only with sigv4: verify
// (ARCHITECTURE.md 7). The verifiers are pure functions over a Normalized
// request: request-time skew is judged on wall time, because clients sign
// with their own clocks, and presigned-URL expiry on the emulator clock, so
// advancing it expires URLs (lambda-runtime-contract.md 3 rows 9 and 10).
//
// Decisions the architecture leaves open:
//
//   - Parse returns a ParseError sum rather than an awserr.Error. It runs
//     before the request is claimed, when the protocol family, and with it
//     the error identity and text, is not yet known; the caller renders it
//     with AWSError once the family is. Parse takes the region table because
//     the credential-scope region is validated at parse time.
//   - The verifiers take a Vocabulary, the auth identities and wording
//     (Dialect) of one protocol family (error-matrix.md 3), as their
//     options. The JSON and Query families follow DynamoDB's recorded
//     answers (error-matrix.md 5.5, T8): two mechanisms at once, a wrong
//     credential scope (region, terminator or date, even a date that is not
//     yyyyMMdd) and an unsigned host are signature mismatches, an
//     Authorization header outside the AWS4- and AWS schemes is missing
//     credentials, and a skewed request is a mismatch with AWS's text. No
//     recording shows an identity for an expired presigned URL, so it is a
//     mismatch too.
//   - Under S3Dialect the verifiers answer as S3 does, with its texts and
//     extra elements in awserr.Error.Fields: AWSAccessKeyId, StringToSign,
//     SignatureProvided, StringToSignBytes and, for SigV4, CanonicalRequest
//     and CanonicalRequestBytes on a mismatch; X-Amz-Expires (SigV4),
//     Expires and ServerTime on an expired URL; RequestTime, ServerTime and
//     MaxAllowedSkewMilliseconds on a skewed request. The skew text is the
//     one S3 responses carry ("the current time"), not the error list's
//     ("the server's time"). The session token is redacted from every
//     element, as from the Coral message.
//   - A presigned request carrying an x-amz-* header it did not sign is
//     refused in every family (HeadersNotSigned), as SigV4 requires. S3
//     names the headers in its HeadersNotSigned element; the Coral answer,
//     unrecorded, names them in the message instead, since Coral has no such
//     member, and its text is understudy's own (UNVERIFIED). Header-signed
//     requests are not checked, although S3 refuses them too (REC
//     test_put_object_with_md5_and_chunk_signature_bad_headers): until the
//     SDK conformance runs show every SDK signs every x-amz-* header it
//     sends, the check could refuse real clients.
//   - A presigned request whose signed header and query parameter of the
//     same name differ in value is refused in every family (Conflicting), but
//     only once its signature verifies: S3 answers a mismatch first (REC
//     test_s3_put_presigned_url_same_header_and_qs_parameter, where an
//     added query parameter breaks the signature). S3's InvalidRequest for a
//     request whose signature verifies is documented
//     (sigv4-checksums-endpoints.md 2.1) but UNVERIFIED; the Coral families,
//     unrecorded, answer a mismatch. The text is understudy's own
//     (UNVERIFIED).
//   - Where S3 answers a parse failure with an identity no Vocabulary holds,
//     AWSError gives Coral's answer whatever the Dialect, and S3 switches on
//     the variant instead: UnknownRegion (AuthorizationHeaderMalformed with
//     the expected region), Conflict and UnknownScheme (InvalidArgument),
//     Invalid with InvalidRequestTime (AccessDenied), and Invalid with
//     MissingPayloadHash (InvalidRequest, as S3-compatible servers answer;
//     UNVERIFIED against AWS). So that S3 can, Parse, not VerifySigV2,
//     reads a SigV2 header's request time, and Parse, not Verify, refuses
//     an S3 header request without x-amz-content-sha256.
//   - A credential scope naming an unknown region is its own ParseError
//     variant, UnknownRegion, carrying the name as sent, because S3 answers
//     it with AuthorizationHeaderMalformed and the expected region.
//   - Checks run in Coral's order (dynalite index.js L296-L372) with one
//     deviation: Parse has no wall clock, so the skew check, which Coral runs
//     right after the date format, comes after every parse check, and a
//     request that is both skewed and, say, missing host from its signed
//     headers gets the host error. A scope date that differs from the
//     request date is a verification failure, checked by Verify after skew;
//     Parse refuses only one that is not yyyyMMdd.
//   - Conflict names the first two credential carriers found. Coral detects
//     only an Authorization header with X-Amz-Algorithm; the text for any
//     other pair is understudy's own in Coral's form.
//   - X-Amz-Expires is required in every family, although DynamoDB's
//     missing-parameter list omits it: every SDK presigner sends it, and an
//     expiry is needed to judge the URL. It must lie in 1..604800, the
//     documented range; the text for 0 is understudy's own, as no recording
//     shows AWS's.
//   - x-amz-content-sha256 must be lowercase hex: the payload line is the
//     lowercase hash, so an uppercase value could never verify.
//   - The skew window is 15 minutes for every service.
//   - VerifyPresigned also takes the wall time: a URL whose X-Amz-Date is more
//     than 15 minutes ahead of it is not yet valid, while expiry stays on the
//     emulator clock.
//   - The dependency rules keep auth from importing awschunked, so
//     ChunkSignatureVerifier returns a ChunkVerifier with Chunk and Trailer
//     methods, and the S3 service adapts them to an awschunked.SignatureFunc.
//     ChunkVerifier holds the day's signing key and redacts it in LogValue,
//     MarshalJSON, String and GoString.
//   - Sign and Presign take a Signer (key, secret, token, region, service);
//     they exist for tests and tools that play the client. Signer redacts
//     its secret and token in LogValue, MarshalJSON, String and GoString.
package auth
