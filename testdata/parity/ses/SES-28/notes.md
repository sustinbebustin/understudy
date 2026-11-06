# SES-28

date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
account: in the SES sandbox (sesv2 get-account ProductionAccessEnabled false; SendQuota 200.0 / 1.0), plan ESSENTIALS
session: no verified identity and no readable mailbox; $M = us1103-me@example.com stays Pending, so every step that sends from $M is n/a (verification needs a mailbox click)

- setup: iam-enforcement-surfaces.md 13.1 reduced to $P-enf-none and the four helpers; the role was created for this case and deleted after it
- 01/02 v1: AccessDenied, Type Sender, 403, Content-Type text/xml, SES namespace: matches ses.denial.identity on code/type/status; no x-amzn-query-error header
- v1 message: differs from "the IAM grammar": "User 'arn:aws:sts::XXXXXXXXXXXX:assumed-role/us1103-enf-none/enf' is not authorized to perform 'ses:SendEmail' on resource 'arn:aws:ses:us-east-2:XXXXXXXXXXXX:identity/us1103-me@example.com'" (quoted values, no colons, no because clause; resource is the sender identity)
- 03/04 v2: 403, x-amzn-ErrorType AccessDeniedException, Content-Type application/json: matches
- v2 body: differs: key is "Message" (capital M), not "message": {"Message":"User '...' is not authorized to perform 'ses:SendEmail' on resource '...identity/us1103-me@example.com'"}
- 04 v2 Raw content: authorized as ses:SendRawEmail (on the identity from the raw From header)
