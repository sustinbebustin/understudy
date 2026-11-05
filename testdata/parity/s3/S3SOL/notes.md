# S3SOL (s3-ssec-object-lock.md section 12)

The 2026-11-03 results of S3SOL-01 and S3SOL-03..24 are recorded in s3-ssec-object-lock.md section 12, beside each case. This file holds S3SOL-02, run later on its own.

## S3SOL-02: SSE-C over plain HTTP (12.3)

- Date: 2026-11-05 (18:02-18:03 UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Script: late-s3sol.sh, opted in 2026-11-05. Only the part of the 12.1 setup that 12.3 uses: `$B` (default SSE-S3, `BlockedEncryptionTypes` `NONE`) with `c1` (SSE-C, key K1) and `p1` (plain), and `$BB` (new, so it blocks SSE-C) with `p1`. Then the four 12.3 requests verbatim, then `aws s3 rb --force` of both buckets (teardown listed no bucket left). Account ids are masked as `<acct>`.

Every step: differs (code). All four answered alike: `HTTP/1.1 400 Bad Request`, `Content-Type: application/xml`, `Transfer-Encoding: chunked`, `Connection: close`, body

```xml
<Error><Code>InvalidArgument</Code><Message>Requests specifying Server Side Encryption with Customer provided keys must be made over a secure connection.</Message><ArgumentName>x-amz-server-side-encryption</ArgumentName><RequestId>...</RequestId><HostId>...</HostId></Error>
```

The text matches the one other S3 servers use (3.9); the code is `InvalidArgument`, not the `InvalidRequest` 12.3 expected, and the body names `ArgumentName` `x-amz-server-side-encryption`, as the 3.2 header refusals do.

- 02-put-http (PUT h1 to `$B`, which allows SSE-C): the 400 above. Whether `h1` was written was not checked (the teardown emptied the bucket).
- 02-get-http (GET c1, the SSE-C object, with its key): the 400 above. Reads with SSE-C headers are refused too.
- 02-get-plain-http (GET p1, not SSE-C, with SSE-C headers): the 400 above, not the "not applicable" 400 that HTTPS gets (3.3). The scheme check runs before the stored encryption is compared.
- 02-put-blocked-http (PUT h1 to `$BB`, which blocks SSE-C): the 400 above, not the 403 of 4.3. The scheme check runs before the bucket block.
