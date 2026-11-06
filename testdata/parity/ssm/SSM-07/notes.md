# SSM-07 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.8; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

- 01/02 GetParameter echoes the request spelling (`us1103-root` / `/us1103-root`), ARN `parameter/us1103-root` both: matches `ssm.name.root-slash-echo`.
- 03 GetParameters with both spellings: two entries, `/us1103-root` then `us1103-root`, each with its own spelling, `InvalidParameters: []`. Note for understudy: GetParameters' "a name given twice is answered once" must key on the spelling, not the canonical name.
- 04/05 DescribeParameters Name filter with either spelling finds it, `Name: "us1103-root"`: matches.
- 06 GetParameterHistory by `/us1103-root` reports `Name: "us1103-root"`: matches.
- 07 GetParametersByPath Path `/`: lists the root parameter as `us1103-root`, no NextToken: matches (understudy: Path / lists root parameters).
- 08 PutParameter `us1103-root` after creating `/us1103-root`: ParameterAlreadyExists: matches (one parameter).
- 09/10 ListTagsForResource by `/us1103-root` and by ARN: `{"TagList":[]}` both: matches.
