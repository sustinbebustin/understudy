# ASL-16 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-16
- account ids masked as <acct> below

## Expected vs AWS

- failure at the history limit (11.1, 25,000 events): status `FAILED`, error `States.Runtime`, cause `The execution reached the maximum number of history events (25000).` (no `An error occurred while executing the state` prefix)
- last events: ExecutionFailed is event 25000 (25000<-24999), right after PassStateExited 24999 (output `{"n":6250}`), i.e. 6,250 loops of four events; it ran 43 s (16:52:02.605 -> 16:52:45.801)
- DescribeExecution: `redriveStatus` `NOT_REDRIVABLE`, `redriveStatusReason` `Execution has exceeded the maximum history event limit` -- differs from the documented reason `Execution history event limit exceeded.` quoted in stepfunctions-api.md 6.3
- about 12,500 state transitions billed (as the recipe estimated)
