# R08

- Date: 2026-10-14T06:46Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

Compared against backlog R08 + Part 1 hypotheses and understudy at the time of writing (paths relative to /home/austin/dev/local-aws-stack).

Step 1 (header form, hello):
- PutObject accepts x-amz-checksum-{xxhash64,xxhash3,xxhash128,sha512,md5} with the 1.4.1/1.4.3 values: matches (200, header echoed, x-amz-checksum-type FULL_OBJECT; 01-put-header-*.http)
- head_object ChecksumMode=ENABLED returns stored value + FULL_OBJECT: matches (01-head-*.http; internal/service/s3/get.go:306-311)
- get_object_attributes Checksum: matches (Checksum<ALG> + ChecksumType FULL_OBJECT; 01-attributes-*.http)
- list_objects_v2 ChecksumAlgorithm lists new names: matches (ChecksumAlgorithm MD5/SHA512/XXHASH*, ChecksumType FULL_OBJECT; 01-list-objects-v2.http; internal/service/s3/list.go:214-219)
- xxhash digest encoding (seed 0, big-endian, XXH3-128 high||low): matches (AWS accepted and echoed every Part 1 vector)

Step 2 (SDK trailer, boto3 1.43.113 + awscrt 0.37.0; CLI 2.37.13):
- boto3 trailer path for XXHASH64/XXHASH3/XXHASH128/SHA512 (STREAMING-UNSIGNED-PAYLOAD-TRAILER, Transfer-Encoding chunked, Expect 100-continue): matches (all 200; values = 1.4.1/1.4.3 for hello and pat1048576; 02-sdk-trailer-*.out)
- CLI put-object --checksum-algorithm XXHASH64: matches (trailer, 200, JseCfYifbaM=; 02-cli-put-xxhash64.out)

Step 3 (raw trailer matrix):
- trailer names md5/sha512/xxhash64/xxhash3/xxhash128 accepted (decision 2 of 1.7): matches (200; 03-trailer-*.http; internal/checksum/algorithm.go:54-61)
- \n\r\n terminator accepted for every algorithm: matches (03-trailer-*-lf-term.http; internal/awschunked/reader.go:370-392)
- wrong trailer value, different length: matches (400 InvalidRequest "Value for x-amz-checksum-<alg> trailing header is invalid."; internal/service/s3/checksum_single.go:131-133)
- wrong trailer value, same length (xxhash128 sent md5 digest): matches (400 BadDigest "The XXHASH128 you specified did not match the calculated checksum."; checksum_single.go:134-135)
- x-amz-checksum-nope: matches (400 InvalidRequest "The value specified in the x-amz-trailer header is not supported"; checksum_single.go:21)
- declared crc32, chunk carries sha1: matches (400 MalformedTrailerError "The request contained trailing data that was not well-formed or did not conform to our published schema."; internal/service/s3/put.go:44,582-583)
- short non-final chunk status (kit addition): differs: AWS 403 Forbidden InvalidChunkSizeError; understudy 400 (codegen/overlays/s3.json:784 httpError 400)
- short non-final chunk <Chunk> element: differs: AWS <Chunk>2</Chunk><BadChunkSize>3</BadChunkSize> for chunks [3,2] (index of the chunk being read when the short one is detected); understudy Chunk=1 (internal/awschunked/reader.go:172-173, index of the short chunk)
- short non-final chunk message: matches ("Only the last chunk is allowed to have a size less than 8192 bytes"; put.go:579)

Step 4 (header errors):
- two checksum headers: matches (400 InvalidRequest "Expecting a single x-amz-checksum- header. Multiple checksum Types are not allowed."; checksum_single.go:19)
- x-amz-sdk-checksum-algorithm without value: matches (400 InvalidRequest "x-amz-sdk-checksum-algorithm specified, but no corresponding x-amz-checksum-* or x-amz-trailer headers were found."; checksum_single.go:20)
- wrong-length / non-base64 sha512, xxhash128 (and sha256 control): matches (400 InvalidRequest "Value for x-amz-checksum-<alg> header is invalid."; checksum_single.go:96)
- BadDigest text per algorithm, upper-cased: matches ("The XXHASH64|XXHASH3|XXHASH128|SHA512|MD5 you specified did not match the calculated checksum."; checksum_single.go:174)
- correct checksum + wrong Content-MD5: matches on code/text (BadDigest "The Content-MD5 you specified did not match what we received.")
- BadDigest element order: differs: AWS <CalculatedDigest> (base64) then <ExpectedDigest> (hex); understudy ExpectedDigest then CalculatedDigest (internal/service/s3/checksum_single.go:167)
- wrong checksum + wrong Content-MD5, which wins: differs: AWS Content-MD5 BadDigest wins (04-wrong-checksum-plus-wrong-content-md5.http); understudy checks the declared checksum first and answers "The XXHASH64 you specified..." (internal/service/s3/put.go:529-532)

Step 5 (multipart, all five new algorithms):
- Create COMPOSITE: matches (200, x-amz-checksum-algorithm + x-amz-checksum-type COMPOSITE echoed; internal/service/s3/multipart.go:218)
- Create FULL_OBJECT for new algorithms: matches (400 InvalidRequest "The FULL_OBJECT checksum type cannot be used with the <lower alg> checksum algorithm."; internal/service/s3/checksums.go:84)
- UploadPart response: matches (ETag = part MD5, x-amz-checksum-<alg> echoed, no x-amz-checksum-type)
- Complete composite value = hash of concatenated raw part digests + "-2" (1.4.3 hypothesis): matches for all five (05-complete-*.http; internal/checksum/multipart.go:39-54)
- Complete ChecksumType COMPOSITE: matches
- head_object PartNumber=1: matches (206, Content-Range bytes 0-5242879/6291456, x-amz-mp-parts-count 2, part's own checksum, type COMPOSITE; get.go:323-327)
- get_object_attributes Checksum on composite object: matches (value without "-2"; internal/service/s3/attributes.go:102-107)
- get_object_attributes ObjectParts: matches (PartsCount 2, per-part Size + Checksum<ALG>)
- get_object_attributes max-parts 1 (kit addition): matches (PartNumberMarker 0, NextPartNumberMarker 1, MaxParts 1, IsTruncated true, one Part; attributes.go:66-95)

Step 6 (doc-stated failures):
- Complete with x-amz-checksum-xxhash64 on an upload created without algorithm: code matches (400 InvalidRequest); text differs: AWS "Checksum Type mismatch occurred, expected checksum Type: null, actual checksum Type: xxhash64"; understudy "The upload was created without a checksum algorithm, so the complete request cannot include the x-amz-checksum-xxhash64 header." (internal/service/s3/checksums.go:371)
- Complete with x-amz-checksum-crc32 on an upload created without algorithm: matches (200, result ChecksumCRC64NVME + FULL_OBJECT, HEAD reports CRC64NVME only; checksums.go:372-373)
- UploadPart without a checksum on an upload created with XXHASH64: differs: AWS 400 InvalidRequest "Checksum Type mismatch occurred, expected checksum Type: xxhash64, actual checksum Type: null" (06-part-without-checksum*.http); understudy accepts and computes (internal/service/s3/checksums.go:155-157)
- Complete with/without part checksums after that: n/a (both parts were refused, so Complete answered InvalidPart with <ETag>None</ETag>; only shows InvalidPart element order UploadId, PartNumber, ETag, which differs from understudy's ETag, PartNumber, UploadId at checksums.go:213)
- Complete with wrong x-amz-mp-object-size: matches (400 InvalidRequest "The provided 'x-amz-mp-object-size' header value 6 does not match what was computed: 5"; multipart.go:422-423)
- UploadPart without checksum on an undeclared upload reports no checksum: matches (06-part-no-alg.http; checksums.go:191-193)
- Complete error body framing: differs: AWS Complete errors have no <?xml ...?> declaration and are sent Transfer-Encoding chunked + Connection close (06-complete-*.http); understudy always writes the declaration (internal/protocol/restxml/codec.go:231)

Step 7 (copy):
- copy_object ChecksumAlgorithm=XXHASH3 from a CRC64NVME source: matches (CopyObjectResult ChecksumXXHASH3 lVXoVVxi3P0= = direct digest, ChecksumType FULL_OBJECT; internal/service/s3/copy.go:370-381)

Step 8 (unknown algorithm):
- code: matches (400 InvalidRequest)
- text: differs: AWS "Checksum algorithm provided is unsupported. Please try again with any of the valid types: [CRC32, CRC32C, CRC64NVME, MD5, SHA1, SHA256, SHA512, XXHASH128, XXHASH3, XXHASH64]"; understudy lists "[CRC32, CRC32C, CRC64NVME, SHA1, SHA256, SHA512, MD5, XXHASH64, XXHASH3, XXHASH128]" (internal/service/s3/checksums.go:22)

Teardown:
- n/a: 99-list-multipart-uploads.out shows uploads nf2, nf3, nf4 still open after the run (abort them).
