# SSM-35 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.36; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

- 01 denial identity (iam-enforcement-surfaces.md 12.3): matches: AccessDeniedException, 400, key `Message`, IAM grammar: `User: arn:aws:sts::<acct>:assumed-role/us1103-ssm-p35/ssm is not authorized to perform: ssm:GetParameter on resource: arn:aws:ssm:us-east-2:<acct>:parameter/us1103/ssm/p35/a because no identity-based policy allows the ssm:GetParameter action`
- 02 trailing slash (understudy drops it, so the call would be allowed): differs: denied; the evaluated ARN keeps the slash: `arn:aws:ssm:us-east-2:<acct>:parameter/us1103/ssm/p35/`.
- 03 Path `/`: matches: denied on `arn:aws:ssm:us-east-2:<acct>:parameter/`.
- The item's exit=1 is the trailing `ssmdel` grep; every step ran and the role was deleted.
