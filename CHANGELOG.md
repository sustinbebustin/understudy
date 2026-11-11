# Changelog

Notable changes to understudy, in the form of [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions follow [Semantic Versioning](https://semver.org/); before 1.0 a minor version may break compatibility. A release's GitHub notes are its section here: `release.yml` refuses a tag without one.

## [Unreleased]

### Added

- Parameter Store puts a `Parameter Store Change` event on the account and Region's default EventBridge bus for each committed create, overwrite, label and delete, with the envelope and detail AWS recorded: source `aws.ssm`, the parameter ARN in `resources`, and `name`, `description`, `type` and `operation` (`Create`, `Update`, `LabelParameterVersion` or `Delete`) in `detail`, plus `label`, `fromVersion` and `toVersion` on a label event. A refused call sends nothing. One event per parameter for `DeleteParameters`, `"description": ""` for a parameter without one, and no event for `UnlabelParameterVersion` are inferred.
- The journal records SQS moves between queues as `redrive` events: a receive that moves messages past a queue's `maxReceiveCount` to its dead-letter queue, and each step of a message move task. Each names both queues, the moved message ids and why, and is a child of the receive or of the move task's timer, so `understudy journal --kind redrive` and traces show how a message reached a dead-letter queue. The CLI and the web UI summarize them.
- `serve --tls --persist` keeps the TLS listener's key and certificate in the data directory and reuses them at the next start, so a CA bundle `env --tls` wrote keeps working across restarts. A kept certificate that is unreadable, expires within a day, or differs from the one the running build issues is replaced, with a warning naming why. Without `--persist` the certificate is still new at every start.
- Step Functions runs a `lambda:invoke` Task whose `InvocationType` is `Event`, given literally or through a path, where it refused a literal `Event` as `UNSUPPORTED_BY_UNDERSTUDY` and failed a path to it with `States.Runtime`. The Task hands the payload to Lambda's asynchronous invoke and succeeds without waiting for the function, with the recorded result: `StatusCode` 202, `Payload` `null`, no `ExecutedVersion`, and `SdkHttpMetadata` with AWS's six headers in AWS's orders. `DryRun` is still refused at create and fails at run time. The Step Functions snapshot section moves to state version 2, so a snapshot or `--persist` state file holding Step Functions state written by an earlier version is refused with a message naming both versions.
- `GET /_understudy/v1/health` names the TLS listener as `tlsEndpoint` once it accepts, the value the ready file and the banner give; it is absent without `--tls`. `understudy env --tls` reads it, so it finds a TLS listener on any port without `--tls-endpoint`. It still tries the endpoint's host at port 4567 when health names none or cannot be read, and `--tls-endpoint` still wins.

### Changed

- `understudy journal --limit N` fetches the newest events in one request, where it paged through every match. The notice for left-out events no longer gives the total: it reads `showing the newest N matching events; ...`, where it read `showing the newest N of M matching events; ...`.
- Under `--persist`, a running clock advanced with `understudy clock advance` keeps that advance across a restart, however long the server was stopped. A restart used to resume it at the later of its saved time and wall time, so a downtime longer than the advance absorbed it. Checkpoints record the wall time of their cut in the manifest (`cutWall`); an explicit `snapshot save` does not, so its archives stay byte-deterministic. A state file written before this resumes as before.
- awsJson success bodies (SQS, DynamoDB, DynamoDB Streams, CloudWatch, Logs, KMS, Secrets Manager, EventBridge, Step Functions, SSM and Kinesis) list each structure's members sorted by name, as AWS writes them, where they were in model order. Map entries, such as an SQS message's `Attributes`, and document values keep their order, and error bodies do not change. For Kinesis, which has no recording, the order is inferred. A client that decodes the JSON is unaffected; one that compares bytes now matches AWS.
- The warning for an operation missing from the vendored model names the model it checked, as the `model_source` log attribute (`api-models-aws@1cfdfb4 sqs-2012-11-05.json`: the model commit and file), so a client newer than the pinned model is easy to spot.

### Removed

- The `s3.maxObjectBytes` and `dynamodb.registry` configuration keys. `serve` parsed them and then refused to start, because nothing applied either. A file that names them now fails as an unknown key, naming it. S3 already enforces AWS's 5 GiB single-PUT limit, and DynamoDB Local already falls back from Docker Hub to the ECR Public mirror when a pull fails.

### Fixed

- An HTTP API refuses a request whose request line plus headers are over 10,240 bytes with `431` `{"message":"Request Header Fields Too Large"}`, where it did not limit headers, and answers a body over 10 MiB (10,485,760 bytes) with `413` `{"message":"Request Entity Too Large"}` without a `Content-Type`, as AWS's front layer does; both carry `apigw-requestid`. A body over 10,000,000 bytes but not over 10 MiB, or an event over Lambda's 6 MB limit, keeps the gateway's `413` with `Content-Type: application/json`.
- Step Functions accepts a JSONPath function such as `$.a.length()` in a path at create, as AWS does, where it refused it as `UNSUPPORTED_BY_UNDERSTUDY`. As on AWS, the execution fails when a state evaluates the path, with `States.Runtime` and the cause `The JSONPath '$.a.length()' specified for the field 'len.$' could not be found in the input '...'`. Other function names are inferred to fail the same way. Multi-clause filters and filter scripts are still refused.
- API Gateway HTTP API `UpdateIntegration` on an AWS service integration refuses `requestParameters` that leave out a parameter the subtype requires, with AWS's `BadRequestException` `Invalid integration URI specified`, and changes nothing, where understudy merged the sent map into the stored one and stored the update. An update that sends every required parameter, as Terraform does, or sends no `requestParameters`, merges as before. The rule is inferred from one recorded case.
- Parameter Store's `InvalidKeyId` and `AccessDeniedException` for a failed KMS call end KMS's message with the KMS client's suffix, as AWS does: ` (Service: AWSKMS; Status Code: 400; Error Code: DisabledException; Request ID: <uuid>; Proxy: null)`. The request id is a fresh one, since understudy's KMS call has no id of AWS's to name. A KMS server error still answers `InternalServerError` in understudy's own words.
- Step Functions `StartExecution` limits `input` to 262,144 bytes in UTF-8, as AWS does, where it counted characters, so a non-ASCII input AWS refuses is now refused. The refusal reads `Member must have size less than or equal to 262144 bytes in UTF-8 encoding`, AWS's recorded text, where it read `Member must have length less than or equal to 262144`.
- SSM and API Gateway REST report a scalar list element's validation failure once at the list, as AWS does: the value is the whole list and the constraint lists every constraint on the element, as in `Value '[WRONG]' at 'createRestApiInput.endpointConfiguration.types' failed to satisfy constraint: Member must satisfy constraint: [Member must satisfy enum value set: [PRIVATE, EDGE, REGIONAL]]`, where understudy named the element (`types.1.member`). A member of a structured element keeps its indexed path. API Gateway lists `EndpointType` values in AWS's order. The rolled-up forms for map keys and values are inferred for both services.
- The Go `admin.Client` no longer tells a caller to start a server that is running. A call whose context ended, such as an `understudytest` `Settle` in a test that timed out, fails with an error naming the call that wraps the context's error, so `errors.Is` sees `context.Canceled` or `context.DeadlineExceeded`. A call the server took and sent no answer to, because the HTTP client's response bound ran out or the connection failed after the request was sent, is the new `*admin.NoAnswerError`, exit code 3. Only a failure to connect is still `*admin.UnreachableError`.
- The CLI no longer tells a user to start a server that is running. A command ended by the CLI itself, by Ctrl-C or by `settle` running past its grace, exits 1 with an error naming the call it gave up, where it exited 3 saying to start the server; `coverage` then fails rather than printing the offline report. A server that closes the connection after taking a call exits 3 saying it took the call but sent no answer.
- Under `--persist`, a periodic checkpoint saves a clock freeze, set or scale, a seeded id draw and a journaled event, where it waited for a state change or shutdown. After a crash, a seeded run no longer draws ids it already drew, and the journal no longer reissues event ids already in `journal.file`. Any API traffic now causes one write per `--persist-every`; an idle server still writes nothing.
- Under `--persist`, a periodic or shutdown checkpoint waits for a running reset instead of cutting inside it. A cut between a reset's DynamoDB table deletion and its cell reset, or before its fault-rule clear, saved a state a crash would then restart into.
- A request-phase latency fault on one delivery's target call, such as SQS `SendMessage` on one queue, delays that delivery alone. Every delivery due after it waited too, so an SNS fan-out to N queues under such a rule took N times the latency.
- With `journal.file` set, `serve` exits within 5 seconds of a shutdown even when the file's disk has stopped answering, such as a stalled network mount, where it waited for the disk forever. It then exits with an error that names the journal file and says how many events it may lack; the file ends at an event boundary except for a write in progress.
- `understudy version` names the version of a binary installed with `go install github.com/sustinbebustin/understudy/cmd/understudy@vX.Y.Z`, where it said `dev`. Without the release's stamp, the version is the module version the go command records, or the git version of a build in a checkout.
- Under `--iam-enforce enforce`, a denied Kinesis call carries its message under `Message`, as AWS does, where it said `message`.

## [0.1.2] - 2026-11-09

The first complete release.

### Fixed

- API Gateway HTTP API `GetApis` lists APIs by id ascending, where it listed them in creation order, and its `nextToken` is AWS's form (the URL-escaped base64 of `id=S` and the page's last id), as REST `GetResources` already was. The id order is inferred from one recorded page; the token form is recorded. `GetRoutes`, `GetIntegrations` and the other lists keep creation order and their own tokens, which are unrecorded. The API Gateway HTTP API snapshot section moves to state version 2, so a snapshot or `--persist` state file holding HTTP APIs written by an earlier version is refused with a message naming both versions.
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
