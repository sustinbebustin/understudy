# ASL-12 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-12
- account ids masked as <acct> below

## Expected vs AWS

- ExecutionTimedOut details (UNVERIFIED 8.6): `{"error":"States.Timeout"}`, no cause key; timestamp exactly 3.000 s after ExecutionStarted
- linkage: ExecutionTimedOut 3<-0 -- `previousEventId` 0 (like ExecutionAborted, 11.2 rule 10), not the WaitStateEntered
- WaitStateAborted or nothing after WaitStateEntered: nothing -- no WaitStateAborted, no WaitStateExited
- DescribeExecution: matches 6.3/8.6 -- status `TIMED_OUT`, no `error`/`cause`, `redriveStatus` `REDRIVABLE`, no `redriveStatusReason`
