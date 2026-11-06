# SES-17

date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
account: in the SES sandbox (sesv2 get-account ProductionAccessEnabled false; SendQuota 200.0 / 1.0), plan ESSENTIALS
session: no verified identity and no readable mailbox; $M = us1103-me@example.com stays Pending, so every step that sends from $M is n/a (verification needs a mailbox click)

- 01-verified, 03-v2, 04-bad-tag-and-set: n/a (send from $M, which needs a mailbox click to verify)
- 02-unverified: configuration set checked before the sender: ConfigurationSetDoesNotExist, Type Sender, 400, "Configuration set <us1103-missing> does not exist."; matches the 6.4 plausible order and the 8 recorded text; understudy answers NotImplemented for ConfigurationSetName (first slice), so its ses.send.check-order has no step here

## Replay (internal/parity)

`scenario.json` replays 02-unverified, which differs: understudy answers 501 NotImplemented for ConfigurationSetName.
