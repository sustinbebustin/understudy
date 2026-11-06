# SSM-05 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.6; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

- 01 15 levels accepted: matches.
- 02 16 levels (/$P + l1..l14 + p): accepted (200, Version 1). The docs show only 15 (valid) and 17 (fails); 3.4 read "maximum depth of fifteen levels" as counting the name, which would reject 16: differs from that reading.
- 03 17 levels: HierarchyLevelLimitExceededException 400 matches the code; text differs: `Depth of the tree cannot exceed 15` (12.4 expected the model doc `A hierarchy can have a maximum of 15 levels...`).
