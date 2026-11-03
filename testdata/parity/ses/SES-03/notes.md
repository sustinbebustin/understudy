# SES-03

date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
account: in the SES sandbox (sesv2 get-account ProductionAccessEnabled false; SendQuota 200.0 / 1.0), plan ESSENTIALS
session: no verified identity and no readable mailbox; $M = us1103-me@example.com stays Pending, so every step that sends from $M is n/a (verification needs a mailbox click)

- sandbox precondition: matches (00-sandbox: ProductionAccessEnabled false)
- 01-rcpt-only: n/a (sends from $M, which needs a mailbox click to verify)
- 02-both: differs: identities listed "us1103-rp@example.com, us1103-r1@example.com, us1103-nobody@example.com, us1103-r2@example.com" (Return-Path, To, Source, Cc); switch ses.error.unverified.text default is senders first, then recipients in request order (Source, Return-Path, To, Cc)
- 03-v2-both: differs: "us1103-r1@example.com, us1103-nobody@example.com" (recipient before sender)
- 04-reply-to: n/a (sends from $M, which needs a mailbox click to verify)
