# ASL-15 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-15
- account ids masked as <acct> below

## Expected vs AWS

- session: big.json and dl.json written under $WORK/ASL-15/ instead of $REC (helper files never go in $REC); removed after use as the recipe does
- 01 execution input of 262,202 bytes (UNVERIFIED 12: `InvalidExecutionInput` or `ValidationException`?): `ValidationException` -- 400, `__type` `com.amazon.coral.validate#ValidationException`, `1 validation error detected: Value at 'input' failed to satisfy constraint: Member must have size less than or equal to 262144 bytes in UTF-8 encoding` (same as SFN-08/07)
- 03 257 KiB Pass Result inside a Parallel with catchers on `States.DataLimitExceeded` and `States.ALL` (sfn.catch.all-matches-data-limit, default true): differs: neither `Named` nor `All` ran -- the execution FAILED with `States.DataLimitExceeded`, cause `The state/task 'A' returned a result with a size exceeding the maximum number of bytes service limit.` (no `An error occurred while executing the state` prefix, matches 8.5's text); history ParallelStateStarted 3<-2, PassStateEntered 4<-3, ExecutionFailed 5<-4 (no ParallelStateFailed): terminal inside a Parallel branch, even for a catcher naming it
