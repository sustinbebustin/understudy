# SSM-16 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.17; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

- 01 label forms: 200, `{"InvalidLabels":["a/b","1abc","awsx","a:b","SSMy"],"ParameterVersion":1}`; `ok-1` attached; `:` and `/` are invalid; InvalidLabels is not in request order: matches 6.2.
- 02 101-character label (switch `ssm.label.too-long` = validation-exception): matches code; Coral text names the list: `1 validation error detected: Value '[aaa...a]' at 'labels' failed to satisfy constraint: Member must satisfy constraint: [Member must have length less than or equal to 100, Member must have length greater than or equal to 1]`
- 03/04 move `ok-1` to version 2: version 1 Labels `[]`, version 2 `["ok-1"]`: matches.
- 05 to ten labels: 200.
- 06 eleventh: ParameterVersionLabelLimitExceeded; text differs from understudy's own: `A parameter version can have maximum 10 labels.Move one or more labels to another version and try again.` (the moto text in 6.2, exact, no space after the period).
- 07 unlabel: `{"InvalidLabels":["nope"],"RemovedLabels":["b1"]}`: matches.
