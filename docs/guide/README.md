# understudy guide

Task-oriented pages. Each command shown was run against a build of this repository; `understudy <command> --help` has every flag.

| Page | What it covers |
|---|---|
| [Getting started](getting-started.md) | Install, start, connect a client, accounts and regions, check the machine |
| [Configuration](configuration.md) | Flags, variables and `understudy.json`; common setups |
| [SDKs and the AWS CLI](sdks.md) | Go v2, boto3, JS v3 and the CLI, S3 addressing, TLS, Go tests with `understudytest` |
| [Terraform](terraform.md) | The provider block, a plan that stays empty, what is slow and why |
| [Lambda](lambda.md) | Runtimes, zip and image functions, hot reload, networking, logs, event sources, function URLs |
| [API Gateway HTTP APIs](apigateway.md) | APIs, routes, Lambda proxy integrations with payload 1.0 and 2.0, stages and deployments, the execute-api host and path, Terraform |
| [SNS HTTP delivery](sns-http.md) | The allow-list, retries and dead letters, https endpoints, validating signatures with AWS's validators and openssl |
| [The clock](clock.md) | Freeze, advance, scale; what runs on emulator time and what does not |
| [Fault injection](faults.md) | Rules, triggers, delivery faults, SDK retries |
| [The journal](journal.md) | Querying, following and tracing calls, deliveries and invocations |
| [The web UI](ui.md) | Live trace trees, resources, fault rules, the clock, IAM policy and coverage in a browser |
| [Snapshots and persistence](snapshots.md) | Save, load, diff, `--persist`, reset |
| [Policy generation](policy.md) | Least-privilege policies from recorded access, and opt-in enforcement |
| [CI](ci.md) | Running understudy in a pipeline |
| [Testcontainers](testcontainers-modules.md) | The Go, Python and Node modules that start the image from a test suite; Lambda through the socket, Terraform, cleanup |
| [Differences from AWS](differences.md) | Recorded behavior that still differs from AWS, by service, with workarounds |

The full setting reference is [../configuration.md](../configuration.md), and the operation-by-operation coverage is [../coverage.md](../coverage.md).
