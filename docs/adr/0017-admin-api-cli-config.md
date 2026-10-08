# ADR 0017: Admin API, non-interactive CLI, configuration and public Go packages

Status: accepted, 2026-10-08.

## Context

The brief requires an admin API under a reserved prefix plus CLI subcommands for health, reset, clock, faults, journal, snapshots, resources, coverage and IAM policy generation. Agents and CI drive the CLI, so it must be scriptable. Fault rules and hot-reload mappings are structured data that a CI job wants to declare up front. Proposals differed on a config file (flags and env only vs an optional JSON file), on prompting, and on whether to expose Go packages.

## Decision

- Admin API at `/_understudy/v1/` on the main listener, JSON both ways, errors `{"error": {"code", "message", "hint"}}`. A non-loopback main listener requires `Authorization: Bearer <admin token>`; without a token `serve` refuses to start on a non-loopback address and explains both options. The container listener exposes only `GET /health` and `GET /clock`. Unversioned AWS-visible URLs (SNS certificate and unsubscribe links, the function URL path fallback) live under `/_understudy/` outside `/v1`.
- The CLI is a thin client of the public `admin.Client`. It never prompts; `--output text|json` on every command; `--endpoint` or `UNDERSTUDY_ENDPOINT`; exit codes 0, 1 (operation failed), 2 (usage), 3 (unreachable), 4 (condition not met); every error names what failed and the next command to try; `--help` carries examples. `understudy coverage` works offline from the registry when no server is up.
- Configuration precedence: flags > `UNDERSTUDY_*` environment > an optional JSON file (`--config`, or `understudy.json` in the working directory) > defaults. The file is parsed strictly into a typed `config.Config` at the boundary with sum types where a setting has modes; unknown keys are errors naming the key and the closest valid one.
- `--ready-file PATH` writes the endpoint and pid once listening; `--listen 127.0.0.1:0` picks an ephemeral port.
- Two public packages, both marked experimental until 1.0: `admin` (wire types and client) and `understudytest` (start an in-process emulator from a Go test with a seed, a frozen clock, `Settle` and journal accessors).

## Alternatives considered

- Flags and env only: smaller promise, but a list of fault rules or hot-reload mappings on the command line is unreadable, and JSON costs no dependency.
- YAML or TOML: would need a dependency.
- Interactive prompts (licence acceptance): a CLI that sometimes blocks on stdin is a hazard for agents and CI.
- No public packages: an in-process emulator is the fastest Go test loop and the admin client is what the CLI already is.

## Consequences

- Everything the CLI can do is scriptable over HTTP, and the admin wire types are versioned under `/v1`.
- A config file is a compatibility surface; strict parsing and typed modes keep it small.
- The public packages constrain internal churn slightly; marking them experimental keeps that honest.

## Amendments (review, 2026-10-08)

- Admin errors carry a closed `admin.ErrorCode` (`invalid_request`, `not_found`, `conflict`, `condition_not_met`, `unavailable`, `internal`) mapped to CLI exit codes in one table in the public package; the CLI never matches on strings (engineering m14).
- `understudytest` returns an `admin.Client` bound to the in-process server and uses only `admin` wire types; it never exposes `internal` types (engineering m13). Parallel instances rely on per-kernel bindings (ADR 0003).
- Configuration models its modes as sum types (engineering m15): `Listen: Loopback{AddrPort} | Exposed{AddrPort, Auth: Token{Secret} | ContainerPublished}` and `DynamoDB: Disabled | AwaitingLicence | Accepted{Digest}`. `ContainerPublished` is accepted by boot validation only inside a container (ADR 0011), so `docker run -p 4566:4566 understudy` works without a token while a non-loopback listener on a host still requires one.
- `GET /health` is a readiness probe: `503` with the list of components still starting, then `200`; `--ready-file`, `understudy health --wait` and container health checks key on it. `/settle` reports pending work by `WorkKind`.
- CLI additions: `journal --request-id`, `journal trace --request-id`, `--canonical`, `snapshot diff --normalize`, every fault match dimension and action as a flag, `images pull` (product m3, m4, m5).
- A short distribution section in the architecture defines the static binary matrix, the `scratch` image with a non-root user and the release configuration; nothing is published in phase 1 (product C2).
