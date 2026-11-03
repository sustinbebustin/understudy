# ASL-14 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-14
- account ids masked as <acct> below

## Expected vs AWS

- 01 Distributed Map in an Express machine (UNVERIFIED 3.4): 400 `InvalidDefinition` `Invalid State Machine Definition: 'SCHEMA_VALIDATION_FAILED: The field 'Mode' with value 'DISTRIBUTED' is not supported inside 'EXPRESS' state machines at /States/M/ItemProcessor/Mode'` (location omits `ProcessorConfig`)
- 02 Express with TimeoutSeconds 600 (over the 5-minute maximum): accepted -- 200, machine created
- 04 StartSyncExecution billingDetails for a 1 s Wait: `billedDurationInMilliseconds` 1000, `billedMemoryUsedInMB` 64 (start to stop exactly 1.000 s); 200 SUCCEEDED; nested `inputDetails`/`outputDetails` carry `__type` `com.amazonaws.swf.base.model#CloudWatchEventsExecutionDataDetails`
- 05 GetExecutionHistory on an Express ARN (15.6 item 23): matches -- 400 `InvalidArn` `Invalid Arn: 'Resource type not valid in this context: express'`
- 06 DescribeExecution on an Express ARN: matches -- 400 `InvalidArn`, same text
- teardown: `asl_rm` reported nothing for xd (never created); xt and xw deleted
