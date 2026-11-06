# SSM-04 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.5; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

Session change: the recipe's step name (`tr -c 'A-Za-z0-9' _`) maps both `a b` and `a$b` to `put-a_b`, so `put-a_b` holds `a$b`; `a b` was sent again afterwards as `put-a_space_b`. Recipe fix: give one of them a distinct step name.

Per name (3.1; understudy uses the [E] path text for path defects and its own text otherwise):
- `a b` (put-a_space_b): ValidationException 400, key `message`: the path text `Parameter name: can't be prefixed with "ssm" (case-insensitive). If formed as a path, it can consist of sub-paths divided by slash symbol; each sub-path can be formed as a mix of letters, numbers and the following 3 symbols .-_`
- `a$b` (put-a_b): ValidationException, the same path text.
- `us1103/x`: ValidationException `Parameter name must be a fully qualified name.`
- `/us1103//x`: ValidationException, the path text: matches the [E] text.
- `/us1103/x/`: ValidationException `Parameter name must not end with slash.`
- `awsome`: ValidationException `Parameter name: can't be prefixed with "aws" or "ssm" (case-insensitive).`: the literal prefix test (3.1 inference) is confirmed.
- `SSMx`: ValidationException, the same aws/ssm text.
- `/aws/x`: differs from ValidationException: AccessDeniedException 400, key `Message`: `No access to reserved parameter name: aws/x.`
- `/ssm/x`: ValidationException, the path text.
- `/us1103/aws/x`: accepted (`aws` is reserved only at the first level); deleted by exact name in the teardown.
- padded `  $N/padded  `: accepted; outer spaces trimmed: `get-parameter --name $N/padded` answers `Name: "/us1103/ssm/padded"`.
- Code: every name defect is ValidationException except `/aws/...` (AccessDeniedException); understudy's own texts differ from the four recorded non-path texts above.
