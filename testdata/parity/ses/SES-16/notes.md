# SES-16

date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
account: in the SES sandbox (sesv2 get-account ProductionAccessEnabled false; SendQuota 200.0 / 1.0), plan ESSENTIALS
session: no verified identity and no readable mailbox; $M = us1103-me@example.com stays Pending, so every step that sends from $M is n/a (verification needs a mailbox click)

- 01 name 255: passes tag validation (then MessageRejected on the sender): matches
- 02 name 256: differs: passes tag validation (MessageRejected on the sender); ses.tags.max-length default 255 expects "Tag name cannot exceed 255 characters."
- 03 value 255: passes: matches
- 04 value 256: differs: passes (MessageRejected); default 255 expects "Tag value cannot exceed 255 characters."
- 05 ses:feedback-id-c: matches ses.tags.ses-prefix: InvalidParameterValue 400 "Invalid tag name <ses:feedback-id-c>: only alphanumeric ASCII characters, '_',  '-' are allowed." (doubled space kept)
- 06 ses:custom: matches, same message with <ses:custom>
- 07 value a.b@c: passes: matches section 10 (value charset admits . and @)
- v2-256: differs: v2 name of 256 also passes (MessageRejected)
- 257 is not recorded, so the rejecting boundary is open (>256 per model docs "256 or fewer")
