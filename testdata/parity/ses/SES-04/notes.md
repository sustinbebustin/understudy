# SES-04

date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
account: in the SES sandbox (sesv2 get-account ProductionAccessEnabled false; SendQuota 200.0 / 1.0), plan ESSENTIALS
session: no verified identity and no readable mailbox; $M = us1103-me@example.com stays Pending, so every step that sends from $M is n/a (verification needs a mailbox click)

- invalid-address loop (v1-to-*, v2-to-*): n/a (sends from $M, which needs a mailbox click to verify)
- v1-empty-source: differs from the 11.5 placeholder texts: InvalidParameterValue, Type Sender, 400, "Invalid email address ." (space before the dot, empty address)
- v1-verify-bad: differs from the 11.5 placeholder texts: InvalidParameterValue, Type Sender, 400, "Invalid email address<not-an-email>." (no space before the bracket)
