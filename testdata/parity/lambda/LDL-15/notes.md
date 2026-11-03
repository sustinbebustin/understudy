date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
Session: helper files under $REC/_work/LDL (# session:). 07-size-limit-at-086.txt is the CLI's stderr for the refused add (no status line).

- 01 and 02 render `{"AWS":"arn:aws:iam::<A>:root"}` with a string value: matches (both). Statement key order `Sid, Effect, Principal, Action, Resource` (matches 6.7).
- 03 a `StringEquals` `aws:PrincipalOrgID` condition: matches (`"Principal":"*"`, `"Condition":{"StringEquals":{"aws:PrincipalOrgID":"o-abcdefghij"}}` after `Resource`).
- 04 account principal with organization id: refused, 400 `InvalidParameterValueException`, `{"Type":"User","message":"If organization id is specified then principal should be *"}`.
- 05: `{"Version":"2012-10-17","Id":"default","Statement":[acct, rootarn, org]}`, insertion order; `RevisionId` equals 03's.
- 06 the 404 `No policy is associated with the given resource.` or an empty statement list: differs from both: 404 `ResourceNotFoundException`, `{"Message":"Layer version arn:aws:lambda:us-east-2:<A>:layer:us1103-ldl-pol:1 does not have any resource policy.","Type":"User"}`.
- 07 statement count and `PolicyLengthExceededException` text: 85 statements accepted; the 86th refused: `PolicyLengthExceededException` `The final policy size (20693) is bigger than the limit (20480).` (Type User). The limit is 20480, the function-policy quota.
- 08 policy length: 85 statements; the returned `Policy` string is 21983 bytes, larger than the 20480 limit, so AWS measures the policy some other way than the rendered string.
- 09 and 10 texts for a foreign version that cannot be read: both 403 `AccessDeniedException` (not the 400 `Layer version ... does not exist.` of 6.8/7.1), `{"Message":"User: arn:aws:sts::<A>:assumed-role/<role>/<session> is not authorized to perform: lambda:GetLayerVersion on resource: arn:aws:lambda:us-east-2:123456789012:layer:us1103-nosuch:1 because no resource-based policy allows the lambda:GetLayerVersion action"}`. A nonexistent foreign version and an unshared one are indistinguishable.
- 11 public Powertools layer: 200; `AWSLambdaPowertoolsPythonV3-python313-x86_64:42` from account 017000801446, `Content.Location` a presigned S3 URL, `UncompressedCodeSize` 0, `ResolvedS3Object` null.
