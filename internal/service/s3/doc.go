// Package s3 implements Amazon S3: buckets and their subresources, objects,
// multipart uploads, flexible checksums, conditional requests and presigned
// URLs, with virtual-hosted and path-style addressing. What exists today is
// below.
//
// Bound so far: CreateBucket, HeadBucket, GetBucketLocation, ListBuckets
// and DeleteBucket; the Get, Put and Delete operations of every bucket
// subresource Terraform reads (versioning, ACL, CORS, website, policy,
// logging, lifecycle, replication, encryption, Object Lock, request
// payment, transfer acceleration, public access block, ownership controls,
// tagging, and the metrics, analytics, inventory and Intelligent-Tiering
// families with their List operations); GetBucketNotificationConfiguration;
// and the S3 Control tag operations ListTagsForResource, TagResource and
// UntagResource, which share each bucket's tag set. Object and multipart
// operations and PutBucketNotificationConfiguration answer 501; objects.go,
// multipart.go and notification.go hold their bind functions and the
// commitObject and notify seams the object operations will commit through.
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
//     phase 1 evaluates no bucket policy or ACL (s3.bucket.cross-account).
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
package s3
