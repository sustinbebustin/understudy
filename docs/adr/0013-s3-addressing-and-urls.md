# ADR 0013: S3 addressing, DNS, cross-region buckets and emulator-minted URLs

Status: accepted, 2026-10-08.

## Context

Go v2 and JS v3 send `Host: <bucket>.localhost:<port>` against `http://localhost:<port>`; boto3 and the AWS CLI send path style; IP literals force path style everywhere (`landscape.md 4.1`, `sigv4-checksums-endpoints.md 6.1`). `*.localhost` resolves on glibc hosts with systemd-resolved, not inside containers, and is unverified on macOS and Windows. Terraform's S3 Control tag read prefixes the account id to the authority, so `127.0.0.1` endpoints break it (`s3.md` finding 1). LocalStack and Floci run DNS servers; the brief requires an offline static binary. Bucket names are global across accounts; a bucket has a home region, and AWS answers region-mismatched requests with redirects or `AuthorizationHeaderMalformed` that only boto3 follows automatically (`wire-rest.md` Implications 11). SQS queue URLs embed the host the creator used; only JS v3 posts to it (`lambda-runtime-contract.md 5.1`).

## Decision

- No DNS server in phase 1. Virtual-host detection is configuration: a request is virtual-hosted iff its Host (port stripped) ends in `.` plus a configured base domain (default `localhost` and `s3.localhost`; the regional form `s3.<region>.<base>` of every base; names under `localhost` and `localhost.localstack.cloud` through `s3.baseDomains`) with a DNS-valid bucket label in front, matched on the longest base suffix (dotted buckets), and the credential scope is `s3` or absent. Host equal to a base domain or an IP literal is path style. Requests signed for other services on such hosts are never captured.
- The documented endpoint is `http://localhost:4566`. `understudy env --sdk <sdk>` prints the exact variables per SDK and Terraform (including `UsePathStyle`, `forcePathStyle`, `s3_use_path_style` and `skip_requesting_account_id` where needed); `understudy doctor` tests `*.localhost` resolution on this host.
- S3 Control `ListTagsForResource`, `TagResource`, `UntagResource` are served on any `<account>.<base>` host and share the bucket tag store.
- Bucket namespace is global across accounts with a home region. Default is AWS behavior for cross-region access: `AuthorizationHeaderMalformed` 400 with the `Region` element for SigV4 requests signed for the wrong region, `PermanentRedirect` 301 for unsigned ones, `x-amz-bucket-region` on `HeadBucket` and bucket-addressed errors; `IllegalLocationConstraintException` only from `CreateBucket`. `s3.crossRegion: lenient` serves the bucket regardless and records a journal warning.
- Queue URLs are `<scheme>://<host as received>/<account>/<name>`; the path is the identity. Function URLs are `http://<url-id>.lambda-url.<region>.localhost:<port>/` with the path fallback `/_understudy/lambda-url/<url-id>/...`. Presigned URLs carry the signing client's host.
- An opt-in TLS listener with a generated self-signed certificate (SANs `localhost`, `*.localhost`, `127.0.0.1`) exists because aws-sdk-go-v2 refuses unseekable `PutObject` bodies over plain HTTP; `understudy env --tls` prints the CA-bundle variables.

## Alternatives considered

- A stdlib DNS responder for hosts and containers: a second network service that needs privileges or configuration; deferred to phase 2 behind an ADR with evidence from macOS and Docker Desktop users.
- Lenient cross-region access by default (two proposals and `wire-rest.md` 11): friendlier, but a test that passes locally and fails in a real account on a region mismatch is the failure this project exists to prevent; leniency is one switch away.
- Canonical IP-literal queue URLs: wrong on Docker Desktop and changes `aws_sqs_queue.url` by topology; echoing the Host plus the `localhost:host-gateway` alias (ADR 0011) covers the cross-boundary case.
- Canonical `https://sqs.<region>.<host>` queue URLs for `terraform import`: would mint URLs no client can reach; import is documented as unsupported instead.

## Consequences

- Go and JS users need `*.localhost` resolution or the path-style knob; the banner and `doctor` say which applies.
- Cross-region errors carry text sourced from documentation and are marked in the coverage report until recorded.
- Virtual-hosted S3 and function URLs inside containers need hostnames no `/etc/hosts` alias covers; functions use IP-literal endpoints, and the remaining cases wait for the DNS ADR.

## Amendments (review, 2026-10-08)

- Requests with no asserted region (SigV2 presigned URLs from boto3's legacy regions, Go's unsigned `GetBucketRegion` HEAD, anonymous GETs) are served from the bucket's home region with `x-amz-bucket-region` set; `AuthorizationHeaderMalformed` and `PermanentRedirect` apply only when the request asserts a region that differs (fidelity F9).
- The S3 fallthrough in the front door applies only to unsigned, SigV2 or `s3`-scoped requests; a request signed for another service that no rule claimed is answered by that service's unknown-operation identity (fidelity F7).
- `localhost.localstack.cloud` may be added to the base domains through configuration for LocalStack migrations (product m10).
- The S3 bodies are streamed (ADR 0001 amendments); bucket subresource dispatch rows are generated from `routeOverrides` so the edge has no S3-specific code.

## Amendments (pre-tag review, 2026-10-13)

Decided at the pre-tag review.

- `s3.amazonaws.com` and `s3.<region>.amazonaws.com` are not base domains in phase 1. Every supported client takes `AWS_ENDPOINT_URL`, serving the AWS names over TLS would need a certificate for `*.amazonaws.com`, and the loopback host policy (DNS rebinding) answers only names the operator chose under `localhost`, IP literals and `localhost.localstack.cloud`. The configuration and the host policy are unchanged. Capturing the AWS names is a phase 2 option behind the DNS ADR.
