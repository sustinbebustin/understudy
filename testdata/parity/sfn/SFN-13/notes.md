# SFN-13 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-api.md 14.14
- account ids masked as <acct> below

## Expected vs AWS

- session: helper files (sfnfn.py, sfnfn.zip) written under $WORK/SFN-13/ instead of the shell's cwd
- InvocationType Event result (UNVERIFIED 9.2; understudy refuses a literal Event at create, sfn.integration.unimplemented): `TaskSucceeded` output `{"Payload":null,"SdkHttpMetadata":{...,"HttpStatusCode":202},"SdkResponseMetadata":{"RequestId":<uuid>},"StatusCode":202}` -- `StatusCode` 202 and `Payload` JSON `null`; no `ExecutedVersion` key; headers `x-amzn-Remapped-Content-Length: 0`, `Connection`, `x-amzn-RequestId`, `Content-Length: 0`, `Date`, `X-Amzn-Trace-Id` (AllHttpHeaders order: x-amzn-Remapped-Content-Length, Connection, x-amzn-RequestId, Content-Length, Date, X-Amzn-Trace-Id; HttpHeaders sorted case-insensitively)
- history sequence: ExecutionStarted, TaskStateEntered 2<-0, TaskScheduled 3<-2 (`resourceType lambda`, `resource invoke`, parameters as sent), TaskStarted 4<-3, TaskSucceeded 5<-4, TaskStateExited 6<-5, ExecutionSucceeded 7<-6 (matches 11.2/11.3 of stepfunctions-asl.md)
