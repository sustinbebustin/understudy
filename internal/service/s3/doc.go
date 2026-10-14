// Package s3 implements Amazon S3: buckets and their subresources, objects,
// multipart uploads, flexible checksums, conditional requests and presigned
// URLs, with virtual-hosted and path-style addressing.
//
// Bound operations: CreateBucket, HeadBucket, GetBucketLocation, ListBuckets
// and DeleteBucket; the Get, Put and Delete operations of every bucket
// subresource Terraform reads (versioning, ACL, CORS, website, policy,
// logging, lifecycle, replication, encryption, Object Lock, request
// payment, transfer acceleration, public access block, ownership controls,
// tagging, and the metrics, analytics, inventory and Intelligent-Tiering
// families with their List operations); Get and
// PutBucketNotificationConfiguration; and the S3 Control tag operations
// ListTagsForResource, TagResource and UntagResource, which share each
// bucket's tag set. The object operations PutObject, GetObject, HeadObject,
// DeleteObject, DeleteObjects, CopyObject, ListObjects, ListObjectsV2,
// ListObjectVersions and the object tagging operations are bound, as are
// the multipart operations CreateMultipartUpload, UploadPart,
// UploadPartCopy, CompleteMultipartUpload, AbortMultipartUpload, ListParts
// and ListMultipartUploads; every object change lands through
// commitObject, which raises the bucket's event notifications as Deliver
// effects in the same write (notify).
//
// State is a partition-wide name index (bucket names are global across
// accounts) and one cell per bucket (ARCHITECTURE.md 8 rule 3).
// CreateBucket reserves the name in the index, then writes the bucket's
// cell; DeleteBucket empties the cell, then releases the name. A fresh
// bucket carries AWS's defaults (s3.md 4.1): BucketOwnerEnforced ownership,
// every public access block flag on, and the SSE-S3 default encryption.
//
// Decisions the architecture leaves open:
//
//   - The region a request asserts is the call's aws.RegionSource: a SigV4
//     request's credential scope (a cross-region one is
//     AuthorizationHeaderMalformed), and none for SigV2 and internal calls
//     (the bucket is served from its home region). An anonymous request to
//     an s3.<region>.<base> host asserts the host's region (a cross-region
//     one is PermanentRedirect); without such a host it asserts none.
//   - s3.crossRegion: lenient records a cross-region-served Advisory on the
//     call's journal event, naming both regions.
//   - A host asserts a region only through s3.<region> directly after the
//     bucket's own labels, so a dotted bucket name holding s3.<region>
//     asserts none.
//   - PermanentRedirect's Endpoint is the request's host with the region
//     label after the bucket's own labels replaced by the bucket's, port
//     kept.
//   - Every error read, change and DeleteBucket answer about an existing
//     bucket carries x-amz-bucket-region; the error builders leave it out.
//     This narrows ARCHITECTURE.md 7, which has S3 always set the header
//     for a request that asserts no region: a success carries it only
//     where the operation's output models it (HeadBucket), since a
//     handler cannot add a header to a modeled success response.
//   - A request from an account that does not own the bucket is served:
//     understudy evaluates no bucket policy or ACL (s3.bucket.cross-account).
//     x-amz-expected-bucket-owner naming another account is AccessDenied.
//   - Reset runs under the gate's exclusive side, where no cell can be
//     read, so it reads the index through its own snapshot section, drops
//     the covered buckets and writes the kept entries back with Import.
//   - Import drops an index name whose bucket section is missing: a
//     snapshot cut between the two writes of CreateBucket or DeleteBucket
//     saves one, and loaded as is the name could never be used again.
//   - The S3 Control tag operations are raw operations, since overlays
//     cannot add routable operations; they write S3 Control's own XML
//     namespace and answer a missing bucket with NoSuchBucket. They accept
//     a TagResource body under any root element, and name a malformed
//     x-amz-account-id under that header. Raw operations get no route
//     row: edge rule 3 sends /v20180820/tags/ on the account host to them
//     by method, and renders their errors in S3 Control's wrapped
//     <ErrorResponse><Error> form, which the SDKs' s3control clients read
//     (TestS3ControlTagsOverHTTP, TestS3ControlErrorsOverHTTP).
//   - ListBuckets pages with a token that is the base64url of the last name
//     returned; a token understudy did not mint is InvalidArgument.
//   - The document checks with S3's own texts (tags, versioning,
//     encryption, lifecycle, ownership, Object Lock, policy, CORS) run at
//     step 7 through kernel.WithCheck; the generated constraint checks
//     still answer InvalidArgument in the Coral grammar, not S3's texts.
//
// Decisions for objects:
//
//   - Bodies live in the content-addressed blob store; a version holds one
//     reference on its blob, as an upload's part does; BucketCell.BlobRefs
//     lists both, object versions and upload parts, for snapshots and the
//     admin reset. Objects.Keys, Uploads.ByID and each Upload.Parts are
//     changed in place inside the bucket's kernel.Write, after every check
//     passed, so a write copies no key set; no read returns those maps, a
//     version chain or an upload.
//   - x-amz-version-id is left out for a null version on a bucket never
//     versioned (reportedVersion), so Terraform does not take its
//     versioned delete path there (tf-remaining.md 8); a write to a
//     Suspended bucket leaves it out too, while reads and delete markers
//     there report "null" (writtenVersion). A well-formed version id that
//     names none of an existing key's versions is InvalidArgument on
//     DeleteObject. In DeleteObjects an id naming no version of the key is
//     a NoSuchVersion entry whatever the bucket's versioning, save "null"
//     on a bucket never versioned. On a key with no versions, DeleteObject
//     deletes nothing with 204.
//   - Every version and upload takes the bucket's next Seq, which orders a
//     key's versions and is carried in the version ids minted: a listing
//     resumes after a version-id-marker deleted since, and a completed
//     upload on a versioned bucket ranks by when it was initiated.
//   - SSE-KMS stores the bytes as sent: a write asks KMS for a data key
//     and every GetObject asks KMS to decrypt it, both charged to the
//     caller, so the key's state is observable. Its ETag is random hex.
//     SSE-C writes are refused with AccessDenied, as new buckets block
//     SSE-C, and SSE-C headers on a read or copy source with
//     InvalidRequest; a KMS key id without aws:kms is InvalidArgument
//     whatever the bucket default.
//   - aws-chunked bodies map their errors as bodyError says:
//     IncompleteBody, MalformedTrailerError, InvalidChunkSizeError, and
//     NotImplemented for SigV4a streaming. Under sigv4 verify, once the
//     edge verified the header signature, each chunk and trailer signature
//     is verified against the key's secret (readChunkFraming); under
//     sigv4 parse they are parsed but not verified.
//   - A malformed or multi-range Range is ignored (whole object, 200) per
//     RFC 9110; a ranged read answers 206 through the restXml S3
//     dialect's PartialContent. An object written in one request is its
//     own part 1, served whole.
//   - GetObject opens the body while the read still holds a reference on
//     the blob, so a concurrent overwrite cannot fail an admitted read. A
//     body the encoder never takes is closed by kernel.Release.
//   - Object bodies are sent with X-Content-Type-Options: nosniff and
//     Content-Security-Policy: sandbox (the dialect's InertStreams), which
//     AWS does not send: they share an origin with the admin prefix. The
//     response-* overrides need a signed or presigned request, as AWS
//     documents; an unsigned one is InvalidRequest (text unrecorded).
//   - CopyObject reads its source without the cross-region and owner
//     admission, since copies may cross regions and understudy enforces no
//     access. The source read's blob reference becomes the copy's. A copy
//     source decodes valid %XX sequences, %2F included, as Terraform
//     escapes the separator, and keeps a literal '+' (UNVERIFIED for
//     Terraform's form). Its over-5-GiB text is UNVERIFIED.
//   - encoding-type=url is applied by the restXml codec; the handlers only
//     validate it and echo EncodingType.
//   - On a BucketOwnerEnforced bucket, any object canned ACL other than
//     bucket-owner-full-control and any x-amz-grant-* header is refused
//     (s3.md 4.2.4); elsewhere an object ACL is accepted and not stored.
//   - Presigned URLs, SigV4 and SigV2, are verified by the edge under
//     sigv4 verify and expire on the emulator clock, refused in S3's words
//     (AuthDialect); under sigv4 parse they are accepted unverified and
//     never expire.
//
// Decisions for multipart uploads:
//
//   - A part is stored in the blob store as it arrives and holds one
//     reference on its blob, which BucketCell.BlobRefs lists beside the
//     object versions' and DeleteBucket releases with the uploads it drops.
//     CompleteMultipartUpload concatenates the listed parts into one new
//     blob, the object's, outside the bucket's lock and with a reference
//     held on every listed part meanwhile; it then commits the object,
//     drops the upload and releases every part, listed or not. A listed
//     part replaced in between is InvalidPart.
//   - Each part is checksummed with its upload's algorithm, CRC64NVME for an
//     upload that declared none, so the object's checksum comes from the
//     part checksums alone: checksum.Combine for FULL_OBJECT and
//     checksum.Composite for COMPOSITE. A part that declares another
//     algorithm (an undeclared upload accepts any) or none is verified as
//     sent and checksummed with the upload's algorithm in the same pass.
//   - An undeclared upload reports no part checksums (UploadPart,
//     UploadPartCopy, ListParts), so clients restate none on Complete.
//   - The completed object's Last-Modified is the upload's initiation, to
//     the second, as its metadata, tags, storage class and encryption are
//     Create's (s3.md 10). An SSE-KMS upload's data key is asked for at
//     Create; its parts carry random hex ETags, and its object the MD5 of
//     those raw part ETags with the "-N" suffix, as for any upload.
//   - Every Complete error is found before the response starts, so the
//     handler answers ordinary 4xx and 5xx; only a StatusOKError fault rule
//     sends the 200-with-error answer (wire-rest.md 3.7).
//   - A COMPOSITE Complete that skips a part number is InvalidPart, where
//     S3 answers 500 (s3.md 6.4).
package s3
