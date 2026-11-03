# SFN-01 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-api.md 14.2
- account ids masked as <acct> below

## Expected vs AWS

- status line (all six steps): matches -- `HTTP/1.1 400 Bad Request` (3.2: every modeled error 400)
- x-amzn-* headers: matches -- only `x-amzn-RequestId`; no `x-amzn-ErrorType`; `Content-Type: application/x-amz-json-1.0`
- `__type` value (sfn.error.type): differs: AWS sends `com.amazonaws.swf.service.v2.model#<Code>` (e.g. `com.amazonaws.swf.service.v2.model#StateMachineDoesNotExist`), not bare `<Code>` and not understudy's generated `com.amazonaws.sfn#<Shape>` (3.2, 13.2)
- message key: matches -- lower-case `message`
- 01 missing machine: matches -- `StateMachineDoesNotExist` `State Machine Does Not Exist: 'arn:aws:states:us-east-2:<acct>:stateMachine:us1103-missing'`
- 02 not an ARN: matches -- `InvalidArn` `Invalid Arn: 'Invalid ARN prefix: not-an-arn'`
- 03 other service ARN (UNVERIFIED in 3.2): `InvalidArn` `Invalid Arn: 'Arn missing resource component: arn:aws:sqs:us-east-2:<acct>:us1103-q'` (13.3's "resource type per operation" order does not produce this text)
- 04 execution ARN where stateMachine expected (UNVERIFIED in 3.2): matches 13.3's form -- `InvalidArn` `Invalid Arn: 'Resource type not valid in this context: execution'`
- 05 other region (UNVERIFIED in 3.2): `InvalidArn` `Invalid Arn: 'Expected the ARN arn:aws:states:us-west-2:<acct>:stateMachine:us1103-pass to be within region (us-east-2).'` (not in 13.3's ARN check order)
- 06 missing execution: matches -- `ExecutionDoesNotExist` `Execution Does Not Exist: 'arn:aws:states:us-east-2:<acct>:execution:us1103-pass:missing'`
