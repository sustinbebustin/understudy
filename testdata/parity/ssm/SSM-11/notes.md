# SSM-11 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.12; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

- 01 create without Type: ValidationException 400 `A parameter type is required when you create a parameter.` (understudy text is its own: differs in text only; the moto text in 7.1 is exact).
- 02 KeyId on String: ValidationException `KeyId is required for SecureString type parameter only.`
- 03 empty Value (understudy accepts): differs: ValidationException `1 validation error detected: Value at 'value' failed to satisfy constraint: Member must have length greater than or equal to 1` (Coral form without the quoted value).
- 04 describe: `{"Parameters":[]}`: none of the three was created.
- 05 `{{ssm:x}}`: ValidationException matches the code; text: `Parameter value: '{{ssm:x}}' failed to satisfy constraint: Parameter value can't nest another parameter. Do not use "{{}}" in the value.` (AWS echoes the value in this message; understudy's rule 12.4 keeps values out of messages).
