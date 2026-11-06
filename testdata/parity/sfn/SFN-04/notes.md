# SFN-04 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-api.md 14.5
- account ids masked as <acct> below

## Expected vs AWS

- 01 stop with error/cause: 200 `{"stopDate":...}`
- 02 stop an already aborted execution (UNVERIFIED 6.4): 200 `{"stopDate":...}`, no error
- 03 describe after stop with error/cause (UNVERIFIED 6.4): status `ABORTED`, `error` `MyError`, `cause` `my cause` present; `redriveStatus` `REDRIVABLE`, `redriveCount` 0; nested `inputDetails` carries `__type` `com.amazonaws.swf.base.model#CloudWatchEventsExecutionDataDetails`
- 04 history: matches 6.4 sequence -- ExecutionStarted, WaitStateEntered, ExecutionAborted with `previousEventId` 0; differs from 6.4's recorded `executionAbortedEventDetails: {}`: with error/cause they appear as `{"cause":"my cause","error":"MyError"}`
- 05 stop a succeeded execution (UNVERIFIED 6.4): 200 `{"stopDate":...}`, no error
