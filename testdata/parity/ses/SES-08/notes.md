# SES-08

date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
account: in the SES sandbox (sesv2 get-account ProductionAccessEnabled false; SendQuota 200.0 / 1.0), plan ESSENTIALS
session: no verified identity and no readable mailbox; $M = us1103-me@example.com stays Pending, so every step that sends from $M is n/a (verification needs a mailbox click)

- 01-mixed: unknown identities omitted from VerificationAttributes: matches switch ses.verification.unknown-identity; the email identity's entry has no VerificationToken (recorded with $M Pending, not Success)
- 02-101: settled (was UNVERIFIED): InvalidParameterValue, Type Sender, 400, "Can only get verification attributes for up to 100 identities at a time."
- 03-notif-raw (9.1): matches the 11.1 coverage note for a known identity: no BounceTopic/ComplaintTopic/DeliveryTopic elements at all (although the model marks them required), ForwardingEnabled true, the three HeadersIn*NotificationsEnabled false; the unknown identity is omitted from NotificationAttributes

## Replay (internal/parity)

`scenario.json` replays the three steps; internal/parity/baseline.json holds the outcomes.

- Setup: `VerifyEmailIdentity` of us1103-me@example.com, the identity SES-00 step 01 created in the session; not made up.
- 01-mixed settles `ses.verification.unknown-identity`.
- 03-notif-raw matches: the topic elements are left out on both sides (the ses overlay's dev.understudy#optionalOutput), and understudy writes the three HeadersIn*NotificationsEnabled as bounce, delivery, complaint and ForwardingEnabled last, as AWS did (dev.understudy#memberOrder), not the model's order.
