# SFN-09 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-api.md 14.10
- account ids masked as <acct> below

## Expected vs AWS

- 01 PutTargets for a state machine without RoleArn (UNVERIFIED 10.1): rejected as a call error, not a FailedEntry -- 400 `ValidationException` `RoleArn is required for target arn:aws:states:us-east-2:<acct>:stateMachine:us1103-pass.`
- 02 PutTargets with RoleArn: 200 `{"FailedEntries":[],"FailedEntryCount":0}`
- 03 PutEvents: 200, one EventId
- execution name (sfn.events.target-execution-name, default a UUID from the seeded stream): differs: `<event id>_<uuid-shaped id>` (e.g. `c0b994c3-4517-5d32-a438-c80b59610a54_de0b4877-ea28-82ed-c77f-fb82ea7c6c11`, the first half equal to the PutEvents EventId), 73 characters
- input is the full event (UNVERIFIED 10.1): yes -- compact JSON with keys in order `version, id, detail-type, source, account, time, region, resources, detail`; `detail` an object
- 05 describe: SUCCEEDED, `redriveStatus NOT_REDRIVABLE`, `redriveStatusReason` `Execution is SUCCEEDED and cannot be redriven` (matches 6.3)
