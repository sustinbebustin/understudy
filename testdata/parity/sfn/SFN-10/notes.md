# SFN-10 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-api.md 14.11
- account ids masked as <acct> below

## Expected vs AWS

- 01 empty poll body (sfn.activity.empty-poll-body, default `{}`): matches -- 200 `{}` after the 60 s hold (step took about 62 s)
- 02 create the same activity name again with tags: 200 with the same activityArn (idempotent; differing tags do not cause an error)
