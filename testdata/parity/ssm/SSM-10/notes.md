# SSM-10 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.11; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

- 01 value-only overwrite with Type omitted: accepted, Version 2; Type stays SecureString: matches.
- 02 Description `d`, AllowedPattern `^v.*`, KeyId `alias/aws/ssm`, DataType `text` kept (switch `ssm.put.omitted-fields` = preserved): matches.
- 03 history: both versions carry the same fields; Value is ciphertext without decryption.
- 04 AllowedPattern `""` on overwrite with value `x3`: accepted (Version 3), so `""` cleared the pattern: matches understudy's choice.
- 05 describe after: no AllowedPattern key: matches.
