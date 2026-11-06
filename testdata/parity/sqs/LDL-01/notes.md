date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1

Session: lambda-destinations-layers.md 12.1 helper files under $REC/_work/LDL instead of $REC/LDL (# session:). Script: scratchpad later/LDL/ldl.sh.

- step 02 empty (each qualifier uses only its own configuration): matches. 02 received nothing, and no record for `via-alias-no-config` or `via-version-no-config` arrived later in any drain of the queue (LDL-02..LDL-04). So an alias or version without its own configuration does not fall back to `$LATEST`'s (`lambda.async.qualifier-config` default `invoked-qualifier-only` is right; 2.4).
- step 05 alias record `functionArn` ends `:live`: matches (`arn:aws:lambda:us-east-2:<A>:function:us1103-ldl-q:live`, `executedVersion` `"1"`).
- step 05 version record `functionArn` ends `:1`, `executedVersion` `"1"`: matches, but the record is not in 05: the single long-poll receive returned only the alias record; the version record arrived in the next drain of the same queue, LDL-04/02-destination-2 (`...:us1103-ldl-q:1`, `executedVersion` `"1"`, `Success`, count 1). Recipe gap: one drain does not collect every record.
- put responses: 01 `FunctionArn` `...:$LATEST`, 03 `...:live`, 04 `...:1`.
