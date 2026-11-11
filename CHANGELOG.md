# Changelog

Notable changes to understudy, in the form of [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions follow [Semantic Versioning](https://semver.org/); before 1.0 a minor version may break compatibility. A release's GitHub notes are its section here: `release.yml` refuses a tag without one.

## [Unreleased]

### Added

- Under `--iam-enforce enforce`, Lambda `CreateFunction`, and `UpdateFunctionConfiguration` when it passes a `Role`, refuse an execution role that `lambda.amazonaws.com` may not assume, because its trust policy does not allow it or it does not exist, with AWS's `InvalidParameterValueException` `The role defined for the function cannot be assumed by Lambda.` Under `advise` the call is served and the denial journaled; under `off` nothing is asked. The check on `UpdateFunctionConfiguration` is inferred: only `CreateFunction` was recorded.
- Under `--iam-enforce enforce`, Lambda `PutFunctionEventInvokeConfig` and `UpdateFunctionEventInvokeConfig` refuse an SNS or SQS destination the function's execution role may not write to, with AWS's `InvalidParameterValueException` `The function execution role does not have permissions to call Publish on <topic arn>` (`SendMessage` for a queue). Under `advise` the call is served and the denial journaled; under `off` nothing is asked. Function, event bus and S3 bucket destinations are not checked.
- The journal records SQS moves between queues as `redrive` events: a receive that moves messages past a queue's `maxReceiveCount` to its dead-letter queue, and each step of a message move task. Each names both queues, the moved message ids and why, and is a child of the receive or of the move task's timer, so `understudy journal --kind redrive` and traces show how a message reached a dead-letter queue. The CLI and the web UI summarize them.
- `serve --tls --persist` keeps the TLS listener's key and certificate in the data directory and reuses them at the next start, so a CA bundle `env --tls` wrote keeps working across restarts. A kept certificate that is unreadable, expires within a day, or differs from the one the running build issues is replaced, with a warning naming why. Without `--persist` the certificate is still new at every start.
- `GET /_understudy/v1/health` names the TLS listener as `tlsEndpoint` once it accepts, the value the ready file and the banner give; it is absent without `--tls`. `understudy env --tls` reads it, so it finds a TLS listener on any port without `--tls-endpoint`. It still tries the endpoint's host at port 4567 when health names none or cannot be read, and `--tls-endpoint` still wins.

### Changed

- `understudy journal --limit N` fetches the newest events in one request, where it paged through every match. The notice for left-out events no longer gives the total: it reads `showing the newest N matching events; ...`, where it read `showing the newest N of M matching events; ...`.
- Under `--persist`, a running clock advanced with `understudy clock advance` keeps that advance across a restart, however long the server was stopped. A restart used to resume it at the later of its saved time and wall time, so a downtime longer than the advance absorbed it. Checkpoints record the wall time of their cut in the manifest (`cutWall`); an explicit `snapshot save` does not, so its archives stay byte-deterministic. A state file written before this resumes as before.
- The warning for an operation missing from the vendored model names the model it checked, as the `model_source` log attribute (`api-models-aws@1cfdfb4 sqs-2012-11-05.json`: the model commit and file), so a client newer than the pinned model is easy to spot.

### Removed

- The `s3.maxObjectBytes` and `dynamodb.registry` configuration keys. `serve` parsed them and then refused to start, because nothing applied either. A file that names them now fails as an unknown key, naming it. S3 already enforces AWS's 5 GiB single-PUT limit, and DynamoDB Local already falls back from Docker Hub to the ECR Public mirror when a pull fails.

### Fixed

- A resource policy naming a bare account id as an AWS principal, `"Principal":{"AWS":"123456789012"}`, is stored and read back as `"arn:aws:iam::123456789012:root"`, as AWS does, for SQS `CreateQueue` and `SetQueueAttributes`, SNS `CreateTopic` and `SetTopicAttributes`, S3 `PutBucketPolicy`, and KMS `CreateKey` and `PutKeyPolicy`. The rest of the policy reads back as sent, and evaluation is unchanged. The `sqs.`, `sns.`, `s3.` and `kms.resource-policy.account-id-principal` switches record it, and the `iam.policy.principal-existence` switch discloses that a principal a policy names is not checked for existence, which AWS checks.
- Under `--iam-enforce enforce`, Lambda `CreateFunction`, and `UpdateFunctionConfiguration` when it passes a `Role`, refuse a caller without `iam:PassRole` on the execution role with `AccessDeniedException`, as AWS does, before the role's trust is checked. The role was recorded in the access record but never asked about. The pass carries `iam:PassedToService` `lambda.amazonaws.com`, so a policy from `/iam/policy` grants it on that condition.
- Under `--iam-enforce enforce`, a denied list operation names the resource AWS names: `on resource: arn:aws:cloudwatch:<region>:<account>:alarm:*` for CloudWatch `DescribeAlarms`, `table/*/stream/*` for DynamoDB Streams `ListStreams`, `schedule/*/*` for Scheduler `ListSchedules`, `stateMachine:*` for Step Functions `ListStateMachines`, `arn:aws:ssm:<region>:<account>:*` for SSM `DescribeParameters` and `*` for SES v2 `ListEmailIdentities`. The message named no resource. Policies are still evaluated, and generated, on `*`.
- Under `--iam-enforce enforce`, a call naming an SQS queue or S3 bucket that does not exist answers `QueueDoesNotExist` or `NoSuchBucket` before it is authorized, as AWS does, where it answered the denial. A `GetObject`, `HeadObject` or `GetObjectAttributes` of a missing key asks `s3:ListBucket` on the bucket before `s3:GetObject`, so a caller denied both gets the `s3:ListBucket` denial AWS gives, and one allowed `s3:ListBucket` gets `NoSuchKey`. The `sqs.enforce.missing-resource` and `s3.enforce.missing-resource` switches record the order, and `kms.enforce.missing-resource.by-name` is now aws-recorded for key ids. S3 Control's tag operations still deny first.
- `GetSessionToken` signed with temporary credentials from an earlier `GetSessionToken` is refused under `--sigv4 parse`, as it already was under `verify` and as AWS refuses it. The edge now resolves the signing key in both modes and carries it on the call (`kernel.Call.SigningKey`).
- The Go `admin.Client` no longer tells a caller to start a server that is running. A call whose context ended, such as an `understudytest` `Settle` in a test that timed out, fails with an error naming the call that wraps the context's error, so `errors.Is` sees `context.Canceled` or `context.DeadlineExceeded`. A call the server took and sent no answer to, because the HTTP client's response bound ran out or the connection failed after the request was sent, is the new `*admin.NoAnswerError`, exit code 3. Only a failure to connect is still `*admin.UnreachableError`.
- The CLI no longer tells a user to start a server that is running. A command ended by the CLI itself, by Ctrl-C or by `settle` running past its grace, exits 1 with an error naming the call it gave up, where it exited 3 saying to start the server; `coverage` then fails rather than printing the offline report. A server that closes the connection after taking a call exits 3 saying it took the call but sent no answer.
- Under `--persist`, a periodic checkpoint saves a clock freeze, set or scale, a seeded id draw and a journaled event, where it waited for a state change or shutdown. After a crash, a seeded run no longer draws ids it already drew, and the journal no longer reissues event ids already in `journal.file`. Any API traffic now causes one write per `--persist-every`; an idle server still writes nothing.
- Under `--persist`, a periodic or shutdown checkpoint waits for a running reset instead of cutting inside it. A cut between a reset's DynamoDB table deletion and its cell reset, or before its fault-rule clear, saved a state a crash would then restart into.
- A request-phase latency fault on one delivery's target call, such as SQS `SendMessage` on one queue, delays that delivery alone. Every delivery due after it waited too, so an SNS fan-out to N queues under such a rule took N times the latency.
- With `journal.file` set, `serve` exits within 5 seconds of a shutdown even when the file's disk has stopped answering, such as a stalled network mount, where it waited for the disk forever. It then exits with an error that names the journal file and says how many events it may lack; the file ends at an event boundary except for a write in progress.
- `understudy version` names the version of a binary installed with `go install github.com/sustinbebustin/understudy/cmd/understudy@vX.Y.Z`, where it said `dev`. Without the release's stamp, the version is the module version the go command records, or the git version of a build in a checkout.
- Under `--iam-enforce enforce`, a denied Kinesis call carries its message under `Message`, as AWS does, where it said `message`.
- Under `--iam-enforce enforce`, a denied call to API Gateway (REST), CloudWatch (JSON), DynamoDB, DynamoDB Streams, EventBridge, KMS, CloudWatch Logs, Scheduler, SES v2, Step Functions or SQS (JSON) carries its message under `Message`, and API Gateway v2 under `message`, as AWS does, where each said the other.
- Under `--iam-enforce enforce`, a denied CloudWatch call over JSON or CBOR that does not send `x-amzn-query-mode: true` answers 400 without `x-amzn-query-error`, as AWS does over JSON, where it answered 403 with `AccessDenied;Sender`. In query mode it still answers 403 with the header, and Query denials and other CloudWatch errors are unchanged. The overlay trait `dev.understudy#queryErrorHeader` limits the header to query mode for one error shape.
- The access record and generated policies hold the version actions AWS needs for a versioned S3 read: a `HeadObject` with a version ID records `s3:GetObjectVersion`, and a `GetObjectAttributes` with one records `s3:GetObjectVersion` and `s3:GetObjectVersionAttributes`. The Service Reference omits them, so a generated policy lacked them and failed on AWS.

## [0.1.2] - 2026-11-09

The first complete release.

### Fixed

- The Homebrew cask and the Testcontainers modules are published: `brew install --cask sustinbebustin/tap/understudy`, the Go module `github.com/sustinbebustin/understudy/testcontainers/go`, and `testcontainers-understudy` on PyPI and npm. Use 0.1.2 or later for any of them: 0.1.0 and 0.1.1 have binaries and images only.

## [0.1.1] - 2026-11-09

### Fixed

- A release pipeline fix meant to publish the Homebrew cask, which did not. The binaries and the image are 0.1.0's with a new version; 0.1.1 has no cask and no Testcontainers modules.

## [0.1.0] - 2026-11-09

The release stopped before its Homebrew cask and Testcontainers modules, which are available from 0.1.2.

### Added

- First release: one binary and a multi-arch image (`ghcr.io/sustinbebustin/understudy`, linux/amd64 and linux/arm64) that stand in for eighteen AWS services through `AWS_ENDPOINT_URL`: SQS, SNS, S3, DynamoDB and DynamoDB Streams, Kinesis Data Streams, Lambda, CloudWatch Logs, Secrets Manager, KMS, IAM, STS, EventBridge, EventBridge Scheduler, CloudWatch, API Gateway (HTTP and REST APIs), SSM Parameter Store, Step Functions and SES. Coverage is partial and listed operation by operation in `docs/coverage.md`; an unimplemented operation answers 501.
- Binaries for linux and darwin on amd64 and arm64, and a Homebrew cask: `brew install --cask sustinbebustin/tap/understudy`.
- One emulator clock for every AWS timer, a causal journal of every call and delivery with a web UI, fault injection, snapshots and opt-in persistence.
- Lambda functions run in AWS's base images through Docker, with DynamoDB Local behind DynamoDB. Docker Desktop on macOS is supported, not CI-tested.
- Testcontainers modules for Go (`github.com/sustinbebustin/understudy/testcontainers/go`), Python and Node (`testcontainers-understudy` on PyPI and npm), versioned with understudy, whose default image is the release's.
- `understudy iam policy` prints the least-privilege policy the recorded calls imply, and `--iam-enforce advise|enforce` evaluates identity, resource, trust and key policies.
