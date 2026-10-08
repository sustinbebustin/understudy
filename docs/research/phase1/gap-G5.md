# Gap G5: DynamoDB Local licence, offline behaviour and telemetry

Status: closed for telemetry and offline behaviour; licence reading still needs counsel. Full evidence: [dynamodb-local-terms.md](dynamodb-local-terms.md). Date: 2026-10-08.

## Resolved (OBSERVED, `amazon/dynamodb-local@sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab`, 3.3.1)

- `DDB_LOCAL_TELEMETRY=0` alone and `-disableTelemetry` alone each disable telemetry: zero DNS and zero connections over a create/put/scan/TTL cycle plus 5 minutes idle. Only the literal `0` works; `false`, `FALSE`, `1`, `true` do not, despite the [AWS Telemetry page](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocalTelemetry.html) saying `false`.
- With telemetry on, Local contacts `cognito-identity.us-east-1.amazonaws.com` then `pinpoint.us-east-1.amazonaws.com` once, at startup. No traffic for API calls, idle time or `docker stop`.
- With the image local, Local starts and serves under `--network none`. It logs a harmless log4j hostname stack trace there.
- Docker Hub and ECR Public serve the identical index, platform and attestation digests. `LICENSE.txt` is byte-identical in the images, the v1/v2/v3 zips and the Maven jars 3.0.0 to 3.3.1.

## Still a legal question (SOURCE text, INTERPRETATION needed)

- The licence says "You may not use the Software if you do not have an AWS account in good standing" and limits use to "your internal business purposes" and "in connection with the Services" ([LICENSE.txt in the image](https://hub.docker.com/r/amazon/dynamodb-local)).
- No AWS document found (Customer Agreement, Service Terms, IP License, Site Terms, DynamoDB Local docs, either registry page) addresses third-party emulators or proxy-backed use. LocalStack and Floci print no licence notice; Floci leaves running Local to the user.

## Brief statements to caveat

"No account" needs an exception for the DynamoDB service. "No telemetry" holds only if understudy passes the opt-out (value exactly `0`, or the flag). "No network at runtime" holds after the first pull. The brief's "one exception" sentence should mention that the DynamoDB Local pull carries a licence with account terms. Table and proposed first-pull notice text: [dynamodb-local-terms.md](dynamodb-local-terms.md) sections 5.1 and 5.2.

## Claims corrected elsewhere

- [dynamodb-local.md](dynamodb-local.md) sections 1.1, 1.4, 2, "Implications" item 1 and 11, and the open-questions list: ECR mirror digest confirmed, env var and flag verified, "false" caveat added.
- [localstack-vs-aws-tooling.md](../localstack-vs-aws-tooling.md) offline row: DynamoDB Local telemetry and account caveat added.
