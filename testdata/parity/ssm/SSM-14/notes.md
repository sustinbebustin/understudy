# SSM-14 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.15; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

- 01 policies on Standard: ValidationException `You can't assign parameter policies to a standard-parameter tier. You must upgrade the parameter to use the advanced-parameter tier. For more information, see https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-store-advanced-parameters.html`
- 02/03 PolicyText normalization: re-serialized, not verbatim: Timestamp `2030-01-01T00:00:00.000Z` comes back as `2030-01-01T00:00:00Z`, compact JSON; `PolicyStatus: Pending`, `PolicyType: Expiration`.
- 04/05 overwrite without Policies keeps them: matches 4.5 [D].
- 06/07 `[{}]` clears: matches: `Policies: []`.
- 08 unknown type: InvalidPolicyTypeException `Policy type Nope is not supported.`
- 09 two Expiration policies: IncompatiblePolicyException `There can be only one expiration policy`
- understudy answers 501 for policies (12.1), so these are AWS facts only.
- The item's exit=1 is the trailing `ssmdel` grep; every step ran.
