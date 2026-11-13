# ADR 0013: S3 addressing, DNS, cross-region buckets and emulator-minted URLs

Status: accepted, 2026-10-08.

## Context

Go v2 and JS v3 send `Host: <bucket>.localhost:<port>` against `http://localhost:<port>`; boto3 and the AWS CLI send path style; IP literals force path style everywhere (`sigv4-checksums-endpoints.md 6.1`). `*.localhost` resolves on glibc hosts with systemd-resolved, not inside containers, and is unverified on macOS and Windows. Terraform's S3 Control tag read prefixes the account id to the authority, so `127.0.0.1` endpoints break it (`s3.md` finding 1). LocalStack and Floci run DNS servers; understudy must be an offline static binary. Bucket names are global across accounts; a bucket has a home region, and AWS answers region-mismatched requests with redirects or `AuthorizationHeaderMalformed` that only boto3 follows automatically (`wire-rest.md` Implications 11). SQS queue URLs embed the host the creator used; only JS v3 posts to it (`lambda-runtime-contract.md 5.1`).

## Decision

- No DNS server. Virtual-host detection is configuration: a request is virtual-hosted iff its Host (port stripped) ends in `.` plus a configured base domain (default `localhost` and `s3.localhost`; the regional form `s3.<region>.<base>` of every base; names under `localhost` and `localhost.localstack.cloud` through `s3.baseDomains`) with a DNS-valid bucket label in front, matched on the longest base suffix (dotted buckets), and the credential scope is `s3` or absent. Host equal to a base domain or an IP literal is path style. Requests signed for other services on such hosts are never captured.
- The documented endpoint is `http://localhost:4566`. (Superseded by the amendment of 2026-10-14: the documented endpoint is the IP literal `http://127.0.0.1:4566`.) `understudy env --sdk <sdk>` prints the exact variables per SDK and Terraform (including `UsePathStyle`, `forcePathStyle`, `s3_use_path_style` and `skip_requesting_account_id` where needed); `understudy doctor` tests `*.localhost` resolution on this host.
- S3 Control `ListTagsForResource`, `TagResource`, `UntagResource` are served on any `<account>.<base>` host and share the bucket tag store.
- Bucket namespace is global across accounts with a home region. Default is AWS behavior for cross-region access: `AuthorizationHeaderMalformed` 400 with the `Region` element for SigV4 requests signed for the wrong region, `PermanentRedirect` 301 for unsigned ones, `x-amz-bucket-region` on `HeadBucket` and bucket-addressed errors; `IllegalLocationConstraintException` only from `CreateBucket`. `s3.crossRegion: lenient` serves the bucket regardless and records a journal warning. (Narrowed by the amendment of 2026-11-13: of the bucket-addressed errors, only `HeadBucket`'s and `ListObjectsV2`'s, the cross-region refusals and those to a request that asserts no region carry `x-amz-bucket-region`.)
- Queue URLs are `<scheme>://<host as received>/<account>/<name>`; the path is the identity. Function URLs are `http://<url-id>.lambda-url.<region>.localhost:<port>/` with the path fallback `/_understudy/lambda-url/<url-id>/...`. Presigned URLs carry the signing client's host.
- An opt-in TLS listener with a generated self-signed certificate (SANs `localhost`, `*.localhost`, `127.0.0.1`) exists because aws-sdk-go-v2 refuses unseekable `PutObject` bodies over plain HTTP; `understudy env --tls` prints the CA-bundle variables.

## Alternatives considered

- A stdlib DNS responder for hosts and containers: a second network service that needs privileges or configuration. Not built; it would need its own ADR, with evidence from macOS and Docker Desktop users.
- Lenient cross-region access by default (`wire-rest.md` 11): friendlier, but a test that passes locally and fails in a real account on a region mismatch is the failure this project exists to prevent; leniency is one switch away.
- Canonical IP-literal queue URLs: wrong on Docker Desktop and changes `aws_sqs_queue.url` by topology; echoing the Host plus the `localhost:host-gateway` alias (ADR 0011) covers the cross-boundary case.
- Canonical `https://sqs.<region>.<host>` queue URLs for `terraform import`: would mint URLs no client can reach; import is documented as unsupported instead.

## Consequences

- Go and JS users need `*.localhost` resolution or the path-style knob; the banner and `doctor` say which applies.
- Cross-region errors carry text sourced from documentation and are marked in the coverage report until recorded.
- Virtual-hosted S3 and function URLs inside containers need hostnames no `/etc/hosts` alias covers; functions use IP-literal endpoints, and the remaining cases wait for a DNS ADR.

## Amendment 2026-10-08: requests with no asserted region; the S3 fallthrough

- Requests with no asserted region (SigV2 presigned URLs from boto3's legacy regions, Go's unsigned `GetBucketRegion` HEAD, anonymous GETs) are served from the bucket's home region with `x-amz-bucket-region` set; `AuthorizationHeaderMalformed` and `PermanentRedirect` apply only when the request asserts a region that differs.
- The S3 fallthrough in the front door applies only to unsigned, SigV2 or `s3`-scoped requests; a request signed for another service that no rule claimed is answered by that service's unknown-operation identity.
- `localhost.localstack.cloud` may be added to the base domains through configuration for LocalStack migrations.
- The S3 bodies are streamed (ADR 0001, amendment 2026-10-08); bucket subresource dispatch rows are generated from `routeOverrides` so the edge has no S3-specific code.

## Amendment 2026-10-13: the AWS host names are not base domains

- `s3.amazonaws.com` and `s3.<region>.amazonaws.com` are not base domains. Every supported client takes `AWS_ENDPOINT_URL`. Serving the AWS names over TLS would need a certificate for `*.amazonaws.com`. The loopback host policy (DNS rebinding) answers only names the operator chose under `localhost`, IP literals and `localhost.localstack.cloud`. The configuration and the host policy are unchanged. Capturing the AWS names is not built; it belongs to a DNS ADR.

## Amendment 2026-10-14: the documented endpoint is an IP literal

- The decision's "the documented endpoint is `http://localhost:4566`" no longer holds. `understudy env` prints the endpoint as the banner and the ready file give it, an IP literal (`http://127.0.0.1:4566`) by default, so the two blocks a user copies agree; with `--tls` it prints the TLS listener's URL. Its S3 notes follow the host form of the endpoint it prints. At an IP literal the Go and JS SDKs and the Terraform provider address S3 path style by themselves, and Terraform needs `skip_requesting_account_id = true`, since its S3 Control bucket tag reads go to `<account>.<IP>`, which cannot resolve. (The second amendment of 2026-10-26 adds `skip_credentials_validation = true`.) Virtual-hosted S3 in Go and JS, and Terraform bucket tag reads with no skip setting, need a localhost endpoint the user chooses (`AWS_ENDPOINT_URL=http://localhost:4566`) and `*.localhost` resolving, which `understudy doctor` checks. At a host name `env` keeps the path-style notes of the decision.

## Amendment 2026-10-26: execute-api addressing

- API Gateway's data plane is addressed as function URLs are (`apigateway-http.md 11.3`): host `<api-id>.execute-api.<region>.<base>` or `<api-id>.execute-api.<base>`, on every listener, and the path fallback `/_understudy/execute-api/<api-id>/...` on the main listener. Both are claimed before S3 virtual hosting and whatever the credentials (ARCHITECTURE.md 4.1 rules 1 and 2), so `abc123.execute-api.us-east-1.localhost` is never the bucket `abc123.execute-api.us-east-1`, as AWS serves execute-api hosts apart from S3 (`apigateway-http.md 4.1`). A bucket of that name stays reachable path style. The host claim ends at a configured base domain, so a regional S3 virtual host such as `abc123.execute-api.bar.s3.us-east-1.localhost` stays the bucket `abc123.execute-api.bar`, as `<bucket>.s3.<region>.amazonaws.com` is S3 on AWS whatever the bucket's name.
- Until API Gateway binds the data plane, both forms answer 501 with API Gateway's `{"message": ...}` body (`apigateway-http.md 4.5`) naming the missing feature and the issues link, and are journaled as `Unclaimed`. (API Gateway has since bound it; ADR 0019, amendment 2026-10-31, says which front serves an id. The 501 remains for a build with no front registered.) The browser defences refuse neither form: any page may call an execute-api endpoint on AWS.

## Amendment 2026-10-26: Terraform at an IP literal also needs `skip_credentials_validation`

- The 2026-10-14 amendment's Terraform note is incomplete: `skip_requesting_account_id = true` alone does not keep the account id away. With credential validation on, the provider takes the account from its startup `sts:GetCallerIdentity` and never consults `skip_requesting_account_id` (tf-messaging.md 1.1), so its bucket tag reads still go to `<account>.<IP>`. Provider 6.68.0 with that setting alone failed on an `aws_s3_bucket` with `lookup 000000000000.127.0.0.1: no such host` (testcontainers.md 9.6). At an IP literal Terraform needs `skip_credentials_validation = true` as well, and `understudy env --sdk terraform` and `docs/guide/terraform.md` name both settings.

## Amendment 2026-11-13: which errors carry `x-amz-bucket-region`

- The decision's "`x-amz-bucket-region` on `HeadBucket` and bucket-addressed errors" is narrowed. The header goes on `HeadBucket`; on every `ListObjectsV2` answer, its 200s and its errors; on the cross-region refusals, `AuthorizationHeaderMalformed` and `PermanentRedirect`; and on every error about an existing bucket to a request that asserts no region (amendment 2026-10-08, kept). Every other error to a request that asserts the bucket's own region answers without it.
- The recordings settle it. Of the 177 recorded S3 error responses (every 4xx under `testdata/parity/s3/` and `testdata/parity/sqs/R17/` on 2026-11-13), 8 carry the header, and all 8 are `ListObjectsV2` errors: `s3/IAM-ENF/12-list.json`, `s3/IAM-ENF/13-s3.json`, `s3/IAM-EVAL-18/{a,b,d,e,f}-*.json` and `s3/R12/05-list-max-keys-abc.json`. The rest carry none, among them a same-region not-configured 404 (`s3/R06/01-get-get-bucket-policy.json`, `NoSuchBucketPolicy`), the other bucket subresource errors and the object errors.
- `ListObjects` (v1) and `ListObjectVersions` are unrecorded and take the default, no header. The `ListObjectsV2` errors S3 answers before it looks the bucket up (an IAM denial built by the kernel, the codec's refusal of a `max-keys` that is not an integer) still lack the header; `docs/guide/differences.md` names them.
