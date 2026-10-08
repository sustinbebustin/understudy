# Gap G7: cross-service validation and error identity

Closed in [error-matrix.md](error-matrix.md). Research date 2026-10-08; models at api-models-aws [1cfdfb4](https://github.com/aws/api-models-aws/commit/1cfdfb4bb9ea858e65623e66d73af04bec353201); AWS-validated recordings from localstack [8b9a79f](https://github.com/localstack/localstack/commit/8b9a79f05846835cf4dff63ab7eefdde9df83783) (1,617 error records over the 12 services, 1,600 with a `last_validated_date`); real-DynamoDB-capable assertions from dynalite [c5e5b46](https://github.com/architect/dynalite/commit/c5e5b46ef5e51e7d907411c001db7839dd146088); Smithy [ec88e02](https://github.com/smithy-lang/smithy/commit/ec88e02aa1e6315f3453b1eb99625352750282f3).

## Outcome

An implementer can now answer "what does service X return when ..." from the per-service tables (error-matrix.md section 5) for every trigger where a recording exists, and sees an explicit `UNVERIFIED` plus a recording case ID where none does (57 lines carry the tag; section 10 holds 37 case rows, each covering up to seven services, as exact `curl` scripts).

## Verification of the smithy-models.md section 7 claim at the pin

| Claim | Result |
|---|---|
| No phase 1 model declares service-level `errors` | Confirmed, 12 of 12. |
| Error-shape counts (19, 21, 35, 13, 38, 48, 56, 21, 12, 34, 28, 12) | Confirmed. |
| DynamoDB lacks `ValidationException`, `AccessDeniedException`, `UnrecognizedClientException`, `MissingAuthenticationTokenException`, `IncompleteSignatureException`, `ServiceUnavailable`, `SerializationException` | Confirmed. It does model `ThrottlingException`, `RequestLimitExceeded`, `InternalServerError`. |
| KMS, IAM, STS, Secrets Manager lack `ValidationException`, `ThrottlingException`, `AccessDeniedException`; only Logs, SNS, CloudWatch have `ValidationException` | Confirmed (Lambda, EventBridge, SQS, S3 also lack it: 9 of 12). |
| 20 S3 codes absent from the model | Confirmed, all 20. S3 error structures carry no members except `InvalidObjectState`. |

The claim understates the gap: AWS returns `ValidationException` or `ValidationError` from 10 of the 12 services (not S3, not SQS) although 9 models lack the shape.

## Findings an implementer must act on

1. **AWS validation text is one grammar and it differs from the Smithy tests'.** `N validation error(s) detected: Value 'v' at 'lowerCamelPath' failed to satisfy constraint: Member must ...`, joined by `; ` (EventBridge, Lambda, KMS, Logs, DynamoDB, CloudWatch, SNS, IAM, STS). The Smithy `malformed-*` validation cases use a different wording and a `fieldList`; use them for routing and status only.
2. **The code for a constraint violation is per operation.** Lambda, Logs and SQS each emit two or three different codes for the same kind of failure. A `(service, operation) -> code` overlay is required.
3. **`__type` strings are not derivable from the model.** 1.1 services send bare shape names (Secrets Manager proven raw); 1.0 services use service-specific namespaces (`com.amazonaws.dynamodb.v20120810`, `com.amazonaws.cloudwatch.v2010_08_01`, `com.amazonaws.sqs`); framework errors use `com.amazon.coral.validate#` and `com.amazon.coral.service#`. SQS JSON also sends unmodeled exception names (`MissingRequiredParameterException`, `InvalidParameterValueException`).
4. **DynamoDB Local is the wrong oracle for generic errors.** Malformed JSON, missing target, wrong Content-Type and unsigned requests all differ from real DynamoDB. understudy must answer them before proxying.
5. **Patterns echo without anchors in 15 of 18 recorded cases; enum lists are in an unspecified order.** Derive the first, do not promise the second.
6. **S3 is wholly hand-owned** (error table and extra elements).

Generator split and the hand-owned overlay list: error-matrix.md section 7.

## Corrections made in other research files (in place, with note and source)

| File | Claim changed |
|---|---|
| [smithy-models.md](smithy-models.md) section 7 | DynamoDB validation `__type` string no longer UNVERIFIED (sourced); S3 error-table pointer added with the member finding. |
| [wire-json-query.md](wire-json-query.md) | Section 9.2 unknown `Action` for SQS Query resolved (`InvalidAction`, 400, `The action FooBar is not valid for this endpoint.`); section 8.1 CloudWatch error `xmlns` resolved; section 5.4 SQS `RequestThrottled` is 403 in the model, not 400; section 5.3 Secrets Manager real body confirmed; section 3.1 DynamoDB type strictness evidence; section 11 note that the DynamoDB Local answers are not real DynamoDB's. |
| [secrets-logs-events-alarms.md](secrets-logs-events-alarms.md) 7.4 | CloudWatch path example `MetricData.member.1.Value` is not a validation path. |
| [lambda-api.md](lambda-api.md) 2.1 | `ResourceNotReadyException` uses `message`; Lambda `ValidationException` body key and `Type` are UNVERIFIED (not "Message only, no Type"); `RequestEntityTooLargeException` is unmodeled. Also notes that this file's model pin (f3c2380) is not the project pin. |
| [iam-sts-kms.md](iam-sts-kms.md) | KMS `plaintext` max-length text, alias prefix text, enum-set text and `KeyUsage ... not compatible` text confirmed against AWS-validated recordings; "use the generated Smithy constraint messages" replaced by the AWS grammar. |
| [smithy-protocol-tests.md](smithy-protocol-tests.md) section 6 | "Whether real services reproduce the Smithy validation wording is UNVERIFIED" resolved: they do not. |

## What stays open

- Wire bytes for the services whose recordings are boto3-parsed only: KMS, Logs, EventBridge `__type` (bare vs namespaced), IAM and STS error `xmlns`, Lambda `ValidationException` body, SQS unmodeled `__type` namespace.
- Auth failures and unknown operations for all JSON and Query services except DynamoDB (DYN) and SQS Query; throttling codes everywhere; request-too-large for most services; malformed-body and wrong-Content-Type cases outside DynamoDB and Lambda. All are listed as cases in error-matrix.md section 10.
- Recorded behaviors of DYN and LocalStack are third-party. Where they disagree with AWS documentation (DynamoDB auth messages, generic statuses 400 vs 403) both are shown and the cell stays open until recorded.

## Limits of this research

No call was made to AWS. The LocalStack corpus is boto3-parsed except where marked REC-RAW. A host hook blocked reading the Secrets Manager snapshot by path, so its content was taken from the JSON parse only. The S3 `ErrorResponses` page could not be extracted; `API_Error.html` was used.
