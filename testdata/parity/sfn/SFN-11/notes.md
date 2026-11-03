# SFN-11 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-api.md 14.12
- account ids masked as <acct> below

## Expected vs AWS

- 01 optimized sqs:sendMessage, missing queue at a valid host (UNVERIFIED 9.2): `TaskFailed` error `SQS.QueueDoesNotExistException` (not `SQS.AmazonSQSException`), cause `The specified queue does not exist. (Service: AmazonSQS; Status Code: 400; Error Code: AWS.SimpleQueueService.NonExistentQueue; Request ID: <uuid>; Proxy: null)`; then `ExecutionFailed` 6<-5 with the same error and cause
- 02 .sync:2 output date form (UNVERIFIED 9.2): `StartDate`/`StopDate` are epoch milliseconds as JSON integers (`1793724100521`); `Input`/`Output` are JSON (`{}`, `{"ok":true}`); output also carries `RedriveCount` 0, `RedriveStatus` `NOT_REDRIVABLE`, `RedriveStatusReason` `Execution is SUCCEEDED and cannot be redriven` (not in 9.2's result list); a `TaskSubmitted` event (5<-4) holds the StartExecution response with `SdkHttpMetadata`/`SdkResponseMetadata` and `StartDate` in epoch ms
- 02 aws-sdk:sfn:startExecution output: `{"ExecutionArn":...,"StartDate":"2026-11-03T16:41:41.922Z"}` -- ISO-8601 string with milliseconds and `Z` (differs from the optimized integration's epoch ms)
- 03 managed rule (sfn.integration.sync-managed-rule, understudy: not created, a documented deviation): differs: `ListRules` shows `StepFunctionsGetEventsForStepFunctionsExecutionRule`, `ManagedBy` `states.amazonaws.com`, `State` `ENABLED`, `Description` `This rule is used to notify Step Functions regarding integrated workflow executions`, pattern on `aws.states` / `Step Functions Execution Status Change` / status FAILED, SUCCEEDED, TIMED_OUT, ABORTED (pretty-printed with 2-space indent and trailing newline)
- 04 aws-sdk:sqs missing queue, ` (SDK Attempt Count: 1)` suffix (sfn.integration.aws-sdk-cause-suffix, default append): matches -- error `Sqs.QueueDoesNotExistException`, cause `The specified queue does not exist. (Service: Sqs, Status Code: 400, Request ID: <uuid>) (SDK Attempt Count: 1)`
