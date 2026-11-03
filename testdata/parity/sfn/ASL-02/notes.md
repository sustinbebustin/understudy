# ASL-02 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-02
- account ids masked as <acct> below

## Expected vs AWS

- key order of the Parameters output (sfn.payload.key-order, default template order): differs: template order `z.$, static1, m.$, b{y.$, x}, a.$, static0`; AWS's raw `PassStateExited.output` is `{"static1":1,"b":{"x":2,"y":1},"static0":0,"a":1,"z":1,"m":1}` -- neither template order nor static-then-path; consistent with a hashed-map iteration order (4.3's HashMap hypothesis), also inside the nested object (`x` before `y`)
- the same string is the execution output (`ExecutionSucceeded`, DescribeExecution `output`)
- history linkage: PassStateEntered 2<-0, PassStateExited 3<-2, ExecutionSucceeded 4<-3 (matches 11.2)
