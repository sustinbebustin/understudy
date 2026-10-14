# R12

- Date: 2026-10-14T07:02Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

Understudy paths are relative to /home/austin/dev/local-aws-stack. Byte-level XML notes are listed last.

Step 1, ranges on `0123456789`:
- multi `bytes=0-1,4-5`: matches (AWS 200, whole body; get.go:173 ignores a range with a comma)
- `bytes=abc`, `bytes=5-2`, `items=0-1`, `bytes=0-1,`, empty `Range:`: matches (AWS 200, whole body, no Content-Range; get.go:172-206 rangeIgnored)
- `bytes=-0`, `bytes=100-`: status/code/message match (416 InvalidRange "The requested range is not satisfiable"); differs: element order. AWS `<RangeRequested>` then `<ActualObjectSize>`; understudy writes ActualObjectSize first (get.go:118-119)
- `bytes=0-`: matches (206, `Content-Range: bytes 0-9/10`)

Step 2, partNumber on a single-part object:
- get part 1: matches (206, `Content-Range: bytes 0-9/10`, no x-amz-mp-parts-count, full-object CRC64NVME with checksum mode ENABLED; get.go:133-135, 320-330)
- get part 2: status/code/message match (416 InvalidPartNumber "The requested partnumber is not satisfiable"); differs: element order. AWS `<PartNumberRequested>2` then `<ActualPartCount>1`; understudy writes ActualPartCount first (get.go:138-139)
- head part 1: matches (206, Content-Range, Content-Length 10, empty body)

Step 3, Complete repeated:
- first Complete: matches (200, ETag `-2`, ChecksumCRC64NVME + ChecksumType FULL_OBJECT)
- identical Complete again: differs: AWS 200 with the same Location/Bucket/Key/ETag, no Checksum* or ChecksumType elements, `x-amz-server-side-encryption: AES256`; understudy 404 NoSuchUpload because the upload is removed on success (multipart.go:392 find, multipart.go:518 remove)
- Complete with a changed part list after success: matches (AWS 404 NoSuchUpload, full text, `<UploadId>`; multipart.go:135-137)

Step 4, DeleteObjects:
- 1001 keys: matches (AWS 400 MalformedXML, standard text; delete.go:64-66)
- Quiet=true with an existing and a missing key: matches (empty `<DeleteResult>`)
- missing key: matches (reported as Deleted)
- duplicate key in one request: differs: AWS reports `d2` once (`<Deleted>d2</Deleted><Deleted>missing</Deleted>`); understudy appends one Deleted entry per request entry (delete.go:72-93)

Step 5, ListObjectsV2:
- max-keys 0: matches (KeyCount 0, MaxKeys 0, IsTruncated false)
- max-keys abc: code, status and fields match (400 InvalidArgument, ArgumentName max-keys, ArgumentValue abc); differs: message. AWS "Provided max-keys not an integer or within integer range"; understudy "Invalid value for the max-keys query parameter." (internal/protocol/restxml/codec.go:293; list.go:21 has the AWS text but only the negative path uses it)
- max-keys 1001: differs: AWS echoes `<MaxKeys>1001</MaxKeys>`; understudy echoes the capped 1000 (list.go:129, 260)
- start-after beyond the last key: matches (StartAfter echoed, KeyCount 0, MaxKeys 1000)

Step 6, DeleteBucket with an open upload:
- delete-bucket: matches (AWS 204; bucket.go:335-340 checks objects only and drops the uploads)
- list-multipart-uploads after: matches (404 NoSuchBucket, bucket is gone)

Step 7, raw Complete bytes:
- `<ETag>` quoting: AWS writes literal `"` inside `<ETag>` in CompleteMultipartUploadResult, `&quot;` in ListBucketResult; differs: understudy writes `&#34;` in both (internal/protocol/xmlw/escape.go:39-40). Parses to the same string.
- whitespace: AWS has nothing before `<?xml`; differs: AWS sends two LFs between the declaration and `<CompleteMultipartUploadResult>` (one elsewhere); understudy one LF (internal/protocol/xmlw/writer.go:22). Parses the same.

Step 8, multipart Last-Modified:
- Last-Modified: matches (AWS `07:03:24 GMT` = initiation 07:03:23Z, not completion 07:04:24Z; also ListObjectsV2 LastModified of `mp` is its initiation; multipart.go:521 dates the version at u.Initiated)

Step 9, limits:
- key of 1025 bytes: code/message/status match (400 KeyTooLongError "Your key is too long"); differs: element order. AWS `<Size>1025` then `<MaxSizeAllowed>1024`; understudy MaxSizeAllowed first (metadata.go:80-81)
- metadata over 2 KiB: differs: AWS 400 MetadataTooLarge "Your metadata headers exceed the maximum allowed metadata size" (no trailing period) with `<MaxSizeAllowed>2048</MaxSizeAllowed><Size>2103</Size>` (Size = key bytes + value bytes, without the `x-amz-meta-` prefix); understudy has a trailing period and no fields (metadata.go:92)
- unsigned GET with response-content-type: matches (400 InvalidRequest "Request specific response headers cannot be used for anonymous GET requests."; errors.go:185-186)
- upload-part-copy `bytes=5-2`: matches (400 InvalidArgument, copyRangeMessage text, ArgumentName x-amz-copy-source-range, ArgumentValue bytes=5-2; uploadpartcopy.go:20-48)
- upload-part-copy `bytes=0-99999999` on a 10-byte source: differs: AWS 400 InvalidArgument "Range specified is not valid for source object of size: 10" with ArgumentName x-amz-copy-source-range, ArgumentValue bytes=0-99999999; understudy 416 InvalidRange with the same text and no fields (uploadpartcopy.go:58-59)

Step 10, wrong Content-MD5 and wrong x-amz-checksum-crc32:
- which wins: differs: AWS 400 BadDigest "The Content-MD5 you specified did not match what we received." (Content-MD5 wins); understudy verifies the flexible checksum first and answers "The CRC32 you specified did not match the calculated checksum." (put.go:529-532)
- BadDigest fields: values match (ExpectedDigest hex `00000000000000000000000000000000`, CalculatedDigest base64 `eB5eJF1ptWaXm4bijSPyxw==`); differs: order. AWS CalculatedDigest first; understudy ExpectedDigest first (checksum_single.go:167)

Step 11, Object Lock default retention, PUT without Content-MD5 or checksum:
- put-object-lock-configuration: matches (200, empty body)
- PUT without checksum: differs: AWS 400 InvalidRequest "Content-MD5 OR x-amz-checksum- HTTP header is required for Put Object requests with Object Lock parameters"; understudy does not enforce Object Lock on PutObject and stores the object (docs/coverage.md:899 "no SSE-C or Object Lock")

Step 12, lifecycle (switch `s3.lifecycle.rule-limits`):
- Transition Days 7 to STANDARD_IA: differs: AWS 200 (accepted, `x-amz-transition-default-minimum-object-size: all_storage_classes_128K`); understudy refuses with InvalidArgument "'Days' in Transition action must be greater than or equal to 30 for storageClass 'STANDARD_IA'" (subresources.go:437-439)
- NoncurrentVersionTransition NoncurrentDays 10 to STANDARD_IA: differs: AWS 200; understudy refuses (same check, subresources.go:437-439)
- successful put response header: differs: AWS sends `x-amz-transition-default-minimum-object-size: all_storage_classes_128K` on the PUT; understudy's put returns an empty output (subresources.go:289-292)
- GLACIER_IR 4 then DEEP_ARCHIVE 20: code/status match (400 InvalidArgument); differs: text and fields. AWS "'Days' in the 'Transition' action for StorageClass 'DEEP_ARCHIVE' for filter '(prefix=)' must be 90 days more than 'filter '(prefix=)'' in the 'Transition' action for StorageClass 'GLACIER_IR'" with ArgumentName Days, ArgumentValue 20; understudy "... for StorageClass 'DEEP_ARCHIVE' must be at least 90 days more than 'Days' in the 'Transition' action for StorageClass 'GLACIER_IR'" with no fields (subresources.go:444-447)
- STANDARD_IA 30 then GLACIER 40: same as above with "must be 30 days more than" and ArgumentValue 40; differs the same way (subresources.go:444-447). The 30-day hold for STANDARD_IA and 90 for GLACIER_IR match minStorageDays.
- Expiration Days 0: message matches ("'Days' for Expiration action must be a positive integer"); differs: AWS adds ArgumentName Days, ArgumentValue 0; understudy no fields (subresources.go:377)
- NoncurrentVersionExpiration NoncurrentDays 0: message matches; differs: AWS adds ArgumentName NoncurrentDays, ArgumentValue 0; understudy no fields (subresources.go:380)
- duplicate rule ID and over-1000-rules texts: n/a (not in the script; stay UNVERIFIED)

Step 13, list-objects-v2 without encoding-type (kit):
- control-character / U+FFFE keys (re-run 2026-10-14 with an empty regular file as the body; bucket recreated for the step): differs in part. AWS writes U+0001 as `&#x1;` (understudy matches: xmlw escapes it as `&#x1;`) but writes U+FFFE as its raw UTF-8 bytes EF BF BE, which XML 1.0 forbids; understudy writes `&#xFFFE;` (internal/protocol/xmlw/escape.go isXMLChar). Settles U+0001 and U+FFFE of the xmlw UNVERIFIED claim (go-toolchain.md 10); see differences.md "Control characters in S3 listings".
- plain listing without encoding-type: matches (no `<EncodingType>`, ETag as `&quot;`, ChecksumAlgorithm/ChecksumType/StorageClass STANDARD per entry)

Step 14, POST on an object without uploadId (kit):
- differs: AWS 405 MethodNotAllowed "The specified method is not allowed against this resource." with `<Method>POST</Method><ResourceType>OBJECT</ResourceType>` and header `Allow: HEAD, DELETE, GET, PUT`; understudy routes it to CompleteMultipartUpload and fails its input validation (codegen/services/s3.json:11 has no uploadId override)

Step 15, GLACIER at 30 then ONEZONE_IA at 60 (kit):
- differs: AWS 400 InvalidArgument "'Days' in the 'Transition' action for StorageClass 'GLACIER' for filter '(prefix=)' must be greater than 'Days' in the 'Transition' action for StorageClass 'ONEZONE_IA' for filter '(prefix=)'" with ArgumentName Days, ArgumentValue 60 (a storage-class order check, matching word for word the text a Terraform write-up quotes, zenn.dev/thaim, 2024-04); understudy has no order check and refuses with its gap text "... for StorageClass 'ONEZONE_IA' must be at least 90 days more than 'Days' in the 'Transition' action for StorageClass 'GLACIER'" (subresources.go:444-447)

Byte-level XML (all steps):
- Some AWS error bodies have no XML declaration (03-complete-changed-parts, 04-delete-1001, 09-upload-part-copy-*, 12-*, 15-*); others start with `<?xml ...?>\n`. Understudy always writes the declaration (internal/protocol/restxml/codec.go:231). Clients parse both.

