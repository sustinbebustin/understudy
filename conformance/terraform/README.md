# Terraform conformance suite

Checks that the Terraform AWS provider can manage every phase 1 resource type against understudy as it would against AWS. For each service the suite runs:

1. `apply` of the initial configuration;
2. `plan -detailed-exitcode`, which must report no changes (read fidelity);
3. `apply` of an update that changes several attributes of every resource;
4. `plan -detailed-exitcode` again, which must report no changes;
5. `destroy`;
6. a check that the Terraform state is empty and that understudy holds nothing in the service's account. The only exceptions are the ones AWS also keeps after a delete: customer managed KMS keys in `PendingDeletion`, and AWS managed keys.

Expected behavior comes from the research in `docs/research/phase1/` (`tf-messaging.md`, `tf-storage.md`, `tf-compute.md`, `tf-remaining.md`), which documents what provider 6.68.0 sends and what it needs back. The suite asserts AWS behavior, never an understudy quirk.

## Running

```sh
conformance/terraform/run.sh                               # every service
CONFORMANCE_DYNAMODB=0 conformance/terraform/run.sh        # every service but DynamoDB
conformance/terraform/run.sh sqs lambda                    # only these services
```

`run.sh`:

- exits 2 before building when `CONFORMANCE_DYNAMODB` is set to anything but `0`, and when `terraform version`, run through `scripts/sealed`, fails (naming the binary and its exit status);
- builds understudy from the repository into `.build/conformance/terraform/understudy`;
- starts it on `127.0.0.1:45665` with a fresh temporary data directory (`--persist`) and no `UNDERSTUDY_*` or `AWS_*` setting from your shell, and waits for the health endpoint; unless DynamoDB is opted out, it exits 1 unless DynamoDB reports `enabled`;
- runs each service through the lifecycle above in its own working directory, its own state and its own account, with up to 11 services in parallel;
- always stops the server, sweeps the containers labeled with its owner, and deletes the data directory;
- prints one line per test, then a summary on stderr (`run.sh: passed=N failed=N skipped=N` and a `run.sh: SKIP <service>/<phase>: <reason>` line per skip), and exits non-zero when any test fails.

Every Terraform and AWS CLI call goes through `scripts/sealed`, so no real AWS setting or credential is used.

Requirements: Go, Terraform 1.16 or later, Docker (for Lambda), `uv` (for `scripts/sealed`), `jq`, `curl` and GNU `timeout`. The first run downloads the providers from the Terraform registry and pulls the Lambda base images; later runs reuse the provider cache in `.build/conformance/terraform/plugin-cache`. The Go worker is built for `x86_64`.

| Variable | Effect |
|---|---|
| `CONFORMANCE_DYNAMODB=0` | Skips the `dynamodb` tests with the reason `DynamoDB opted out (CONFORMANCE_DYNAMODB=0)`. Unset, they run on DynamoDB Local, pulled on first use. |
| `CONFORMANCE_JOBS` | Services run in parallel (default 11). |
| `CONFORMANCE_PHASE_TIMEOUT` | Seconds before one Terraform command is interrupted (default 900). |
| `CONFORMANCE_PORT` | Port understudy listens on (default 45665). |
| `TF_LOG` | Turns on Terraform's debug log, written next to each phase log. |

## Layout

| Path | Contents |
|---|---|
| `provider.tf` | Provider and Terraform settings, and the variables every service shares. |
| `<service>.tf` | One self-contained configuration per service: `sqs`, `sns`, `s3`, `dynamodb`, `kms`, `secretsmanager`, `iam`, `lambda`, `logs`, `events` (EventBridge) and `cloudwatch` (metric alarms). Resources from other services that a configuration needs, such as the queue an S3 notification targets, are declared in that file and marked as fixtures. |
| `.terraform.lock.hcl` | Pinned provider checksums, copied into every working directory (`init -lockfile=readonly`). |
| `functions/go-worker` | The Go worker for `provided.al2023`, standard library only, its own module. |
| `functions/python-handler` | The Python handler for `python3.13`. |

The `step` variable selects the scenario: `create` is the initial configuration and `update` the changed one. Each file's header says what the update changes.

Resource names start with `tfc-<service>`. Each service signs with a 12-digit access key from which understudy derives a separate account (`100000000001` to `100000000011`, in the order of the table above). So one service never sees another's resources, and the leftover check can ask understudy for exactly one account.

The provider is configured as the research documents for a custom endpoint (`tf-messaging.md` 1.2): the endpoint comes from `AWS_ENDPOINT_URL`, set by `scripts/sealed`, and the provider block sets `skip_credentials_validation`, `skip_requesting_account_id`, `skip_metadata_api_check` and `s3_use_path_style`. The endpoint is an IP literal, so S3 must use path style.

## Results and evidence

Everything a run produces is under `.build/conformance/terraform/run/`:

- `results.tsv`: one line per test, `<service>/<phase>`, the status (`PASS`, `FAIL` or `SKIP`) and the evidence for a failure. Evidence is the Terraform error, or the changes a plan wanted to make.
- `logs/<service>/<phase>.log`: the full output of each Terraform command. `nothing-left.log.resources.json` holds what understudy still listed after destroy.
- `logs/server.log`: understudy's JSON log. `logs/journal-errors.json`: every failed call in understudy's journal, taken before shutdown.

To work on one service, run `run.sh <service>` and read its logs. The working directory `run/work/<service>` stays in place, with its state, after the run.
