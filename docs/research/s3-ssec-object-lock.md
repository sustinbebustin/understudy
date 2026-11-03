# S3 SSE-C and Object Lock: primary-source research

Scope: roadmap item 9, server-side encryption with customer-provided keys (SSE-C), the per-bucket SSE-C block that AWS made the default in April 2026, and S3 Object Lock (bucket configuration, per-object retention and legal holds, deletes under lock, the new variable retention). Sources: the S3 API reference and user guide (Markdown twins, retrieved 2026-10-25), the S3 Smithy model vendored at `third_party/api-models-aws` (aws/api-models-aws @ 1cfdfb4bb9ea858e65623e66d73af04bec353201), LocalStack's AWS-validated snapshots (8 `TestS3SSECEncryption` cases and 20 Object Lock cases, all last validated 2026-02-21), the repo's own AWS recordings in `testdata/parity/s3/R06`, `R12` and `R15` (2026-10-14), botocore source, and hashicorp/terraform-provider-aws v6.68.0. Retrieved 2026-10-25. It extends [s3.md](s3.md) 4.1, 4.2.10 and 5.7 and does not repeat the wire layer ([wire-rest.md](wire-rest.md)).

## 1. Sources and labels

| Label | Meaning | Pin |
|---|---|---|
| aws-doc | AWS documentation | the pages in the link list below, retrieved 2026-10-25 |
| model | Smithy model, cited by shape id (`com.amazonaws.s3#PutObjectRetentionRequest$BypassGovernanceRetention`) | `third_party/api-models-aws/models/s3/service/2006-03-01/s3-2006-03-01.json` @ 1cfdfb4 |
| aws-recorded | this repo's captures against real AWS, cited by repo-relative path | `testdata/parity/s3/R06`, `R12`, `R15`, us-east-2, 2026-10-14 |
| third-party-recorded | LocalStack snapshots recorded against AWS. "LS" below means `localstack@8b9a79f tests/aws/services/s3/`; every cited test carries `last_validated_date` 2026-02-21 in `test_s3.validation.json` / `test_s3_api.validation.json`, which is **before** the April 2026 SSE-C block, so the SSE-C recordings ran on buckets that allowed SSE-C | localstack@8b9a79f (archived repo, final commit) |
| sdk-source | client source | botocore @ bfb412faf00c249a54816e287a09a8bb79442c65, aws-sdk-go-v2 @ ab2110b01f03b3990bfd50208a8a05df1d8c5eac |
| tf | "TF" below means `hashicorp/terraform-provider-aws v6.68.0 internal/service/s3/` | tag v6.68.0 (c9b83d8) |
| emu | other emulators' source; a hint, never proof | named where used |
| inferred | reasoning from the above; marked UNVERIFIED where it decides behavior | |

Documentation pages used (short name, URL):

- ssec: https://docs.aws.amazon.com/AmazonS3/latest/userguide/ServerSideEncryptionCustomerKeys.html
- ssec-spec: https://docs.aws.amazon.com/AmazonS3/latest/userguide/specifying-s3-c-encryption.html
- ssec-block: https://docs.aws.amazon.com/AmazonS3/latest/userguide/blocking-unblocking-s3-c-encryption-gpb.html
- ssec-faq: https://docs.aws.amazon.com/AmazonS3/latest/userguide/default-s3-c-encryption-setting-faq.html
- lock: https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html
- lock-manage: https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html
- lock-config: https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-configure.html
- notif-types: https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-how-to-event-types-and-destinations.html
- notif-shape: https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-content-structure.html
- API pages: `https://docs.aws.amazon.com/AmazonS3/latest/API/API_<Name>.html` for PutObject, GetObject, HeadObject, CopyObject, CreateMultipartUpload, UploadPart, UploadPartCopy, CompleteMultipartUpload, GetObjectAttributes, PutObjectRetention, GetObjectRetention, PutObjectLegalHold, GetObjectLegalHold, PutObjectLockConfiguration, GetObjectLockConfiguration, DeleteObject, DeleteObjects, PutBucketEncryption, BlockedEncryptionTypes, ServerSideEncryptionRule, Error.

## 2. What the repo's recordings already settled

Lines that [s3.md](s3.md) or the code marked UNVERIFIED and that `testdata/parity/s3` has since settled:

| Former claim | Now | Evidence |
|---|---|---|
| s3.md 4.1: the wire form of the April 2026 SSE-C block on a new bucket is UNVERIFIED | A fresh bucket's `GetBucketEncryption` rule is, in this order: `<BucketKeyEnabled>false</BucketKeyEnabled>`, `<ApplyServerSideEncryptionByDefault><SSEAlgorithm>AES256</SSEAlgorithm></ApplyServerSideEncryptionByDefault>`, `<BlockedEncryptionTypes><EncryptionType>SSE-C</EncryptionType></BlockedEncryptionTypes>` | `testdata/parity/s3/R06/01-get-get-bucket-encryption.json` |
| s3.md 4.1: `GetBucketEncryption` after `DeleteBucketEncryption` returns the default rule again (UNVERIFIED; switch `s3.encryption.after-delete`, label aws-doc) | 200 with the same default rule, SSE-C still blocked | `testdata/parity/s3/R06/02-get-encryption-after-delete.json`; the switch can be relabelled aws-recorded |
| (new) a `PutBucketEncryption` that omits `BlockedEncryptionTypes` | the GET still reports SSE-C blocked (starting from the default; starting from `NONE` is S3SOL-11) | `testdata/parity/s3/R06/02-put-encryption-valid-arn.json` then `02-get-encryption-arn.json` |
| s3.md 4.2.10: the error for an Object Lock upload without `Content-MD5` or a checksum is UNVERIFIED (moto's `MissingUploadObjectWithObjectLockHeaders`) | `400 InvalidRequest`, "Content-MD5 OR x-amz-checksum- HTTP header is required for Put Object requests with Object Lock parameters", no extra elements, even when the request carries no lock header and the retention comes from the bucket default | `testdata/parity/s3/R12/11-put-default-retention.json`, `11-put-without-checksum.json` |
| `internal/service/s3/sse.go` `errSSECNotApplicable`: "The text is UNVERIFIED" | `400 InvalidRequest`, "The encryption parameters are not applicable to this object." for SSE-C headers on a GET of a non-SSE-C object. The bucket was created on 2026-10-14 and R15 did not unblock it, so it blocks SSE-C. That state is inferred from 4.1 and row 3 here: R15 did not record the bucket's encryption configuration | `testdata/parity/s3/R15/x-ssec-on-non-ssec-read.json` |

Still open: `internal/service/s3/sse.go` `errSSECBlocked` (the 403 text, S3SOL-01). `testdata/parity/s3/R12/notes.md` step 11 also records that understudy stores an Object Lock upload it should refuse.

## 3. SSE-C on objects

### 3.1 Headers and the operations that take them

- Three request headers, all required together: `x-amz-server-side-encryption-customer-algorithm` ("The header value must be AES256"), `x-amz-server-side-encryption-customer-key` ("the 256-bit, base64-encoded encryption key"), `x-amz-server-side-encryption-customer-key-MD5` ("the base64-encoded 128-bit MD5 digest of the encryption key according to RFC 1321") ([ssec-spec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/specifying-s3-c-encryption.html), "S3 API headers required"). The copy-source trio is `x-amz-copy-source-server-side-encryption-customer-{algorithm,key,key-MD5}` (same page).
- Model members: `SSECustomerAlgorithm`, `SSECustomerKey`, `SSECustomerKeyMD5` on `com.amazonaws.s3#PutObjectRequest`, `GetObjectRequest`, `HeadObjectRequest`, `CopyObjectRequest`, `CreateMultipartUploadRequest`, `UploadPartRequest`, `UploadPartCopyRequest`, `CompleteMultipartUploadRequest`, `GetObjectAttributesRequest`, and also `ListPartsRequest` and `SelectObjectContentRequest` (SelectObjectContent is outside this item); the `CopySourceSSECustomer*` members only on `CopyObjectRequest` and `UploadPartCopyRequest`. `com.amazonaws.s3#SSECustomerKey` and `#CopySourceSSECustomerKey` carry `@sensitive`.
- Write operations that accept SSE-C: CopyObject, CreateMultipartUpload, CompleteMultipartUpload, POST Object, PutObject, UploadPart, UploadPartCopy ([ssec-spec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/specifying-s3-c-encryption.html), "Amazon S3 actions that support writing data with SSE-C"). Reads: GetObject, HeadObject, GetObjectAttributes (same page; [API_GetObjectAttributes](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectAttributes.html)).
- Directory buckets do not support SSE-C ([API_GetObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObject.html)); understudy has none.

### 3.2 Write-side validation (third-party-recorded)

All 400, all with `ArgumentName` `x-amz-server-side-encryption` (sic, even for copy-source headers):

| Request | Code | Message | Extra | Source |
|---|---|---|---|---|
| `x-amz-server-side-encryption: AES256` plus SSE-C headers | `InvalidArgument` | Server Side Encryption with Customer provided key is incompatible with the encryption method specified | `ArgumentValue` = the SSE value | LS test_s3.snapshot.json:L12706 (PutObject), L12984 (CopyObject) |
| algorithm other than `AES256` | `InvalidEncryptionAlgorithmError` | The Encryption request you specified is not valid. Supported value: AES256. | `ArgumentValue` = the algorithm | LS test_s3.snapshot.json:L12718 (PutObject), L12960 (copy source), L12972 (copy target) |
| key and MD5, no algorithm | `InvalidArgument` | Requests specifying Server Side Encryption with Customer provided keys must provide a valid encryption algorithm. | | LS test_s3.snapshot.json:L12730 |
| algorithm and MD5, no key | `InvalidArgument` | Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key. | | LS test_s3.snapshot.json:L12741 |
| key that decodes to other than 32 bytes (10 bytes; 44 bytes) | `InvalidArgument` | The secret key was invalid for the specified algorithm. | | LS test_s3.snapshot.json:L12763, L12752, L12875 |
| MD5 that does not match the key | `InvalidArgument` | The calculated MD5 hash of the key did not match the hash that was provided. | | LS test_s3.snapshot.json:L12774, L12886 |

- Order: the SSE-header conflict is checked before the algorithm value (the L12706 request also sent algorithm `KMS`; test inputs at LS test_s3.py:L12123-L12131).
- Two snapshot steps named `get-obj-sse-c-wrong-key-size` and `get-obj-sse-c-bad-md5` are PutObject calls (LS test_s3.py:L12287, L12299), so they are write-side evidence.
- The `put-obj-sse-c-no-md5` step (L12752) does not test a missing MD5: botocore fills the MD5 in itself when the caller omits it (3.12), base64-encoding the already-base64 key string, so AWS saw a 44-byte key with a matching MD5. The response is the key-size error.
- UNVERIFIED (S3SOL-03): a raw request with algorithm and key but no MD5 header. Options: an `InvalidArgument` naming the missing MD5 (Cloudflare's R2 emulator and MinIO both use "Requests specifying Server Side Encryption with Customer provided keys must provide the client calculated MD5 of the secret key.", emu, `cloudflare/workers-sdk packages/miniflare/src/workers/r2/s3/operations.worker.ts`, `minio/minio cmd/api-errors.go` `ErrMissingSSECustomerKeyMD5`), or S3 computing it. Docs say all three headers are required.
- UNVERIFIED (S3SOL-04): a key header that is not valid base64, and the read-side key checks (an MD5 mismatch or a short key on GET). Plausible: the write-side texts above for a key that is not base64 and for an MD5 mismatch. A short key with a matching MD5 on GET plausibly gets the 403 that a 44-byte key got (3.3).

### 3.3 Reads: GetObject, HeadObject, GetObjectAttributes

| Request | Status, code | Message | Source |
|---|---|---|---|
| GET of an SSE-C object without SSE-C headers | 400 `InvalidRequest` | The object was stored using a form of Server Side Encryption. The correct parameters must be provided to retrieve the object. | LS test_s3.snapshot.json:L12801, L12940, L13093 |
| GET with a different (valid) key | 403 `AccessDenied` | Requests specifying Server Side Encryption with Customer provided keys must provide the correct secret key. | LS test_s3.snapshot.json:L12811, L13301 (another version's key) |
| GET with the botocore double-encoded 44-byte key and its matching MD5 | 403 `AccessDenied` | same as above (not the key-size error) | LS test_s3.snapshot.json:L12855 |
| HEAD with the 44-byte key | 403, no body | | LS test_s3.snapshot.json:L12865 |
| GET / GetObjectAttributes with algorithm `KMS` | 400 `InvalidEncryptionAlgorithmError` (as 3.2) | | LS test_s3.snapshot.json:L12821, L12843 |
| HEAD with algorithm `KMS` | 400, no body | | LS test_s3.snapshot.json:L12833 |
| GET with SSE-C headers of an object that is not SSE-C | 400 `InvalidRequest` | The encryption parameters are not applicable to this object. | aws-recorded `testdata/parity/s3/R15/x-ssec-on-non-ssec-read.json` |

- With the right key, GET, HEAD and GetObjectAttributes succeed (LS test_s3.snapshot.json:L13190, L13204, L13221); `DeleteObject` needs no key (L13230, 204).
- On GET a key of the wrong length is answered like a wrong key (row 3), so the stored-key comparison runs before, or instead of, the length check on reads (inferred from one recording).
- HEAD errors have no body, as for every HEAD ([API_HeadObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadObject.html)).
- UNVERIFIED (S3SOL-05): HEAD and GetObjectAttributes of an SSE-C object without SSE-C headers. Plausible: HEAD 400 with no body, GetObjectAttributes 400 `InvalidRequest` with the "stored using a form of Server Side Encryption" text. The docs say these headers "must" be sent to "retrieve the object's metadata" ([API_HeadObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadObject.html)). This decides Terraform's `aws_s3_object_copy` refresh (9.4).

### 3.4 Response headers and ETag

- SSE-C writes and reads report `x-amz-server-side-encryption-customer-algorithm: AES256` and `x-amz-server-side-encryption-customer-key-MD5: <request MD5>`, and **no** `x-amz-server-side-encryption` header (PutObject LS test_s3.snapshot.json:L12790; HEAD L13190; GET L13204; CopyObject L12926; CreateMultipartUpload L13001; UploadPart L13012). Every non-SSE-C object reports `x-amz-server-side-encryption: AES256` ([s3.md](s3.md) 5.2), so this is a branch, not an omission.
- `CompleteMultipartUpload` echoes no SSE-C header (LS test_s3.snapshot.json:L13083), matching `com.amazonaws.s3#CompleteMultipartUploadOutput`, which has no SSE-C members. `com.amazonaws.s3#GetObjectAttributesOutput` has no SSE members at all.
- ETag of an SSE-C single PUT or SSE-C copy is not the MD5 of the data ([s3.md](s3.md) 5.3, `API_Object`): three PUTs of `test_data` gave `b68bda52...`, `b876fdc9...`, `b84c1aee...` (L12790, L12902, L13179) and an SSE-C copy of it gave `5e04d65f...` (L12930), while `md5("test_data")` is `6af8307c2460f2d208ad254f04be4b0d` (computed). A random 128-bit hex value per write matches.
- SSE-C parts get random ETags too: three identical 5,242,881-byte parts got three different ETags (L13012-L13032; the MD5 of that content is `c4c753e6...`). The object's ETag keeps the `-N` suffix but is **not** the MD5 of the returned part ETags: AWS answered `"2d296eb6540ecd21430c385336c5dc9c-3"` on Complete and GET (L13083), while the MD5 of the three 16-byte part ETags `cf96b8ba... baa1d9ab... 6e702181...` is `f65e8fc76274ff489bb2b9cb55d378a4` and the MD5 of the three plaintext part MD5s is `c7cb0938a47e31f70cf07028d22e6913` (both computed). The hex digits come from nothing the client sees (inferred).
- Copying an SSE-C source to a non-SSE-C target gives the plaintext MD5 ETag (`6af8307c...`, L12913) and `x-amz-server-side-encryption: AES256`.

### 3.5 CopyObject and UploadPartCopy

- Source SSE-C headers decrypt the source, target SSE-C headers encrypt the target; either can appear without the other; `x-amz-server-side-encryption` selects another target encryption ([ssec-spec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/specifying-s3-c-encryption.html), "Copy operation"). Recorded: SSE-C source to SSE-S3 target (L12913) and to SSE-C target (L12926).
- Copying an SSE-C source without the source headers: 400 `InvalidRequest`, "The object was stored using a form of Server Side Encryption. The correct parameters must be provided to retrieve the object." (L12950).
- UNVERIFIED (S3SOL-06): copy-source SSE-C headers on a source that is not SSE-C. Plausible: the R15 GET text "The encryption parameters are not applicable to this object." (what `internal/service/s3/copy.go` answers today).
- UNVERIFIED (S3SOL-07): copy-source headers with the wrong key, on CopyObject and UploadPartCopy. Plausible: 403 `AccessDenied` with the GET "correct secret key" text.
- UploadPartCopy "see CopyObject and UploadPart" ([API_UploadPartCopy](https://docs.aws.amazon.com/AmazonS3/latest/API/API_UploadPartCopy.html)); no recording. Treat it as CopyObject's source rules plus UploadPart's target rules (inferred; S3SOL-07 includes an UploadPartCopy step).

### 3.6 Multipart uploads

- "If you request server-side encryption using a customer-provided encryption key (SSE-C) in your initiate multipart upload request, you must provide identical encryption information in each part upload" ([API_UploadPart](https://docs.aws.amazon.com/AmazonS3/latest/API/API_UploadPart.html)).
- Part without SSE-C on an SSE-C upload, and part with SSE-C on a non-SSE-C upload: both 400 `InvalidRequest`, "The multipart upload initiate requested encryption. Subsequent part requests must include the appropriate encryption parameters." (LS test_s3.snapshot.json:L13154, L13133). Part with another key: 400 `InvalidRequest`, "The provided encryption parameters did not match the ones used originally." (L13164).
- Complete needs no SSE-C headers ([ssec-spec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/specifying-s3-c-encryption.html): "You do not need to provide any encryption-specific headers in your CompleteMultipartUploadRequest"; recorded at L13083 for an upload without a checksum algorithm), except that the model says the key "is needed only when the object was created using a checksum algorithm" (`com.amazonaws.s3#CompleteMultipartUploadRequest$SSECustomerKey`).
- UNVERIFIED (S3SOL-08): Complete without SSE-C headers on an SSE-C upload that declared a checksum algorithm, Complete with a mismatching key, and `ListParts` without SSE-C headers on such an upload. Plausible: the UploadPart texts above for Complete; ListParts answering 200 as below.
- `ListParts` works without SSE-C headers on an SSE-C upload without a checksum algorithm (L13042). `com.amazonaws.s3#ListPartsRequest$SSECustomerKey` carries the same "needed only when the object was created using a checksum algorithm" note as Complete (model), so the checksum-algorithm case is part of S3SOL-08.

### 3.7 Versions

Each version can have its own key; reading version 1 with version 2's key is the 403 above (L13241-L13311; [ssec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/ServerSideEncryptionCustomerKeys.html): "each object version that you upload can have its own encryption key").

### 3.8 Checksums

SSE-C does not change the checksum rules of [s3.md](s3.md) 6: a raw SSE-C PUT without any checksum gets the default `CRC64NVME` (`FULL_OBJECT`), returned by HEAD with `x-amz-checksum-mode: ENABLED`, GET and `GetObjectAttributes` `Checksum` (LS test_s3.snapshot.json:L17518, L17534, L17551; request at LS test_s3.py:L12643-L12659). A client CRC32 is stored and returned (L12790, L13204). Parts carry their own checksum (L13012).

### 3.9 HTTPS

"You must use HTTPS when specifying SSE-C headers on your requests. Amazon S3 rejects any requests made over HTTP when using SSE-C." ([ssec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/ServerSideEncryptionCustomerKeys.html), "Considerations before using SSE-C").

- UNVERIFIED (S3SOL-02): status, code and text, whether reads with SSE-C headers are refused the same way, and the order against the bucket block. Every S3-compatible server checked uses 400 `InvalidRequest`, "Requests specifying Server Side Encryption with Customer provided keys must be made over a secure connection." (emu: `minio/minio cmd/api-errors.go` `ErrInsecureSSECustomerRequest`, `treeverse/lakeFS pkg/gateway/errors/errors.go`), and a client that parses real AWS errors matches the SSE-KMS sibling "Requests specifying Server Side Encryption with AWS KMS managed keys must be made over a secure connection." (`dremio/dremio-oss sabot/kernel/src/main/java/com/dremio/exec/store/AbstractRecordReader.java`, handling `AmazonS3Exception`).
- Whether SDKs refuse SSE-C over HTTP client-side was not checked; botocore's `sse_md5` handler (3.12) has no such check.

### 3.10 Presigned URLs

"When creating a presigned URL, you must specify the algorithm by using the `x-amz-server-side-encryption-customer-algorithm` header in the signature calculation", and the client must send all three headers when it uses the URL ([ssec-spec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/specifying-s3-c-encryption.html), "Presigned URLs and SSE-C"). S3 refuses a presigned request carrying `x-amz-*` headers it did not sign with `403 AccessDenied` "There were headers present in the request which were not signed" and `HeadersNotSigned` ([s3.md](s3.md) 12; `internal/auth/verify.go` `checkAllAmzHeadersSigned`).

- UNVERIFIED (S3SOL-09): that a URL signing only `host` and used with the three SSE-C headers gets that `HeadersNotSigned` refusal (rather than SSE-C being exempt).

### 3.11 What S3 keeps

"Amazon S3 does not store the encryption key that you provide. Instead, it stores a randomly salted Hash-based Message Authentication Code (HMAC) value of the encryption key to validate future requests." ([ssec-spec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/specifying-s3-c-encryption.html), Note). Losing the key loses the object ([ssec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/ServerSideEncryptionCustomerKeys.html)).

### 3.12 Client behavior that shapes tests

- botocore registers `sse_md5` for HeadObject, GetObject, PutObject, CopyObject, CreateMultipartUpload, UploadPart, UploadPartCopy, CompleteMultipartUpload, SelectObjectContent, and `copy_source_sse_md5` for CopyObject and UploadPartCopy, but **not** for GetObjectAttributes (botocore handlers.py:L1612-L1622 @ bfb412f). When `KeyMD5` is absent the handler base64-encodes the given key and computes its MD5; when present it sends both as given (handlers.py:L349-L369). So boto3 and the AWS CLI take the raw 32-byte key (the CLI sent `MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=`, base64 of a 32-character key, in `testdata/parity/s3/R15/x-ssec-on-non-ssec-read.json`), and a GetObjectAttributes caller must pass base64 and MD5 itself.
- aws-sdk-go-v2 documents the three fields as plain strings with no encoding note (`service/s3/api_op_PutObject.go` @ ab2110b, `SSECustomerKey` field comment); whether it has encoding middleware was not checked.

## 4. Blocking SSE-C per bucket

### 4.1 The April 2026 default

- "In April 2026, Amazon S3 deployed an update so all new general purpose buckets have SSE-C encryption disabled for all new write requests. For existing buckets in AWS accounts with no SSE-C encrypted objects, Amazon S3 also disabled SSE-C for all new write requests." ([ssec-faq](https://docs.aws.amazon.com/AmazonS3/latest/userguide/default-s3-c-encryption-setting-faq.html), Important box; same text in [ssec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/ServerSideEncryptionCustomerKeys.html) and [ssec-block](https://docs.aws.amazon.com/AmazonS3/latest/userguide/blocking-unblocking-s3-c-encryption-gpb.html)).
- Regional exception: "All newly created buckets in all AWS Regions except Middle East (Bahrain) and Middle East (UAE) will have SSE-C disabled by default." ([ssec-faq](https://docs.aws.amazon.com/AmazonS3/latest/userguide/default-s3-c-encryption-setting-faq.html), question 1). Those are `me-south-1` and `me-central-1` (region names inferred from the display names).
- Wire form on a new bucket: section 2, row 1 (aws-recorded). A fresh bucket in `me-south-1`/`me-central-1` allows SSE-C (aws-doc); how its configuration reads back is not recorded and cannot be from a single-region free-plan account, so understudy uses S3SOL-10's unblocked read form there (inferred).

### 4.2 Blocking and unblocking

- `BlockedEncryptionTypes` is a member of `com.amazonaws.s3#ServerSideEncryptionRule`, a flattened list of `com.amazonaws.s3#EncryptionType` with values `NONE` and `SSE-C` (model; [API_BlockedEncryptionTypes](https://docs.aws.amazon.com/AmazonS3/latest/API/API_BlockedEncryptionTypes.html): "Valid Values: NONE | SSE-C"). "If your workload requires SSE-C, you must explicitly enable it by setting `BlockedEncryptionTypes` to `NONE`" ([ssec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/ServerSideEncryptionCustomerKeys.html)).
- Rule shapes AWS documents: with a default (`ApplyServerSideEncryptionByDefault` + `BlockedEncryptionTypes`, the boto3 and Java examples in [ssec-block](https://docs.aws.amazon.com/AmazonS3/latest/userguide/blocking-unblocking-s3-c-encryption-gpb.html)), and **without** a default: the CLI examples there and `API_PutBucketEncryption` Example 4 send a `Rule` holding only `BlockedEncryptionTypes` ([API_PutBucketEncryption](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketEncryption.html)). `ApplyServerSideEncryptionByDefault` is "Required: No" ([API_ServerSideEncryptionRule](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ServerSideEncryptionRule.html)). `internal/service/s3/subresources.go` `checkEncryption` (L546) refuses that second form with `MalformedXML`, contradicting the docs.
- UNVERIFIED (S3SOL-10): what `GetBucketEncryption` returns after an unblock, in either form (an `<EncryptionType>NONE</EncryptionType>` element, or no element, and whether the default-free form gains the `AES256` default), and whether an empty `Rule` is refused with `MalformedXML`. Terraform's second plan depends on the first (9.5).
- UNVERIFIED (S3SOL-11): a later `PutBucketEncryption` that omits `BlockedEncryptionTypes`, and `DeleteBucketEncryption`, on a bucket set to `NONE`: keep `NONE` or return to `SSE-C`. Recorded only from the default state (section 2).

### 4.3 What the block refuses

- "While SSE-C is blocked, any `PutObject`, `CopyObject`, `PostObject`, Multipart Upload, or replication request that specifies SSE-C encryption is rejected with an HTTP 403 `AccessDenied` error." ([ssec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/ServerSideEncryptionCustomerKeys.html); [API_UploadPart](https://docs.aws.amazon.com/AmazonS3/latest/API/API_UploadPart.html) and [API_UploadPartCopy](https://docs.aws.amazon.com/AmazonS3/latest/API/API_UploadPartCopy.html) say the same for parts.)
- Reads are not blocked: existing SSE-C objects can still be read "with `GetObject` or `HeadObject` by providing the required SSE-C headers" ([ssec-block](https://docs.aws.amazon.com/AmazonS3/latest/userguide/blocking-unblocking-s3-c-encryption-gpb.html)); SSE-C headers on a read of a plain object in a blocked bucket get the 400 "not applicable" answer, not the 403 (`testdata/parity/s3/R15/x-ssec-on-non-ssec-read.json`). The bucket was created on 2026-10-14, so it is blocked; that is inferred, because R15 did not record its encryption configuration (section 2). Copy-source SSE-C headers are a read of the source, so a blocked target bucket refuses only target SSE-C headers (inferred).
- UNVERIFIED (S3SOL-01): the 403 message, whether the block is checked before or after the 3.2 header validation, and that copy-source-only SSE-C headers pass a blocked target bucket. `internal/service/s3/sse.go` `errSSECBlocked` (L260) uses an invented sentence today. Plausible: S3's generic "Access Denied" ([API_Error](https://docs.aws.amazon.com/AmazonS3/latest/API/API_Error.html) description for `AccessDenied`) or a specific sentence.

## 5. Object Lock bucket configuration

### 5.1 Enabling

- At creation: `x-amz-bucket-object-lock-enabled: true` (`com.amazonaws.s3#CreateBucketRequest$ObjectLockEnabledForBucket`; CLI `--object-lock-enabled-for-bucket`, [lock-config](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-configure.html)). Such a bucket is versioned at once: its first PUT returns a `VersionId` (LS test_s3.snapshot.json:L9596) and `GetObjectLockConfiguration` returns `ObjectLockEnabled=Enabled` with no rule (LS test_s3_api.snapshot.json:L2471).
- Later: `PutObjectLockConfiguration` on a bucket whose versioning is `Enabled`; unversioned or suspended → `409 InvalidBucketState`, "Versioning must be 'Enabled' on the bucket to apply a Object Lock configuration" (LS test_s3_api.snapshot.json:L2419, L2435; success L2451, L2457).
- Permanent: "After you enable Object Lock on a bucket, you can't disable Object Lock or suspend versioning for that bucket." ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html)). Suspending → `409 InvalidBucketState`, "An Object Lock configuration is present on this bucket, so the versioning state cannot be changed."; re-putting `Enabled` → 200 (LS test_s3_api.snapshot.json:L2623, L2633).
- Unset: `404 ObjectLockConfigurationNotFoundError`, "Object Lock configuration does not exist for this bucket", `BucketName` (aws-recorded `testdata/parity/s3/R06/01-get-get-object-lock-configuration.json`; LS test_s3_api.snapshot.json:L2408, L2596).

### 5.2 PutObjectLockConfiguration and GetObjectLockConfiguration

- `PUT /?object-lock`, body root `ObjectLockConfiguration` with `ObjectLockEnabled` (only value `Enabled`, `com.amazonaws.s3#ObjectLockEnabled`) and optional `Rule/DefaultRetention{Mode, Days | Years, DefaultEventHold{Days | Years}}`; headers `x-amz-bucket-object-lock-token` ("A token to allow Object Lock to be enabled for an existing bucket"), `Content-MD5`, `x-amz-sdk-checksum-algorithm`, `x-amz-expected-bucket-owner`, `x-amz-request-payer`; response 200 empty ([API_PutObjectLockConfiguration](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObjectLockConfiguration.html); aws-recorded `testdata/parity/s3/R12/11-put-default-retention.json`).
- `MalformedXML` (standard text) for: no `ObjectLockEnabled`, an empty configuration, an empty `Rule`, an empty `DefaultRetention`, `Mode` without a period, a bad `Mode`, `Days` and `Years` together (LS test_s3_api.snapshot.json:L2521-L2581). "The `DefaultRetention` settings require both a mode and a period" and "You cannot specify `Days` and `Years` at the same time" (`com.amazonaws.s3#DefaultRetention` documentation).
- `Get` echoes the stored rule (`Days=1`, `Mode=GOVERNANCE`, LS test_s3_api.snapshot.json:L2486); a later Put of `ObjectLockEnabled` alone removes the default (L2501, L2507; also the documented removal command in [lock-config](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-configure.html)).
- The model has no `@range` on `com.amazonaws.s3#Days`, `#Years` or `#EventHoldDuration` members. "The maximum retention period is 100 years." ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html), retention limits). UNVERIFIED (S3SOL-13, with 5.3): the refusal for `Days` 0 or above 36,500, and `Years` 0 or above 100.

### 5.3 Default retention dating

- "Amazon S3 calculates a Retain Until Date for the object version by adding the specified duration to the object version's creation timestamp"; explicit per-object settings override the default ([lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html), "Retention periods"). Recorded: a PUT into a `GOVERNANCE`/`Days=1` bucket reports `ObjectLockMode=GOVERNANCE` and a retain-until date within 2 minutes of `LastModified` + 1 day, which the test asserts as "precise" (LS test_s3.snapshot.json:L9711; assertion LS test_s3.py:L9945-L9953).
- The PUT response does not echo the lock state (L9700, L9656): `com.amazonaws.s3#PutObjectOutput` has no lock members.
- UNVERIFIED (S3SOL-13): how `Years` is converted. Plausible: calendar years, or 365-day years. The only AWS statement is about event-hold durations checked against a policy condition key: "When you set a duration in years, Amazon S3 converts it to days for evaluation against the `s3:object-lock-event-hold-duration-days` condition key. Amazon S3 counts 1 year as 365 days and doesn't count leap years" ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html), "Bucket policy doesn't match a duration that is set in years").

## 6. Per-object lock state

### 6.1 Request headers

`x-amz-object-lock-mode` (`GOVERNANCE | COMPLIANCE`), `x-amz-object-lock-retain-until-date` ("Must be formatted as a timestamp parameter"), `x-amz-object-lock-legal-hold` (`ON | OFF`), and the variable-retention trio `x-amz-object-lock-event-hold` (`ON | OFF`), `x-amz-object-lock-event-hold-duration-days`, `-years` ([API_PutObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObject.html)). The same six members exist on `com.amazonaws.s3#PutObjectRequest`, `#CopyObjectRequest` and `#CreateMultipartUploadRequest` and nowhere else on the write path. `ObjectLockRetainUntilDate` has `@timestampFormat("date-time")`, so the header is ISO 8601, not an HTTP date (model).

### 6.2 Header validation (third-party-recorded, all 400 `InvalidArgument`)

| Request | Message | Elements | Source |
|---|---|---|---|
| mode without date | x-amz-object-lock-retain-until-date and x-amz-object-lock-mode must both be supplied | `ArgumentName=x-amz-object-lock-retain-until-date` | LS test_s3.snapshot.json:L9754, L17916 (the latter with a bad mode: pairing is checked first) |
| date without mode | same | `ArgumentName=x-amz-object-lock-mode` | L17927 |
| bad mode with a date | Unknown wormMode directive. | `ArgumentName=x-amz-object-lock-mode`, `ArgumentValue=BAD-VALUE` | L17938 (PutObject), L17950 (CreateMultipartUpload) |

- UNVERIFIED (S3SOL-17): lock headers on a bucket without Object Lock. Plausible: `400 InvalidRequest` "Bucket is missing Object Lock Configuration", the recorded text of the retention and legal-hold operations (6.4).
- UNVERIFIED (S3SOL-18): a past date, an unparsable date, a date more than 100 years out, a bad legal-hold value on PutObject, and the summer-time rendering of the 6.4 past-date `ArgumentValue`. Plausible: the 6.4 past-date text with `ArgumentName=x-amz-object-lock-retain-until-date`.

### 6.3 The Content-MD5 or checksum requirement

- "The `Content-MD5` or `x-amz-sdk-checksum-algorithm` header is required for any request to upload an object with a retention period configured using Object Lock." ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html), "Uploading objects to an Object Lock enabled bucket"; `com.amazonaws.s3#PutObjectRequest$ContentMD5` documentation). `x-amz-sdk-checksum-algorithm` must come with an `x-amz-checksum-*` or `x-amz-trailer` header or the request fails 400 ([API_PutObjectRetention](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObjectRetention.html), header description).
- Recorded (aws-recorded `testdata/parity/s3/R12/11-put-without-checksum.json`): a PutObject with `x-amz-content-sha256: UNSIGNED-PAYLOAD`, no lock headers, into a bucket whose default retention is `GOVERNANCE`/1 day → `400 InvalidRequest`, "Content-MD5 OR x-amz-checksum- HTTP header is required for Put Object requests with Object Lock parameters".
- Current SDKs and the CLI send a checksum on every PutObject by default ([s3.md](s3.md) 1 item 5), and Terraform forces CRC32 when any lock argument is set (9.3), so the refusal hits raw clients and older SDKs.
- UNVERIFIED (S3SOL-14): whether a legal hold alone triggers it (the message says "Object Lock parameters", the docs say "retention period"), and whether a signed `x-amz-content-sha256` payload hash satisfies it (UploadPart says SigV4's payload hash replaces `Content-MD5` there, [API_UploadPart](https://docs.aws.amazon.com/AmazonS3/latest/API/API_UploadPart.html)).
- UNVERIFIED (S3SOL-15): multipart uploads under retention: whether `UploadPart` without a checksum is refused, and whether lock headers on `CreateMultipartUpload` land on the completed object (the model carries them only on Create).
- UNVERIFIED (S3SOL-16): `CopyObject` into a bucket with default retention: whether the new version gets the bucket default (the docs say "every object version placed in the bucket") and whether the copy needs a checksum (it has no body).

### 6.4 PutObjectRetention and GetObjectRetention

- `PUT /{Key+}?retention&versionId=`, body root `Retention` (`com.amazonaws.s3#PutObjectRetentionRequest$Retention` has `@xmlName("Retention")`) with `Mode`, `RetainUntilDate`, `EventHold`, `EventHoldDuration{Days,Years}`; headers `x-amz-bypass-governance-retention`, `Content-MD5`, `x-amz-sdk-checksum-algorithm`, `x-amz-expected-bucket-owner`, `x-amz-request-payer`; response 200 empty. `GET` returns `Retention` with the same members ([API_PutObjectRetention](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObjectRetention.html), [API_GetObjectRetention](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectRetention.html)). The operation carries `aws.protocols#httpChecksum{requestChecksumRequired: true}` (model), so SDKs always send a checksum.
- Errors (third-party-recorded unless noted):

| Situation | Status, code | Message | Source |
|---|---|---|---|
| missing bucket | 404 `NoSuchBucket` + `BucketName` | The specified bucket does not exist | LS test_s3.snapshot.json:L9436 |
| missing key | 404 `NoSuchKey` + `Key` | The specified key does not exist. | L9447 |
| bucket without Object Lock (Put and Get) | 400 `InvalidRequest` | Bucket is missing Object Lock Configuration | L9510, L9520 |
| Get on a version that never had retention | 404 `NoSuchObjectLockConfiguration` | The specified object does not have a ObjectLock configuration | L9458 |
| `Mode` without `RetainUntilDate`; bad `Mode` | 400 `MalformedXML` | The XML you provided was not well-formed or did not validate against our published schema | L9468, L9500 |
| `RetainUntilDate` in the past | 400 `InvalidArgument`, `ArgumentName=RetainUntilDate`, `ArgumentValue=Tue Dec 31 16:00:00 PST 2019` for a request of 2020-01-01T00:00:00Z | The retain until date must be in the future! | L9488 |
| shortening GOVERNANCE without bypass; any change that shortens COMPLIANCE or turns it into GOVERNANCE | 403 `AccessDenied` | Access Denied because object protected by object lock. | L9478, L9667, L9893, L17884, L17894 |
| a delete-marker version | 405 `MethodNotAllowed`, `Method=PUT` or `GET`, `ResourceType=DeleteMarker` | The specified method is not allowed against this resource. | L9805, L9817 |

- The past-date `ArgumentValue` is a Java `Date.toString()` in US Pacific time (the UTC instant 2020-01-01T00:00Z rendered as 2019-12-31 16:00 PST); the PDT form in summer is part of S3SOL-18.
- Allowed: setting retention on an unprotected version (L9607); extending GOVERNANCE without bypass (L9871; "Any user with permissions to place an object retention period can extend a retention period", [lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html)); an empty `<Retention/>` with `x-amz-bypass-governance-retention: true` removes GOVERNANCE retention (L9677); after removal a new short retention can be set without bypass (L9683). Bypass also allows "shortening the retention period, or removing the Object Lock retention period by placing a new `PutObjectRetention` request with empty parameters" ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html), "Bypassing governance mode").
- COMPLIANCE: "its retention mode can't be changed, and its retention period can't be shortened" ([lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html)); recorded at L17884, L17894.
- UNVERIFIED (S3SOL-19): GOVERNANCE to COMPLIANCE (a stricter mode, same or later date) without bypass. Plausible: refused with the 403 above ("users can't overwrite or delete an object version or alter its lock settings unless they have special permissions", [lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html)), or allowed as an extension.
- UNVERIFIED (S3SOL-20): `RetainUntilDate` without `Mode`. Plausible: `MalformedXML`, mirroring the recorded `Mode`-without-date case.
- UNVERIFIED (S3SOL-21): `GetObjectRetention` after the retention was removed (404 `NoSuchObjectLockConfiguration`, or 200 with an empty `Retention`), and whether HEAD then omits the retention headers.

### 6.5 PutObjectLegalHold and GetObjectLegalHold

- `PUT /{Key+}?legal-hold&versionId=`, body `LegalHold{Status}`; `GET` returns `LegalHold{Status}` ([API_PutObjectLegalHold](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObjectLegalHold.html), [API_GetObjectLegalHold](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectLegalHold.html)); `requestChecksumRequired: true` (model). No bypass header: "Legal holds can be freely placed and removed by any user who has the `s3:PutObjectLegalHold` permission" ([lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html)).
- Recorded: never set → 404 `NoSuchObjectLockConfiguration` (LS test_s3.snapshot.json:L9919); `ON` → GET `Status=ON` (L9950); missing bucket / key / bucket without Object Lock as in 6.4 (L9970, L9981, L9992, L10012); a request without a body on a bucket **without** Object Lock → `MalformedXML` (L10002), so the body is validated before the bucket's lock state.
- UNVERIFIED (S3SOL-21): `GetObjectLegalHold` after `OFF`. Plausible: 200 `Status=OFF`, since HEAD then reports `OFF` (6.7).
- UNVERIFIED (S3SOL-22): which error wins for a missing key on a bucket without Object Lock, and the legal-hold answer for a delete-marker version (plausible: the 405 of 6.4).
- Legal holds are independent of retention: an expired retention does not lift a hold, and lifting a hold does not end a retention ([lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html), "Legal holds"). Bypass does not lift a hold ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html): "If an object version has a legal hold enabled, the legal hold remains and prevents requests to overwrite or delete the object version").

### 6.6 GOVERNANCE, COMPLIANCE and bypass

- COMPLIANCE: no user, root included, can overwrite or delete the version before the date ([lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html)); recorded: a versioned delete with bypass is still 403 (LS test_s3.snapshot.json:L17874).
- GOVERNANCE: bypass needs the `s3:BypassGovernanceRetention` permission "and must explicitly include `x-amz-bypass-governance-retention:true` as a request header" (same page). `com.amazonaws.s3#BypassGovernanceRetention` is a boolean on `DeleteObjectRequest`, `DeleteObjectsRequest` and `PutObjectRetentionRequest` only.
- New versions of a locked key are always allowed: "Retention periods and legal holds don't prevent new versions of the object from being created, or delete markers to be added" ([lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html)); recorded second PUT on a held key (LS test_s3.snapshot.json:L10111).

### 6.7 Response headers on GET and HEAD

`x-amz-object-lock-mode`, `x-amz-object-lock-retain-until-date`, `x-amz-object-lock-legal-hold`, `x-amz-object-lock-event-hold`, `-event-hold-duration-days|years` ([API_HeadObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadObject.html), [API_GetObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObject.html); members on `com.amazonaws.s3#GetObjectOutput` and `#HeadObjectOutput`).

- The legal-hold header "is not returned if the specified version of this object has never had a legal hold applied" ([API_HeadObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadObject.html)). Recorded: `ON` (LS test_s3.snapshot.json:L9935, L10247), and `OFF` after removal (L10149).
- Mode and date appear whenever retention is set, from headers, PutObjectRetention or the bucket default (L9623, L9711, L9738).
- Event-hold duration headers are "Only returned when the event hold is enabled" (same pages).
- UNVERIFIED (S3SOL-12): the exact form of `x-amz-object-lock-retain-until-date` and of `GetObjectRetention`'s `RetainUntilDate` (`2030-01-01T00:00:00.000Z` or without fraction), and whether sub-second precision of a requested date is kept. Snapshots replace the value with `datetime`.

### 6.8 CopyObject does not carry lock state

A copy of a version under GOVERNANCE retention, or with a legal hold, has neither (LS test_s3.snapshot.json:L9562-L9577, L10202-L10217). Lock state on the target comes only from the copy's own headers or the target bucket's default (S3SOL-16).

### 6.9 Variable retention (event holds), new since the LocalStack recordings

- Doc-only. An object stays protected while its event hold is `ON`; releasing the hold fixes the retain-until date at release time plus the duration; a `RetainUntilDate` given alongside is a minimum; while the hold is on, reads return "current time plus the duration"; decreasing the duration never moves the date earlier ([lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html), "Retain-until-date behavior with variable retention"). Durations are 1 to 36,500 days or 1 to 100 years ([lock-config](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-configure.html)). A bucket default can carry `DefaultEventHold` ([API_PutObjectLockConfiguration](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObjectLockConfiguration.html)).
- Refusals are documented as "a `400 Bad Request` response" without codes: no mode; missing or duplicate duration; a duration with `OFF`; `OFF` with no date; a duration out of range ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html), "Request that sets an event hold is rejected").
- UNVERIFIED (S3SOL-24): the codes and texts of those refusals. Not needed while understudy refuses event holds (11.1).

## 7. Deletes under Object Lock

### 7.1 DeleteObject

- Without `versionId` (simple delete) on a locked version: a new delete marker, whatever the lock ([lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html), "How deletes work"). The status is `204`, with `DeleteMarker=true` (recorded at LS test_s3.snapshot.json:L9797 and L17866; the model binds DeleteObject to 204, section 8). The doc page says "`200 OK`" here (section 10). "Delete markers are not WORM-protected" ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html)).
- With `versionId` on a protected version: `403 AccessDenied`, "Access Denied because object protected by object lock." (L9639 GOVERNANCE, L17856 COMPLIANCE, L10044 legal hold). GOVERNANCE with `x-amz-bypass-governance-retention: true` → 204 (L9649); COMPLIANCE with bypass → 403 (L17874).
- After the retain-until date passes, a versioned delete without bypass succeeds (L17904; LS test_s3.py:L9871-L9883 deletes after sleeping past a 5-second retention, validated). The comparison runs on S3's clock; the boundary (equal instant) is part of S3SOL-12.

### 7.2 DeleteObjects

A locked version in the batch is reported per key, HTTP 200: `<Error><Key>..</Key><VersionId>..</VersionId><Code>AccessDenied</Code><Message>Access Denied because object protected by object lock.</Message></Error>` (LS test_s3.snapshot.json:L10054). Terraform depends on the per-key `AccessDenied` code (9.6).

### 7.3 The bypass header on a bucket without Object Lock

`DeleteObject` with `x-amz-bypass-governance-retention: true` **or `false`**, and `DeleteObjects` with `true`, on an unversioned bucket without Object Lock → `400 InvalidArgument`, "x-amz-bypass-governance-retention is only applicable to Object Lock enabled buckets.", `ArgumentName=x-amz-bypass-governance-retention` (LS test_s3_api.snapshot.json:L3535, L3546, L3557). The header's presence, not its value, triggers it.

- UNVERIFIED (S3SOL-23): the same on a versioned bucket without Object Lock, with a `versionId`, and what `false` means on a bucket with Object Lock. Plausible: the same 400, and no bypass. Terraform's `aws_s3_object` sends the header with `true` on destroy with `force_destroy = true` (9.6).

### 7.4 Lifecycle, replication, notifications

- "A locked version of an object cannot be deleted by a S3 Lifecycle expiration policy"; lifecycle still places delete markers ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html), "Managing S3 Lifecycle policies with Object Lock"). understudy stores lifecycle rules without running them (`docs/coverage.md`), so this binds only when expiration is implemented.
- Replication of lock state: out of scope (replication is not implemented).
- `s3:ObjectRetention:Put` fires on explicit `PutObjectRetention` calls ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html), "Configuring events and notifications"; [notif-types](https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-how-to-event-types-and-destinations.html)), with an `objectRetentionEventData{mode, retainUntilDate, eventHold?, eventHoldDuration?}` block ([notif-shape](https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-content-structure.html)). The value is in the model's `com.amazonaws.s3#Event` enum. The same page now says all events use a unified `eventVersion` (2.6 in its example; "Starting with version 2.4, Amazon S3 uses a single unified version for all event types"), while `internal/service/s3/notify/record.go` emits 2.1 and 2.3; that disagreement belongs to the notifications research, flagged here only.

## 8. Smithy model facts

- Service `com.amazonaws.s3#AmazonS3`: protocol `aws.protocols#restXml`, `smithy.api#xmlNamespace` `http://s3.amazonaws.com/doc/2006-03-01/`, `aws.auth#sigv4` name `s3`. No `smithy.api#endpoint` host prefix on any operation here; addressing is the endpoint rule set ([wire-rest.md](wire-rest.md)).
- HTTP bindings: `PutObjectRetention PUT /{Bucket}/{Key+}?retention` 200; `GetObjectRetention GET` same path; `PutObjectLegalHold PUT /{Bucket}/{Key+}?legal-hold` 200; `GetObjectLegalHold GET` same; `PutObjectLockConfiguration PUT /{Bucket}?object-lock` 200; `GetObjectLockConfiguration GET` same; `DeleteObject DELETE /{Bucket}/{Key+}?x-id=DeleteObject` 204; `DeleteObjects POST /{Bucket}?delete` 200.
- `aws.protocols#httpChecksum{requestAlgorithmMember: ChecksumAlgorithm, requestChecksumRequired: true}` on PutObjectRetention, PutObjectLegalHold, PutObjectLockConfiguration, DeleteObjects; `PutObject` has `requestAlgorithmMember` only.
- UNVERIFIED: what S3 answers when one of those four operations arrives with neither `Content-MD5` nor an `x-amz-checksum-*` header, or with a wrong one. SDKs always send one (6.4), so only raw clients meet the answer. understudy enforces `requestChecksumRequired` on no operation (`internal/codegen/optraits.go` keeps only the algorithm member), so it accepts such a request. The gap is not specific to Object Lock: `PutBucketTagging` and `DeleteObjects` share it. No item in section 12 records it yet.
- Modeled errors: none on the four lock operations or DeleteObject(s). `PutObject` lists `EncryptionTypeMismatch` (400; its documentation is about directory-bucket sessions), `InvalidRequest` (400), `InvalidWriteOffset`, `TooManyParts`. `com.amazonaws.s3#AccessDenied` is 403. Every lock and SSE-C error code in this note (`InvalidArgument`, `InvalidEncryptionAlgorithmError`, `NoSuchObjectLockConfiguration`, `MalformedXML`, `MethodNotAllowed`, `InvalidBucketState`) is unmodeled and must be hand-coded, as in [s3.md](s3.md).
- Shapes: `ObjectLockRetention{Mode, RetainUntilDate, EventHold, EventHoldDuration}`, `DefaultRetention{Mode, Days, Years, DefaultEventHold}`, `EventHoldDuration{Days, Years}`, `ObjectLockLegalHold{Status}`, `ObjectLockConfiguration{ObjectLockEnabled, Rule}`; enums `ObjectLockRetentionMode` / `ObjectLockMode` (`GOVERNANCE`, `COMPLIANCE`), `ObjectLockLegalHoldStatus` and `ObjectLockEventHold` (`ON`, `OFF`), `ObjectLockEnabled` (`Enabled`), `EncryptionType` (`NONE`, `SSE-C`). `com.amazonaws.s3#Date` and `#ObjectLockRetainUntilDate` are `date-time` timestamps. No constraint traits on any of them.
- `UpdateObjectEncryption` (`PUT /{Bucket}/{Key+}?encryption`) is new and changes SSE-S3/SSE-KMS objects in place; unbound in understudy and outside this item.

## 9. Terraform v6.68.0

### 9.1 `aws_s3_bucket`

- `object_lock_enabled` is Optional+Computed (TF bucket.go:L423-L428); `true` sends `ObjectLockEnabledForBucket` on CreateBucket (L797-L804); turning it from true to false forces replacement (L91-L94); turning it on later calls `PutObjectLockConfiguration{ObjectLockEnabled: Enabled}` retried on `NoSuchBucket` (L1640-L1653).
- Read: `GetObjectLockConfiguration`; success sets `object_lock_enabled = (ObjectLockEnabled == Enabled)` and the deprecated `object_lock_configuration`; not-found, `MethodNotAllowed`, `NotImplemented`, `XNotImplemented` clear both (L1163-L1190).
- Destroy with `force_destroy` empties the bucket with bypass only when the state's deprecated `object_lock_configuration` says Enabled (L1726-L1730), so a non-lock bucket never gets the bypass header from this path.

### 9.2 `aws_s3_bucket_object_lock_configuration`

- Schema: `object_lock_enabled` (default `Enabled`, ForceNew), `rule` (MaxItems 1) with `default_retention` (Required, MaxItems 1) of `days` / `years` (ConflictsWith each other) and `mode`; `token` Sensitive; no `DefaultEventHold` (TF bucket_object_lock_configuration.go:L56-L100).
- Create: `PutObjectLockConfiguration{ObjectLockEnabled, Rule}` with optional `Token` and `RequestPayer`, retried on `NoSuchBucket`; then waits until `GetObjectLockConfiguration` finds it (L115-L152). A 501 is reported as "directory bucket" (L140).
- Read: Get, then `object_lock_enabled` and `rule` flattened from `Days`, `Mode`, `Years` (L174-L190, L356-L380). Expand sends `days`/`years` only when > 0 (L341-L351).
- Delete: `PutObjectLockConfiguration{ObjectLockEnabled}` with no rule, tolerating `NoSuchBucket` and `ObjectLockConfigurationNotFoundError` (L252-L270).

### 9.3 `aws_s3_object`

- `object_lock_legal_hold_status`, `object_lock_mode`, `object_lock_retain_until_date` (RFC 3339) (TF object.go:L184-L197), sent on upload (L545-L555). When any is set and no `checksum_algorithm` is, the provider sets CRC32, quoting the R12 error text in its comment (L582-L586).
- Read: `HeadObject` without SSE-C headers (L648-L661), then the three attributes from `ObjectLockLegalHoldStatus`, `ObjectLockMode` and `ObjectLockRetainUntilDate` formatted with `time.RFC3339` (L321-L323, L692-L698), so second precision is what Terraform compares.
- Update: `PutObjectLegalHold` when the status changes (L372-L386); `PutObjectRetention` when mode or date changes, with `BypassGovernanceRetention=true` when the new date is earlier or empty (L388-L413).
- Delete: on a versioned bucket (`version_id` set) every version is deleted with `force_destroy` as the bypass flag (L436-L440; TF delete.go:L473-L483 sets the header whenever force is true). `aws_s3_object` has no SSE-C arguments.

### 9.4 `aws_s3_object_copy`

- SSE-C: `customer_algorithm`, `customer_key` (Sensitive), `customer_key_md5`, and `source_customer_algorithm`, `source_customer_key` (Sensitive), `source_customer_key_md5` (TF object_copy.go:L144-L158, L325-L337), passed to CopyObject as given (L599-L608, L669-L678), so the user supplies base64 and MD5; changing any re-copies (L455-L490).
- Read is `findObjectByBucketAndKey` with no SSE-C headers (L390), so against AWS the refresh of an SSE-C target hits the 3.3 refusal (inferred; S3SOL-05 settles the HEAD form). understudy must refuse the same way rather than make this resource work.

### 9.5 `aws_s3_bucket_server_side_encryption_configuration`

`rule.blocked_encryption_types` is Optional+Computed, a list of `EncryptionType` (TF bucket_server_side_encryption_configuration.go:L84-L92), sent only when non-empty (L332-L334) and set from the GET only when the element has values (L356-L360, L398-L404). Omitted in config: the computed value from the GET (`["SSE-C"]` on AWS, section 2) is accepted. Set to `["NONE"]`: an empty second plan needs the GET to return `NONE` (S3SOL-10).

### 9.6 Destroy paths that touch Object Lock

- `emptyBucket(force)` deletes in pages with `DeleteObjects{Quiet: true}` plus bypass when forced (TF delete.go:L226-L235). For each per-key `AccessDenied` it puts the legal hold `OFF` and retries `DeleteObject` **without** bypass (L259-L289). A failed retry is collected (L288) and `deletePage` returns it (L298-L299), `emptyBucket` returns that error (L24-L31), and the bucket delete fails with "emptying S3 Bucket" (TF bucket.go:L1730-L1731). The bucket delete recurses (L1737) only after `emptyBucket` succeeds. So a version that has both a legal hold and GOVERNANCE retention fails the first `terraform destroy`, because the bypass-free retry gets the lock 403. The hold is `OFF` by then, so a second destroy deletes the version with DeleteObjects and bypass.
- `deleteAllObjectVersions` (single-object path) on `AccessDenied` with force: `HeadObject` the version, put the hold `OFF` if `ON`, delete again with bypass (L366-L401).
- `errCodeAccessDenied = "AccessDenied"` (TF errors.go:L14): any other code for a locked delete breaks both paths.

## 10. Sources that disagree

- `InvalidEncryptionAlgorithmError` text: [API_Error](https://docs.aws.amazon.com/AmazonS3/latest/API/API_Error.html) says "The encryption request you specified is not valid. The valid value is AES256."; AWS answered "The Encryption request you specified is not valid. Supported value: AES256." (3.2). The recording wins.
- Locked-delete text: the AWS SDK examples' Object Lock scenario shows `Access Denied` for a locked delete in its sample output (`awsdocs/aws-doc-sdk-examples scenarios/features/s3_object_lock/SPECIFICATION.md:L259` @ a352b04, date unknown; a .NET exception message), while the 2026-02-21 recordings say "Access Denied because object protected by object lock." (7.1). The recording wins; S3SOL-19 captures a fresh one as a side step.
- Unblocking rule shape: the API reference and CLI examples send a rule without `ApplyServerSideEncryptionByDefault` (4.2); understudy refuses it. The docs win.
- Status of a simple delete under lock: [lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html), "How deletes work", says S3 "returns a `200 OK` response and inserts a delete marker". The 2026-02-21 recordings got 204 (LS test_s3.snapshot.json:L9797, L17866), and the model binds `DeleteObject` to 204 (7.1). The recordings win.
- Checksum requirement: the docs name `Content-MD5` or `x-amz-sdk-checksum-algorithm`; the recorded message names `Content-MD5` or an `x-amz-checksum-` header (6.3). Both are satisfied by a real `x-amz-checksum-*` header, which is what clients send.
- `docs/coverage.md` marks Put/Get/DeleteBucketEncryption `stored-only ... not applied`, but `internal/service/s3/sse.go` `bucketDefault` applies the default algorithm and KMS key to writes. The coverage note is stale. Its source is the `stored("the default encryption rule is")` registration in `internal/service/s3/subresources.go` `bindEncryption` (L523-L543), which generates those rows, so the fix belongs in that registration, followed by `just coverage-check`.

## 11. Implications for understudy

### 11.1 Operation coverage

Note texts are the exact `registry.Partial{Note: ...}` strings proposed; parts of today's notes that stay true are kept verbatim.

| Operation | Today | Target | Note |
|---|---|---|---|
| PutObject | partial | partial | an object ACL a bucket accepts is not stored; user metadata is stored as sent, without RFC 2047 decoding; chunk signatures are verified under sigv4 verify; Object Lock event-hold headers are refused with NotImplemented |
| GetObject | partial | partial | presigned URLs are verified and expire on the emulator clock under sigv4 verify; x-amz-checksum-mode is accepted in any case, and a value other than ENABLED is refused with InvalidArgument, where AWS's answer is UNVERIFIED (s3.md 5.8, recording S3-01) |
| HeadObject | partial | partial | presigned URLs are verified and expire on the emulator clock under sigv4 verify; x-amz-checksum-mode is accepted in any case, and a value other than ENABLED is refused with InvalidArgument, where AWS's answer is UNVERIFIED (s3.md 5.8, recording S3-01) |
| GetObjectAttributes | partial | full | |
| CopyObject | partial | partial | an object ACL a bucket accepts is not stored; errors after the copy starts are not sent as 200-with-error; Object Lock event-hold headers are refused with NotImplemented |
| CreateMultipartUpload | partial | partial | an object ACL a bucket accepts is not stored; no lifecycle abort date; Object Lock event-hold headers are refused with NotImplemented |
| UploadPart | partial | partial | chunk signatures are verified under sigv4 verify |
| UploadPartCopy | partial | partial | errors are answered before the response starts, so only a StatusOKError fault rule sends 200-with-error |
| CompleteMultipartUpload | partial | partial | errors are answered before the response starts, so only a StatusOKError fault rule sends 200-with-error |
| DeleteObject | partial | partial | no MFA delete; s3:BypassGovernanceRetention is not evaluated, so x-amz-bypass-governance-retention alone bypasses GOVERNANCE retention |
| DeleteObjects | partial | partial | no MFA delete; s3:BypassGovernanceRetention is not evaluated, so x-amz-bypass-governance-retention alone bypasses GOVERNANCE retention |
| PutObjectRetention | unbound | partial | event holds (EventHold, EventHoldDuration) are refused with NotImplemented; s3:BypassGovernanceRetention is not evaluated; no s3:ObjectRetention:Put notification is sent |
| GetObjectRetention | unbound | full | |
| PutObjectLegalHold | unbound | full | |
| GetObjectLegalHold | unbound | full | |
| PutObjectLockConfiguration | stored-only | partial | DefaultEventHold is refused with NotImplemented; x-amz-bucket-object-lock-token is accepted and ignored |
| GetObjectLockConfiguration | stored-only | full | |
| PutBucketEncryption | stored-only | full | (the default rule is already applied, section 10; `BlockedEncryptionTypes` becomes enforced) |
| GetBucketEncryption, DeleteBucketEncryption | stored-only | full | |
| PutBucketNotificationConfiguration | partial | partial | append to today's note: "; s3:ObjectRetention:Put is stored but sends nothing" (until 7.4 is implemented) |
| UpdateObjectEncryption | unbound | unbound (501) | |

Run `just coverage-check` after re-marking and commit the regenerated `docs/coverage.md`.

### 11.2 Data model

- Object encryption becomes a closed sum of SSE-S3, SSE-KMS and SSE-C (today `Encryption{KMS *KMSEncryption}` with nil for SSE-S3, `internal/service/s3/sse.go`). The SSE-C variant holds a per-version random salt and HMAC-SHA256(salt, key), never the key (3.11). Bytes may be stored as sent, as SSE-KMS already does (`internal/service/s3/doc.go`); the observable contract is the key check, not ciphertext at rest.
- Responses echo the request's key MD5; nothing needs to store it.
- A multipart upload records its SSE choice, including the SSE-C fingerprint, so UploadPart can compare (3.6); ListParts needs none (recorded without a checksum algorithm; with one it follows `s3.sse-c.complete-headers`, S3SOL-08).
- Per version lock state: `Retention` = none, or `{Mode, RetainUntil}` with `RetainUntil` an emulator-clock instant; `LegalHold` = never, on, or off (three states, since HEAD omits the header only for never, 6.7). Delete markers carry no lock state.
- Bucket: `ObjectLock` config (today stored, `internal/service/s3/state.go`) becomes applied at write time (5.3). Encryption config keeps `BlockedEncryptionTypes` as a separate field so that a Put omitting it follows S3SOL-11's answer instead of erasing it (today `bindEncryption` stores the input verbatim, `internal/service/s3/subresources.go:L523`).
- New buckets get SSE-C blocked unless their region is `me-south-1` or `me-central-1` (4.1). The overlay default already carries the element (`codegen/overlays/s3.json`, `GetBucketEncryption` `notConfigured`).

### 11.3 Rules to implement

- `resolveSSE` (`internal/service/s3/sse.go:L77`): replace the unconditional `errSSECBlocked` with: 3.2 validation in the order conflict, algorithm, presence, key length, MD5 (only "conflict before algorithm" is recorded, 3.2; the rest of the order is inferred, because each recorded request had a single fault), then the bucket block (order against validation from S3SOL-01), then HTTPS (switch).
- Reads: GET, HEAD, GetObjectAttributes, copy source and UploadPartCopy source compare the request against the stored variant: SSE-C object without headers → 400 "stored using a form"; with headers on a non-SSE-C object → 400 "not applicable" (aws-recorded); wrong key → 403, including a wrong-length key with a matching MD5 (3.3; this is recorded for 44 bytes only, and a short key is S3SOL-04).
- ETags: random 32 hex for SSE-C single writes, SSE-C copy targets and SSE-C parts, and random 32 hex plus `-N` for an SSE-C multipart object, because AWS's value is not the MD5 of the part ETags (3.4). The random digits are inferred; no recording can pin a value AWS derives from state it never returns. Never derive an SSE-C ETag from content or from part ETags.
- Omit `x-amz-server-side-encryption` on every SSE-C response; echo the SSE-C pair on PutObject, CopyObject, CreateMultipartUpload, UploadPart, UploadPartCopy, GetObject, HeadObject; none on CompleteMultipartUpload and GetObjectAttributes (3.4).
- `checkEncryption` (`internal/service/s3/subresources.go:L546`): accept a rule whose only member is `BlockedEncryptionTypes` (4.2); keep `MalformedXML` for zero or two rules ([s3.md](s3.md) 4.2.2) and for a rule with neither member (inferred; S3SOL-10).
- PutObject, CopyObject, CreateMultipartUpload: 6.2 pairing check, then mode value, then (S3SOL-17) the bucket's lock state, then the 6.3 checksum rule for PutObject, then apply explicit settings or the bucket default with `RetainUntil = creation instant + Days` (Years per switch).
- Retention and legal hold operations: 6.4/6.5 tables in order: body (`MalformedXML`), bucket exists, bucket has Object Lock, key/version exists, delete marker (405), date in the future. Only "body before the bucket's lock state" is recorded (6.5). Body before bucket existence is inferred, and lock state before key is S3SOL-22 (`s3.object-lock.check-order`). Then come the change rules: extend always; shorten or remove GOVERNANCE only with bypass true; COMPLIANCE never shortened or changed to GOVERNANCE.
- Deletes: simple delete always adds a marker on a lock bucket; versioned delete of a version with a legal hold `ON`, or with `RetainUntil` after now (emulator clock) and COMPLIANCE, or GOVERNANCE without bypass true → 403 `AccessDenied` (7.1); DeleteObjects reports it per key with `VersionId` (7.2); the bypass header on any bucket without Object Lock, whatever its value → 400 (7.3; this is recorded on an unversioned bucket only, and a versioned bucket follows `s3.object-lock.bypass-without-lock`, S3SOL-23).
- `x-amz-bypass-governance-retention: false` on a lock bucket means no bypass (inferred from the boolean shape; S3SOL-23).
- Event-hold headers and elements (`x-amz-object-lock-event-hold*`, `EventHold`, `EventHoldDuration`, `DefaultEventHold`): 501 `NotImplemented` with a `Header` element, in the style of `internal/service/s3/conditional.go` `errConditionNotImplemented`, naming understudy as the source.

### 11.4 Fidelity switches

| Name | Default | Label | Grounds | Record |
|---|---|---|---|---|
| `s3.sse-c.plain-http` | `refused`: 400 `InvalidRequest` "Requests specifying Server Side Encryption with Customer provided keys must be made over a secure connection." (text from other S3 servers, 3.9) on the plain-HTTP listener, reads included; SSE-C works over `--tls` | aws-doc | 3.9 | S3SOL-02 |
| `s3.sse-c.blocked-write` | 403 `AccessDenied` "Access Denied", checked after header validation | inferred | 4.3 | S3SOL-01 |
| `s3.sse-c.missing-key-md5` | 400 `InvalidArgument` "Requests specifying Server Side Encryption with Customer provided keys must provide the client calculated MD5 of the secret key.", `ArgumentName=x-amz-server-side-encryption` | inferred | 3.2 | S3SOL-03 |
| `s3.sse-c.key-encoding` | a key that is not base64, and an MD5 mismatch on reads, get the write-side texts of 3.2; a short key with a matching MD5 on reads gets the 403 of 3.3, as the 44-byte key did | inferred | 3.2, 3.3 | S3SOL-04 |
| `s3.sse-c.metadata-without-key` | HEAD 400 no body; GetObjectAttributes 400 `InvalidRequest` "stored using a form" text | inferred | 3.3 | S3SOL-05 |
| `s3.sse-c.copy-source-not-applicable` | 400 `InvalidRequest` "The encryption parameters are not applicable to this object." | inferred | 3.5 | S3SOL-06 |
| `s3.sse-c.copy-source-wrong-key` | 403 `AccessDenied` "correct secret key" text | inferred | 3.5 | S3SOL-07 |
| `s3.sse-c.complete-headers` | Complete checks SSE-C headers like UploadPart when sent, and requires them when the upload has a checksum algorithm; ListParts never requires them | inferred | 3.6 | S3SOL-08 |
| `s3.encryption.unblocked-read` | after `NONE`, GET returns `<BlockedEncryptionTypes><EncryptionType>NONE</EncryptionType></BlockedEncryptionTypes>`; a default-free rule reads back with the `AES256` default added; an empty `Rule` is `MalformedXML` | inferred | 4.2 | S3SOL-10 |
| `s3.encryption.blocked-types-omitted` | a Put without the element, and DeleteBucketEncryption, keep the previous blocked setting | inferred | 4.2 | S3SOL-11 |
| `s3.object-lock.retain-until-format` | ISO 8601 UTC with milliseconds (`2030-01-01T00:00:00.000Z`), milliseconds of the request kept; a version is deletable once now >= RetainUntil | inferred | 6.7, 7.1 | S3SOL-12 |
| `s3.object-lock.default-years` | a year is 365 days; Days 1..36500 and Years 1..100, else `MalformedXML` | inferred | 5.2, 5.3 | S3SOL-13 |
| `s3.object-lock.checksum-scope` | PutObject only, when the new version gets a retention period; satisfied by `Content-MD5` or an `x-amz-checksum-*` header or trailer, not by a signed payload hash | inferred | 6.3 | S3SOL-14, S3SOL-15 |
| `s3.object-lock.copy-default` | CopyObject into a default-retention bucket applies the default, no checksum needed | inferred | 6.3, 6.8 | S3SOL-16 |
| `s3.object-lock.headers-without-lock` | 400 `InvalidRequest` "Bucket is missing Object Lock Configuration" | inferred | 6.2 | S3SOL-17 |
| `s3.object-lock.header-texts` | past date: 400 `InvalidArgument` "The retain until date must be in the future!" with `ArgumentName` the header; a date over 100 years ahead, an unparsable date, a bad legal-hold value: 400 `InvalidArgument` with `ArgumentName` and `ArgumentValue` and an understudy text naming the header; date `ArgumentValue`s rendered as Java `Date.toString()` in America/Los_Angeles (PST or PDT) | inferred | 6.2, 6.4 | S3SOL-18 |
| `s3.object-lock.mode-upgrade` | GOVERNANCE to COMPLIANCE without bypass refused with the lock 403 | inferred | 6.4 | S3SOL-19 |
| `s3.object-lock.date-without-mode` | `MalformedXML` | inferred | 6.4 | S3SOL-20 |
| `s3.object-lock.cleared-reads` | removed retention reads as 404 `NoSuchObjectLockConfiguration` and HEAD omits mode and date; a hold set `OFF` reads as 200 `Status=OFF` | inferred | 6.4, 6.5 | S3SOL-21 |
| `s3.object-lock.check-order` | the bucket's lock state is checked before the key; legal hold on a delete marker is the 405 of 6.4 | inferred | 6.5 | S3SOL-22 |
| `s3.object-lock.bypass-without-lock` | the 7.3 400 on every bucket without Object Lock, versioned or not, with or without `versionId`; `false` on a lock bucket is no bypass | third-party-recorded (unversioned only) | 7.3 | S3SOL-23 |

The `Ref` of each switch is `docs/research/s3-ssec-object-lock.md#<section anchor>`; `s3.encryption.after-delete` moves to aws-recorded (section 2).

### 11.5 Limits and texts

Hand-code every text in 3.2, 3.3, 3.6, 5.1, 5.2, 6.2, 6.4, 7.1-7.3 exactly as quoted, with their elements and element order as recorded. Limits: SSE-C key exactly 32 bytes after base64 decoding; key MD5 is base64 of a 16-byte digest; retention at most 100 years; event-hold durations 1..36,500 days or 1..100 years (refused anyway, 11.3).

### 11.6 Terraform: apply plus an empty second plan

- `aws_s3_bucket` with `object_lock_enabled = true`: honour the create header, version the bucket, answer `GetObjectLockConfiguration` with `ObjectLockEnabled=Enabled` (9.1). Already true (`internal/service/s3/bucket.go` `newBucket`).
- `aws_s3_bucket_object_lock_configuration`: Get must echo `Mode` and exactly the one of `Days`/`Years` that was put; Delete's enabled-only Put must drop the rule (9.2). Already true; keep it when the config becomes applied.
- `aws_s3_object` with lock arguments: HEAD must return the three headers in a form that `time.RFC3339` re-renders to the configured string (whole seconds, `Z`), and the CRC32 the provider adds satisfies 6.3 (9.3). Retention updates send bypass when lowering; the 6.4 rules must accept that.
- Destroy: per-key `AccessDenied` from DeleteObjects and `AccessDenied` from DeleteObject on locked versions, legal-hold removal, then bypass deletes (9.6). A version with both a hold and GOVERNANCE retention must fail the first `terraform destroy` with the lock 403 and go on the second, as on AWS (9.6); understudy must not make the first one succeed.
- `aws_s3_bucket_server_side_encryption_configuration` with `blocked_encryption_types = ["NONE"]` needs S3SOL-10's read form; without the attribute, the default GET form already round-trips.
- `aws_s3_object_copy` with `customer_key`: its refresh is expected to fail on AWS (inferred, 9.4; S3SOL-05 records the HEAD); understudy must fail it the same way.

### 11.7 Never fake

- Never return an SSE-C object's data or metadata without the matching key, and never store the key (3.11).
- Never give an SSE-C object an MD5 ETag, or `x-amz-server-side-encryption: AES256` (3.4).
- Never accept lock headers, lock configuration or the bypass header with no effect: either apply them or refuse them as 11.3 says. Event holds are refused with 501, never stored.
- Never let bypass lift COMPLIANCE or a legal hold (6.6, 7.1).
- Retention dates compare on the emulator clock only (CLAUDE.md invariant 5), so tests advance time instead of sleeping.
- When lifecycle expiration is implemented, it must skip locked versions (7.4).

## 12. Recording queue

Agents never run these. A human runs them in the recording-kit shell ([recording-kit.md](../recording-kit.md) 1-2), which defines `P`, `R`, `A`, `REC`, `E`, `rec` and `recurl`. Costs are S3 request charges (fractions of a cent per case) and assume the shared buckets of 12.1. Run 12.1's setup, the cases and 12.1's teardown in a kit shell of its own: 12.1 exports `SVC` and sets `B`, `N`, `U` and `SH`, which other queues set to other values (recording-kit.md 2). Each case writes under `$REC/S3SOL/`. Free-plan SCP refusals are noted `n/a (free-plan SCP)`, as in the kit.

WARNING: COMPLIANCE retention cannot be shortened or removed by anyone. Only S3SOL-19 may create COMPLIANCE state, and only with a date at most 2 minutes ahead. Every other case uses GOVERNANCE, which the shared teardown removes with bypass.

### 12.1 Shared setup and teardown

```sh
export SVC=s3
B=$P-ssec; BB=$P-ssec-blk; BR=$P-ssec-rule; L=$P-olock; LY=$P-olock-y; N=$P-nolock
K1=0123456789abcdef0123456789abcdef   # public throwaway keys: never protect real data with them
K1B=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=; K1M=hRasmdxgYDKV3nvbahU1MA==
K2=fedcba9876543210fedcba9876543210
K2B=ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA=; K2M=dT1y7SEiqn6YCJ3QeqwoIw==
C1=(-H 'x-amz-server-side-encryption-customer-algorithm: AES256' -H "x-amz-server-side-encryption-customer-key: $K1B" -H "x-amz-server-side-encryption-customer-key-MD5: $K1M")
U=UNSIGNED-PAYLOAD
SH=84d89877f0d4041efb6bf91a16f0248f2fd573e6af05c19f96bedb9f882f7882   # SHA-256 of 0123456789
MD=eB5eJF1ptWaXm4bijSPyxw==                                           # Content-MD5 of 0123456789
printf 0123456789 > "$REC/body10"; mkdir -p "$REC/S3SOL"
url() { echo "https://$1.s3.$R.amazonaws.com/$2"; }
mb() { local b=$1; shift
  if [ "$R" = us-east-1 ]; then aws s3api create-bucket --bucket "$b" "$@"
  else aws s3api create-bucket --bucket "$b" --create-bucket-configuration LocationConstraint="$R" "$@"; fi; }
soon() { python3 -c 'import datetime as d,sys;print((d.datetime.now(d.timezone.utc)+d.timedelta(seconds=int(sys.argv[1]))).strftime("%Y-%m-%dT%H:%M:%S.123Z"))' "$1"; }

mb "$B" && aws s3api put-bucket-encryption --bucket "$B" --server-side-encryption-configuration \
  '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"},"BlockedEncryptionTypes":{"EncryptionType":["NONE"]}}]}'
mb "$BB"
mb "$L" --object-lock-enabled-for-bucket
mb "$N" && aws s3api put-bucket-versioning --bucket "$N" --versioning-configuration Status=Enabled
aws s3api put-object --bucket "$B" --key c1 --body "$REC/body10" --sse-customer-algorithm AES256 --sse-customer-key "$K1"
aws s3api put-object --bucket "$B" --key p1 --body "$REC/body10"
aws s3api put-object --bucket "$BB" --key p1 --body "$REC/body10"
aws s3api put-object --bucket "$N" --key n1 --body "$REC/body10"
```

Teardown, after the last case (cost and time are in each case; this takes about 3 minutes):

```sh
# empty_versions <bucket> [--bypass-governance-retention]: lift holds, delete every version and marker, delete the bucket.
empty_versions() { local b=$1; shift
  aws s3api list-object-versions --bucket "$b" --output text \
    --query '[Versions[].[Key,VersionId], DeleteMarkers[].[Key,VersionId]][]' |
  while read -r k v; do
    [ "$k" = None ] && continue
    aws s3api put-object-legal-hold --bucket "$b" --key "$k" --version-id "$v" --legal-hold Status=OFF >/dev/null 2>&1
    aws s3api delete-object --bucket "$b" --key "$k" --version-id "$v" "$@" >/dev/null
  done
  aws s3api delete-bucket --bucket "$b"; }
for b in "$B" "$BB" "$BR"; do aws s3 rb "s3://$b" --force; done
empty_versions "$N"
for b in "$L" "$LY"; do empty_versions "$b" --bypass-governance-retention; done
```

A `delete-object` that still fails names a version under COMPLIANCE (wait for its date) or an event hold (release it, S3SOL-24).

Index, priority first: S3SOL-01, -10, -05, -12, -23, -02, then the rest in order.

### 12.2 S3SOL-01. SSE-C write to a bucket that blocks SSE-C

Settles 4.3 and `s3.sse-c.blocked-write`: the 403 text, and whether header validation runs first.

```sh
rec S3SOL/01-put aws s3api put-object --bucket "$BB" --key c --body "$REC/body10" --sse-customer-algorithm AES256 --sse-customer-key "$K1"
rec S3SOL/01-put-bad-algo aws s3api put-object --bucket "$BB" --key c --body "$REC/body10" --sse-customer-algorithm KMS --sse-customer-key "$K1"
rec S3SOL/01-mpu aws s3api create-multipart-upload --bucket "$BB" --key c --sse-customer-algorithm AES256 --sse-customer-key "$K1"
rec S3SOL/01-copy-target aws s3api copy-object --bucket "$BB" --key c --copy-source "$BB/p1" --sse-customer-algorithm AES256 --sse-customer-key "$K1"
rec S3SOL/01-copy-source-only aws s3api copy-object --bucket "$BB" --key c2 --copy-source "$B/c1" --copy-source-sse-customer-algorithm AES256 --copy-source-sse-customer-key "$K1"
```

Expected: 403 `AccessDenied` for the first four (text to record); the bad-algo step shows the order; the last copies (copy-source headers are a read). Cost under USD 0.01, 3 minutes. Teardown: `aws s3api list-multipart-uploads --bucket "$BB"` and abort any upload listed.

### 12.3 S3SOL-02. SSE-C over plain HTTP

Settles 3.9 and `s3.sse-c.plain-http`. The keys are public, so sending them in clear is harmless.

WARNING: `recurl` also sends the access key id, the signature and `x-amz-security-token` in clear on these four requests. The token cannot sign anything without the secret key, but run this step only on a network you trust, or skip it and mark it `n/a`.

```sh
recurl S3SOL/02-put-http -X PUT -H "x-amz-content-sha256: $U" "${C1[@]}" --data-binary @"$REC/body10" "http://$B.s3.$R.amazonaws.com/h1"
recurl S3SOL/02-get-http -H "x-amz-content-sha256: $E" "${C1[@]}" "http://$B.s3.$R.amazonaws.com/c1"
recurl S3SOL/02-get-plain-http -H "x-amz-content-sha256: $E" "${C1[@]}" "http://$B.s3.$R.amazonaws.com/p1"
recurl S3SOL/02-put-blocked-http -X PUT -H "x-amz-content-sha256: $U" "${C1[@]}" --data-binary @"$REC/body10" "http://$BB.s3.$R.amazonaws.com/h1"
```

Expected: 400 `InvalidRequest` with the "secure connection" text on all four (hypothesis); the last shows HTTPS versus block order. Cost under USD 0.01, 2 minutes. Teardown: `aws s3api delete-object --bucket "$B" --key h1`.

### 12.4 S3SOL-03. SSE-C key without its MD5

Settles 3.2 and `s3.sse-c.missing-key-md5` (botocore cannot send this; raw requests only).

```sh
recurl S3SOL/03-put -X PUT -H "x-amz-content-sha256: $U" -H 'x-amz-server-side-encryption-customer-algorithm: AES256' \
  -H "x-amz-server-side-encryption-customer-key: $K1B" --data-binary @"$REC/body10" "$(url "$B" m3)"
recurl S3SOL/03-get -H "x-amz-content-sha256: $E" -H 'x-amz-server-side-encryption-customer-algorithm: AES256' \
  -H "x-amz-server-side-encryption-customer-key: $K1B" "$(url "$B" c1)"
```

Expected: 400 `InvalidArgument` naming the MD5 (text to record). Cost under USD 0.01, 2 minutes. Teardown: `aws s3api delete-object --bucket "$B" --key m3`.

### 12.5 S3SOL-04. Key encoding, and key checks on reads

Settles 3.2 and `s3.sse-c.key-encoding`.

```sh
recurl S3SOL/04-put-not-base64 -X PUT -H "x-amz-content-sha256: $U" -H 'x-amz-server-side-encryption-customer-algorithm: AES256' \
  -H 'x-amz-server-side-encryption-customer-key: not base64!' -H "x-amz-server-side-encryption-customer-key-MD5: $K1M" \
  --data-binary @"$REC/body10" "$(url "$B" m4)"
recurl S3SOL/04-get-short-key -H "x-amz-content-sha256: $E" -H 'x-amz-server-side-encryption-customer-algorithm: AES256' \
  -H 'x-amz-server-side-encryption-customer-key: MDEyMzQ1Njc4OQ==' -H "x-amz-server-side-encryption-customer-key-MD5: $MD" "$(url "$B" c1)"
recurl S3SOL/04-get-bad-md5 -H "x-amz-content-sha256: $E" -H 'x-amz-server-side-encryption-customer-algorithm: AES256' \
  -H "x-amz-server-side-encryption-customer-key: $K1B" -H "x-amz-server-side-encryption-customer-key-MD5: $K2M" "$(url "$B" c1)"
```

Expected: 400 `InvalidArgument` with the key-size text, then key-size or 403, then the MD5-mismatch text. Cost under USD 0.01, 2 minutes. Teardown: `aws s3api delete-object --bucket "$B" --key m4`.

### 12.6 S3SOL-05. Metadata reads of an SSE-C object without the key

Settles 3.3, 9.4 and `s3.sse-c.metadata-without-key`.

```sh
rec S3SOL/05-head aws s3api head-object --bucket "$B" --key c1
rec S3SOL/05-attrs aws s3api get-object-attributes --bucket "$B" --key c1 --object-attributes ETag ObjectSize
rec S3SOL/05-attrs-with-key aws s3api get-object-attributes --bucket "$B" --key c1 --object-attributes ETag ObjectSize \
  --sse-customer-algorithm AES256 --sse-customer-key "$K1B" --sse-customer-key-md5 "$K1M"
```

Expected: HEAD 400 without body; attributes 400 `InvalidRequest`; the control 200. Cost under USD 0.01, 1 minute. Teardown: none.

### 12.7 S3SOL-06. Copy-source SSE-C headers on a plain source

Settles 3.5 and `s3.sse-c.copy-source-not-applicable`.

```sh
rec S3SOL/06-copy aws s3api copy-object --bucket "$B" --key t6 --copy-source "$B/p1" \
  --copy-source-sse-customer-algorithm AES256 --copy-source-sse-customer-key "$K1"
```

Expected: 400 `InvalidRequest` "not applicable" (hypothesis). Cost under USD 0.01, 1 minute. Teardown: `aws s3api delete-object --bucket "$B" --key t6`.

### 12.8 S3SOL-07. Copy with the wrong source key

Settles 3.5 and `s3.sse-c.copy-source-wrong-key`.

```sh
rec S3SOL/07-copy aws s3api copy-object --bucket "$B" --key t7 --copy-source "$B/c1" \
  --copy-source-sse-customer-algorithm AES256 --copy-source-sse-customer-key "$K2"
UP7=$(aws s3api create-multipart-upload --bucket "$B" --key t7p --query UploadId --output text)
rec S3SOL/07-upload-part-copy aws s3api upload-part-copy --bucket "$B" --key t7p --upload-id "$UP7" --part-number 1 \
  --copy-source "$B/c1" --copy-source-sse-customer-algorithm AES256 --copy-source-sse-customer-key "$K2"
```

Expected: 403 `AccessDenied` "correct secret key" on both. Cost under USD 0.01, 1 minute. Teardown: `aws s3api delete-object --bucket "$B" --key t7`; `aws s3api abort-multipart-upload --bucket "$B" --key t7p --upload-id "$UP7"`.

### 12.9 S3SOL-08. CompleteMultipartUpload and SSE-C

Settles 3.6 and `s3.sse-c.complete-headers`. Each variant uses a fresh upload with a checksum algorithm.

```sh
mk8() { UP=$(aws s3api create-multipart-upload --bucket "$B" --key "$1" --checksum-algorithm CRC32 \
    --sse-customer-algorithm AES256 --sse-customer-key "$K1" --query UploadId --output text)
  aws s3api upload-part --bucket "$B" --key "$1" --upload-id "$UP" --part-number 1 --body "$REC/body10" --checksum-algorithm CRC32 \
    --sse-customer-algorithm AES256 --sse-customer-key "$K1" > "$REC/_work/S3SOL/08-$1-part.json"
  MPU=$(jq -c '{Parts:[{PartNumber:1, ETag:.ETag, ChecksumCRC32:.ChecksumCRC32}]}' "$REC/_work/S3SOL/08-$1-part.json"); }
mkdir -p "$REC/_work/S3SOL"   # helper outputs stay out of the item directory, which distill-parity reads
mk8 m8a; rec S3SOL/08-list-parts-no-key aws s3api list-parts --bucket "$B" --key m8a --upload-id "$UP"
rec S3SOL/08-no-key aws s3api complete-multipart-upload --bucket "$B" --key m8a --upload-id "$UP" --multipart-upload "$MPU"
mk8 m8b; rec S3SOL/08-wrong-key aws s3api complete-multipart-upload --bucket "$B" --key m8b --upload-id "$UP" --multipart-upload "$MPU" \
  --sse-customer-algorithm AES256 --sse-customer-key "$K2"
mk8 m8c; rec S3SOL/08-right-key aws s3api complete-multipart-upload --bucket "$B" --key m8c --upload-id "$UP" --multipart-upload "$MPU" \
  --sse-customer-algorithm AES256 --sse-customer-key "$K1"
```

Expected: list-parts 200 (hypothesis) or a refusal; refusal (text to record) for the first two completes, 200 for the third. Cost under USD 0.01, 3 minutes. Teardown: `aws s3api list-multipart-uploads --bucket "$B"`, abort each listed upload; `aws s3 rm "s3://$B/" --recursive --exclude '*' --include 'm8*'`.

### 12.10 S3SOL-09. Presigned URL used with unsigned SSE-C headers

Settles 3.10. Redact the URL's signature before sharing (kit section 2).

```sh
URL=$(aws s3 presign "s3://$B/c1" --expires-in 300)
curl -sS -i --http1.1 "${C1[@]}" "$URL" > "$REC/S3SOL/09-presign-unsigned-ssec.http"
curl -sS -i --http1.1 "$URL" > "$REC/S3SOL/09-presign-no-ssec.http"
```

Expected: 403 `AccessDenied` with `HeadersNotSigned` listing the three headers; the control is the 400 "stored using a form". Cost under USD 0.01, 1 minute. Teardown: none.

### 12.11 S3SOL-10. GetBucketEncryption after unblocking

Settles 4.2, 9.5 and `s3.encryption.unblocked-read`.

```sh
mb "$BR"
rec S3SOL/10-put-rule-only aws s3api put-bucket-encryption --bucket "$BR" \
  --server-side-encryption-configuration '{"Rules":[{"BlockedEncryptionTypes":{"EncryptionType":["NONE"]}}]}'
rec S3SOL/10-get-rule-only aws s3api get-bucket-encryption --bucket "$BR"
rec S3SOL/10-get-full-form aws s3api get-bucket-encryption --bucket "$B"
rec S3SOL/10-put-ssec aws s3api put-object --bucket "$BR" --key c --body "$REC/body10" --sse-customer-algorithm AES256 --sse-customer-key "$K1"
rec S3SOL/10-put-empty-rule aws s3api put-bucket-encryption --bucket "$BR" --server-side-encryption-configuration '{"Rules":[{}]}'
rec S3SOL/10-get-after-empty-rule aws s3api get-bucket-encryption --bucket "$BR"
```

Expected: the rule-only put accepted; record both GET bodies byte for byte; the empty rule refused with `MalformedXML` (if it is accepted, the last GET shows what it did). Cost under USD 0.01, 2 minutes. Teardown: kept for S3SOL-11; removed by 12.1.

### 12.12 S3SOL-11. Omitting BlockedEncryptionTypes, and deleting, after NONE

Settles 4.2 and `s3.encryption.blocked-types-omitted`. Run after S3SOL-10.

```sh
NONE='{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"},"BlockedEncryptionTypes":{"EncryptionType":["NONE"]}}]}'
rec S3SOL/11-put-omit aws s3api put-bucket-encryption --bucket "$BR" \
  --server-side-encryption-configuration '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'
rec S3SOL/11-get-after-omit aws s3api get-bucket-encryption --bucket "$BR"
rec S3SOL/11-ssec-after-omit aws s3api put-object --bucket "$BR" --key c2 --body "$REC/body10" --sse-customer-algorithm AES256 --sse-customer-key "$K1"
aws s3api put-bucket-encryption --bucket "$BR" --server-side-encryption-configuration "$NONE"
rec S3SOL/11-delete aws s3api delete-bucket-encryption --bucket "$BR"
rec S3SOL/11-get-after-delete aws s3api get-bucket-encryption --bucket "$BR"
rec S3SOL/11-ssec-after-delete aws s3api put-object --bucket "$BR" --key c3 --body "$REC/body10" --sse-customer-algorithm AES256 --sse-customer-key "$K1"
```

Expected: keep `NONE` (hypothesis) or fall back to `SSE-C`; the SSE-C puts confirm. Cost under USD 0.01, 2 minutes. Teardown: 12.1.

### 12.13 S3SOL-12. Retain-until-date wire form and precision

Settles 6.7, 7.1 and `s3.object-lock.retain-until-format`.

```sh
D12=$(soon 3600)
rec S3SOL/12-put aws s3api put-object --bucket "$L" --key d12 --body "$REC/body10" --object-lock-mode GOVERNANCE --object-lock-retain-until-date "$D12"
rec S3SOL/12-head aws s3api head-object --bucket "$L" --key d12
rec S3SOL/12-get-retention aws s3api get-object-retention --bucket "$L" --key d12
aws s3api put-object --bucket "$L" --key e12 --body "$REC/body10" --object-lock-mode GOVERNANCE --object-lock-retain-until-date "$(soon 20)"
sleep 25
rec S3SOL/12-delete-after-expiry aws s3api delete-object --bucket "$L" --key e12 \
  --version-id "$(aws s3api list-object-versions --bucket "$L" --prefix e12 --query 'Versions[0].VersionId' --output text)"
```

Expected: record the raw `x-amz-object-lock-retain-until-date` header and the XML `RetainUntilDate` against the sent `.123Z`; the expired delete succeeds. Cost under USD 0.01, 2 minutes. Teardown: 12.1.

### 12.14 S3SOL-13. Default retention in years, and range refusals

Settles 5.2, 5.3 and `s3.object-lock.default-years`. Put no object until the `Years=2` rule is in place: an accepted out-of-range default would otherwise lock it for a century.

```sh
mb "$LY" --object-lock-enabled-for-bucket
cfg() { echo "{\"ObjectLockEnabled\":\"Enabled\",\"Rule\":{\"DefaultRetention\":{\"Mode\":\"GOVERNANCE\",$1}}}"; }
rec S3SOL/13-days-0 aws s3api put-object-lock-configuration --bucket "$LY" --object-lock-configuration "$(cfg '"Days":0')"
rec S3SOL/13-days-36501 aws s3api put-object-lock-configuration --bucket "$LY" --object-lock-configuration "$(cfg '"Days":36501')"
rec S3SOL/13-years-101 aws s3api put-object-lock-configuration --bucket "$LY" --object-lock-configuration "$(cfg '"Years":101')"
rec S3SOL/13-years-2 aws s3api put-object-lock-configuration --bucket "$LY" --object-lock-configuration "$(cfg '"Years":2')"
rec S3SOL/13-put aws s3api put-object --bucket "$LY" --key y --body "$REC/body10"
rec S3SOL/13-head aws s3api head-object --bucket "$LY" --key y
```

Expected: the three refusals (texts to record); the HEAD date minus `Last-Modified` is 730 days (365-day years) or 731 (calendar years; the span includes 2028-02-29). Cost under USD 0.01, 2 minutes. Teardown: 12.1 (GOVERNANCE, bypass).

### 12.15 S3SOL-14. Checksum requirement: legal hold alone, signed payload hash

Settles 6.3 and `s3.object-lock.checksum-scope`.

```sh
recurl S3SOL/14-legal-hold-only -X PUT -H "x-amz-content-sha256: $U" -H 'x-amz-object-lock-legal-hold: ON' \
  --data-binary @"$REC/body10" "$(url "$L" h14)"
recurl S3SOL/14-retention-signed-sha256 -X PUT -H "x-amz-content-sha256: $SH" -H 'x-amz-object-lock-mode: GOVERNANCE' \
  -H "x-amz-object-lock-retain-until-date: $(soon 3600)" --data-binary @"$REC/body10" "$(url "$L" s14)"
recurl S3SOL/14-retention-unsigned -X PUT -H "x-amz-content-sha256: $U" -H 'x-amz-object-lock-mode: GOVERNANCE' \
  -H "x-amz-object-lock-retain-until-date: $(soon 3600)" --data-binary @"$REC/body10" "$(url "$L" u14)"
```

Expected: the third is the R12 refusal (control); record the first two. Cost under USD 0.01, 2 minutes. Teardown: 12.1.

### 12.16 S3SOL-15. Multipart upload under retention

Settles 6.3 and `s3.object-lock.checksum-scope` for multipart.

```sh
rec S3SOL/15-create aws s3api create-multipart-upload --bucket "$L" --key m15 --object-lock-mode GOVERNANCE --object-lock-retain-until-date "$(soon 3600)"
UP=$(aws s3api list-multipart-uploads --bucket "$L" --prefix m15 --query 'Uploads[0].UploadId' --output text)
recurl S3SOL/15-part-no-checksum -X PUT -H "x-amz-content-sha256: $U" --data-binary @"$REC/body10" "$(url "$L" m15)?partNumber=1&uploadId=$UP"
ET=$(sed -n 's/^[Ee][Tt]ag: //p' "$REC/S3SOL/15-part-no-checksum.http" | tr -d '\r')
rec S3SOL/15-complete aws s3api complete-multipart-upload --bucket "$L" --key m15 --upload-id "$UP" \
  --multipart-upload "$(jq -cn --arg e "$ET" '{Parts:[{PartNumber:1,ETag:$e}]}')"
rec S3SOL/15-head aws s3api head-object --bucket "$L" --key m15
```

If the part is refused, upload it with `aws s3api upload-part ... --checksum-algorithm CRC32` and complete with that ETag. Expected: record whether the part is refused and whether HEAD shows the Create's lock settings. Cost under USD 0.01, 3 minutes. Teardown: abort any open upload (`aws s3api abort-multipart-upload --bucket "$L" --key m15 --upload-id "$UP"`); objects by 12.1.

### 12.17 S3SOL-16. CopyObject into a bucket with default retention

Settles 6.3, 6.8 and `s3.object-lock.copy-default`. Run after S3SOL-12 and S3SOL-13.

```sh
rec S3SOL/16-copy-locked-source aws s3api copy-object --bucket "$LY" --key c16 --copy-source "$L/d12"
rec S3SOL/16-head aws s3api head-object --bucket "$LY" --key c16
rec S3SOL/16-copy-plain-source aws s3api copy-object --bucket "$LY" --key c16b --copy-source "$N/n1"
rec S3SOL/16-head-b aws s3api head-object --bucket "$LY" --key c16b
```

Expected: both targets show GOVERNANCE and the 2-year default, not the source's date. Cost under USD 0.01, 2 minutes. Teardown: 12.1.

### 12.18 S3SOL-17. Lock headers on a bucket without Object Lock

Settles 6.2 and `s3.object-lock.headers-without-lock`.

```sh
rec S3SOL/17-versioned-mode aws s3api put-object --bucket "$N" --key l17 --body "$REC/body10" --object-lock-mode GOVERNANCE --object-lock-retain-until-date "$(soon 3600)"
rec S3SOL/17-versioned-hold aws s3api put-object --bucket "$N" --key l17 --body "$REC/body10" --object-lock-legal-hold-status ON
rec S3SOL/17-unversioned-mode aws s3api put-object --bucket "$B" --key l17 --body "$REC/body10" --object-lock-mode GOVERNANCE --object-lock-retain-until-date "$(soon 3600)"
rec S3SOL/17-mpu-hold aws s3api create-multipart-upload --bucket "$N" --key l17 --object-lock-legal-hold-status ON
rec S3SOL/17-copy-hold aws s3api copy-object --bucket "$N" --key l17 --copy-source "$N/n1" --object-lock-legal-hold-status ON
```

Expected: 400 `InvalidRequest` "Bucket is missing Object Lock Configuration" each (hypothesis). Cost under USD 0.01, 2 minutes. Teardown: abort any upload on `$N`; objects by 12.1.

### 12.19 S3SOL-18. PutObject lock header validation texts

Settles 6.2, 6.4 and `s3.object-lock.header-texts`.

```sh
rec S3SOL/18-past-date aws s3api put-object --bucket "$L" --key v18 --body "$REC/body10" --object-lock-mode GOVERNANCE --object-lock-retain-until-date 2020-07-01T00:00:00Z
recurl S3SOL/18-bad-date -X PUT -H "x-amz-content-sha256: $U" -H "Content-MD5: $MD" -H 'x-amz-object-lock-mode: GOVERNANCE' \
  -H 'x-amz-object-lock-retain-until-date: tomorrow' --data-binary @"$REC/body10" "$(url "$L" v18)"
rec S3SOL/18-bad-hold aws s3api put-object --bucket "$L" --key v18 --body "$REC/body10" --object-lock-legal-hold-status MAYBE
rec S3SOL/18-over-100-years aws s3api put-object --bucket "$L" --key v18 --body "$REC/body10" --object-lock-mode GOVERNANCE --object-lock-retain-until-date 2127-01-01T00:00:00Z
rec S3SOL/18-retention-past-summer aws s3api put-object-retention --bucket "$L" --key d12 --bypass-governance-retention \
  --retention '{"Mode":"GOVERNANCE","RetainUntilDate":"2020-07-01T00:00:00Z"}'
```

Expected: record each status, code, text and `ArgumentName`/`ArgumentValue`; the last should read `Tue Jun 30 17:00:00 PDT 2020` if the PST rendering holds. Cost under USD 0.01, 2 minutes. Teardown: 12.1 (an accepted 2127 object is GOVERNANCE, removable with bypass).

### 12.20 S3SOL-19. GOVERNANCE to COMPLIANCE without bypass

Settles 6.4 and `s3.object-lock.mode-upgrade`. This is the only case that may create COMPLIANCE state; keep the dates.

```sh
aws s3api put-object --bucket "$L" --key g19 --body "$REC/body10" --object-lock-mode GOVERNANCE --object-lock-retain-until-date "$(soon 60)"
V19=$(aws s3api head-object --bucket "$L" --key g19 --query VersionId --output text)
rec S3SOL/19-upgrade aws s3api put-object-retention --bucket "$L" --key g19 --version-id "$V19" \
  --retention "{\"Mode\":\"COMPLIANCE\",\"RetainUntilDate\":\"$(soon 120)\"}"
rec S3SOL/19-head aws s3api head-object --bucket "$L" --key g19
rec S3SOL/19-delete-locked aws s3api delete-object --bucket "$L" --key g19 --version-id "$V19"
sleep 130
```

Expected: refusal with the lock 403 (hypothesis); the delete step re-records the current locked-delete text (section 10). Cost under USD 0.01, 4 minutes including the wait. Teardown: after the wait, 12.1.

### 12.21 S3SOL-20. RetainUntilDate without Mode

Settles 6.4 and `s3.object-lock.date-without-mode`.

```sh
rec S3SOL/20-date-only aws s3api put-object-retention --bucket "$L" --key d12 --retention "{\"RetainUntilDate\":\"$(soon 7200)\"}"
rec S3SOL/20-date-only-bypass aws s3api put-object-retention --bucket "$L" --key d12 --bypass-governance-retention \
  --retention "{\"RetainUntilDate\":\"$(soon 7200)\"}"
```

Expected: `MalformedXML` both (hypothesis). Cost under USD 0.01, 1 minute. Teardown: 12.1.

### 12.22 S3SOL-21. Reads after retention is removed and a hold is lifted

Settles 6.4, 6.5 and `s3.object-lock.cleared-reads`.

```sh
aws s3api put-object --bucket "$L" --key r21 --body "$REC/body10" --object-lock-mode GOVERNANCE \
  --object-lock-retain-until-date "$(soon 3600)" --object-lock-legal-hold-status ON
aws s3api put-object-retention --bucket "$L" --key r21 --retention '{}' --bypass-governance-retention
aws s3api put-object-legal-hold --bucket "$L" --key r21 --legal-hold Status=OFF
rec S3SOL/21-get-retention aws s3api get-object-retention --bucket "$L" --key r21
rec S3SOL/21-get-legal-hold aws s3api get-object-legal-hold --bucket "$L" --key r21
rec S3SOL/21-head aws s3api head-object --bucket "$L" --key r21
```

Expected: 404 `NoSuchObjectLockConfiguration`, 200 `Status=OFF`, HEAD with only the legal-hold header `OFF` (hypotheses). Cost under USD 0.01, 1 minute. Teardown: 12.1.

### 12.23 S3SOL-22. Error order for legal holds

Settles 6.5 and `s3.object-lock.check-order`. Run after S3SOL-16, -18 and -20 (it puts a delete marker on `d12`).

```sh
rec S3SOL/22-hold-missing-key-nolock aws s3api put-object-legal-hold --bucket "$N" --key missing --legal-hold Status=ON
rec S3SOL/22-retention-missing-key-nolock aws s3api get-object-retention --bucket "$N" --key missing
DM=$(aws s3api delete-object --bucket "$L" --key d12 --query VersionId --output text)
rec S3SOL/22-hold-delete-marker aws s3api put-object-legal-hold --bucket "$L" --key d12 --version-id "$DM" --legal-hold Status=ON
rec S3SOL/22-get-hold-delete-marker aws s3api get-object-legal-hold --bucket "$L" --key d12 --version-id "$DM"
```

Expected: `InvalidRequest` "Bucket is missing..." for the first two (hypothesis), 405 for the last two. Cost under USD 0.01, 1 minute. Teardown: 12.1.

### 12.24 S3SOL-23. Bypass header on a versioned bucket without Object Lock

Settles 7.3, 9.6 and `s3.object-lock.bypass-without-lock`. Run after S3SOL-16 (it uses `n1`).

```sh
V=$(aws s3api list-object-versions --bucket "$N" --prefix n1 --query 'Versions[0].VersionId' --output text)
rec S3SOL/23-delete-objects-version-bypass aws s3api delete-objects --bucket "$N" --bypass-governance-retention \
  --delete "{\"Objects\":[{\"Key\":\"n1\",\"VersionId\":\"$V\"}]}"
rec S3SOL/23-delete-version-bypass aws s3api delete-object --bucket "$N" --key n1 --version-id "$V" --bypass-governance-retention
aws s3api put-object --bucket "$L" --key f23 --body "$REC/body10" --object-lock-mode GOVERNANCE --object-lock-retain-until-date "$(soon 3600)"
V23=$(aws s3api head-object --bucket "$L" --key f23 --query VersionId --output text)
rec S3SOL/23-lock-bucket-false aws s3api delete-object --bucket "$L" --key f23 --version-id "$V23" --no-bypass-governance-retention
```

Expected: 400 `InvalidArgument` "x-amz-bypass-governance-retention is only applicable to Object Lock enabled buckets." on the first two (hypothesis); the lock 403 on the third (`false` is no bypass). If the first deletes the version, the second records a missing version; note it. Cost under USD 0.01, 1 minute. Teardown: 12.1.

### 12.25 S3SOL-24. Event-hold refusals

Settles 6.9. Needs a CLI whose model has `EventHold` (`aws s3api put-object-retention help | grep -i EventHold`); otherwise `n/a`.

```sh
aws s3api put-object --bucket "$L" --key e24 --body "$REC/body10"
pr() { rec "S3SOL/24-$1" aws s3api put-object-retention --bucket "$L" --key e24 --retention "$2"; }
pr no-duration '{"Mode":"GOVERNANCE","EventHold":"ON"}'
pr no-mode '{"EventHold":"ON","EventHoldDuration":{"Days":1}}'
pr both-units '{"Mode":"GOVERNANCE","EventHold":"ON","EventHoldDuration":{"Days":1,"Years":1}}'
pr over-range '{"Mode":"GOVERNANCE","EventHold":"ON","EventHoldDuration":{"Days":36501}}'
pr ok '{"Mode":"GOVERNANCE","EventHold":"ON","EventHoldDuration":{"Days":1}}'
rec S3SOL/24-get aws s3api get-object-retention --bucket "$L" --key e24
rec S3SOL/24-head aws s3api head-object --bucket "$L" --key e24
pr release '{"Mode":"GOVERNANCE","EventHold":"OFF"}'
rec S3SOL/24-get-released aws s3api get-object-retention --bucket "$L" --key e24
```

Expected: four 400s (codes and texts to record), then the hold, its read forms, and the release fixing a date one day out. Cost under USD 0.01, 2 minutes. Teardown: the release step must succeed before 12.1 can delete `e24` with bypass.
