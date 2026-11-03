# SFN-12 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-api.md 14.13
- account ids masked as <acct> below

## Expected vs AWS

- request host: matches -- `sync-states.us-east-2.amazonaws.com` (the endpoint provider resolves `states.us-east-2.amazonaws.com`, the CLI prepends `sync-`; 3.3)
- 01 billingDetails for a 1 s Wait (sfn.sync.billing-details, default round up to 100 ms, 64 MB): matches -- `billedDurationInMilliseconds` 1000 (start 16:42:26.499, stop 16:42:27.499, exactly 1.000 s, so rounding up is not distinguished from exact here), `billedMemoryUsedInMB` 64; status SUCCEEDED, `express:` ARN, `inputDetails`/`outputDetails {included:true}`
- 02 StartSyncExecution on Standard: matches -- 400 `StateMachineTypeNotSupported` `This operation is not supported by this type of state machine`
