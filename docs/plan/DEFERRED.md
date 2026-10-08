# Deferred items

Work a wave could not finish because the paths belong to a later task, or because the prerequisite does not exist yet. Each entry names the task expected to close it.

## Wave 1: scaffold

- **`package main` in `internal/cmd/gen`, `internal/cmd/vendor` and `internal/cmd/budgets`** (T10, T19). Each `doc.go` uses the non-main names `gen`, `vendor` and `budgets`, because a `main` package without `func main` breaks `go build` and `go vet`. The task that adds the main file changes the package clause in that directory's `doc.go` to `package main`.
- **Package comments for generated packages** (T10, T19). ARCHITECTURE.md section 2 lists `internal/api/<svc>api` and `internal/protocoltest/gen` as generated, so the scaffold gives them no `doc.go`. The generator emits their package comments.
- **Recipes that cannot run yet.** `just generate` and `generate-check` need `internal/cmd/vendor` (T10). `budgets` needs `internal/cmd/budgets` (T19). `smoke` needs the smoke test (T45). `image` needs a Dockerfile (T43). The first task that makes each recipe runnable runs it.
- **`just coverage-check` target** (T25, T44). The recipe fails until `docs/coverage.md` is tracked, and it runs `go run ./cmd/understudy coverage --format markdown`, which exists only after T44. The plan says T25 runs `go run ./internal/cmd/coverage` until then, but only T01 owns the `justfile`. If T25 lands before T44, the integrator for that wave points the recipe at `./internal/cmd/coverage` and T44 points it back.
- **CI not run on GitHub.** Pushing is not allowed, so `.github/workflows/ci.yml` was checked only against each action's `action.yml` at the pinned tag. The first push runs it.
- **`go mod tidy` would drop `pgregory.net/rapid`.** `go.mod` requires it although nothing imports it yet. Do not run `go mod tidy` until the first test imports rapid.
