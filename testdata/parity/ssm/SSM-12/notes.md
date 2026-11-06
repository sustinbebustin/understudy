# SSM-12 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.13; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

- 01 4096 ASCII Standard: accepted: matches.
- 02 4097 ASCII: ValidationException `Standard tier parameters support a maximum parameter value of 4096 characters. To create a larger parameter value, upgrade the parameter to use the advanced-parameter tier. For more information, see https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-store-advanced-parameters.html`: code matches.
- 03 2048 x U+00E9 (4096 bytes): accepted.
- 04 2049 x U+00E9 (4098 bytes) (switch `ssm.value.size-unit` = bytes): differs: accepted, `{"Tier":"Standard","Version":1}`; the limit counts characters.
- 05 Advanced 8192: accepted, Tier Advanced.
- 06 Advanced 8193: ValidationException `The specified parameter value is too large. Advanced-tier parameters support a maximum parameter value of 8192 characters.`
