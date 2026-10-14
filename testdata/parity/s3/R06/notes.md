# R06

- Date: 2026-10-14T06:34Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

Compared against backlog R06 (s3.md 4.1, wire-rest.md 3.6) and understudy at internal/service/s3 + codegen/overlays/s3.json. Account id written as 123456789012.

Step 1 (fresh-bucket subresources):
- create-bucket: matches (200, empty body, x-amz-bucket-arn). Location header differs (cosmetic): AWS `http://<bucket>.s3.amazonaws.com/` (http, global host, over an https regional request); understudy request scheme + endpoint host (bucket.go:203-210). Clients ignore it.
- acl: matches (200, Owner/ID only, no DisplayName; one CanonicalUser FULL_CONTROL grant with xsi:type; subresources.go:91, validate.go:249-254).
- policy: matches code/status/text/BucketName (404 NoSuchBucketPolicy "The bucket policy does not exist").
- policyStatus: n/a in backlog table; AWS 404 NoSuchBucketPolicy "The bucket policy does not exist" +BucketName. Differs: understudy does not bind GetBucketPolicyStatus (no match in internal/service/s3), so it answers the generic unimplemented error.
- cors: matches (404 NoSuchCORSConfiguration "The CORS configuration does not exist" +BucketName).
- website: matches (404 NoSuchWebsiteConfiguration "The specified bucket does not have a website configuration" +BucketName).
- lifecycle: matches (404 NoSuchLifecycleConfiguration "The lifecycle configuration does not exist" +BucketName; AWS body has no XML declaration, cosmetic).
- replication: matches (404 ReplicationConfigurationNotFoundError "The replication configuration was not found" +BucketName).
- object-lock: matches (404 ObjectLockConfigurationNotFoundError "Object Lock configuration does not exist for this bucket" +BucketName).
- tagging: matches (404 NoSuchTagSet "The TagSet does not exist" +BucketName).
- not-configured 404 headers: differs: AWS sends no x-amz-bucket-region on any of these 404s (01-get-get-bucket-policy.http etc.); understudy adds it to every error fn returns for an existing bucket (resolve.go:332 via inRegion, errors.go:45-55).
- logging: matches (200 empty BucketLoggingStatus; AWS body also carries an XML comment, cosmetic).
- versioning: matches (200 empty VersioningConfiguration).
- accelerate: matches (200 empty AccelerateConfiguration).
- requestPayment: matches (200 `<Payer>BucketOwner</Payer>`; overlay s3.json:609).
- encryption (fresh): differs: AWS rule = BucketKeyEnabled false + SSEAlgorithm AES256 + `<BlockedEncryptionTypes><EncryptionType>SSE-C</EncryptionType></BlockedEncryptionTypes>`; understudy default has no BlockedEncryptionTypes (codegen/overlays/s3.json:581).
- ownershipControls: matches (200 BucketOwnerEnforced; bucket.go:222-224).
- publicAccessBlock: matches (200, all four true; bucket.go:225-227).
- notification: matches (200 empty NotificationConfiguration).
- location: matches (us-east-2 bucket returns `us-east-2`; bucket.go:253-259).
- list analytics/inventory/metrics/intelligent-tiering: matches (200, IsTruncated false, empty list, no ContinuationToken).
- get analytics/inventory/metrics/intelligent-tiering id=nope: matches (404 NoSuchConfiguration "The specified configuration does not exist.", no BucketName); settles inventory and intelligent-tiering codes (were inferred).

Step 2 (encryption):
- delete-bucket-encryption: matches (204).
- get after delete: status/shape matches the s3.encryption.after-delete switch (200, default AES256 rule again, no ServerSideEncryptionConfigurationNotFoundError); same BlockedEncryptionTypes difference as step 1 (overlay s3.json:581).
- KMS key validation on put (nonexistent id, disabled, pending deletion, missing alias): matches (AWS 200 for all four; understudy checkEncryption does not resolve the key, subresources.go:543-555).
- KMSMasterKeyID echo: matches (bare id, ARN and alias/aws/s3 each echoed exactly as sent; subresources.go:530 stores the input).
- BucketKeyEnabled omitted on put: differs: AWS GET returns `<BucketKeyEnabled>false</BucketKeyEnabled>`; understudy stores the input verbatim and omits it (subresources.go:529-531).
- BlockedEncryptionTypes after a put that omits it: differs: AWS GET still returns SSE-C blocked; understudy stores the input verbatim, so the element disappears (subresources.go:529-531).

Step 3 (PAB / ownership delete):
- delete-public-access-block: matches (204).
- get-public-access-block after delete: matches code/status/text (404 NoSuchPublicAccessBlockConfiguration "The public access block configuration was not found" +BucketName); x-amz-bucket-region difference as above.
- delete-bucket-ownership-controls: matches (204).
- get-bucket-ownership-controls after delete: matches code/status/text (404 OwnershipControlsNotFoundError "The bucket ownership controls were not found" +BucketName); x-amz-bucket-region difference as above.

Step 4 (exact ACL / request-payment bytes): matches (see step 1).

Step 5 (EU bucket, BucketRegion, head-bucket): n/a (skipped; free-plan account, single region).

Step 6 (S3 Control tags):
- tag-resource request: path `/v20180820/tags/arn%3Aaws%3As3%3A%3A%3A<bucket>`, header x-amz-account-id, body root `<TagResourceRequest xmlns="http://awss3control.amazonaws.com/doc/2018-08-20/">`; matches (understudy accepts any root, control.go:203-225). 204 matches.
- list-tags-for-resource: matches (200 `ListTagsForResourceResult` in the s3control namespace, Tags/Tag{Key,Value}; control.go:98-114).
- untag-resource: matches (DELETE ?tagKeys=k, 204).
- list-tags missing bucket: differs: AWS 404 `<ErrorResponse><Error><Code>NoSuchResource</Code><Message>The specified resource doesn't exist.</Message></Error><RequestId>..` (no BucketName); understudy 404 NoSuchBucket "The specified bucket does not exist" +BucketName in the same wrapper (control.go:94 -> resolve.go:317, errors.go:57-58).
- list-tags not-owned bucket: AWS same 404 NoSuchResource; the probe bucket name may simply not exist, so this does not separate "not owned" from "missing". Understudy behaviour for another account's bucket not compared.

Step 7 (storage class / object headers):
- put-object x-amz-storage-class: differs: AWS PUT response echoes `x-amz-storage-class: STANDARD_IA` for a non-STANDARD write (omitted for STANDARD); understudy never sends it (put.go:95; PutObjectOutput has no StorageClass member).
- head/get x-amz-storage-class: matches (omitted for STANDARD, `STANDARD_IA` otherwise; metadata.go:121-126).
- list-objects-v2 StorageClass: matches (STANDARD listed explicitly; list.go:201), ChecksumAlgorithm CRC64NVME + ChecksumType FULL_OBJECT per entry matches (list.go:203).
- list-objects-v2 headers: differs (minor): AWS 200 carries `x-amz-bucket-region: us-east-2`; understudy does not (list.go:258).
- checksum headers: matches (PUT returns crc64nvme + FULL_OBJECT; GET with x-amz-checksum-mode ENABLED returns them; HEAD without mode omits them; get.go:306).
- SSE headers under the alias/aws/s3 bucket rule: matches (x-amz-server-side-encryption aws:kms, key id = full ARN of the aws/s3 managed key, no bucket-key header; sse.go:234-243).
- default Content-Type binary/octet-stream: matches (metadata.go:21).
