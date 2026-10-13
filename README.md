# understudy

understudy is a local AWS emulator: a single Go binary that stands in for AWS on a developer machine and in CI. Applications, the AWS SDKs, the AWS CLI and Terraform reach it through the standard `AWS_ENDPOINT_URL` override with no code changes. It aims for fidelity on a small set of services (SQS, SNS, S3, DynamoDB, Lambda, CloudWatch Logs, Secrets Manager, KMS, IAM, STS, EventBridge and CloudWatch metrics) rather than shallow coverage of many, and adds a controllable clock, a causal event journal, fault injection and snapshots.

## Status

understudy is pre-release: nothing has been published as a release, a package or an image, so you build it from a clone. All twelve services above answer requests. Coverage is partial, and [docs/coverage.md](docs/coverage.md) lists every operation of every service as full, stored only, partial (with its gaps named) or unbound. An unbound operation answers `501 NotImplemented` in AWS's own error format; nothing is silently faked. `understudy coverage` prints the same report from a running server.

## Quick start

With Go 1.27, from a clone:

```sh
go install ./cmd/understudy
understudy serve
```

`serve` listens on `127.0.0.1:4566` and keeps its state in memory. In another shell, point the SDKs, the AWS CLI and Terraform at it, then check that it is ready:

```sh
eval "$(understudy env)"
understudy health
aws sqs create-queue --queue-name demo
```

`understudy env` prints `AWS_ENDPOINT_URL`, dummy credentials and a region, plus a comment for each client that needs a setting no variable carries, such as path-style S3 for the Go and JS SDKs. It also writes dotenv, fish and `$GITHUB_ENV` formats (`understudy env --help`). `understudy serve --help` lists every setting; each has a flag, an `UNDERSTUDY_*` variable and a key in an optional `understudy.json`, and [docs/configuration.md](docs/configuration.md) is the full reference.

The access key picks the account: a 12-digit access key id is that account, and any other is the default account, `000000000000` unless `accounts.default` sets another. So `AWS_ACCESS_KEY_ID=111111111111` works in account 111111111111.

### Lambda and DynamoDB

Lambda functions run in AWS's Lambda base images and DynamoDB is served by AWS's DynamoDB Local, so both need a Docker Engine (`DOCKER_HOST`, else `/var/run/docker.sock`). The images are pulled on first use. DynamoDB Local is under the Amazon DynamoDB Local License Agreement, which you accept by starting understudy with `--accept-dynamodb-local-license` (or `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1`). Until then DynamoDB calls answer 501 and say how to accept it. `understudy doctor` checks what this machine provides.

### Docker

Build the image with `just image`, then run it with the port published on loopback:

```sh
docker run --rm -p 127.0.0.1:4566:4566 understudy:dev
```

To run Lambda functions and DynamoDB Local from the container, mount the Docker socket and give the container user (65532) the socket's group:

```sh
docker run --rm -p 127.0.0.1:4566:4566 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --group-add "$(stat -c %g /var/run/docker.sock)" \
  -e UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1 \
  understudy:dev
```

Inside the container understudy listens on `0.0.0.0:4566` without an admin token, so the published port is what keeps it private: publish it on `127.0.0.1` only. Mount a volume on `/data` and set `UNDERSTUDY_PERSIST=/data` to keep state across restarts. The image's healthcheck runs `understudy health`, which finds the server at `UNDERSTUDY_ENDPOINT`, else at the port `UNDERSTUDY_LISTEN` names on loopback, else at `http://127.0.0.1:4566`; changing the port with `-e UNDERSTUDY_LISTEN=0.0.0.0:8080` alone keeps the container healthy.

Outside a container, listening beyond loopback needs an admin token (`UNDERSTUDY_ADMIN_TOKEN`) and `--allow-unauthenticated-aws-api`. The token guards only the admin API under `/_understudy/`: this build checks no request signature, so every peer that reaches the port can read every secret and object and run any image as a Lambda function. Do it only on a network whose every peer you trust.

## Admin API and CLI

The admin API lives under `/_understudy/v1/` on the same port as the AWS API. The CLI is its client, and the Go package `github.com/sustinbebustin/understudy/admin` is another. Every command takes `--endpoint` (default `UNDERSTUDY_ENDPOINT`, else the port of `UNDERSTUDY_LISTEN` on loopback, else `http://127.0.0.1:4566`) and `--output json`, and `understudy <command> --help` shows its flags and examples.

| Command | What it does |
|---|---|
| `health`, `settle` | Report readiness; wait until no delivery, invocation or poll is pending |
| `reset` | Drop service state, all of it or by service, account and region |
| `clock` | Read, freeze, advance or scale the emulator clock that drives every timeout, delay, retention and schedule |
| `faults` | Add and remove rules that inject AWS-shaped errors, throttling or latency |
| `journal` | Query or follow the causal journal of API calls, deliveries, invocations, timers and faults |
| `snapshot` | Save, load, inspect and compare archives of the whole emulator state |
| `resources` | List every resource by service, account and region |
| `coverage` | Report protocol compliance, operation coverage and evidence |
| `iam` | Show the IAM actions each principal used and the least-privilege policy they need |
| `lambda`, `images` | Lambda conveniences and the container images understudy runs |
| `doctor`, `cleanup` | Check this machine; remove containers a crashed understudy left behind |

On loopback the admin API needs no token. A listener beyond loopback on a host requires one: start `serve` with `UNDERSTUDY_ADMIN_TOKEN` set (and `--allow-unauthenticated-aws-api`, since the token does not guard the AWS API), and set the same variable for the CLI.

Every command exits with one of these codes, which `understudy --help` also lists:

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | the operation failed |
| 2 | usage error: fix the command line |
| 3 | understudy is not reachable at the endpoint, or did not answer in time |
| 4 | a condition was not met, such as `health --wait` or `settle` timing out |
| 5 | the server refused the admin token (`UNDERSTUDY_ADMIN_TOKEN`) |

## Development

To check a change, install Go 1.27, [golangci-lint](https://golangci-lint.run) 2.13 and [just](https://just.systems), then run `just verify`. It checks formatting, runs `go vet` and `golangci-lint`, and runs the tests with the race detector. `just --list` shows the other recipes. The design lives in `docs/design/ARCHITECTURE.md` and the decisions behind it in `docs/adr/`. The suites under `conformance/` check understudy from the outside with the Go, Python and JS SDKs, the AWS CLI, Terraform and an end-to-end pipeline; each has its own `run.sh`.
