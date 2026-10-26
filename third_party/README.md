# third_party

Inputs taken from other projects, kept verbatim or with a recorded derivation. Licenses, pins and provenance are listed in the top-level `NOTICE`.

- `api-models-aws/`: the Smithy models of the supported services at a pinned commit, with `UPSTREAM.json`.
- `smithy-protocol-tests/`: the Smithy protocol compliance cases as a flattened AST, with `PIN.json`.
- `aws-service-reference/`: the derived operation to IAM action table, with provenance.
- `aws-managed-policies/`: the AWS managed policies from the AWS Managed Policy Reference Guide as JSON (`policies.json`), with `SOURCE.json`, `LICENSE` and `NOTICE`, written by `internal/cmd/managedpolicies` and embedded by the Go package beside them. The package embeds `policies.json.gz`, the generator's gzip copy of `policies.json`, instead of `policies.json` itself.

Do not edit these files by hand. `go run ./internal/cmd/managedpolicies -verify` checks `aws-managed-policies/` offline, and `go run ./internal/cmd/vendor -verify` the rest. `go run ./internal/cmd/managedpolicies -pack` rewrites the two derived files offline: `policies.json.gz` from `policies.json`, and `LICENSE`.
