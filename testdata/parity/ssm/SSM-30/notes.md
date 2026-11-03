# SSM-30 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.31; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

- 01 blocked 101st version: ParameterMaxVersionLimitExceeded code matches; text differs from 12.4 in two places: the name has no leading slash and the apostrophe is U+2019: `You attempted to create a new version of us1103/ssm/c30 by calling the PutParameter API with the overwrite flag. Version 1, the oldest version, can’t be deleted because it has a label associated with it. Move the label to another version of the parameter, and try again.`
- 02 after moving the label: `{"Tier":"Standard","Version":101}`: numbering continues: matches.
- 03 history default page (understudy 50): matches: 50 entries, versions 2-51 oldest first, NextToken present; version 2 carries `keep`.
- 04 `:1`: ParameterVersionNotFound `Systems Manager could not find version 1 of /us1103/ssm/c30. Verify the version and try again.`: version 1 was dropped.
