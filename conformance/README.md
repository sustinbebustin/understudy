# conformance

Suites that check understudy from the outside, each a separate module or project with its own dependency manifest.

- `gosdk/`: aws-sdk-go-v2.
- `python/`: boto3.
- `js/`: AWS SDK for JavaScript v3.
- `cli/`: the AWS CLI.
- `terraform/`: apply, a no-change plan and destroy.
- `e2e/`: the event-driven pipeline end to end.
- `parity-record/`: the record mode of the parity harness.

Run every suite through `scripts/sealed` so no real AWS setting or credential is used. The suites arrive in a later phase.
