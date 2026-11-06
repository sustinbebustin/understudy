# SSM-13 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.14; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

- 01 Intelligent-Tiering small: response Tier `Standard` (resolved, not echoed): matches understudy.
- 02 Intelligent-Tiering 4097: response Tier `Advanced`: matches.
- 03 describe: Standard / Advanced: matches.
- 04 downgrade: ValidationException code matches; text differs from 12.4 ([T] quote): AWS appends a sentence: `This parameter uses the advanced-parameter tier. You can't downgrade a parameter from the advanced-parameter tier to the standard-parameter tier. If necessary, you can delete the advanced parameter and recreate it as a standard parameter. Be aware that standard parameters have a value limit of 4096 characters.`
- 05 overwrite without Tier (switch `ssm.tier.omitted-on-advanced` = kept-advanced): matches: `{"Tier":"Advanced","Version":2}`.
- 06 Intelligent-Tiering small on Advanced: stays Advanced (Version 3): matches no-downgrade.
- 07 describe after: Advanced, Version 3: matches.
- The item's exit=1 in session.log is the trailing `ssmdel` grep finding nothing to print; every step ran and the deletes succeeded.
