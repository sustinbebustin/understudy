# SSM-23 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.24; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

- understudy answers 501 for Secrets Manager references (12.1), so every line is an AWS fact against section 8.2.
- 01/02 SourceResult key casing (switch `ssm.secretsmanager-ref.source-result-keys` = lower-except-arn): matches: keys `ARN`, `name`, `versionId`, `secretString`, `versionStages`, `createdDate`; `createdDate` is `"Nov 3, 2026, 4:37:44 PM"`. Raw bytes in 02-raw.http.
- 01 fields: `Name` is the reference path, `Type: SecureString`, `Value` the secret string, `Version: 0`, `ARN` the secret's ARN, `LastModifiedDate` set, no `Selector`, no `DataType`.
- 03 without decryption (switch = rejected, ValidationException): matches: `WithDecryption flag must be True for retrieving a Secret Manager secret.`
- 04 `:AWSCURRENT`: `Selector: ":AWSCURRENT"`, Name without the selector.
- 05 missing: ParameterNotFound `An error occurred (ParameterNotFound) when referencing Secrets Manager: Secret aws/reference/secretsmanager/us1103-none not found. Secrets Manager can't find the specified secret.`
- 06 GetParameters: missing reference in InvalidParameters.
- 07 GetParametersByPath on the prefix: ValidationException, the long name-requirements text recorded in SSM-19 step 02.
- 08 PutParameter under the prefix: AccessDeniedException, key `Message`: `No access to reserved parameter name: aws/reference/secretsmanager/us1103-x.`
