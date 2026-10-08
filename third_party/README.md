# third_party

Inputs taken from other projects, kept verbatim or with a recorded derivation. Licenses, pins and provenance are listed in the top-level `NOTICE`.

- `api-models-aws/`: the Smithy models of the supported services at a pinned commit, with `UPSTREAM.json`.
- `smithy-protocol-tests/`: the Smithy protocol compliance cases as a flattened AST, with `PIN.json`.
- `aws-service-reference/`: the derived operation to IAM action table, with provenance.

Do not edit these files by hand. `go run ./internal/cmd/vendor -verify` checks them offline.
