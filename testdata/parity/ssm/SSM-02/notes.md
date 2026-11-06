# SSM-02 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.3; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

- 01 DeleteParameter missing (switch `ssm.error.parameter-not-found-message` = empty): matches: `{"__type":"ParameterNotFound"}`, no message key.
- 02 GetParameterHistory missing: matches: no message key.
- 03 LabelParameterVersion missing: differs: `"message":"Parameter /us1103/ssm/missing not found."`
- 04 UnlabelParameterVersion missing: differs: `"message":"Parameter /us1103/ssm/missing not found."`
- 05 GetParameter /aws/service/nonexistent/x (expected ParameterNotFound): differs: `AccessDeniedException`, 400, key `Message`: `No access to "/aws/" namespace: aws/service/nonexistent is not a valid namespace`. understudy answers 501 for /aws/service/ (12.1), so this is AWS fact only.
