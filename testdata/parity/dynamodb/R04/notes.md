# R04

- Date: 2026-10-14T06:33Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

Compared against understudy at the working tree of 2026-10-14 (replayed locally via scripts/sealed; paths under internal/service/dynamodb unless stated).

- 01 create-table (PAY_PER_REQUEST): differs: AWS omits WarmThroughput and BillingModeSummary.LastUpdateToPayPerRequestDateTime in the CreateTable answer; understudy adds WarmThroughput {12000,4000,CREATING} (describe.go:98) and keeps Local's LastUpdateToPayPerRequestDateTime (describe.go:112-114)
- 02 describe-table: matches (shape, WarmThroughput {12000,4000,ACTIVE}, LastUpdateToPayPerRequestDateTime = CreationDateTime, ProvisionedThroughput 0/0 without Last*DateTime); AWS also sends unmodeled TableThroughputModeSummary (understudy omits; SDKs drop it)
- 02 describe-time-to-live: matches ({"TimeToLiveDescription":{"TimeToLiveStatus":"DISABLED"}}, no AttributeName)
- 02 describe-continuous-backups: matches (ContinuousBackupsStatus ENABLED, PointInTimeRecoveryDescription {PointInTimeRecoveryStatus: DISABLED} present)
- 02 list-tags-of-resource: matches ({"Tags":[]})
- 02 describe-contributor-insights: differs: AWS 200 {"ContributorInsightsStatus":"DISABLED","TableName":...}; understudy 501 NotImplemented (unbound, docs/coverage.md DescribeContributorInsights)
- 03 update-time-to-live enable: matches ({"TimeToLiveSpecification":{"AttributeName":"ExpiresAt","Enabled":true}})
- 03 describe-time-to-live at 0/5/30/120 s: matches (ENABLED immediately, never ENABLING)
- 03 enable again (already enabled): matches (400 ValidationException "TimeToLive is already enabled", ttl.go:73)
- 03 disable right after enable: differs: AWS 400 com.amazon.coral.validate#ValidationException "Time to live has been modified multiple times within a fixed interval"; understudy 200 and disables (ttl.go:78-81, no change interval enforced)
- 03 disable again: differs: AWS 400 same "modified multiple times within a fixed interval" text; understudy 400 "TimeToLive is already disabled" (ttl.go:79), reached only because its first disable succeeded
- 03 enable after (refused) disable: matches AWS's state ("TimeToLive is already enabled") only when the disable is refused; understudy 200 because it had disabled
- 03 empty AttributeName: n/a (CLI rejects client-side, never sent)
- 03 256-char AttributeName: matches (400 "1 validation error detected: Value '<a*256>' at 'timeToLiveSpecification.attributeName' failed to satisfy constraint: Member must have length less than or equal to 255")
- 04 update-continuous-backups enable / RecoveryPeriodInDays=1 / enable again / disable and describes: matches (35 default, 1 kept on re-enable, Earliest = Latest = enable instant, DISABLED body without timestamps); AWS also sends unmodeled PointInTimeRecoveryWindowDays and whole-second timestamps (1.791959752E9), understudy milliseconds
- 04 RecoveryPeriodInDays=36: matches (400 "1 validation error detected: Value '36' at 'pointInTimeRecoverySpecification.recoveryPeriodInDays' failed to satisfy constraint: Member must have value less than or equal to 35")
- 05 put-item ok: matches ({})
- 05 ConditionalCheckFailedException: matches (400, __type com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException, lower-case "message": "The conditional request failed")
- 05 ConditionalCheckFailedException with ALL_OLD: differs: AWS body {"__type":...,"Item":{...},"Message":"The conditional request failed"} (upper-case Message, Item first); understudy {"__type":...,"message":...,"Item":{...}} (rewrite.go:208-216 forces the model's lower-case key)
- 06 x-amz-crc32 on 200s: matches (present, equals CRC32 of body on every 200)
- 06 x-amz-crc32 on errors: differs: AWS sends a correct x-amz-crc32 on every error (all 400s here); understudy sends it only on errors forwarded from Local (CCF); errors understudy raises itself (TTL/tag/PITR/KMS refusals, ResourceNotFound/TableNotFound from shims, model validation) carry none (internal/protocol/awsjson/codec.go:274-279, shims.go:211-213)
- 07 describe-time-to-live / update-time-to-live missing table: matches (400 ResourceNotFoundException "Requested resource not found: Table: does-not-exist not found")
- 07 describe-continuous-backups missing table: matches (400 com.amazonaws.dynamodb.v20120810#TableNotFoundException "Table not found: does-not-exist")
- 07 list-tags-of-resource missing table: differs: AWS "Requested resource not found: ResourceArn: arn:aws:dynamodb:us-east-2:123456789012:table/<name> not found"; understudy "...: ResourcArn: ..." (tags.go:177)
- x create-table / update-table with unknown KMS key ARN: differs: AWS 400 ValidationException "KMS validation error: com.amazonaws.services.kms.model.NotFoundException: Key 'arn:aws:kms:us-east-2:123456789012:key/00000000-0000-0000-0000-000000000000' does not exist (Service: AWSKMS; Status Code: 400; Error Code: NotFoundException; Request ID: <uuid>; Proxy: null)"; understudy "Invalid KMSMasterKeyId: KMS key <arn> was not found" (sse.go:94-96)
- x tag-resource 51 tags: differs: AWS 400 ValidationException "Number of Tags exceed the current limit for the provided ResourceArn"; understudy "Too many tags: the table would have 51 tags; a table can have at most 50" (tags.go:64)
- x tag-resource aws: prefix: differs: AWS 400 ValidationException "Tag Key cannot be prefixed with aws:, Key: aws:x"; understudy "Invalid tag key aws:x: tag keys starting with aws: are reserved for AWS use" (tags.go:53)
- x list-tags after refused tags: matches ({"Tags":[]}, both refusals atomic)
- x tag-resource missing table: differs: same "ResourcArn" typo as 07 (tags.go:177)
- x update-table PAY_PER_REQUEST -> PROVISIONED answer: differs: AWS stamps ProvisionedThroughput.LastIncreaseDateTime (the update instant) in the UpdateTable answer; understudy omits it (table.go:139-143, 294-302: no prior Throughput, so no increase)
- x describe-table after the switch (ACTIVE): matches (no LastIncreaseDateTime, LastUpdateToPayPerRequestDateTime kept, BillingMode PROVISIONED, 1/1)
- 99 delete-table: differs: AWS answer omits AttributeDefinitions, KeySchema, CreationDateTime and WarmThroughput; understudy returns Local's full description plus WarmThroughput {..,DELETING} (describe.go:98, service.go:195-197)
- Number and key formatting: differs (wire only): AWS writes timestamps as 1.791959601263E9 and members in alphabetical order; understudy 1791959601.263 (describe.go:258-265) in Local's order
- 08 TTL deletion timing: n/a (not run)

