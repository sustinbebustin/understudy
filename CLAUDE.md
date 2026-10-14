# understudy

A local AWS emulator for development machines and CI, built for testing event-driven systems: one Go binary that stands in for twelve AWS services, reached through `AWS_ENDPOINT_URL`. Pre-release; what each operation supports is listed in `docs/coverage.md`.

```sh
just verify          # gofmt check, vet, golangci-lint, go test -race: the gate for every change
just fmt             # gofumpt + goimports
just generate        # after changing codegen/ or internal/cmd/gen
just coverage-check  # after binding or re-marking an operation; commit the regenerated docs/coverage.md
just test-docker     # Lambda, DynamoDB Local, container networking
just aws ...         # AWS CLI against a loopback endpoint; `just tf ...` for Terraform
```

`CONTRIBUTING.md` is the source of truth for the rules below in full, the recipes for adding an operation or a service, and the test layer that proves each kind of change. Design is in `docs/ARCHITECTURE.md` and `docs/adr/`; read both before a change that crosses packages. AWS behavior is researched in `docs/research/`.

## Invariants

Do not weaken them. The linter or a test enforces most.

1. Never reach real AWS. Run the AWS CLI, Terraform, SDK scripts and conformance suites through `scripts/sealed` (`just aws`, `just tf`, each suite's `run.sh`).
2. AWS is the arbiter. Tests assert what AWS does as `docs/research/` records it, never what understudy happens to do. A failing conformance test is a finding: fix understudy or the research, not the test. When AWS's behavior is unknown, say so in the code and the coverage note.
3. Nothing silently faked. An unimplemented operation stays unbound and answers 501; a setting stored without effect is `registry.StoredOnly`; a deviation from AWS is a `fidelity.Switch` or a coverage note.
4. Dependencies are the allow-list in ADR 0016 (amendment 2026-10-14), mirrored by `depguard`: no runtime dependency; `rapid`, `go-cmp`, `cespare/xxhash/v2` and `zeebo/xxh3` in tests. AWS behaviour is never delegated to a library. A new dependency meets the ADR's criteria and adds a table row.
5. One clock. Outside `internal/clock` and `cmd/`, no `time.Now`, `time.Sleep` or timers; anything AWS times runs on the emulator clock, and tests use `testing/synctest` or the emulator clock. No `fmt.Print*`, `os.Exit` or `panic` there either.
6. Generated files (`// Code generated ... DO NOT EDIT.`) change only through the generator or its inputs (`codegen/`), then `just generate`.

## Changing things

- Cite the source beside a non-obvious expectation: `(sqs.md 6.1)` for a research note and section, `ARCHITECTURE.md <section>`, `ADR <number>`. `internal/ref/citations_test.go` checks that comment citations resolve.
- A new package needs a row in the import-direction table in `internal/ref/deps_test.go`.
- For a user-visible change, update `docs/guide/` with commands run against a build. `docs/configuration.md` carries `understudy serve --help` verbatim; a test fails when they differ.
- `admin/` and `understudytest/` are the public Go API; everything else is `internal/`.
- Commits use the conventional form in the history: `feat:`, `fix:`, `docs:`, `test:`.

## Agent skills

### Issue tracker

Local markdown under `.scratch/<feature>/`, gitignored and never committed. When work comes from a ticket, tick each acceptance checkbox as it passes and set `Status: resolved` at the end. See `docs/agents/issue-tracker.md`.

### Triage labels

Default five-role vocabulary, recorded as `Status:` lines. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: root `GLOSSARY.md` + `docs/adr/`. See `docs/agents/domain.md`.
