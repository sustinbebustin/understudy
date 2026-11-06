# SES-14

date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
account: in the SES sandbox (sesv2 get-account ProductionAccessEnabled false; SendQuota 200.0 / 1.0), plan ESSENTIALS
session: no verified identity and no readable mailbox; $M = us1103-me@example.com stays Pending, so every step that sends from $M is n/a (verification needs a mailbox click)

- 01-v1-page: order newest first (us1103.example.org, us1103.example.com, the last two created) [correction: us1103.example.com was created by SES-10, before us1103-dup@, -v1@ and -v2@, so this is not newest first; it is descending name order (ses.md 5.3)]; NextToken is base64 wrapped every 76 characters with newlines inside the XML element
- 02-v2-post: same first page, opaque base64 NextToken; Content-Type application/json
- 03-v2-get: settled: the GET binding is still served (200, same page), Content-Type application/x-amz-json-1.1
- 04-v2-filter: IDENTITY_NAME_CONTAINS accepted; returned only 4 of the 6 us1103 identities then present (us1103-dup@, us1103-me@, us1103-v1@, us1103.example.com; not us1103-v2@, us1103.example.org) with NextToken null; the two missing ones were created 1-2 s earlier (filter index lag, inferred)
