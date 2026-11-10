# ADR 0017: Admin API, non-interactive CLI, configuration and public Go packages

Status: accepted, 2026-10-08.

## Context

understudy needs an admin API under a reserved prefix plus CLI subcommands for health, reset, clock, faults, journal, snapshots, resources, coverage and IAM policy generation. Agents and CI drive the CLI, so it must be scriptable. Fault rules and hot-reload mappings are structured data that a CI job wants to declare up front. The designs considered differed on a config file (flags and env only vs an optional JSON file), on prompting, and on whether to expose Go packages.

## Decision

- Admin API at `/_understudy/v1/` on the main listener, JSON both ways, errors `{"error": {"code", "message", "hint"}}`. A non-loopback main listener requires `Authorization: Bearer <admin token>`; without a token `serve` refuses to start on a non-loopback address and explains both options. The container listener exposes only `GET /health` and `GET /clock`. Unversioned AWS-visible URLs (SNS certificate and unsubscribe links, the function URL path fallback) live under `/_understudy/` outside `/v1`.
- The CLI is a thin client of the public `admin.Client`. It never prompts; `--output text|json` on every command; `--endpoint` or `UNDERSTUDY_ENDPOINT`; exit codes 0, 1 (operation failed), 2 (usage), 3 (unreachable), 4 (condition not met), and 5 (admin token refused) since the first amendment of 2026-10-13; every error names what failed and the next command to try; `--help` carries examples. `understudy coverage` works offline from the registry when no server is up.
- Configuration precedence: flags > `UNDERSTUDY_*` environment > an optional JSON file (`--config`, or `understudy.json` in the working directory) > defaults. The file is parsed strictly into a typed `config.Config` at the boundary with sum types where a setting has modes; unknown keys are errors naming the key and the closest valid one.
- `--ready-file PATH` writes the endpoint and pid once listening; `--listen 127.0.0.1:0` picks an ephemeral port.
- Two public packages, both marked experimental until 1.0: `admin` (wire types and client) and `understudytest` (start an in-process emulator from a Go test with a seed, a frozen clock, `Settle` and journal accessors). The Testcontainers module amendment of 2026-10-26 adds a third public Go surface, the nested module `testcontainers/go`.

## Alternatives considered

- Flags and env only: smaller promise, but a list of fault rules or hot-reload mappings on the command line is unreadable, and JSON costs no dependency.
- YAML or TOML: would need a dependency.
- Interactive prompts (licence acceptance): a CLI that sometimes blocks on stdin is a hazard for agents and CI.
- No public packages: an in-process emulator is the fastest Go test loop and the admin client is what the CLI already is.

## Consequences

- Everything the CLI can do is scriptable over HTTP, and the admin wire types are versioned under `/v1`.
- A config file is a compatibility surface; strict parsing and typed modes keep it small.
- The public packages constrain internal churn slightly; marking them experimental keeps that honest.

## Amendment 2026-10-08: closed error codes, listen modes as sum types and readiness

- Admin errors carry a closed `admin.ErrorCode` (`invalid_request`, `not_found`, `conflict`, `condition_not_met`, `unavailable`, `internal`) mapped to CLI exit codes in one table in the public package; the CLI never matches on strings.
- `understudytest` returns an `admin.Client` bound to the in-process server and uses only `admin` wire types; it never exposes `internal` types. Parallel instances rely on per-kernel bindings (ADR 0003).
- Configuration models its modes as sum types: `Listen: Loopback{AddrPort} | Exposed{AddrPort, Auth: Token{Secret} | ContainerPublished}` and `DynamoDB: Disabled | AwaitingLicence | Accepted{Digest}` (`Enabled | Disabled` since ADR 0014's first amendment of 2026-10-14). `ContainerPublished` is accepted by boot validation only inside a container (ADR 0011), so `docker run -p 4566:4566 understudy` works without a token while a non-loopback listener on a host still requires one.
- `GET /health` is a readiness probe: `503` with the list of components still starting, then `200`; `--ready-file`, `understudy health --wait` and container health checks key on it. `/settle` reports pending work by `WorkKind`.
- CLI additions: `journal --request-id`, `journal trace --request-id`, `--canonical`, `snapshot diff --normalize`, every fault match dimension and action as a flag, `images pull`.
- A short distribution section in the architecture (ARCHITECTURE.md 22) defines the static binary matrix, the `scratch` image with a non-root user and the release configuration; nothing is published before the first release.

## Amendment 2026-10-13: exit code 5; a host listener beyond loopback needs an explicit flag

- Exit code 5 is an admin call refused for its token (`admin.Unauthorized`), so a script tells a missing or rejected `UNDERSTUDY_ADMIN_TOKEN` from a usage error (2). The mapping stays in the one table in `admin`.
- On a host, a non-loopback listener needs `--allow-unauthenticated-aws-api` with the admin token: the token guards only `/_understudy/`, and the AWS API verifies no signature in this build. `ContainerPublished` is accepted only in a container with its own network namespace, outside Kubernetes.

## Amendment 2026-10-13: `Known` alternatives, the flag under `--sigv4 verify` and the snapshot size cap

- `admin.Error.Known` lists each alternative in the form the refused field accepts. For a fault rule's `action.shape` that is the short error name when it names exactly one error the operation sends, else the full shape id (the form an ambiguity rejection lists too); the server renders it and the CLI shows it unchanged. The other `Known` emitters (service names in reset and journal) already follow this rule, which is now the field's documented contract.
- `--allow-unauthenticated-aws-api` stays required with the admin token beyond loopback now that `--sigv4 verify` exists. The previous amendment's reason ("the AWS API verifies no signature in this build") becomes that every client may sign with the public dummy pair `test`/`test`, which verifies even under `--sigv4 verify` (ADR 0012, amendment 2026-10-13).
- One setting, `snapshotMaxBytes` (`--snapshot-max-bytes`, `UNDERSTUDY_SNAPSHOT_MAX_BYTES`), caps every snapshot archive, counted as the uncompressed tar: save, upload, `--load-snapshot`, and the `--persist` load and checkpoint. It is a top-level scalar beside `persist`, `persistEvery` and `loadSnapshot`, default 1 GiB, accepted from 1 MiB to 4 GiB (ARCHITECTURE.md 13).

## Amendment 2026-10-26: Testcontainers module

Approved by the maintainer with the three Testcontainers modules (testcontainers.md 8, 9.8).

- A third public Go surface: package `understudy` in the nested module `github.com/sustinbebustin/understudy/testcontainers/go` (directory `testcontainers/go`, import `.../testcontainers/go/understudy`). It runs understudy's image as a Testcontainers for Go module: `Run`, a `Container` embedding `testcontainers.Container`, module options, and the 9.5 refusals as typed errors (testcontainers.md 8.1, 8.2). It is experimental until 1.0, like `admin` and `understudytest`. `understudytest` stays the recommendation for Go tests that need no container (testcontainers.md 9.4).
- It imports only `admin` from the root module, never `internal/`, so it constrains internal churn no more than `admin` already does; its helpers are `admin.Client` calls and its error types carry `admin` wire types. The import sets the module's Go floor to the root's (testcontainers.md 8.10).
- Versioning is lockstep: understudy `vX.Y.Z` is also tagged `testcontainers/go/vX.Y.Z` on the same commit, the Go form for a module in a subdirectory, and the module's `DefaultImage` is `ghcr.io/sustinbebustin/understudy:X.Y.Z` (testcontainers.md 4.6, 8.8). In-repo builds use `replace github.com/sustinbebustin/understudy => ../..`, which consumers ignore; a released module requires the root at the same tag. Nothing is tagged before the GHCR image exists.
- Its dependencies are the nested module's own, outside the root allow-list and the release binary (ADR 0016, Testcontainers module amendment of 2026-10-26).

## Amendment 2026-10-26: SES sent messages

Approved by the maintainer for SES (ses.md 11.2), and for this surface only.

- `GET /_understudy/v1/ses/messages` and `DELETE /_understudy/v1/ses/messages`, with `admin.Client.SESMessages` and `ClearSESMessages` and the `understudy ses messages` command. SES records every message it accepts, and each email identity's verification email, instead of sending it; these are the only way to read them. They refuse browser calls and need the admin token beyond loopback like every other endpoint, since a message's content is the application's.
- Not approved: an endpoint or client method that verifies an identity or sets production access (`POST /ses/identities/{identity}/verification`, `PUT /ses/account`). Verification completes through the identity's verification link or `ses.verifiedIdentities`, and production access is configuration.
