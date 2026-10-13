# Contributing to understudy

Thanks for helping. This file covers the setup, the rules every change follows, how to add an operation or a service, and the tests that prove a change.

## Setup

- Go 1.27, [golangci-lint](https://golangci-lint.run) 2.13 and [just](https://just.systems) for the core gate.
- [uv](https://docs.astral.sh/uv/), which runs `scripts/sealed`.
- Docker, for Lambda, DynamoDB Local and the `-tags docker` tests.
- For the conformance suites: the AWS CLI v2, Terraform 1.16 or later, Python 3.14 (through uv), [Bun](https://bun.sh), `jq` and `curl`.

```sh
just verify
```

`just verify` checks formatting, runs `go vet` and `golangci-lint`, and runs every test with the race detector. It is the gate for every change; CI runs it with the other checks below. `just --list` shows every recipe.

## Rules

- **Standard library only.** The main module depends on nothing outside the Go standard library except `pgregory.net/rapid` in tests. A new dependency needs an ADR in `docs/adr/` that justifies it. The conformance suites are separate modules with their own manifests and may use the AWS SDKs.
- **Never call real AWS.** Run the AWS CLI, Terraform, SDK scripts and conformance suites through `scripts/sealed` (`just aws ...`, `just tf ...`). It strips inherited `AWS_*` settings, uses dummy credentials, never reads `~/.aws`, and refuses any endpoint that is not loopback.
- **AWS is the arbiter.** Tests assert what AWS does, as the research in `docs/research/phase1/` documents it, never what understudy happens to do. Cite the research next to a non-obvious expectation as `<note>.md <section>`, for example `(sqs.md 6.1)`: the file name of a note under `docs/research/` and the number of its section heading. Cite the architecture as `ARCHITECTURE.md <section>` and decisions as `ADR <number>`. Data shown outside the source tree, such as a `fidelity.Switch` `Ref`, uses the repo-relative path and an anchor (`docs/research/phase1/sqs.md#6.1`). Where a note is the only source of an AWS value, quote the value too. When AWS's behavior is unknown, say so in the code and in the coverage note rather than guessing silently.
- **Nothing is silently faked.** An operation that is not implemented stays unbound and answers 501. An operation that stores a setting without acting on it is marked stored-only. A deviation from AWS is a named fidelity switch or a coverage note.
- **Generated code is checked in** and carries a `// Code generated ... DO NOT EDIT.` header. Change the generator or its inputs, then run `just generate`; never edit generated files.
- **Design.** Read `docs/design/ARCHITECTURE.md` and `docs/adr/` before a change that crosses packages. Prefer sum types over boolean flags, return typed errors as values, parse at boundaries, and keep packages small around one domain type. `internal/ref/deps_test.go` enforces which packages may import which.
- **Time.** Code outside `internal/clock` never calls `time.Now`, `time.Sleep` or timers; the linter refuses them. Anything AWS times runs on the emulator clock. Tests use `testing/synctest` or the emulator clock, never wall-clock sleeps.
- **Commits** follow the conventional form already in the history: `feat: ...`, `fix: ...`, `docs: ...`.

## Adding an operation

Every operation of the twelve services already has generated types, serialization, validation and a binder method, so adding one needs no generator run.

1. **Read the research.** Find the operation in `docs/research/phase1/<service>.md`: its errors, limits and edge cases. If something you need is not there, record what you found and where.
2. **Write the handler** in the service package, next to its resource family:

   ```go
   func (s *Service) listQueueTags(_ context.Context, c *kernel.Call, in *sqsapi.ListQueueTagsInput) (*sqsapi.ListQueueTagsOutput, *awserr.Error) {
   	q, err := resolveURL(c, in.QueueUrl)
   	if err != nil {
   		return nil, err
   	}
   	tags, err := readQueue(s, q, func(_ *State, queue *Queue) map[string]string { return maps.Clone(queue.Tags) })
   	if err != nil {
   		return nil, err
   	}
   	return &sqsapi.ListQueueTagsOutput{Tags: tags}, nil
   }
   ```

   State changes go through `kernel.Write`, which holds one cell lock and records effects (deliveries, timers) that run after it commits. Build errors with the generated constructors in the service's `errors.go`, never with a literal error identity.
3. **Bind it** with an honest coverage mark:

   ```go
   b.ListQueueTags(s.listQueueTags, registry.Full{})
   b.AddPermission(s.addPermission, registry.StoredOnly{Note: "statements are stored in the queue's Policy attribute and not enforced"})
   b.CreateSecret(s.createSecret, registry.Partial{Note: "AddReplicaRegions (replication) answers NotImplemented"})
   ```

   The note is what `understudy coverage` and `docs/coverage.md` show users. Bind options add a typed validator (`kernel.WithValidator`), a check (`kernel.WithCheck`) or the ARNs the call touches (`kernel.WithResources`), which fault rules, the journal and the access recorder use.
4. **Constraints the model lacks.** If AWS enforces a limit or an error the Smithy model does not declare, add an entry to `codegen/overlays/<service>.json` with its `dev.understudy#evidence` (label and source), then `just generate`. Limits that depend on state belong in the handler.
5. **Unsettled behavior.** Where the research leaves a choice open, make it a `fidelity.Switch` with its evidence label and research reference, and list it in the service's `Switches`.
6. **Test it** through the kernel, in a `kerneltest` bubble, so timers and long polls run on the emulator clock with no sleeps:

   ```go
   func TestTagsRoundTrip(t *testing.T) {
   	run(t, func(h *harness) {
   		// h.Invoke(h.Context(), sqsapi.OpTagQueue, ...), then sqsapi.OpListQueueTags, and assert the result
   	})
   }
   ```

   Assert AWS's error codes and messages for the failure paths, not only the success. Use `rapid` property tests for parsers, matchers, encoders and state machines.
7. **Add conformance cases** for each client that should exercise it (see below), and regenerate the coverage page:

   ```sh
   just coverage-check
   ```

   It rewrites `docs/coverage.md` and fails while the file differs from what is committed, so commit the regenerated page with the change.

## Adding a service

A new service is a larger change; open an issue first so the depth and the research can be agreed.

1. **Research.** Write `docs/research/phase1/<service>.md` (or a later phase's directory), under a file name no other document in `docs/` uses, from AWS's documentation, SDK source and published recordings, with every claim sourced.
2. **Pin the model.** Add the model to `pinnedModels` in `internal/cmd/vendor/upstream.go` and its IAM prefix to `referencePrefixes` in `internal/cmd/vendor/reference.go`. A maintainer then runs the vendor command's two networked modes, `-fetch` (the models, with their hashes recorded in `third_party/api-models-aws/UPSTREAM.json`) and `-service-reference` (the operation-to-IAM-action table); `internal/cmd/vendor/doc.go` describes both. Everything after that is offline:

   ```sh
   go run ./internal/cmd/vendor -verify
   ```
3. **Describe the generation.** Add `codegen/services/<service>.json` (the model's service shape, the Go package name, the model directory, the IAM prefix) and `codegen/overlays/<service>.json` with at least the service's `throttleError` and validation error identities, each with evidence. Add a `//go:generate go run ../cmd/gen -service <service>` line to `internal/api/generate.go`, then:

   ```sh
   just generate
   ```

   This produces `internal/api/<service>api/`. `go run ./internal/cmd/gen -all -check` reports drift without writing.
4. **Write the service package** in `internal/service/<service>/`. `internal/service/sts` is the smallest complete example: `New(kernel.ServiceDeps, Options)` binds the operations, and the service implements `kernel.Stateful` (export, import, reset, re-arming timers, listing resources) so snapshots, reset and `understudy resources` work.
5. **Wire it** in `internal/server/wire.go`: construct it, add it to the stateful set, and register its descriptor with its throttle and unknown-operation errors.
6. **Prove it** with service tests, a request in `internal/smoke`, conformance cases per SDK and Terraform, and the coverage page.

A new wire protocol needs an ADR; the protocol compliance corpus already carries its cases.

## Test layers

| Layer | Command | What it proves |
|---|---|---|
| Unit and service tests | `just test` | Service semantics on the emulator clock through `kernel.Invoke`; property tests for engines and codecs; generator golden tests; the 260 model examples |
| Protocol compliance | `go test -race -run TestCorpus ./internal/protocoltest` | Codecs and error writers against Smithy's official compliance corpus, as a ratchet over `internal/protocoltest/baseline.json` |
| Docker integration | `just test-docker` | Lambda execution, DynamoDB Local (only with `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1`), container networking, the image |
| Smoke | `just smoke` | The built binary serves one request per service, over HTTP and TLS |
| Budgets | `just budgets` | Binary size, startup time, idle memory, build and lint time stay under their ceilings |
| Generated code | `go run ./internal/cmd/gen -all -check` | Regenerating from the vendored inputs reproduces what is committed; CI runs `just generate-check`, which also fails on any uncommitted change |
| Coverage page | `just coverage-check` | `docs/coverage.md` matches the binary |
| Conformance | `conformance/<suite>/run.sh` | Real clients against a real server; see below |

When a fix makes a protocol compliance case pass, record it with `just protocoltest-baseline`. A case understudy cannot pass yet goes in `internal/protocoltest/skips.json` with its reason; the ratchet fails if a passing case regresses or the baseline would shrink.

## Conformance suites

Each suite in `conformance/` is its own module or project, builds understudy from the repository, starts it on its own loopback port with a fresh data directory, runs through `scripts/sealed`, and cleans up its containers. Each takes arguments to run a subset:

```sh
conformance/go-sdk/run.sh -run TestSTS
conformance/js-sdk-v3/run.sh tests/sts.test.ts
conformance/aws-cli/run.sh sts
conformance/terraform/run.sh iam
```

| Suite | Client |
|---|---|
| `go-sdk/` | aws-sdk-go-v2 |
| `python-boto3/` | boto3, current and the last Query-protocol release for SQS |
| `js-sdk-v3/` | AWS SDK for JavaScript v3 |
| `aws-cli/` | AWS CLI v2 |
| `terraform/` | Terraform: apply, an empty second plan, an update, destroy, nothing left behind |
| `e2e-pipeline/` | The phase 1 event-driven pipeline end to end, including Lambda workers |

Every suite has one file per service so a fix can own one service. Each suite's README lists its requirements and options. Every suite runs DynamoDB only with `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1`, which accepts the DynamoDB Local licence for the run, and `CONFORMANCE_DYNAMODB=0` skips it even then; the end-to-end suite's workers lock in DynamoDB, so it needs one of the two to start.

A conformance failure is a finding about understudy, or about the research: fix whichever is wrong, never the test to match understudy.

## Documentation

- `docs/configuration.md` carries `understudy serve --help` verbatim; a test fails when they differ.
- `docs/coverage.md` is generated (`just coverage-check`).
- The guide in `docs/guide/` shows only commands that were run against a build. Update it with any user-visible change.

## Reporting problems

Open an issue with the understudy version (`understudy version`), the client and its version, the request id or the `understudy journal --errors --output json` lines, and what AWS does instead, with a source if you have one. For security issues, follow [SECURITY.md](SECURITY.md) instead.
