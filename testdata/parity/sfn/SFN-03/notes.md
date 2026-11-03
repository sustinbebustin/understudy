# SFN-03 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-api.md 14.4
- account ids masked as <acct> below

## Expected vs AWS

- 02 same name+input while running returns 01's startDate: matches -- same executionArn and startDate (1.793723802122E9)
- 03 reformatted input `{ "a": 1 }` (sfn.execution.idempotent-input-compare): matches understudy's byte-exact default -- 400 `ExecutionAlreadyExists` `Execution Already Exists: 'arn:aws:states:us-east-2:<acct>:execution:us1103-wait:us1103-idem'`
- 04 different input: matches -- 400 `ExecutionAlreadyExists` (same text)
- 05 after close: matches -- 400 `ExecutionAlreadyExists` (same text)
