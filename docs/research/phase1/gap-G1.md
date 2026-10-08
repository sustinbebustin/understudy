# Gap G1: rpcv2Cbor wire format and the CloudWatch three-protocol contradiction

Status: closed for phase 1 design. Full write-up, contract and 24 Go-SDK plus 13 Smithy-corpus vectors: [wire-cbor.md](wire-cbor.md). Nothing touched real AWS.

## Outcome

- **Which client sends what to CloudWatch** (wire-captured against loopback stubs): aws-sdk-go-v2 cloudwatch v1.73.0 (the Terraform v6.68.0 pin) sends rpcv2Cbor; boto3 1.43.109, AWS CLI 2.36.38 and `@aws-sdk/client-cloudwatch` 3.1146.0 send `awsJson1_0` with `x-amzn-query-mode: true`. botocore's legacy `protocol` field says CBOR but `resolved_protocol` returns `json` ([model.py L436](https://github.com/boto/botocore/blob/1.43.109/botocore/model.py#L436), [utils.py L194](https://github.com/boto/botocore/blob/1.43.109/botocore/utils.py#L194)). Among the phase 1 services only CloudWatch is CBOR in Go v2.
- **Server contract** extracted from the Smithy 1.74.0 spec and all 92 `rpcv2Cbor` test cases (68 server-runnable: 39 requests + 29 responses; the old 45 and 47 were request and response totals, not server counts).
- **Exact Go request bytes** for PutMetricAlarm, DescribeAlarms, ListTagsForResource, TagResource, UntagResource, DeleteAlarms, PutMetricData and GetMetricData, and what the Go deserializer accepts and rejects on success and error responses.
- **Minimum codec**: about 340 lines of stdlib Go; a prototype decoded all 83 non-empty corpus bodies, took 3M mutated inputs without a panic, and satisfied the real Go client across an alarm lifecycle.

## Findings that change the implementation

1. A 200 response to a `Unit`-output operation (PutMetricAlarm, DeleteAlarms, PutMetricData, 7 more) with any body member panics the Go client; send an empty body.
2. An error response with an empty body fails the Go client with "failed to decode response body"; always send `{"__type": ..., <message>}`.
3. Typed errors read the modeled message member (`Message` vs `message` per shape); the table of all 19 CloudWatch error shapes is in wire-cbor.md 3.5.
4. Terraform's destroy accepts only the Go type `ResourceNotFoundException`, while the model lists `ResourceNotFound` for `DeleteAlarms`; answer a missing alarm with success.
5. No CloudWatch operation has `Unit` input, so the Go client sends `bfff` with `Content-Type`, not an empty body.
6. `PutMetricData` bodies of 10240 bytes or more arrive gzipped; the SigV4 payload hash covers the compressed bytes (verified with a stdlib verifier, 4 of 4).

## Corrections made in place (each marked `CORRECTED (G1)`, citing wire-cbor.md)

| File | Lines | Claim fixed |
|---|---|---|
| smithy-models.md | 12, 173, 188, 278, 397 | boto3 and CLI do not default to rpcv2Cbor; legacy `protocol` field vs `resolved_protocol`; "no-input operations have no body" applies to `Unit` input only |
| tf-compute.md | 29, 31, 32, 383, 412 | boto3 "does the same" (no); empty input sends `bfff` for CloudWatch; the CLI/botocore question settled; smithy-go version checked |
| smithy-protocol-tests.md | 85, 106, 292, 303 | botocore "confirmed CBOR" (no); "add the 47 CBOR server cases" (68); the 45 and 47 are request and response totals |
| secrets-logs-events-alarms.md | 40, 491, 521, 570 | CLI no longer "assumed"; Terraform delete treats `ResourceNotFoundException`, not `ResourceNotFound`, as success; Q9 answered for the Go client |
| s3-checksum-residuals-and-record-backlog.md | 158 | "botocore: rpcv2Cbor" |
| gap-G8.md | 19 | "botocore uses CBOR" |
| wire-json-query.md | 3, 48, 561 | stale "CBOR researcher" pointers now link to wire-cbor.md |

## Residual UNVERIFIED (evidence needed; full register in wire-cbor.md section 11)

- Real CloudWatch CBOR bytes: definite versus indefinite, float widths, `__type` for unlisted validation errors, message member casing, `x-amzn-query-error` for shapes without `awsQueryError`. Needs a human-run record against AWS.
- `DeleteAlarms` of a missing alarm, and the response to unknown operations on the CBOR route.
- Other CBOR clients' response checks (the Go client never checks `Smithy-Protocol`).
- A full `terraform plan` / `apply` / second plan / `destroy` against a CBOR server (no server exists yet).
- Future SDK releases changing the protocol they pick; rerun wire-cbor.md 2.1 at each pin bump.
