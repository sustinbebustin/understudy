# SFN-05 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-api.md 14.6
- account ids masked as <acct> below

## Expected vs AWS

- 01 includeExecutionData false (sfn.history.exclude-data): matches understudy default -- `input`/`output` and `inputDetails`/`outputDetails` dropped; `*EventDetails` kept (`executionStartedEventDetails {roleArn}`, `stateEnteredEventDetails {name}`, `executionSucceededEventDetails {}`)
- 02 page token shape: base64 (standard alphabet with `+` `/` `=`), 112 characters, starts `AwBsAAAA`
- 03 bad page token: differs: 400 `InvalidToken` message `Invalid Token` (no `: 'Invalid token'` subject), not 3.2's `Invalid Token: 'Invalid token'`
- 04 variableReferences absent or {} (UNVERIFIED 5.2): absent; defaults match 5.2 (`loggingConfiguration {includeExecutionData:false, level:OFF}`, `tracingConfiguration {enabled:false}`, `encryptionConfiguration {type:AWS_OWNED_KEY}`, `status ACTIVE`, no `revisionId`)
- wire detail: every nested structure carries its own `__type` (`com.amazonaws.swf.base.model#LoggingConfiguration`, `#TracingConfiguration`, `#EncryptionConfiguration`, `#HistoryEvent`); clients ignore it
