# SSM-17 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.18; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

- 01 `:1`: Name bare, `Selector: ":1"`, Version 1: matches 6.3.
- 02 `:ok-1`: `Selector: ":ok-1"`, Version 2: matches.
- 03 missing version: ParameterVersionNotFound `Systems Manager could not find version 99 of /us1103/ssm/l16. Verify the version and try again.` (understudy's text is its own: differs in text).
- 04 missing label: ParameterVersionNotFound `Systems Manager could not find a version of /us1103/ssm/l16 that has the specified label, nolabel. Verify the parameter label and try again.`
- 05 version 0: ParameterVersionNotFound `Systems Manager could not find version 0 of /us1103/ssm/l16. Verify the version and try again.`
- 06 `:1:2`: ValidationException `Invalid parameter name. Please use correct syntax for referencing a version/label  <name>:<version/label>` (two spaces before `<name>`).
- 07 ARN with selector: works, `Selector: ":1"`.
- 08 GetParameters: bad selectors and missing names in InvalidParameters, as sent (`/us1103/ssm/l16:99`, `/us1103/ssm/l16:nolabel`, `/us1103/ssm/missing`): matches understudy.
