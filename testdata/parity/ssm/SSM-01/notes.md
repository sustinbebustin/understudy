# SSM-01 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.2; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

- `__type` is the bare shape name (2.2): matches (`ParameterNotFound`, `ParameterAlreadyExists`, `ValidationException`, `InvalidResourceId`, `InvalidFilterKey`; no namespace prefix).
- 01 GetParameter missing, message (2.2 [R] `"Message": ""`): differs: body is `{"__type":"ParameterNotFound"}`, 400, no message key at all (botocore shows the empty string).
- 02 ParameterAlreadyExists key casing (`message`): matches; text `The parameter already exists. To overwrite this value, set the overwrite option in the request to true.`
- 03 bad name `$N//bad`: ValidationException 400 with the [E] text of 3.1 verbatim: matches the text; differs on key casing: the model's `com.amazonaws.ssm#ValidationException` member is `Message`, the wire key is `message`.
- 04 member-less InvalidResourceId: matches: `{"__type":"InvalidResourceId"}`, no message.
- 05 member-less InvalidFilterKey: differs: the model has no member, AWS sends `"Message":"The following filter key is not valid: Label. Valid filter keys include: [Path, Name, Type, KeyId, Tier, DataType]."`
