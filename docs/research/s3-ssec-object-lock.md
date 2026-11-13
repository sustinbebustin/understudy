# S3 SSE-C and Object Lock: primary-source research

Scope: the roadmap's S3 SSE-C and Object Lock feature: server-side encryption with customer-provided keys (SSE-C), the per-bucket SSE-C block that AWS made the default in April 2026, and S3 Object Lock (bucket configuration, per-object retention and legal holds, deletes under lock, the new variable retention). Sources: the S3 API reference and user guide (Markdown twins, retrieved 2026-10-25), the S3 Smithy model vendored at `third_party/api-models-aws` (aws/api-models-aws @ 1cfdfb4bb9ea858e65623e66d73af04bec353201), LocalStack's AWS-validated snapshots (8 `TestS3SSECEncryption` cases and 20 Object Lock cases, all last validated 2026-02-21), the repo's own AWS recordings in `testdata/parity/s3/R06`, `R12` and `R15` (2026-10-14), botocore source, and hashicorp/terraform-provider-aws v6.68.0. Retrieved 2026-10-25. It extends [s3.md](s3.md) 4.1, 4.2.10 and 5.7 and does not repeat the wire layer ([wire-rest.md](wire-rest.md)).

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
| (new) a `PutBucketEncryption` that omits `BlockedEncryptionTypes` | the GET still reports SSE-C blocked (starting from the default; starting from `NONE` too, S3SOL-11, 4.2) | `testdata/parity/s3/R06/02-put-encryption-valid-arn.json` then `02-get-encryption-arn.json` |
| s3.md 4.2.10: the error for an Object Lock upload without `Content-MD5` or a checksum is UNVERIFIED (moto's `MissingUploadObjectWithObjectLockHeaders`) | `400 InvalidRequest`, "Content-MD5 OR x-amz-checksum- HTTP header is required for Put Object requests with Object Lock parameters", no extra elements, even when the request carries no lock header and the retention comes from the bucket default | `testdata/parity/s3/R12/11-put-default-retention.json`, `11-put-without-checksum.json` |
| `internal/service/s3/sse.go` `errSSECNotApplicable`: "The text is UNVERIFIED" | `400 InvalidRequest`, "The encryption parameters are not applicable to this object." for SSE-C headers on a GET of a non-SSE-C object. The bucket was created on 2026-10-14 and R15 did not unblock it, so it blocks SSE-C. That state is inferred from 4.1 and row 3 here: R15 did not record the bucket's encryption configuration | `testdata/parity/s3/R15/x-ssec-on-non-ssec-read.json` |

`internal/service/s3/sse.go` `errSSECBlocked` (the 403 text) is settled by S3SOL-01 (4.3). `testdata/parity/s3/R12/notes.md` step 11 also records that understudy stores an Object Lock upload it should refuse.

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
- Recorded (S3SOL-03, recorded 2026-11-03): a raw PutObject with algorithm and key but no MD5 header gets `400 InvalidArgument`, "Requests specifying Server Side Encryption with Customer provided keys must provide the client calculated MD5 of the secret key.", `ArgumentName=x-amz-server-side-encryption`, the text Cloudflare's R2 emulator and MinIO use (`testdata/parity/s3/S3SOL/03-put.json`). A raw GetObject of an SSE-C object with algorithm and key but no MD5 **succeeds**: 200 with the body and `x-amz-server-side-encryption-customer-key-MD5` set to the MD5 S3 computed (`03-get.json`). So the docs' "all three headers are required" holds for writes only. HEAD, GetObjectAttributes and a copy source without the MD5 are inferred to behave as GET (UNVERIFIED; understudy computes it on every read).
- Recorded (S3SOL-04, recorded 2026-11-03): a PutObject key header that is not base64 (`not base64!`) with the MD5 of a valid key gets the MD5-mismatch text of the table above, not the key-size text (`04-put-not-base64.json`); whether a key that is not base64 without its MD5 is refused for the MD5 or the encoding is UNVERIFIED. On GET, a 10-byte key with its matching MD5 gets the 403 "must provide the correct secret key" (`04-get-short-key.json`), as the 44-byte key did (3.3), and the right key with another key's MD5 gets `400 InvalidArgument` with the MD5-mismatch text and `ArgumentName=x-amz-server-side-encryption` (`04-get-bad-md5.json`).

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
- Recorded (S3SOL-05, recorded 2026-11-03; `testdata/parity/s3/S3SOL/05-*.json`): HEAD of an SSE-C object without SSE-C headers is 400 with no body (`05-head`); GetObjectAttributes is 400 `InvalidRequest`, "The object was stored using a form of Server Side Encryption. The correct parameters must be provided to retrieve the object." (`05-attrs`), and 200 with the key (`05-attrs-with-key`). The docs say these headers "must" be sent to "retrieve the object's metadata" ([API_HeadObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadObject.html)). This decides Terraform's `aws_s3_object_copy` refresh (9.4).

### 3.4 Response headers and ETag

- SSE-C writes and reads report `x-amz-server-side-encryption-customer-algorithm: AES256` and `x-amz-server-side-encryption-customer-key-MD5: <request MD5>`, and **no** `x-amz-server-side-encryption` header (PutObject LS test_s3.snapshot.json:L12790; HEAD L13190; GET L13204; CopyObject L12926; CreateMultipartUpload L13001; UploadPart L13012). Every non-SSE-C object reports `x-amz-server-side-encryption: AES256` ([s3.md](s3.md) 5.2), so this is a branch, not an omission.
- `CompleteMultipartUpload` sent without SSE-C headers echoes none (LS test_s3.snapshot.json:L13083), matching `com.amazonaws.s3#CompleteMultipartUploadOutput`, which has no SSE-C members; one sent with the key gets the pair echoed all the same (S3SOL-08 `08-right-key`, recorded 2026-11-03; 3.6). `com.amazonaws.s3#GetObjectAttributesOutput` has no SSE members at all.
- ETag of an SSE-C single PUT or SSE-C copy is not the MD5 of the data ([s3.md](s3.md) 5.3, `API_Object`): three PUTs of `test_data` gave `b68bda52...`, `b876fdc9...`, `b84c1aee...` (L12790, L12902, L13179) and an SSE-C copy of it gave `5e04d65f...` (L12930), while `md5("test_data")` is `6af8307c2460f2d208ad254f04be4b0d` (computed). A random 128-bit hex value per write matches.
- SSE-C parts get random ETags too: three identical 5,242,881-byte parts got three different ETags (L13012-L13032; the MD5 of that content is `c4c753e6...`). The object's ETag keeps the `-N` suffix but is **not** the MD5 of the returned part ETags: AWS answered `"2d296eb6540ecd21430c385336c5dc9c-3"` on Complete and GET (L13083), while the MD5 of the three 16-byte part ETags `cf96b8ba... baa1d9ab... 6e702181...` is `f65e8fc76274ff489bb2b9cb55d378a4` and the MD5 of the three plaintext part MD5s is `c7cb0938a47e31f70cf07028d22e6913` (both computed). The hex digits come from nothing the client sees (inferred).
- Copying an SSE-C source to a non-SSE-C target gives the plaintext MD5 ETag (`6af8307c...`, L12913) and `x-amz-server-side-encryption: AES256`.

### 3.5 CopyObject and UploadPartCopy

- Source SSE-C headers decrypt the source, target SSE-C headers encrypt the target; either can appear without the other; `x-amz-server-side-encryption` selects another target encryption ([ssec-spec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/specifying-s3-c-encryption.html), "Copy operation"). Recorded: SSE-C source to SSE-S3 target (L12913) and to SSE-C target (L12926).
- Copying an SSE-C source without the source headers: 400 `InvalidRequest`, "The object was stored using a form of Server Side Encryption. The correct parameters must be provided to retrieve the object." (L12950).
- Copy-source SSE-C headers on a source that is not SSE-C: `400 InvalidRequest`, "The encryption parameters are not applicable to this object.", the R15 GET text (S3SOL-06, recorded 2026-11-03; `testdata/parity/s3/S3SOL/06-copy.json`).
- Copy-source headers with the wrong key: `400 InvalidRequest`, "The provided encryption parameters did not match the ones used originally.", on CopyObject and UploadPartCopy alike (S3SOL-07, recorded 2026-11-03; `07-copy.json`, `07-upload-part-copy.json`). This is UploadPart's mismatch text (3.6), not the GET's 403 "correct secret key" that was guessed.
- UploadPartCopy "see CopyObject and UploadPart" ([API_UploadPartCopy](https://docs.aws.amazon.com/AmazonS3/latest/API/API_UploadPartCopy.html)). Its source answers as CopyObject's did in S3SOL-07; its other source and target rules are treated as CopyObject's source rules plus UploadPart's target rules (inferred).

### 3.6 Multipart uploads

- "If you request server-side encryption using a customer-provided encryption key (SSE-C) in your initiate multipart upload request, you must provide identical encryption information in each part upload" ([API_UploadPart](https://docs.aws.amazon.com/AmazonS3/latest/API/API_UploadPart.html)).
- Part without SSE-C on an SSE-C upload, and part with SSE-C on a non-SSE-C upload: both 400 `InvalidRequest`, "The multipart upload initiate requested encryption. Subsequent part requests must include the appropriate encryption parameters." (LS test_s3.snapshot.json:L13154, L13133). Part with another key: 400 `InvalidRequest`, "The provided encryption parameters did not match the ones used originally." (L13164).
- Complete needs no SSE-C headers ([ssec-spec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/specifying-s3-c-encryption.html): "You do not need to provide any encryption-specific headers in your CompleteMultipartUploadRequest"; recorded at L13083 for an upload without a checksum algorithm), except that the model says the key "is needed only when the object was created using a checksum algorithm" (`com.amazonaws.s3#CompleteMultipartUploadRequest$SSECustomerKey`).
- Recorded (S3SOL-08, recorded 2026-11-03; `testdata/parity/s3/S3SOL/08-*.json`), on SSE-C uploads created with `--checksum-algorithm CRC32` whose one part carried the key and a CRC32:
  - Complete without SSE-C headers: `400 InvalidRequest`, "The object was stored using a form of Server Side Encryption. Correct SSE-C request parameters are required for this request when specifying checksums for each part." (`08-no-key`).
  - Complete with another (valid) key: the same 400 and text, not UploadPart's mismatch text (`08-wrong-key`).
  - Complete with the right key: 200 with `ChecksumCRC32` `WcQMlQ==-1` and `ChecksumType` `COMPOSITE`, and the response **echoes** `x-amz-server-side-encryption-customer-algorithm: AES256` and the key MD5 (`08-right-key`), which `com.amazonaws.s3#CompleteMultipartUploadOutput` has no member for; understudy does not echo them yet.
  - `ListParts` without SSE-C headers: `400 InvalidRequest`, "The multipart upload initiate requested encryption. To retrieve Part Checksum data requests must include the appropriate encryption parameters." (`08-list-parts-no-key`), not the 200 that was guessed.
- `ListParts` works without SSE-C headers on an SSE-C upload without a checksum algorithm (L13042). `com.amazonaws.s3#ListPartsRequest$SSECustomerKey` carries the same "needed only when the object was created using a checksum algorithm" note as Complete (model), which S3SOL-08 bears out. Still UNVERIFIED: SSE-C headers sent on ListParts or Complete of an upload without a checksum algorithm (understudy judges them as UploadPart's), and ListParts with another key.

### 3.7 Versions

Each version can have its own key; reading version 1 with version 2's key is the 403 above (L13241-L13311; [ssec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/ServerSideEncryptionCustomerKeys.html): "each object version that you upload can have its own encryption key").

### 3.8 Checksums

SSE-C does not change the checksum rules of [s3.md](s3.md) 6: a raw SSE-C PUT without any checksum gets the default `CRC64NVME` (`FULL_OBJECT`), returned by HEAD with `x-amz-checksum-mode: ENABLED`, GET and `GetObjectAttributes` `Checksum` (LS test_s3.snapshot.json:L17518, L17534, L17551; request at LS test_s3.py:L12643-L12659). A client CRC32 is stored and returned (L12790, L13204). Parts carry their own checksum (L13012).

### 3.9 HTTPS

"You must use HTTPS when specifying SSE-C headers on your requests. Amazon S3 rejects any requests made over HTTP when using SSE-C." ([ssec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/ServerSideEncryptionCustomerKeys.html), "Considerations before using SSE-C").

- Recorded (S3SOL-02, recorded 2026-11-05; `testdata/parity/s3/S3SOL/02-*.json`): four plain-HTTP requests carrying a valid SSE-C trio each got `400 InvalidArgument`, "Requests specifying Server Side Encryption with Customer provided keys must be made over a secure connection.", with `ArgumentName` `x-amz-server-side-encryption` (as the 3.2 header refusals name it) and no other element but `RequestId` and `HostId`, `Content-Type: application/xml`: a PUT to a bucket that allows SSE-C (`02-put-http`); a GET of an SSE-C object with its key (`02-get-http`), so reads are refused too; a GET of a plain object with SSE-C headers (`02-get-plain-http`), so the scheme is checked before the stored encryption and the 3.3 "not applicable" 400 never comes; and a PUT to a new bucket (`02-put-blocked-http`), so the scheme is checked before the 4.3 403. That this bucket blocked SSE-C was not read in that session; it is inferred from a new bucket's default (4.1, `testdata/parity/s3/R06/01-get-get-bucket-encryption.json`) and S3SOL-01's 403 on one (4.3). The response also carried `Connection: close`, which understudy does not send. Still inferred: the same answer on HEAD (with no body), GetObjectAttributes, CopyObject (target and source), the multipart operations and ListParts, and the order against the 3.2 header checks (each request had a valid trio; understudy checks the trio first).
- The S3-compatible servers checked use the same text with another code, 400 `InvalidRequest` (emu: `minio/minio cmd/api-errors.go` `ErrInsecureSSECustomerRequest`, `treeverse/lakeFS pkg/gateway/errors/errors.go`), and a client that parses real AWS errors matches the SSE-KMS sibling "Requests specifying Server Side Encryption with AWS KMS managed keys must be made over a secure connection." (`dremio/dremio-oss sabot/kernel/src/main/java/com/dremio/exec/store/AbstractRecordReader.java`, handling `AmazonS3Exception`).
- Whether SDKs refuse SSE-C over HTTP client-side was not checked; botocore's `sse_md5` handler (3.12) has no such check.

### 3.10 Presigned URLs

"When creating a presigned URL, you must specify the algorithm by using the `x-amz-server-side-encryption-customer-algorithm` header in the signature calculation", and the client must send all three headers when it uses the URL ([ssec-spec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/specifying-s3-c-encryption.html), "Presigned URLs and SSE-C"). S3 refuses a presigned request carrying `x-amz-*` headers it did not sign with `403 AccessDenied` "There were headers present in the request which were not signed" and `HeadersNotSigned` ([s3.md](s3.md) 12; `internal/auth/verify.go` `checkAllAmzHeadersSigned`).

- Recorded (S3SOL-09, recorded 2026-11-03; `testdata/parity/s3/S3SOL/09-*.json`): a URL signing only `host`, used with the three SSE-C headers, gets `403 AccessDenied` "There were headers present in the request which were not signed" with `HeadersNotSigned` naming only `x-amz-server-side-encryption-customer-algorithm` (`09-presign-unsigned-ssec`). S3 names every unsigned header it refuses (error-matrix.md 5.4 T8), so the key and its MD5 are exempt from signing; that a URL signing the algorithm then serves the object with the key and MD5 unsigned is inferred. Without SSE-C headers the URL gets the 3.3 400 (`09-presign-no-ssec`).

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
- Rule shapes AWS documents: with a default (`ApplyServerSideEncryptionByDefault` + `BlockedEncryptionTypes`, the boto3 and Java examples in [ssec-block](https://docs.aws.amazon.com/AmazonS3/latest/userguide/blocking-unblocking-s3-c-encryption-gpb.html)), and **without** a default: the CLI examples there and `API_PutBucketEncryption` Example 4 send a `Rule` holding only `BlockedEncryptionTypes` ([API_PutBucketEncryption](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketEncryption.html)). `ApplyServerSideEncryptionByDefault` is "Required: No" ([API_ServerSideEncryptionRule](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ServerSideEncryptionRule.html)). `internal/service/s3/subresources.go` `checkEncryption` accepts both, and the empty rule (S3SOL-10, below).
- Recorded (S3SOL-10, recorded 2026-11-03; `testdata/parity/s3/S3SOL/10-*.json`): a rule holding only `BlockedEncryptionTypes` `NONE` is accepted (200), and `GetBucketEncryption` then reads, byte for byte, `<Rule><BucketKeyEnabled>false</BucketKeyEnabled><ApplyServerSideEncryptionByDefault><SSEAlgorithm>AES256</SSEAlgorithm></ApplyServerSideEncryptionByDefault><BlockedEncryptionTypes><EncryptionType>NONE</EncryptionType></BlockedEncryptionTypes></Rule>`: the default-free form gains the `AES256` default and `BucketKeyEnabled` `false` (`10-get-rule-only`). The full form put in 12.1 reads back the same (`10-get-full-form`). An SSE-C PutObject then succeeds (`10-put-ssec`). An empty `<Rule />` is **accepted** (200), not `MalformedXML`, and reads back as a new bucket's rule, `AES256` with `SSE-C` blocked (`10-put-empty-rule`, `10-get-after-empty-rule`).
- Recorded (S3SOL-11, recorded 2026-11-03; `11-*.json`): on a bucket set to `NONE`, a `PutBucketEncryption` whose rule omits `BlockedEncryptionTypes` and a `DeleteBucketEncryption` each put the block **back to `SSE-C`**: the GET reads `SSE-C` and an SSE-C PutObject is refused with the 4.3 403 (`11-get-after-omit`, `11-ssec-after-omit`, `11-get-after-delete`, `11-ssec-after-delete`). A Put without the element is a whole new rule, not a merge. In me-south-1 and me-central-1 the reset is inferred to go to their new-bucket state, `NONE` (4.1); unrecorded. A rule with only `BucketKeyEnabled` is inferred to be accepted like the empty rule (unrecorded).

### 4.3 What the block refuses

- "While SSE-C is blocked, any `PutObject`, `CopyObject`, `PostObject`, Multipart Upload, or replication request that specifies SSE-C encryption is rejected with an HTTP 403 `AccessDenied` error." ([ssec](https://docs.aws.amazon.com/AmazonS3/latest/userguide/ServerSideEncryptionCustomerKeys.html); [API_UploadPart](https://docs.aws.amazon.com/AmazonS3/latest/API/API_UploadPart.html) and [API_UploadPartCopy](https://docs.aws.amazon.com/AmazonS3/latest/API/API_UploadPartCopy.html) say the same for parts.)
- Reads are not blocked: existing SSE-C objects can still be read "with `GetObject` or `HeadObject` by providing the required SSE-C headers" ([ssec-block](https://docs.aws.amazon.com/AmazonS3/latest/userguide/blocking-unblocking-s3-c-encryption-gpb.html)); SSE-C headers on a read of a plain object in a blocked bucket get the 400 "not applicable" answer, not the 403 (`testdata/parity/s3/R15/x-ssec-on-non-ssec-read.json`). The bucket was created on 2026-10-14, so it is blocked; that is inferred, because R15 did not record its encryption configuration (section 2). Copy-source SSE-C headers are a read of the source, so a blocked target bucket refuses only target SSE-C headers (inferred).
- Recorded (S3SOL-01, recorded 2026-11-03; `testdata/parity/s3/S3SOL/01-*.json`): the refusal is `403 AccessDenied` with no element but `Message`, a specific sentence naming the caller, an action and the object: `User: arn:aws:sts::123456789012:assumed-role/AccountFullAccessRole/<session> is not authorized to perform: s3:PutObject on resource: "arn:aws:s3:::<bucket>/<key>" because this bucket has blocked upload requests that specify Server Side Encryption with Customer provided keys (SSE-C). Please specify a different server-side encryption type.` PutObject and CreateMultipartUpload name `s3:PutObject`; CopyObject names `s3:CopyObject`, which is not an IAM action. The 3.2 header validation runs first: algorithm `KMS` to a blocked bucket got `InvalidEncryptionAlgorithmError`, not the 403 (`01-put-bad-algo`). A copy with copy-source SSE-C headers only into a blocked bucket succeeds, 200 with `x-amz-server-side-encryption: AES256` (`01-copy-source-only`).
- Still UNVERIFIED: the action UploadPart and UploadPartCopy name (understudy names `s3:PutObject`, their IAM action), and the text for a caller outside the owner's account or an anonymous one (understudy renders the recorded form for any caller).

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
- The model has no `@range` on `com.amazonaws.s3#Days`, `#Years` or `#EventHoldDuration` members. "The maximum retention period is 100 years." ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html), retention limits). Recorded (S3SOL-13, recorded 2026-11-03; `testdata/parity/s3/S3SOL/13-*.json`): `Days` 0 gets `400 InvalidArgument`, "Default retention period must be a positive integer value.", `ArgumentName=Days`, `ArgumentValue=0`; `Days` 36501 and `Years` 101 get `400 InvalidArgument`, "Default retention period too large.", with `ArgumentName` `Days` or `Years` and the value. Not `MalformedXML`, as was guessed. `Years` 0 is inferred to get the "positive integer" text (unrecorded).

### 5.3 Default retention dating

- "Amazon S3 calculates a Retain Until Date for the object version by adding the specified duration to the object version's creation timestamp"; explicit per-object settings override the default ([lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html), "Retention periods"). Recorded: a PUT into a `GOVERNANCE`/`Days=1` bucket reports `ObjectLockMode=GOVERNANCE` and a retain-until date within 2 minutes of `LastModified` + 1 day, which the test asserts as "precise" (LS test_s3.snapshot.json:L9711; assertion LS test_s3.py:L9945-L9953).
- The PUT response does not echo the lock state (L9700, L9656): `com.amazonaws.s3#PutObjectOutput` has no lock members.
- Recorded (S3SOL-13, recorded 2026-11-03): `Years` are calendar years. A PUT into a `GOVERNANCE`/`Years=2` bucket at 2026-11-03T03:40:47Z got `x-amz-object-lock-retain-until-date: 2028-11-03T03:40:47.030Z` (`13-head`), 731 days later, as the span holds 2028-02-29; 365-day years would have given 2028-11-02. CopyObject into the same bucket dates the same way (S3SOL-16 `16-head`, `16-head-b`). The base instant has milliseconds and falls between the request's `X-Amz-Date` and its response `Date` (03:40:46 and 03:40:48 for `13-put`), with `Last-Modified` 03:40:48, so S3 dates from the start of the write while `Last-Modified` is its end (inferred from one sample). How 29 February moves by a year is UNVERIFIED. For contrast, the only earlier AWS statement is about event-hold durations checked against a policy condition key: "When you set a duration in years, Amazon S3 converts it to days for evaluation against the `s3:object-lock-event-hold-duration-days` condition key. Amazon S3 counts 1 year as 365 days and doesn't count leap years" ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html), "Bucket policy doesn't match a duration that is set in years").

## 6. Per-object lock state

### 6.1 Request headers

`x-amz-object-lock-mode` (`GOVERNANCE | COMPLIANCE`), `x-amz-object-lock-retain-until-date` ("Must be formatted as a timestamp parameter"), `x-amz-object-lock-legal-hold` (`ON | OFF`), and the variable-retention trio `x-amz-object-lock-event-hold` (`ON | OFF`), `x-amz-object-lock-event-hold-duration-days`, `-years` ([API_PutObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObject.html)). The same six members exist on `com.amazonaws.s3#PutObjectRequest`, `#CopyObjectRequest` and `#CreateMultipartUploadRequest` and nowhere else on the write path. `ObjectLockRetainUntilDate` has `@timestampFormat("date-time")`, so the header is ISO 8601, not an HTTP date (model).

### 6.2 Header validation (third-party-recorded, all 400 `InvalidArgument`)

| Request | Message | Elements | Source |
|---|---|---|---|
| mode without date | x-amz-object-lock-retain-until-date and x-amz-object-lock-mode must both be supplied | `ArgumentName=x-amz-object-lock-retain-until-date` | LS test_s3.snapshot.json:L9754, L17916 (the latter with a bad mode: pairing is checked first) |
| date without mode | same | `ArgumentName=x-amz-object-lock-mode` | L17927 |
| bad mode with a date | Unknown wormMode directive. | `ArgumentName=x-amz-object-lock-mode`, `ArgumentValue=BAD-VALUE` | L17938 (PutObject), L17950 (CreateMultipartUpload) |

- Recorded (S3SOL-17, recorded 2026-11-03; `testdata/parity/s3/S3SOL/17-*.json`): lock headers on a bucket without Object Lock get `400 InvalidRequest`, "Bucket is missing ObjectLockConfiguration", with no space in the last word, unlike the retention and legal-hold operations' "Bucket is missing Object Lock Configuration" (6.4). Recorded for a retention and for a legal hold `ON` on PutObject to a versioned bucket, a retention on PutObject to an unversioned one, and a legal hold `ON` on CreateMultipartUpload and CopyObject. A hold `OFF` is inferred to be refused the same way; whether this check runs before the 6.3 checksum rule is inferred (every S3SOL-17 step sent a checksum).
- Recorded (S3SOL-18, recorded 2026-11-03; `testdata/parity/s3/S3SOL/18-*.json`), each on PutObject to a lock bucket:

| Request | Status, code | Message | Elements | Step |
|---|---|---|---|---|
| past date `2020-07-01T00:00:00Z` | 400 `InvalidArgument` | The retain until date must be in the future! | `ArgumentName=x-amz-object-lock-retain-until-date`, `ArgumentValue=2020-07-01T00:00:00Z` | `18-past-date` |
| unparsable date `tomorrow` | 400 `InvalidArgument` | The retain until date must be provided in ISO 8601 format | `ArgumentName=x-amz-object-lock-retain-until-date`, `ArgumentValue=tomorrow` | `18-bad-date` |
| legal hold `MAYBE` | 400 `InvalidArgument` | Legal Hold must be either of 'ON' or 'OFF' | `ArgumentName=x-amz-object-lock-legal-hold`, `ArgumentValue=MAYBE` | `18-bad-hold` |
| date `2127-01-01T00:00:00Z`, over 100 years out | 200, accepted | | | `18-over-100-years` |

- Unlike the 6.4 element, the header's past date is named in its ISO 8601 form, not Java's. Whether S3 echoes the sent text or renders the parsed date is UNVERIFIED (the recorded value is the same either way); understudy renders the parsed date in UTC, with fractional seconds only when present. The 100-year limit of the docs (5.2) does not bound a retain-until date; PutObjectRetention is inferred to accept one too, and CopyObject and CreateMultipartUpload to answer as PutObject.

### 6.3 The Content-MD5 or checksum requirement

- "The `Content-MD5` or `x-amz-sdk-checksum-algorithm` header is required for any request to upload an object with a retention period configured using Object Lock." ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html), "Uploading objects to an Object Lock enabled bucket"; `com.amazonaws.s3#PutObjectRequest$ContentMD5` documentation). `x-amz-sdk-checksum-algorithm` must come with an `x-amz-checksum-*` or `x-amz-trailer` header or the request fails 400 ([API_PutObjectRetention](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObjectRetention.html), header description).
- Recorded (aws-recorded `testdata/parity/s3/R12/11-put-without-checksum.json`): a PutObject with `x-amz-content-sha256: UNSIGNED-PAYLOAD`, no lock headers, into a bucket whose default retention is `GOVERNANCE`/1 day → `400 InvalidRequest`, "Content-MD5 OR x-amz-checksum- HTTP header is required for Put Object requests with Object Lock parameters".
- Current SDKs and the CLI send a checksum on every PutObject by default ([s3.md](s3.md) 1 item 5), and Terraform forces CRC32 when any lock argument is set (9.3), so the refusal hits raw clients and older SDKs.
- Recorded (S3SOL-14, recorded 2026-11-03; `testdata/parity/s3/S3SOL/14-*.json`): a raw PutObject with only `x-amz-object-lock-legal-hold: ON` and no checksum is refused with the same `400 InvalidRequest` (`14-legal-hold-only`): the message's "Object Lock parameters", not the docs' "retention period", is the rule. A signed `x-amz-content-sha256` payload hash does not satisfy it (`14-retention-signed-sha256`), and the `UNSIGNED-PAYLOAD` control is refused too (`14-retention-unsigned`). Whether a hold set `OFF` alone triggers it is inferred (understudy: yes).
- Recorded (S3SOL-15, recorded 2026-11-03): CreateMultipartUpload with `x-amz-object-lock-mode` and `-retain-until-date` and no checksum succeeds (`15-create`); an UploadPart of that upload without a checksum is refused, `400 InvalidRequest`, "Content-MD5 OR x-amz-checksum- HTTP header is required for Put Part requests with Object Lock parameters" (`15-part-no-checksum`). A part of an upload under a bucket default retention, without lock headers on Create, is inferred to be refused the same way (unrecorded). Still UNVERIFIED: whether Create's lock headers land on the completed object; the recipe's Complete sent the refused part's empty ETag and got `InvalidPart` (`15-complete`), so HEAD found no object (`15-head`, 404), and the CRC32 fallback of 12.16 was not run.
- Recorded (S3SOL-16, recorded 2026-11-03; `testdata/parity/s3/S3SOL/16-*.json`): `CopyObject` into a bucket with a GOVERNANCE 2-year default retention, without a checksum, succeeds, and the new version gets the bucket default (HEAD: GOVERNANCE, a date two years out), whether the source was locked (`16-copy-locked-source`, `16-head`) or in a bucket without Object Lock (`16-copy-plain-source`, `16-head-b`); the source's own date is not carried (6.8).

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

- The past-date `ArgumentValue` is a Java `Date.toString()` in US Pacific time (the UTC instant 2020-01-01T00:00Z rendered as 2019-12-31 16:00 PST). Recorded (S3SOL-18, recorded 2026-11-03; `18-retention-past-summer`): in summer it is PDT, `Tue Jun 30 17:00:00 PDT 2020` for `2020-07-01T00:00:00Z`.
- Allowed: setting retention on an unprotected version (L9607); extending GOVERNANCE without bypass (L9871; "Any user with permissions to place an object retention period can extend a retention period", [lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html)); an empty `<Retention/>` with `x-amz-bypass-governance-retention: true` removes GOVERNANCE retention (L9677); after removal a new short retention can be set without bypass (L9683). Bypass also allows "shortening the retention period, or removing the Object Lock retention period by placing a new `PutObjectRetention` request with empty parameters" ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html), "Bypassing governance mode").
- COMPLIANCE: "its retention mode can't be changed, and its retention period can't be shortened" ([lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html)); recorded at L17884, L17894.
- Recorded (S3SOL-19, recorded 2026-11-03; `testdata/parity/s3/S3SOL/19-*.json`): GOVERNANCE to COMPLIANCE with a later date, without bypass, is refused with the 403 above (`19-upgrade`); HEAD still shows GOVERNANCE and its date (`19-head`), and a delete of the version gets the same 403 text (`19-delete-locked`, section 10). With bypass it is inferred to be allowed (unrecorded).
- Recorded (S3SOL-20, recorded 2026-11-03): `RetainUntilDate` without `Mode` is 400 `MalformedXML` with the text above, with or without bypass (`20-date-only`, `20-date-only-bypass`).
- Recorded (S3SOL-21, recorded 2026-11-03; `testdata/parity/s3/S3SOL/21-*.json`): after the retention was removed (empty `<Retention/>` with bypass), `GetObjectRetention` is 404 `NoSuchObjectLockConfiguration`, "The specified object does not have a ObjectLock configuration" (`21-get-retention`), and HEAD omits the mode and date headers (`21-head`).

### 6.5 PutObjectLegalHold and GetObjectLegalHold

- `PUT /{Key+}?legal-hold&versionId=`, body `LegalHold{Status}`; `GET` returns `LegalHold{Status}` ([API_PutObjectLegalHold](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObjectLegalHold.html), [API_GetObjectLegalHold](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectLegalHold.html)); `requestChecksumRequired: true` (model). No bypass header: "Legal holds can be freely placed and removed by any user who has the `s3:PutObjectLegalHold` permission" ([lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html)).
- Recorded: never set → 404 `NoSuchObjectLockConfiguration` (LS test_s3.snapshot.json:L9919); `ON` → GET `Status=ON` (L9950); missing bucket / key / bucket without Object Lock as in 6.4 (L9970, L9981, L9992, L10012); a request without a body on a bucket **without** Object Lock → `MalformedXML` (L10002), so the body is validated before the bucket's lock state.
- Recorded (S3SOL-21, recorded 2026-11-03): `GetObjectLegalHold` after `OFF` is 200 `Status=OFF` (`21-get-legal-hold`), and HEAD reports `x-amz-object-lock-legal-hold: OFF` (`21-head`, 6.7).
- Recorded (S3SOL-22, recorded 2026-11-03; `testdata/parity/s3/S3SOL/22-*.json`): for a missing key on a bucket without Object Lock, the lock state wins: PutObjectLegalHold and GetObjectRetention get 400 `InvalidRequest` "Bucket is missing Object Lock Configuration" (`22-hold-missing-key-nolock`, `22-retention-missing-key-nolock`). PutObjectLegalHold and GetObjectLegalHold of a delete-marker version get the 405 of 6.4 with `Method` and `ResourceType=DeleteMarker`, and the headers `x-amz-delete-marker`, `x-amz-version-id` (the marker's) and `Allow`, without the `Last-Modified` GetObject's 405 carries (`22-hold-delete-marker`, `22-get-hold-delete-marker`); the retention operations' 405 is inferred to match. None of the S3SOL error responses carries `x-amz-bucket-region`.
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
- Recorded (S3SOL-12, recorded 2026-11-03; `testdata/parity/s3/S3SOL/12-*.json`, with S3SOL-13 and -16): the `x-amz-object-lock-retain-until-date` header is ISO 8601 UTC with milliseconds only when they are not zero, `2026-11-03T04:40:14Z` for a whole-second date (`12-head`) and `2028-11-03T03:40:47.030Z` for a default-retention date (`13-head`; also `16-head`, `24-head`), the way Java's `Instant.toString()` writes a millisecond instant. `GetObjectRetention`'s `RetainUntilDate` always has them: `2026-11-03T04:40:14.000Z` (`12-get-retention`), `2026-11-04T03:43:27.296Z` (`24-get`). A version whose GOVERNANCE date has passed is deleted by version id without bypass, 204 (`12-delete-after-expiry`, 25 s after a 20 s retention).
- Still UNVERIFIED: whether a requested date's sub-second part is kept (the CLI sent `2026-11-03T04:40:14Z`, dropping the `.123` the recipe asked for; understudy keeps milliseconds), and the boundary instant equal to the date.

### 6.8 CopyObject does not carry lock state

A copy of a version under GOVERNANCE retention, or with a legal hold, has neither (LS test_s3.snapshot.json:L9562-L9577, L10202-L10217). Lock state on the target comes only from the copy's own headers or the target bucket's default (S3SOL-16, recorded 2026-11-03: the default applies, 6.3).

### 6.9 Variable retention (event holds), new since the LocalStack recordings

- Doc-only. An object stays protected while its event hold is `ON`; releasing the hold fixes the retain-until date at release time plus the duration; a `RetainUntilDate` given alongside is a minimum; while the hold is on, reads return "current time plus the duration"; decreasing the duration never moves the date earlier ([lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html), "Retain-until-date behavior with variable retention"). Durations are 1 to 36,500 days or 1 to 100 years ([lock-config](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-configure.html)). A bucket default can carry `DefaultEventHold` ([API_PutObjectLockConfiguration](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObjectLockConfiguration.html)).
- Refusals are documented as "a `400 Bad Request` response" without codes: no mode; missing or duplicate duration; a duration with `OFF`; `OFF` with no date; a duration out of range ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html), "Request that sets an event hold is rejected").
- Recorded (S3SOL-24, recorded 2026-11-03; `testdata/parity/s3/S3SOL/24-*.json`), PutObjectRetention on an unprotected version of a lock bucket:

| Body | Status, code | Message | Elements | Step |
|---|---|---|---|---|
| `Mode`, `EventHold=ON`, no duration | 400 `InvalidRequest` | EventHold:ON must be supplied with an EventHoldDuration. | | `24-no-duration` |
| `EventHold=ON`, `Days=1`, no `Mode` | 400 `MalformedXML` | the 6.4 text | | `24-no-mode` |
| `Days=1` and `Years=1` | 400 `MalformedXML` | the 6.4 text | | `24-both-units` |
| `Days=36501` | 400 `InvalidArgument` | Event hold duration must be at most 36500 days. | `ArgumentName=Days`, `ArgumentValue=36501` | `24-over-range` |
| `Mode=GOVERNANCE`, `EventHold=ON`, `Days=1` | 200, with `x-amz-version-id` | | | `24-ok` |

- While the hold is on, GetObjectRetention returns `Mode`, `RetainUntilDate` (the read time plus the duration, with milliseconds), `EventHold=ON` and `EventHoldDuration{Days=1}` (`24-get`), and HEAD sends `x-amz-object-lock-event-hold: ON`, `x-amz-object-lock-event-hold-duration-days: 1` and a retain-until date that moves with the read time (`24-head`). `EventHold=OFF` with `Mode` and no date releases it, 200 with `x-amz-version-id` (`24-release`); GetObjectRetention then returns the release time plus one day, fixed, with `EventHold=OFF` and no duration (`24-get-released`). understudy still refuses event holds with 501 (11.1, 11.7); the model's PutObjectRetentionOutput has no member for the `x-amz-version-id` header, and whether a plain retention put sends it is unrecorded.

## 7. Deletes under Object Lock

### 7.1 DeleteObject

- Without `versionId` (simple delete) on a locked version: a new delete marker, whatever the lock ([lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html), "How deletes work"). The status is `204`, with `DeleteMarker=true` (recorded at LS test_s3.snapshot.json:L9797 and L17866; the model binds DeleteObject to 204, section 8). The doc page says "`200 OK`" here (section 10). "Delete markers are not WORM-protected" ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html)).
- With `versionId` on a protected version: `403 AccessDenied`, "Access Denied because object protected by object lock." (L9639 GOVERNANCE, L17856 COMPLIANCE, L10044 legal hold). GOVERNANCE with `x-amz-bypass-governance-retention: true` → 204 (L9649); COMPLIANCE with bypass → 403 (L17874).
- After the retain-until date passes, a versioned delete without bypass succeeds (L17904; LS test_s3.py:L9871-L9883 deletes after sleeping past a 5-second retention, validated; S3SOL-12 `12-delete-after-expiry`, recorded 2026-11-03). The comparison runs on S3's clock; the boundary (equal instant) is UNVERIFIED: S3SOL-12 deleted 5 s after the date.

### 7.2 DeleteObjects

A locked version in the batch is reported per key, HTTP 200: `<Error><Key>..</Key><VersionId>..</VersionId><Code>AccessDenied</Code><Message>Access Denied because object protected by object lock.</Message></Error>` (LS test_s3.snapshot.json:L10054). Terraform depends on the per-key `AccessDenied` code (9.6).

### 7.3 The bypass header on a bucket without Object Lock

`DeleteObject` with `x-amz-bypass-governance-retention: true` **or `false`**, and `DeleteObjects` with `true`, on an unversioned bucket without Object Lock → `400 InvalidArgument`, "x-amz-bypass-governance-retention is only applicable to Object Lock enabled buckets.", `ArgumentName=x-amz-bypass-governance-retention` (LS test_s3_api.snapshot.json:L3535, L3546, L3557). The header's presence, not its value, triggers it.

- Recorded (S3SOL-23, recorded 2026-11-03; `testdata/parity/s3/S3SOL/23-*.json`): on a versioned bucket without Object Lock, `DeleteObject` with a `versionId` and `true` (`23-delete-version-bypass`) and `DeleteObjects` naming a version with `true` (`23-delete-objects-version-bypass`) get the same 400 with `ArgumentName`; on a lock bucket, `false` is no bypass: deleting a GOVERNANCE version gets the lock 403 (`23-lock-bucket-false`). Terraform's `aws_s3_object` sends the header with `true` on destroy with `force_destroy = true` (9.6).

### 7.4 Lifecycle, replication, notifications

- "A locked version of an object cannot be deleted by a S3 Lifecycle expiration policy"; lifecycle still places delete markers ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html), "Managing S3 Lifecycle policies with Object Lock"). understudy stores lifecycle rules without running them (`docs/coverage.md`), so this binds only when expiration is implemented.
- Replication of lock state: out of scope (replication is not implemented).
- `s3:ObjectRetention:Put` fires on explicit `PutObjectRetention` calls ([lock-manage](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html), "Configuring events and notifications"; [notif-types](https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-how-to-event-types-and-destinations.html)), with an `objectRetentionEventData{mode, retainUntilDate, eventHold?, eventHoldDuration?}` block ([notif-shape](https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-content-structure.html)). The value is in the model's `com.amazonaws.s3#Event` enum. The same page now says all events use a unified `eventVersion` (2.6 in its example; "Starting with version 2.4, Amazon S3 uses a single unified version for all event types"), which R17 confirmed in 2026 and `internal/service/s3/notify/record.go` now emits for every event type (s3.md 13.2).

## 8. Smithy model facts

- Service `com.amazonaws.s3#AmazonS3`: protocol `aws.protocols#restXml`, `smithy.api#xmlNamespace` `http://s3.amazonaws.com/doc/2006-03-01/`, `aws.auth#sigv4` name `s3`. No `smithy.api#endpoint` host prefix on any operation here; addressing is the endpoint rule set ([wire-rest.md](wire-rest.md)).
- HTTP bindings: `PutObjectRetention PUT /{Bucket}/{Key+}?retention` 200; `GetObjectRetention GET` same path; `PutObjectLegalHold PUT /{Bucket}/{Key+}?legal-hold` 200; `GetObjectLegalHold GET` same; `PutObjectLockConfiguration PUT /{Bucket}?object-lock` 200; `GetObjectLockConfiguration GET` same; `DeleteObject DELETE /{Bucket}/{Key+}?x-id=DeleteObject` 204; `DeleteObjects POST /{Bucket}?delete` 200.
- `aws.protocols#httpChecksum{requestAlgorithmMember: ChecksumAlgorithm, requestChecksumRequired: true}` on PutObjectRetention, PutObjectLegalHold, PutObjectLockConfiguration, DeleteObjects; `PutObject` has `requestAlgorithmMember` only.
- UNVERIFIED: what S3 answers when one of those four operations arrives with neither `Content-MD5` nor an `x-amz-checksum-*` header, or with a wrong one. SDKs always send one (6.4), so only raw clients meet the answer. understudy enforces `requestChecksumRequired` on no operation (`internal/codegen/optraits.go` keeps only the algorithm member), so it accepts such a request. The gap is not specific to Object Lock: the model carries `requestChecksumRequired` on 25 operations, and the coverage note of each one that is bound (18 of them, among them `PutBucketTagging`, `PutBucketPolicy` and `DeleteObjects`) says so (11.1). No item in section 12 records it yet.
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
- Read is `findObjectByBucketAndKey` with no SSE-C headers (L390), so against AWS the refresh of an SSE-C target hits the 3.3 refusal (inferred; the HEAD form is S3SOL-05's 400 with no body, recorded 2026-11-03). understudy must refuse the same way rather than make this resource work.

### 9.5 `aws_s3_bucket_server_side_encryption_configuration`

`rule.blocked_encryption_types` is Optional+Computed, a list of `EncryptionType` (TF bucket_server_side_encryption_configuration.go:L84-L92), sent only when non-empty (L332-L334) and set from the GET only when the element has values (L356-L360, L398-L404). Omitted in config: the computed value from the GET (`["SSE-C"]` on AWS, section 2) is accepted. Set to `["NONE"]`: an empty second plan needs the GET to return `NONE`, which it does (S3SOL-10, 4.2).

### 9.6 Destroy paths that touch Object Lock

- `emptyBucket(force)` deletes in pages with `DeleteObjects{Quiet: true}` plus bypass when forced (TF delete.go:L226-L235). For each per-key `AccessDenied` it puts the legal hold `OFF` and retries `DeleteObject` **without** bypass (L259-L289). A failed retry is collected (L288) and `deletePage` returns it (L298-L299), `emptyBucket` returns that error (L24-L31), and the bucket delete fails with "emptying S3 Bucket" (TF bucket.go:L1730-L1731). The bucket delete recurses (L1737) only after `emptyBucket` succeeds. So a version that has both a legal hold and GOVERNANCE retention fails the first `terraform destroy`, because the bypass-free retry gets the lock 403. The hold is `OFF` by then, so a second destroy deletes the version with DeleteObjects and bypass.
- `deleteAllObjectVersions` (single-object path) on `AccessDenied` with force: `HeadObject` the version, put the hold `OFF` if `ON`, delete again with bypass (L366-L401).
- `errCodeAccessDenied = "AccessDenied"` (TF errors.go:L14): any other code for a locked delete breaks both paths.

## 10. Sources that disagree

- `InvalidEncryptionAlgorithmError` text: [API_Error](https://docs.aws.amazon.com/AmazonS3/latest/API/API_Error.html) says "The encryption request you specified is not valid. The valid value is AES256."; AWS answered "The Encryption request you specified is not valid. Supported value: AES256." (3.2). The recording wins.
- Locked-delete text: the AWS SDK examples' Object Lock scenario shows `Access Denied` for a locked delete in its sample output (`awsdocs/aws-doc-sdk-examples scenarios/features/s3_object_lock/SPECIFICATION.md:L259` @ a352b04, date unknown; a .NET exception message), while the 2026-02-21 recordings say "Access Denied because object protected by object lock." (7.1). The recording wins; S3SOL-19 captures a fresh one as a side step.
- Unblocking rule shape: the API reference and CLI examples send a rule without `ApplyServerSideEncryptionByDefault` (4.2); understudy refused it. The docs won, and S3SOL-10 recorded AWS accepting it (4.2).
- Status of a simple delete under lock: [lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html), "How deletes work", says S3 "returns a `200 OK` response and inserts a delete marker". The 2026-02-21 recordings got 204 (LS test_s3.snapshot.json:L9797, L17866), and the model binds `DeleteObject` to 204 (7.1). The recordings win.
- Checksum requirement: the docs name `Content-MD5` or `x-amz-sdk-checksum-algorithm`; the recorded message names `Content-MD5` or an `x-amz-checksum-` header (6.3). Both are satisfied by a real `x-amz-checksum-*` header, which is what clients send.
- `docs/coverage.md` marks Put/Get/DeleteBucketEncryption `stored-only ... not applied`, but `internal/service/s3/sse.go` `bucketDefault` applies the default algorithm and KMS key to writes. The coverage note is stale. Its source is the `stored("the default encryption rule is")` registration in `internal/service/s3/subresources.go` `bindEncryption` (L523-L543), which generates those rows, so the fix belongs in that registration, followed by `just coverage-check`.

## 11. Implications for understudy

### 11.1 Operation coverage

Note texts are the exact `registry.Partial{Note: ...}` strings proposed; parts of today's notes that stay true are kept verbatim.

| Operation | Today | Target | Note |
|---|---|---|---|
| PutObject | partial | partial | an object ACL a bucket accepts is not stored; user metadata is stored as sent, without RFC 2047 decoding; chunk signatures are verified under sigv4 verify; Object Lock event-hold headers are refused with NotImplemented |
| GetObject | partial | partial | presigned URLs are verified and expire on the emulator clock under sigv4 verify |
| HeadObject | partial | partial | presigned URLs are verified and expire on the emulator clock under sigv4 verify |
| GetObjectAttributes | partial | full | |
| CopyObject | partial | partial | an object ACL a bucket accepts is not stored; errors after the copy starts are not sent as 200-with-error; Object Lock event-hold headers are refused with NotImplemented |
| CreateMultipartUpload | partial | partial | an object ACL a bucket accepts is not stored; no lifecycle abort date; Object Lock event-hold headers are refused with NotImplemented |
| UploadPart | partial | partial | chunk signatures are verified under sigv4 verify |
| UploadPartCopy | partial | partial | errors are answered before the response starts, so only a StatusOKError fault rule sends 200-with-error |
| CompleteMultipartUpload | partial | partial | errors are answered before the response starts, so only a StatusOKError fault rule sends 200-with-error |
| DeleteObject | partial | partial | no MFA delete; s3:BypassGovernanceRetention is not evaluated, so x-amz-bypass-governance-retention alone bypasses GOVERNANCE retention |
| DeleteObjects | partial | partial | no MFA delete; s3:BypassGovernanceRetention is not evaluated, so x-amz-bypass-governance-retention alone bypasses GOVERNANCE retention; a request with neither Content-MD5 nor an x-amz-checksum-* header is accepted, and so is one with a wrong checksum; what AWS answers is unrecorded |
| PutObjectRetention | unbound | partial | event holds (EventHold, EventHoldDuration) are refused with NotImplemented; s3:BypassGovernanceRetention is not evaluated; no s3:ObjectRetention:Put notification is sent; a request with neither Content-MD5 nor an x-amz-checksum-* header is accepted, and so is one with a wrong checksum; what AWS answers is unrecorded |
| GetObjectRetention | unbound | full | |
| PutObjectLegalHold | unbound | partial | a request with neither Content-MD5 nor an x-amz-checksum-* header is accepted, and so is one with a wrong checksum; what AWS answers is unrecorded |
| GetObjectLegalHold | unbound | full | |
| PutObjectLockConfiguration | stored-only | partial | DefaultEventHold is refused with NotImplemented; x-amz-bucket-object-lock-token is accepted and ignored; a request with neither Content-MD5 nor an x-amz-checksum-* header is accepted, and so is one with a wrong checksum; what AWS answers is unrecorded |
| GetObjectLockConfiguration | stored-only | full | |
| PutBucketEncryption | stored-only | partial | a request with neither Content-MD5 nor an x-amz-checksum-* header is accepted, and so is one with a wrong checksum; what AWS answers is unrecorded (the default rule is already applied, section 10; `BlockedEncryptionTypes` becomes enforced) |
| GetBucketEncryption, DeleteBucketEncryption | stored-only | full | |
| PutBucketNotificationConfiguration | partial | partial | append to today's note: "; s3:ObjectRetention:Put is stored but sends nothing" (until 7.4 is implemented) |
| UpdateObjectEncryption | unbound | unbound (501) | |

Run `just coverage-check` after re-marking and commit the regenerated `docs/coverage.md`.

### 11.2 Data model

- Object encryption becomes a closed sum of SSE-S3, SSE-KMS and SSE-C (today `Encryption{KMS *KMSEncryption}` with nil for SSE-S3, `internal/service/s3/sse.go`). The SSE-C variant holds a per-version random salt and HMAC-SHA256(salt, key), never the key (3.11). Bytes may be stored as sent, as SSE-KMS already does (`internal/service/s3/doc.go`); the observable contract is the key check, not ciphertext at rest.
- Responses echo the request's key MD5; nothing needs to store it.
- A multipart upload records its SSE choice, including the SSE-C fingerprint, so UploadPart can compare (3.6); ListParts needs none without a checksum algorithm (L13042) and needs the key with one (S3SOL-08, `s3.sse-c.complete-headers`).
- Per version lock state: `Retention` = none, or `{Mode, RetainUntil}` with `RetainUntil` an emulator-clock instant; `LegalHold` = never, on, or off (three states, since HEAD omits the header only for never, 6.7). Delete markers carry no lock state.
- Bucket: `ObjectLock` config (today stored, `internal/service/s3/state.go`) becomes applied at write time (5.3). Encryption config keeps `BlockedEncryptionTypes` as a separate field, which a Put omitting it, or a Delete, puts back to the new-bucket block (S3SOL-11, 4.2).
- New buckets get SSE-C blocked unless their region is `me-south-1` or `me-central-1` (4.1). The overlay default already carries the element (`codegen/overlays/s3.json`, `GetBucketEncryption` `notConfigured`).

### 11.3 Rules to implement

- `resolveSSE` (`internal/service/s3/sse.go:L77`): replace the unconditional `errSSECBlocked` with: 3.2 validation in the order conflict, algorithm, presence, key length, MD5 (only "conflict before algorithm" is recorded, 3.2; the rest of the order is inferred, because each recorded request had a single fault), then HTTPS (switch; before the block, S3SOL-02), then the bucket block (order against validation from S3SOL-01).
- Reads: GET, HEAD, GetObjectAttributes, copy source and UploadPartCopy source compare the request against the stored variant: SSE-C object without headers → 400 "stored using a form"; with headers on a non-SSE-C object → 400 "not applicable" (aws-recorded); wrong key → 403 on a read, and 400 `InvalidRequest` "did not match the ones used originally" on a copy source (S3SOL-07), including a wrong-length key with a matching MD5 (3.3; recorded for 44 bytes and, S3SOL-04, 10 bytes); a key without its MD5 passes with the MD5 computed (S3SOL-03, recorded on GET).
- ETags: random 32 hex for SSE-C single writes, SSE-C copy targets and SSE-C parts, and random 32 hex plus `-N` for an SSE-C multipart object, because AWS's value is not the MD5 of the part ETags (3.4). The random digits are inferred; no recording can pin a value AWS derives from state it never returns. Never derive an SSE-C ETag from content or from part ETags.
- Omit `x-amz-server-side-encryption` on every SSE-C response; echo the SSE-C pair on PutObject, CopyObject, CreateMultipartUpload, UploadPart, UploadPartCopy, GetObject, HeadObject; none on CompleteMultipartUpload and GetObjectAttributes (3.4).
- `checkEncryption` (`internal/service/s3/subresources.go`): accept a rule whose only member is `BlockedEncryptionTypes`, and an empty rule (4.2; S3SOL-10); keep `MalformedXML` for zero or two rules ([s3.md](s3.md) 4.2.2).
- PutObject, CopyObject, CreateMultipartUpload: 6.2 pairing check, then mode value, then (S3SOL-17) the bucket's lock state, then the 6.3 checksum rule for PutObject (and, per part, for UploadPart), then apply explicit settings or the bucket default with `RetainUntil = creation instant + Days`, or plus calendar `Years` (5.3).
- Retention and legal hold operations: 6.4/6.5 tables in order: body (`MalformedXML`), bucket exists, bucket has Object Lock, key/version exists, delete marker (405), date in the future. "Body before the bucket's lock state" is recorded (6.5), and so is lock state before key (S3SOL-22, recorded 2026-11-03; `s3.object-lock.check-order`). Body before bucket existence is inferred. Then come the change rules: extend always; shorten or remove GOVERNANCE only with bypass true; COMPLIANCE never shortened or changed to GOVERNANCE.
- Deletes: simple delete always adds a marker on a lock bucket; versioned delete of a version with a legal hold `ON`, or with `RetainUntil` after now (emulator clock) and COMPLIANCE, or GOVERNANCE without bypass true → 403 `AccessDenied` (7.1); DeleteObjects reports it per key with `VersionId` (7.2); the bypass header on any bucket without Object Lock, whatever its value → 400 (7.3; this is recorded on an unversioned bucket only, and a versioned bucket follows `s3.object-lock.bypass-without-lock`, S3SOL-23).
- `x-amz-bypass-governance-retention: false` on a lock bucket means no bypass (S3SOL-23, recorded 2026-11-03; 7.3).
- Event-hold headers and elements (`x-amz-object-lock-event-hold*`, `EventHold`, `EventHoldDuration`, `DefaultEventHold`): 501 `NotImplemented` with a `Header` element, in the style of `internal/service/s3/conditional.go` `errConditionNotImplemented`, naming understudy as the source.

### 11.4 Fidelity switches

| Name | Default | Label | Grounds | Record |
|---|---|---|---|---|
| `s3.sse-c.plain-http` | `refused`: 400 `InvalidArgument` "Requests specifying Server Side Encryption with Customer provided keys must be made over a secure connection." with `ArgumentName=x-amz-server-side-encryption` on the plain-HTTP listener, reads included (a plain object's too), before the bucket block; SSE-C works over `--tls`. Inferred: the operations other than PutObject and GetObject | aws-recorded | 3.9 | S3SOL-02 |
| `s3.sse-c.blocked-write` | 403 `AccessDenied` with the recorded "is not authorized to perform ... has blocked upload requests" text, checked after header validation | aws-recorded | 4.3 | S3SOL-01 |
| `s3.sse-c.missing-key-md5` | on writes, 400 `InvalidArgument` "Requests specifying Server Side Encryption with Customer provided keys must provide the client calculated MD5 of the secret key.", `ArgumentName=x-amz-server-side-encryption`; reads compute the MD5 and succeed | aws-recorded | 3.2 | S3SOL-03 |
| `s3.sse-c.key-encoding` | a key that is not base64 gets the MD5-mismatch text; an MD5 mismatch on reads gets it too; a short key with a matching MD5 on reads gets the 403 of 3.3, as the 44-byte key did | aws-recorded | 3.2, 3.3 | S3SOL-04 |
| `s3.sse-c.metadata-without-key` | HEAD 400 no body; GetObjectAttributes 400 `InvalidRequest` "stored using a form" text | aws-recorded | 3.3 | S3SOL-05 |
| `s3.sse-c.copy-source-not-applicable` | 400 `InvalidRequest` "The encryption parameters are not applicable to this object." | aws-recorded | 3.5 | S3SOL-06 |
| `s3.sse-c.copy-source-wrong-key` | 400 `InvalidRequest` "The provided encryption parameters did not match the ones used originally." | aws-recorded | 3.5 | S3SOL-07 |
| `s3.sse-c.complete-headers` | ListParts and Complete of an SSE-C upload with a checksum algorithm require the key, with the 3.6 texts (another key on Complete gets the same text); headers sent otherwise are checked like UploadPart's (inferred) | aws-recorded | 3.6 | S3SOL-08 |
| `s3.encryption.unblocked-read` | after `NONE`, GET returns `<BlockedEncryptionTypes><EncryptionType>NONE</EncryptionType></BlockedEncryptionTypes>`; a default-free rule reads back with the `AES256` default and `BucketKeyEnabled` `false` added; an empty `Rule` is accepted | aws-recorded | 4.2 | S3SOL-10 |
| `s3.encryption.blocked-types-omitted` | a Put without the element (the empty rule included), and DeleteBucketEncryption, put the block back to a new bucket's, `SSE-C` | aws-recorded | 4.2 | S3SOL-11 |
| `s3.object-lock.retain-until-format` | ISO 8601 UTC; bodies with milliseconds always (`2026-11-03T04:40:14.000Z`), the header with them only when not zero (`2026-11-03T04:40:14Z`, `2028-11-03T03:40:47.030Z`); a version past its date is deletable. Inferred: milliseconds of the request kept, deletable once now >= RetainUntil | aws-recorded | 6.7, 7.1 | S3SOL-12 |
| `s3.object-lock.default-years` | a year is a calendar year; Days 1..36500 and Years 1..100, else `InvalidArgument` "Default retention period must be a positive integer value." or "Default retention period too large." naming the element | aws-recorded | 5.2, 5.3 | S3SOL-13 |
| `s3.object-lock.checksum-scope` | PutObject with any lock header, a legal hold alone included, or into a default-retention bucket, and UploadPart of an upload created with lock headers (or under a default, inferred), with the part's own text; satisfied by `Content-MD5` or an `x-amz-checksum-*` header or trailer, not by a signed payload hash | aws-recorded | 6.3 | S3SOL-14, S3SOL-15 |
| `s3.object-lock.copy-default` | CopyObject into a default-retention bucket applies the default, no checksum needed | aws-recorded | 6.3, 6.8 | S3SOL-16 |
| `s3.object-lock.headers-without-lock` | 400 `InvalidRequest` "Bucket is missing ObjectLockConfiguration" | aws-recorded | 6.2 | S3SOL-17 |
| `s3.object-lock.header-texts` | 400 `InvalidArgument` with `ArgumentName` and `ArgumentValue`: past date "The retain until date must be in the future!" (the header's date in ISO 8601, the element's as Java `Date.toString()` in US Pacific time, PST or PDT), unparsable date "The retain until date must be provided in ISO 8601 format", bad legal hold "Legal Hold must be either of 'ON' or 'OFF'"; a date over 100 years ahead is accepted. Inferred: the same on CopyObject, CreateMultipartUpload and PutObjectRetention | aws-recorded | 6.2, 6.4 | S3SOL-18 |
| `s3.object-lock.mode-upgrade` | GOVERNANCE to COMPLIANCE without bypass refused with the lock 403 | aws-recorded | 6.4 | S3SOL-19 |
| `s3.object-lock.date-without-mode` | `MalformedXML` | aws-recorded | 6.4 | S3SOL-20 |
| `s3.object-lock.cleared-reads` | removed retention reads as 404 `NoSuchObjectLockConfiguration` and HEAD omits mode and date; a hold set `OFF` reads as 200 `Status=OFF` | aws-recorded | 6.4, 6.5 | S3SOL-21 |
| `s3.object-lock.check-order` | the bucket's lock state is checked before the key; legal hold on a delete marker is the 405 of 6.4, without `Last-Modified` | aws-recorded | 6.5 | S3SOL-22 |
| `s3.object-lock.bypass-without-lock` | the 7.3 400 on every bucket without Object Lock, versioned or not, with or without `versionId`; `false` on a lock bucket is no bypass | aws-recorded | 7.3 | S3SOL-23 |

The `Ref` of each switch is `docs/research/s3-ssec-object-lock.md#<section anchor>`; `s3.encryption.after-delete` moves to aws-recorded (section 2).

### 11.5 Limits and texts

Hand-code every text in 3.2, 3.3, 3.6, 5.1, 5.2, 6.2, 6.4, 7.1-7.3 exactly as quoted, with their elements and element order as recorded. Limits: SSE-C key exactly 32 bytes after base64 decoding; key MD5 is base64 of a 16-byte digest; default retention at most 36,500 days or 100 years, with no bound on a retain-until date (6.2); event-hold durations 1..36,500 days or 1..100 years (refused anyway, 11.3).

### 11.6 Terraform: apply plus an empty second plan

- `aws_s3_bucket` with `object_lock_enabled = true`: honour the create header, version the bucket, answer `GetObjectLockConfiguration` with `ObjectLockEnabled=Enabled` (9.1). Already true (`internal/service/s3/bucket.go` `newBucket`).
- `aws_s3_bucket_object_lock_configuration`: Get must echo `Mode` and exactly the one of `Days`/`Years` that was put; Delete's enabled-only Put must drop the rule (9.2). Already true; keep it when the config becomes applied.
- `aws_s3_object` with lock arguments: HEAD must return the three headers in a form that `time.RFC3339` re-renders to the configured string (whole seconds, `Z`), and the CRC32 the provider adds satisfies 6.3 (9.3). Retention updates send bypass when lowering; the 6.4 rules must accept that.
- Destroy: per-key `AccessDenied` from DeleteObjects and `AccessDenied` from DeleteObject on locked versions, legal-hold removal, then bypass deletes (9.6). A version with both a hold and GOVERNANCE retention must fail the first `terraform destroy` with the lock 403 and go on the second, as on AWS (9.6); understudy must not make the first one succeed.
- `aws_s3_bucket_server_side_encryption_configuration` with `blocked_encryption_types = ["NONE"]` needs S3SOL-10's read form, which understudy writes; without the attribute, the default GET form already round-trips. Dropping the attribute later sends a rule without the element, which re-blocks SSE-C (S3SOL-11).
- `aws_s3_object_copy` with `customer_key`: its refresh is expected to fail on AWS (inferred, 9.4; S3SOL-05 recorded the HEAD refusal 2026-11-03); understudy must fail it the same way.

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
B=$P-ssec; BB=$P-ssec-blk; BR=$P-ssec-rule; L=$P-olock; LY=$P-olock-y; LE=$P-olock-e; N=$P-nolock
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
for b in "$L" "$LY" "$LE"; do empty_versions "$b" --bypass-governance-retention; done   # $LE exists only after S3SOL-25
```

A `delete-object` that still fails names a version under COMPLIANCE (wait for its date) or an event hold (release it, S3SOL-24).

Index, priority first: S3SOL-01, -10, -05, -12, -23, -02, then the rest in order. S3SOL-25 to -28 were added after the 2026-11-03 session, for what it left open; a rerun may run 12.1, those cases and the teardown alone, since `scripts/distill-parity` rewrites only the steps `$REC` holds. Start `$REC/S3SOL/notes.md` from the committed `testdata/parity/s3/S3SOL/notes.md` and add the new lines to it (recording-kit.md 6).

### 12.2 S3SOL-01. SSE-C write to a bucket that blocks SSE-C

Settles 4.3 and `s3.sse-c.blocked-write`: the 403 text, and whether header validation runs first.

```sh
rec S3SOL/01-put aws s3api put-object --bucket "$BB" --key c --body "$REC/body10" --sse-customer-algorithm AES256 --sse-customer-key "$K1"
rec S3SOL/01-put-bad-algo aws s3api put-object --bucket "$BB" --key c --body "$REC/body10" --sse-customer-algorithm KMS --sse-customer-key "$K1"
rec S3SOL/01-mpu aws s3api create-multipart-upload --bucket "$BB" --key c --sse-customer-algorithm AES256 --sse-customer-key "$K1"
rec S3SOL/01-copy-target aws s3api copy-object --bucket "$BB" --key c --copy-source "$BB/p1" --sse-customer-algorithm AES256 --sse-customer-key "$K1"
rec S3SOL/01-copy-source-only aws s3api copy-object --bucket "$BB" --key c2 --copy-source "$B/c1" --copy-source-sse-customer-algorithm AES256 --copy-source-sse-customer-key "$K1"
```

Expected: 403 `AccessDenied` for the first four (text to record); the bad-algo step shows the order; the last copies (copy-source headers are a read). Recorded 2026-11-03: three 403s with the 4.3 text; the bad-algo step got `InvalidEncryptionAlgorithmError` (validation first), not a 403; the last copied (4.3). Cost under USD 0.01, 3 minutes. Teardown: `aws s3api list-multipart-uploads --bucket "$BB"` and abort any upload listed.

### 12.3 S3SOL-02. SSE-C over plain HTTP

Settles 3.9 and `s3.sse-c.plain-http`. The keys are public, so sending them in clear is harmless.

WARNING: `recurl` also sends the access key id, the signature and `x-amz-security-token` in clear on these four requests. The token cannot sign anything without the secret key, but run this step only on a network you trust, or skip it and mark it `n/a`.

```sh
recurl S3SOL/02-put-http -X PUT -H "x-amz-content-sha256: $U" "${C1[@]}" --data-binary @"$REC/body10" "http://$B.s3.$R.amazonaws.com/h1"
recurl S3SOL/02-get-http -H "x-amz-content-sha256: $E" "${C1[@]}" "http://$B.s3.$R.amazonaws.com/c1"
recurl S3SOL/02-get-plain-http -H "x-amz-content-sha256: $E" "${C1[@]}" "http://$B.s3.$R.amazonaws.com/p1"
recurl S3SOL/02-put-blocked-http -X PUT -H "x-amz-content-sha256: $U" "${C1[@]}" --data-binary @"$REC/body10" "http://$BB.s3.$R.amazonaws.com/h1"
```

Expected: 400 `InvalidRequest` with the "secure connection" text on all four (hypothesis); the last shows HTTPS versus block order. Not run in the 2026-11-03 session: it sends credentials over plain HTTP, so it is opt-in. Recorded 2026-11-05 (opted in): the text matched on all four; the code differed, `InvalidArgument` with `ArgumentName` `x-amz-server-side-encryption`; the plain object's GET got it too, and so did the blocking bucket's PUT, not the 403 (3.9; `testdata/parity/s3/S3SOL/notes.md`). Cost under USD 0.01, 2 minutes. Teardown: `aws s3api delete-object --bucket "$B" --key h1`.

### 12.4 S3SOL-03. SSE-C key without its MD5

Settles 3.2 and `s3.sse-c.missing-key-md5` (botocore cannot send this; raw requests only).

```sh
recurl S3SOL/03-put -X PUT -H "x-amz-content-sha256: $U" -H 'x-amz-server-side-encryption-customer-algorithm: AES256' \
  -H "x-amz-server-side-encryption-customer-key: $K1B" --data-binary @"$REC/body10" "$(url "$B" m3)"
recurl S3SOL/03-get -H "x-amz-content-sha256: $E" -H 'x-amz-server-side-encryption-customer-algorithm: AES256' \
  -H "x-amz-server-side-encryption-customer-key: $K1B" "$(url "$B" c1)"
```

Expected: 400 `InvalidArgument` naming the MD5 (text to record). Recorded 2026-11-03: the PUT matched (3.2); the GET differed, 200 with the computed MD5 echoed (3.2). Cost under USD 0.01, 2 minutes. Teardown: `aws s3api delete-object --bucket "$B" --key m3`.

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

Expected: 400 `InvalidArgument` with the key-size text, then key-size or 403, then the MD5-mismatch text. Recorded 2026-11-03: the first differed (the MD5-mismatch text, not the key-size one); the second was the 403; the third matched (3.2). Cost under USD 0.01, 2 minutes. Teardown: `aws s3api delete-object --bucket "$B" --key m4`.

### 12.6 S3SOL-05. Metadata reads of an SSE-C object without the key

Settles 3.3, 9.4 and `s3.sse-c.metadata-without-key`.

```sh
rec S3SOL/05-head aws s3api head-object --bucket "$B" --key c1
rec S3SOL/05-attrs aws s3api get-object-attributes --bucket "$B" --key c1 --object-attributes ETag ObjectSize
rec S3SOL/05-attrs-with-key aws s3api get-object-attributes --bucket "$B" --key c1 --object-attributes ETag ObjectSize \
  --sse-customer-algorithm AES256 --sse-customer-key "$K1B" --sse-customer-key-md5 "$K1M"
```

Expected: HEAD 400 without body; attributes 400 `InvalidRequest`; the control 200. Recorded 2026-11-03: matches (3.3). Cost under USD 0.01, 1 minute. Teardown: none.

### 12.7 S3SOL-06. Copy-source SSE-C headers on a plain source

Settles 3.5 and `s3.sse-c.copy-source-not-applicable`.

```sh
rec S3SOL/06-copy aws s3api copy-object --bucket "$B" --key t6 --copy-source "$B/p1" \
  --copy-source-sse-customer-algorithm AES256 --copy-source-sse-customer-key "$K1"
```

Expected: 400 `InvalidRequest` "not applicable" (hypothesis). Recorded 2026-11-03: matches (3.5). Cost under USD 0.01, 1 minute. Teardown: `aws s3api delete-object --bucket "$B" --key t6`.

### 12.8 S3SOL-07. Copy with the wrong source key

Settles 3.5 and `s3.sse-c.copy-source-wrong-key`.

```sh
rec S3SOL/07-copy aws s3api copy-object --bucket "$B" --key t7 --copy-source "$B/c1" \
  --copy-source-sse-customer-algorithm AES256 --copy-source-sse-customer-key "$K2"
UP7=$(aws s3api create-multipart-upload --bucket "$B" --key t7p --query UploadId --output text)
rec S3SOL/07-upload-part-copy aws s3api upload-part-copy --bucket "$B" --key t7p --upload-id "$UP7" --part-number 1 \
  --copy-source "$B/c1" --copy-source-sse-customer-algorithm AES256 --copy-source-sse-customer-key "$K2"
```

Expected: 403 `AccessDenied` "correct secret key" on both. Recorded 2026-11-03: differs on both, 400 `InvalidRequest` "The provided encryption parameters did not match the ones used originally." (3.5). Cost under USD 0.01, 1 minute. Teardown: `aws s3api delete-object --bucket "$B" --key t7`; `aws s3api abort-multipart-upload --bucket "$B" --key t7p --upload-id "$UP7"`.

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

Expected: list-parts 200 (hypothesis) or a refusal; refusal (text to record) for the first two completes, 200 for the third. Recorded 2026-11-03: list-parts was refused, both completes refused with one text, the third 200 with the key echoed (3.6). Cost under USD 0.01, 3 minutes. Teardown: `aws s3api list-multipart-uploads --bucket "$B"`, abort each listed upload; `aws s3 rm "s3://$B/" --recursive --exclude '*' --include 'm8*'`.

### 12.10 S3SOL-09. Presigned URL used with unsigned SSE-C headers

Settles 3.10. Redact the URL's signature before sharing (kit section 2).

```sh
URL=$(aws s3 presign "s3://$B/c1" --expires-in 300)
curl -sS -i --http1.1 "${C1[@]}" "$URL" > "$REC/S3SOL/09-presign-unsigned-ssec.http"
curl -sS -i --http1.1 "$URL" > "$REC/S3SOL/09-presign-no-ssec.http"
```

Expected: 403 `AccessDenied` with `HeadersNotSigned` listing the three headers; the control is the 400 "stored using a form". Recorded 2026-11-03: the 403 names only the algorithm header; the control matches (3.10). Cost under USD 0.01, 1 minute. Teardown: none.

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

Expected: the rule-only put accepted; record both GET bodies byte for byte; the empty rule refused with `MalformedXML` (if it is accepted, the last GET shows what it did). Recorded 2026-11-03: as expected but for the empty rule, which was accepted and reset the block to `SSE-C` (4.2). Cost under USD 0.01, 2 minutes. Teardown: kept for S3SOL-11; removed by 12.1.

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

Expected: keep `NONE` (hypothesis) or fall back to `SSE-C`; the SSE-C puts confirm. Recorded 2026-11-03: falls back to `SSE-C` on both paths, and both SSE-C puts got the 4.3 403 (4.2). Cost under USD 0.01, 2 minutes. Teardown: 12.1.

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

Expected: record the raw `x-amz-object-lock-retain-until-date` header and the XML `RetainUntilDate` against the sent `.123Z`; the expired delete succeeds. Recorded 2026-11-03: the CLI sent the date without its `.123`, so precision is unrecorded; the header had no fraction, the XML `.000Z`, and the expired delete succeeded (6.7). To record precision, send the date with `recurl`. Cost under USD 0.01, 2 minutes. Teardown: 12.1.

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

Expected: the three refusals (texts to record); the HEAD date minus `Last-Modified` is 730 days (365-day years) or 731 (calendar years; the span includes 2028-02-29). Recorded 2026-11-03: the three refusals are `InvalidArgument`, not `MalformedXML` (5.2); 731 days, calendar years (5.3). Cost under USD 0.01, 2 minutes. Teardown: 12.1 (GOVERNANCE, bypass).

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

Expected: the third is the R12 refusal (control); record the first two. Recorded 2026-11-03: all three refused with the R12 text (6.3). Cost under USD 0.01, 2 minutes. Teardown: 12.1.

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

If the part is refused, upload it with `aws s3api upload-part ... --checksum-algorithm CRC32` and complete with that ETag. Expected: record whether the part is refused and whether HEAD shows the Create's lock settings. Recorded 2026-11-03: the part was refused (6.3); the fallback was not run, so the Complete (empty ETag) failed and whether HEAD shows Create's settings stays UNVERIFIED. Re-run with the fallback. Cost under USD 0.01, 3 minutes. Teardown: abort any open upload (`aws s3api abort-multipart-upload --bucket "$L" --key m15 --upload-id "$UP"`); objects by 12.1.

### 12.17 S3SOL-16. CopyObject into a bucket with default retention

Settles 6.3, 6.8 and `s3.object-lock.copy-default`. Run after S3SOL-12 and S3SOL-13.

```sh
rec S3SOL/16-copy-locked-source aws s3api copy-object --bucket "$LY" --key c16 --copy-source "$L/d12"
rec S3SOL/16-head aws s3api head-object --bucket "$LY" --key c16
rec S3SOL/16-copy-plain-source aws s3api copy-object --bucket "$LY" --key c16b --copy-source "$N/n1"
rec S3SOL/16-head-b aws s3api head-object --bucket "$LY" --key c16b
```

Expected: both targets show GOVERNANCE and the 2-year default, not the source's date. Recorded 2026-11-03: matches, with no checksum sent (6.3). Cost under USD 0.01, 2 minutes. Teardown: 12.1.

### 12.18 S3SOL-17. Lock headers on a bucket without Object Lock

Settles 6.2 and `s3.object-lock.headers-without-lock`.

```sh
rec S3SOL/17-versioned-mode aws s3api put-object --bucket "$N" --key l17 --body "$REC/body10" --object-lock-mode GOVERNANCE --object-lock-retain-until-date "$(soon 3600)"
rec S3SOL/17-versioned-hold aws s3api put-object --bucket "$N" --key l17 --body "$REC/body10" --object-lock-legal-hold-status ON
rec S3SOL/17-unversioned-mode aws s3api put-object --bucket "$B" --key l17 --body "$REC/body10" --object-lock-mode GOVERNANCE --object-lock-retain-until-date "$(soon 3600)"
rec S3SOL/17-mpu-hold aws s3api create-multipart-upload --bucket "$N" --key l17 --object-lock-legal-hold-status ON
rec S3SOL/17-copy-hold aws s3api copy-object --bucket "$N" --key l17 --copy-source "$N/n1" --object-lock-legal-hold-status ON
```

Expected: 400 `InvalidRequest` "Bucket is missing Object Lock Configuration" each (hypothesis). Recorded 2026-11-03: 400 `InvalidRequest` each, with the text "Bucket is missing ObjectLockConfiguration" (6.2). Cost under USD 0.01, 2 minutes. Teardown: abort any upload on `$N`; objects by 12.1.

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

Expected: record each status, code, text and `ArgumentName`/`ArgumentValue`; the last should read `Tue Jun 30 17:00:00 PDT 2020` if the PST rendering holds. Recorded 2026-11-03: as expected for the last; the header past date names `2020-07-01T00:00:00Z` in ISO 8601, `tomorrow` and `MAYBE` get their own texts, and the 2127 date is accepted (6.2). Cost under USD 0.01, 2 minutes. Teardown: 12.1 (an accepted 2127 object is GOVERNANCE, removable with bypass).

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

Expected: refusal with the lock 403 (hypothesis); the delete step re-records the current locked-delete text (section 10). Recorded 2026-11-03: matches; HEAD still shows GOVERNANCE, and the delete got the 403 with the section 10 text (6.4). Cost under USD 0.01, 4 minutes including the wait. Teardown: after the wait, 12.1.

### 12.21 S3SOL-20. RetainUntilDate without Mode

Settles 6.4 and `s3.object-lock.date-without-mode`.

```sh
rec S3SOL/20-date-only aws s3api put-object-retention --bucket "$L" --key d12 --retention "{\"RetainUntilDate\":\"$(soon 7200)\"}"
rec S3SOL/20-date-only-bypass aws s3api put-object-retention --bucket "$L" --key d12 --bypass-governance-retention \
  --retention "{\"RetainUntilDate\":\"$(soon 7200)\"}"
```

Expected: `MalformedXML` both (hypothesis). Recorded 2026-11-03: matches (6.4). Cost under USD 0.01, 1 minute. Teardown: 12.1.

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

Expected: 404 `NoSuchObjectLockConfiguration`, 200 `Status=OFF`, HEAD with only the legal-hold header `OFF` (hypotheses). Recorded 2026-11-03: matches (6.4, 6.5). Cost under USD 0.01, 1 minute. Teardown: 12.1.

### 12.23 S3SOL-22. Error order for legal holds

Settles 6.5 and `s3.object-lock.check-order`. Run after S3SOL-16, -18 and -20 (it puts a delete marker on `d12`).

```sh
rec S3SOL/22-hold-missing-key-nolock aws s3api put-object-legal-hold --bucket "$N" --key missing --legal-hold Status=ON
rec S3SOL/22-retention-missing-key-nolock aws s3api get-object-retention --bucket "$N" --key missing
DM=$(aws s3api delete-object --bucket "$L" --key d12 --query VersionId --output text)
rec S3SOL/22-hold-delete-marker aws s3api put-object-legal-hold --bucket "$L" --key d12 --version-id "$DM" --legal-hold Status=ON
rec S3SOL/22-get-hold-delete-marker aws s3api get-object-legal-hold --bucket "$L" --key d12 --version-id "$DM"
```

Expected: `InvalidRequest` "Bucket is missing..." for the first two (hypothesis), 405 for the last two. Recorded 2026-11-03: matches; the 405s carry `x-amz-delete-marker`, `x-amz-version-id` and `Allow` but no `Last-Modified` (6.5). Cost under USD 0.01, 1 minute. Teardown: 12.1.

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

Expected: 400 `InvalidArgument` "x-amz-bypass-governance-retention is only applicable to Object Lock enabled buckets." on the first two (hypothesis); the lock 403 on the third (`false` is no bypass). If the first deletes the version, the second records a missing version; note it. Recorded 2026-11-03: matches; the first deleted nothing (7.3). Cost under USD 0.01, 1 minute. Teardown: 12.1.

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

Expected: four 400s (codes and texts to record), then the hold, its read forms, and the release fixing a date one day out. Recorded 2026-11-03: as expected; the 400s are one `InvalidRequest`, two `MalformedXML` and one `InvalidArgument` (6.9). Cost under USD 0.01, 2 minutes. Teardown: the release step must succeed before 12.1 can delete `e24` with bypass.

### 12.26 S3SOL-25. Event holds: header path, Years bounds, OFF with a duration, decreasing, bucket default

Settles the event-hold behavior S3SOL-24 left unrecorded (6.9) and so the refusals understudy answers with 501 (11.1, 11.3, 11.7): the PutObject header path, the `Years` bounds, a duration with `EventHold` `OFF`, `OFF` on a version never held, decreasing a duration while the hold is on, and `DefaultEventHold`. Needs the CLI of S3SOL-24 for the body steps. Every hold is GOVERNANCE, and the case releases each one, so 12.1's teardown can delete the versions with bypass.

```sh
hp() {  # hp <step> <key> <header args...>: PutObject to $L with these lock headers and a Content-MD5
  local step=$1 key=$2; shift 2
  recurl "S3SOL/25-$step" -X PUT -H "x-amz-content-sha256: $U" -H "Content-MD5: $MD" "$@" \
    --data-binary @"$REC/body10" "$(url "$L" "$key")"
}
EH='x-amz-object-lock-event-hold'; MODE=(-H 'x-amz-object-lock-mode: GOVERNANCE')
hp hdr-no-duration   h25 "${MODE[@]}" -H "$EH: ON"
hp hdr-no-mode       h25 -H "$EH: ON" -H "$EH-duration-days: 1"
hp hdr-both-units    h25 "${MODE[@]}" -H "$EH: ON" -H "$EH-duration-days: 1" -H "$EH-duration-years: 1"
hp hdr-days-36501    h25 "${MODE[@]}" -H "$EH: ON" -H "$EH-duration-days: 36501"
hp hdr-years-0       h25 "${MODE[@]}" -H "$EH: ON" -H "$EH-duration-years: 0"
hp hdr-years-101     h25 "${MODE[@]}" -H "$EH: ON" -H "$EH-duration-years: 101"
hp hdr-off-duration  h25 "${MODE[@]}" -H "$EH: OFF" -H "$EH-duration-days: 1"
hp hdr-ok            h25ok "${MODE[@]}" -H "$EH: ON" -H "$EH-duration-days: 1"
rec S3SOL/25-hdr-head aws s3api head-object --bucket "$L" --key h25ok

aws s3api put-object --bucket "$L" --key e25 --body "$REC/body10"
pr25() { rec "S3SOL/25-$1" aws s3api put-object-retention --bucket "$L" --key "$2" --retention "$3" "${@:4}"; }
pr25 days-0         e25 '{"Mode":"GOVERNANCE","EventHold":"ON","EventHoldDuration":{"Days":0}}'
pr25 years-0        e25 '{"Mode":"GOVERNANCE","EventHold":"ON","EventHoldDuration":{"Years":0}}'
pr25 years-101      e25 '{"Mode":"GOVERNANCE","EventHold":"ON","EventHoldDuration":{"Years":101}}'
pr25 off-duration   e25 '{"Mode":"GOVERNANCE","EventHold":"OFF","EventHoldDuration":{"Days":1}}'
pr25 off-never-held e25 '{"Mode":"GOVERNANCE","EventHold":"OFF"}'
aws s3api put-object --bucket "$L" --key y25 --body "$REC/body10"
pr25 years-1        y25 '{"Mode":"GOVERNANCE","EventHold":"ON","EventHoldDuration":{"Years":1}}'
rec S3SOL/25-years-1-get aws s3api get-object-retention --bucket "$L" --key y25
aws s3api put-object --bucket "$L" --key d25 --body "$REC/body10"
pr25 decrease-start d25 '{"Mode":"GOVERNANCE","EventHold":"ON","EventHoldDuration":{"Days":2}}'
pr25 decrease       d25 '{"Mode":"GOVERNANCE","EventHold":"ON","EventHoldDuration":{"Days":1}}'
rec S3SOL/25-decrease-get aws s3api get-object-retention --bucket "$L" --key d25
pr25 decrease-bypass d25 '{"Mode":"GOVERNANCE","EventHold":"ON","EventHoldDuration":{"Days":1}}' --bypass-governance-retention
rec S3SOL/25-decrease-bypass-get aws s3api get-object-retention --bucket "$L" --key d25

mb "$LE" --object-lock-enabled-for-bucket
lc() { rec "S3SOL/25-$1" aws s3api put-object-lock-configuration --bucket "$LE" --object-lock-configuration "$2"; }
lc default-hold-only '{"ObjectLockEnabled":"Enabled","Rule":{"DefaultRetention":{"Mode":"GOVERNANCE","DefaultEventHold":{"Days":1}}}}'
lc default-hold-days '{"ObjectLockEnabled":"Enabled","Rule":{"DefaultRetention":{"Mode":"GOVERNANCE","Days":1,"DefaultEventHold":{"Days":1}}}}'
lc default-hold-years-101 '{"ObjectLockEnabled":"Enabled","Rule":{"DefaultRetention":{"Mode":"GOVERNANCE","DefaultEventHold":{"Years":101}}}}'
rec S3SOL/25-default-get aws s3api get-object-lock-configuration --bucket "$LE"
rec S3SOL/25-default-put aws s3api put-object --bucket "$LE" --key o25 --body "$REC/body10"
rec S3SOL/25-default-head aws s3api head-object --bucket "$LE" --key o25
rec S3SOL/25-default-get-retention aws s3api get-object-retention --bucket "$LE" --key o25
```

Expected, per step (none is recorded; the docs give only "a `400 Bad Request` response", 6.9). The header steps against S3SOL-24's body answers: `hdr-no-duration` its `InvalidRequest` "EventHold:ON must be supplied with an EventHoldDuration.", `hdr-days-36501` its `InvalidArgument` "Event hold duration must be at most 36500 days." (with the header's name in `ArgumentName`, or `Days`), and `hdr-no-mode` and `hdr-both-units` a header-path refusal where the body got `MalformedXML`; record each status, code, text and element. `hdr-years-0`, `hdr-years-101`, `years-0` and `years-101` settle the `Years` bounds (1 to 100, 6.9) and their texts; `days-0` the lower `Days` bound. `hdr-off-duration` and `off-duration` settle a duration with `OFF`; `off-never-held` settles `OFF` with no date on a version that never had a hold (the docs refuse "`OFF` with no date"; S3SOL-24's release was of a held version). `hdr-ok` and `hdr-head` show whether the header path places a hold and HEAD reports it as S3SOL-24's `24-head` did; `years-1-get` shows a `Years` duration's read form. `decrease` settles whether a shorter duration is accepted while the hold is on, without bypass and then with it, and `decrease-get`, `decrease-bypass-get` whether GetObjectRetention then reports the read time plus 1 day or keeps 2 ("decreasing the duration never moves the date earlier", 6.9). The `lc` steps settle whether `DefaultEventHold` is accepted alone or only beside `Days` or `Years`, its range text, and its GET form, which is also the configuration in force for the put (the last `lc` step S3 accepted); `default-put` (the CLI sends its default checksum, so the 6.3 rule is met), `default-head` and `default-get-retention` whether a new version is held by the default, and for how long. Each answer replaces the 501 understudy gives that input today (11.3). Cost under USD 0.01, 4 minutes.

Teardown, before 12.1's: release every hold this case placed, then 12.1 deletes the versions with bypass (`$LE` is in its loop).

```sh
for k in h25ok y25 d25; do
  aws s3api put-object-retention --bucket "$L" --key "$k" --retention '{"Mode":"GOVERNANCE","EventHold":"OFF"}'
done
aws s3api put-object-retention --bucket "$LE" --key o25 --retention '{"Mode":"GOVERNANCE","EventHold":"OFF"}'
```

A release that fails names a version whose hold did not take, which needs nothing.

### 12.27 S3SOL-26. `x-amz-version-id` on a plain retention or legal-hold put

Settles the last line of 6.9: the model's `PutObjectRetentionOutput` and `PutObjectLegalHoldOutput` carry no version id, yet S3 sent `x-amz-version-id` on the event-hold puts of S3SOL-24 (`24-ok`, `24-release`). These are plain puts, by key and by version id.

```sh
aws s3api put-object --bucket "$L" --key v26 --body "$REC/body10"
V26=$(aws s3api head-object --bucket "$L" --key v26 --query VersionId --output text)
rec S3SOL/26-retention aws s3api put-object-retention --bucket "$L" --key v26 \
  --retention "{\"Mode\":\"GOVERNANCE\",\"RetainUntilDate\":\"$(soon 3600)\"}"
rec S3SOL/26-retention-version aws s3api put-object-retention --bucket "$L" --key v26 --version-id "$V26" \
  --retention "{\"Mode\":\"GOVERNANCE\",\"RetainUntilDate\":\"$(soon 7200)\"}"
rec S3SOL/26-legal-hold-on aws s3api put-object-legal-hold --bucket "$L" --key v26 --legal-hold Status=ON
rec S3SOL/26-legal-hold-off-version aws s3api put-object-legal-hold --bucket "$L" --key v26 --version-id "$V26" --legal-hold Status=OFF
```

Expected: the response headers of each step in its `.http` debug log; for each, whether `x-amz-version-id` is present and equals `$V26`. Present on all four means understudy's PutObjectRetention and PutObjectLegalHold answers need the header, which the model cannot carry (an overlay or a hand-written header); absent means it is particular to event holds. Cost under USD 0.01, 1 minute. Teardown: 12.1 (GOVERNANCE, bypass; the hold is off).

### 12.28 S3SOL-27. Header retain-until dates in other ISO 8601 forms

Settles the 6.2 UNVERIFIED line: whether the `ArgumentValue` of a refused header date is the text as sent or the parsed date rendered again. S3SOL-18 sent `2020-07-01T00:00:00Z`, which reads the same either way. These past dates differ from their rendering; `recurl` sends them as written (the CLI would normalize them).

```sh
pd() {  # pd <step> <retain-until date>: PutObject to $L with GOVERNANCE and that past date
  recurl "S3SOL/27-$1" -X PUT -H "x-amz-content-sha256: $U" -H "Content-MD5: $MD" -H 'x-amz-object-lock-mode: GOVERNANCE' \
    -H "x-amz-object-lock-retain-until-date: $2" --data-binary @"$REC/body10" "$(url "$L" p27)"
}
pd millis-zero   2020-07-01T00:00:00.000Z
pd millis        2020-07-01T00:00:00.5Z
pd offset        2020-07-01T02:00:00+02:00
pd no-seconds    2020-07-01T00:00Z
```

Expected: each 400 `InvalidArgument` "The retain until date must be in the future!" with `ArgumentName=x-amz-object-lock-retain-until-date`. The `ArgumentValue` decides: the four texts as sent mean S3 echoes the header, and understudy's switch `s3.object-lock.header-texts` should echo it too; `2020-07-01T00:00:00Z` for the first, third and fourth (and `2020-07-01T00:00:00.500Z` for the second) mean S3 renders the parsed date, as understudy does now (6.2). A step answered "must be provided in ISO 8601 format" instead shows a form S3 does not parse (likely `no-seconds`): note it. Cost under USD 0.01, 1 minute. Teardown: none (nothing is created).

### 12.29 S3SOL-28. Bypass by a principal without `s3:BypassGovernanceRetention`

Settles 6.6 and the DeleteObject, DeleteObjects and PutObjectRetention coverage notes of 11.1, which say understudy does not evaluate `s3:BypassGovernanceRetention`: what S3 answers a bypass header from a caller whose policy allows the delete but not the bypass. The role trusts the account, and its one inline policy omits the bypass action.

```sh
TRUST28=$(jq -nc --arg a "arn:aws:iam::$A:root" '{Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{AWS:$a},Action:"sts:AssumeRole"}]}')
aws iam create-role --role-name "$P-s3sol-nobypass" --assume-role-policy-document "$TRUST28" >/dev/null
aws iam put-role-policy --role-name "$P-s3sol-nobypass" --policy-name p --policy-document "$(jq -nc --arg o "arn:aws:s3:::$L/*" \
  '{Version:"2012-10-17",Statement:[{Effect:"Allow",Action:["s3:DeleteObject","s3:DeleteObjectVersion","s3:GetObject","s3:GetObjectVersion","s3:PutObjectRetention"],Resource:$o}]}')"
sleep 15   # role propagation
aws s3api put-object --bucket "$L" --key b28 --body "$REC/body10" --object-lock-mode GOVERNANCE --object-lock-retain-until-date "$(soon 3600)"
V28=$(aws s3api head-object --bucket "$L" --key b28 --query VersionId --output text)
read -r k28 s28 t28 <<<"$(aws sts assume-role --role-arn "arn:aws:iam::$A:role/$P-s3sol-nobypass" --role-session-name s3sol \
  --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text)"
as28() {  # as28 <aws args...>: the AWS CLI as the role session; refuses if assume-role failed
  [ -n "$t28" ] || { echo "as28: assume-role failed; nothing was sent" >&2; return 1; }
  env -u AWS_PROFILE AWS_ACCESS_KEY_ID="$k28" AWS_SECRET_ACCESS_KEY="$s28" AWS_SESSION_TOKEN="$t28" aws "$@"
}
rec S3SOL/28-delete-bypass as28 s3api delete-object --bucket "$L" --key b28 --version-id "$V28" --bypass-governance-retention
rec S3SOL/28-delete-no-bypass as28 s3api delete-object --bucket "$L" --key b28 --version-id "$V28"
rec S3SOL/28-retention-remove-bypass as28 s3api put-object-retention --bucket "$L" --key b28 --version-id "$V28" \
  --retention '{}' --bypass-governance-retention
rec S3SOL/28-head aws s3api head-object --bucket "$L" --key b28 --version-id "$V28"
```

Expected: `delete-no-bypass` is the control, the lock 403 "Access Denied because object protected by object lock." (7.1). `delete-bypass` and `retention-remove-bypass` are refused too, since the caller lacks the bypass permission ([lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html)); record each status, code and text: the lock text, or an IAM denial naming `s3:BypassGovernanceRetention`, which decides the text understudy gives once it evaluates the permission. `head` shows the version still under GOVERNANCE and its date. A `delete-bypass` that deletes the version (204) means S3 does not check the permission on this path: note it, and `head` then answers 404. Cost under USD 0.01 (IAM is free), 2 minutes. Teardown: `aws iam delete-role-policy --role-name "$P-s3sol-nobypass" --policy-name p; aws iam delete-role --role-name "$P-s3sol-nobypass"`; the object by 12.1.
