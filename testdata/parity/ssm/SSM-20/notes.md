# SSM-20 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.21; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

- 01 default page (switch `ssm.paging.default-page-size` describe = 50): differs: 10 entries and a NextToken for 12 parameters.
- 01 order (switch `ssm.list.order` = name-ascending): differs: page 1 holds k01-k07, k09, k11, k12; k08 and k10 are left for a later page.
- 01 `ARN` on each entry: present: matches; `Policies: []` on each.
- 02 empty: `{"Parameters":[]}`.
- The item's exit=1 is the trailing `ssmdel` grep; every step ran.
