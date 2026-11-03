# SES-13

date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
account: in the SES sandbox (sesv2 get-account ProductionAccessEnabled false; SendQuota 200.0 / 1.0), plan ESSENTIALS
session: no verified identity and no readable mailbox; $M = us1103-me@example.com stays Pending, so every step that sends from $M is n/a (verification needs a mailbox click)

- 01-email raw JSON: every member present, unset ones explicit null: ConfigurationSetName null, DkimAttributes {CurrentSigningKeyLength null, LastKeyGenerationTimestamp null, NextSigningKeyLength RSA_1024_BIT, SigningAttributesOrigin AWS_SES, SigningEnabled false, SigningHostedZone null, Status NOT_STARTED, Tokens null}, FeedbackForwardingStatus true, MailFromAttributes {BehaviorOnMxFailure USE_DEFAULT_VALUE, MailFromDomain null, MailFromDomainStatus null}, Policies {}, Tags [], VdmAttributes null, VerificationInfo {all four null}, VerificationStatus PENDING, VerifiedForSendingStatus false; matches the 5.4 Terraform table
- 02-domain-create: DKIM Status NOT_STARTED in the create response, SigningEnabled true, SigningHostedZone dkim.amazonses.com, 3 tokens, LastKeyGenerationTimestamp as epoch seconds in exponent form (1.79...E9)
- 03/04-domain: DKIM Status PENDING (matches the 5.4 table), tokens identical across both reads (stable: matches 9.3 need)
- Content-Type of GET responses: application/x-amz-json-1.1 (not application/json)
