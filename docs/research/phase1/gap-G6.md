# Gap G6: Terraform call sequences for the remaining phase 1 resources and data sources

Status: closed by [tf-remaining.md](tf-remaining.md). Brief goal 6 (apply works for every phase 1 resource type, second plan empty, destroy clean) now has per-operation evidence for the resources and data sources the critic listed.

## What was done

- Read the provider source at tag `v6.68.0` for 14 resources and 13 data sources and ran the released provider (Terraform 1.16.0) through a logging reverse proxy in front of moto 5.2.3: one 37-resource, 17-data-source configuration through apply, `plan -detailed-exitcode`, update, plan, destroy (all plans exit 0 with a conforming server), plus 14 single-deviation runs of the full configuration and 12 minimal runs that show what the provider does when one response field is wrong. No real AWS was contacted; nothing outside the repo and the session scratchpad was written, apart from the in-place corrections below.
- Prior partial coverage, for the record: `aws_cloudwatch_log_stream` had one correct line in [secrets-logs-events-alarms.md 3.9](secrets-logs-events-alarms.md); `aws_s3_object` had a trace in [s3.md E4](s3.md) (two details wrong, below); secret and KMS key reads were in [tf-storage.md](tf-storage.md). Everything else listed in the gap had operation names only or nothing.

## Findings that change what understudy must build

1. **Missing fields crash the provider process.** `CreateDate` absent from `GetInstanceProfile` or `GetRole`, `LastChangedDate` absent from `DescribeSecret`, `CreatedDate` absent from `GetSecretValue`: nil-pointer panic, plugin crash, every in-flight resource errors (register rows 1-4 in tf-remaining.md).
2. **Perpetual diffs the server can cause**, each reproduced: `ListAccessKeys` without `UserName` (key replaced on every plan); `GetUserPolicy` not percent-encoded (`+` becomes a space); `GetAlias` with `"RoutingConfig":{}`; event-invoke-config `GET` with a default `MaximumEventAgeInSeconds`; provisioned concurrency `Allocated` different from the request; `GetFunction` with `"Concurrency":{}` when nothing is reserved, or dropping it when the reserved value is 0; `GetResourcePolicy` without `ResourcePolicy`.
3. **A hang**: `GetKeyPolicy` returning the stored policy with an added `Id` makes `aws_kms_key_policy` wait out its 10 minute budget (5 consecutive equivalent reads are required).
4. **Provider-side floors** the emulator clock cannot shorten: instance profile 5 s on create and on every update (tags-only included), provisioned concurrency 5 s on create and update, KMS key policy about 4 s on create, update and delete. Everything else in scope is under 1 s.
5. **New operations**: `Put|Get|DeleteProvisionedConcurrencyConfig` (appear in no other file), `ListGroupsForUser` on every user delete, `PutKeyPolicy` as the delete of `aws_kms_key_policy`, `CopyObject` with a `%2F`-encoded `x-amz-copy-source`, `GetQueueUrl`, and `DescribeKey` called with an alias name.
6. **Local-only data sources**: `aws_region` (fails for region names the provider does not know, so `local-1` style regions break it), `aws_partition`, `aws_iam_policy_document` make no API calls; `aws_caller_identity` is one STS call per read.

## Corrections made in other files (in place, marked `CORRECTED (G6)`)

- [iam-sts-kms.md](iam-sts-kms.md) 3.1: `ListGroupsForUser` runs on every user delete; `GetAccessKeyLastUsed` is import-only (read is `ListAccessKeys`); instance profile read calls `GetRole` and has a 5 s waiter; tag calls observed. Startup paragraph and item 19: `GetUser`-first discovery applies only with `skip_credentials_validation = true`; default flags make one `GetCallerIdentity`.
- [tf-storage.md](tf-storage.md) 1.1 and implication 9: same startup correction (STS is required by default).
- [s3.md](s3.md) E4 and the 4.1 `aws_s3_object` row: object `HEAD` sends `x-amz-checksum-mode` only when `checksum_algorithm` is set and never sends `If-Match`.
- [lambda-api.md](lambda-api.md) 11: provisioned concurrency operations and the alias, event-invoke-config and reserved-concurrency read-back traps added.

## Consolidated list not already required by the other tf-* files

Full text with sources is [tf-remaining.md section 14](tf-remaining.md); the headings:
- Operations: IAM user, access key, user policy, attachment and instance profile calls; provisioned concurrency Put/Get/Delete; `DeleteFunctionConcurrency`; secret `PutResourcePolicy`/`DeleteResourcePolicy`; `PutKeyPolicy` on delete; `CopyObject`; `ListObjectVersions(prefix)` plus versioned `DeleteObject`; `PutObjectTagging`; SQS `GetQueueUrl`; STS `GetCallerIdentity` per data-source read.
- Behaviors: always return the crash-prone date fields; instance profile and provisioned concurrency floors; `GetInstanceProfile` with parseable `Arn` and a full attached `Role`; provisioned concurrency 202/`READY`/404 shape; event invoke config keyed by (function, qualifier) with unset age omitted; `GetAlias` without empty `RoutingConfig`; reserved concurrency 0 present, unset absent; percent-encoded `GetUserPolicy`; `UserName` in `ListAccessKeys`; verbatim key-policy round trip; `ARN` in the `PutResourcePolicy` response; `%2F` in the copy-source header; no `x-amz-version-id` on unversioned buckets; real AWS region names for `aws_region`; latest-published-version resolution in the `aws_lambda_function` data source; `DescribeKey` accepting alias names; the SQS non-existent-queue code.

## Open items (UNVERIFIED, details in tf-remaining.md section 15)

Real Lambda `RoutingConfig` omitted-or-null on a plain alias (the acceptance test shows it is nil); `$LATEST` as a provisioned-concurrency qualifier; real S3 handling of `+` in the copy-source header; the empty-account KMS default policy principal; the `aws_lambda_function` data-source sequence when no version is published; `aws_iam_access_key` with `status = "Inactive"` at create.
