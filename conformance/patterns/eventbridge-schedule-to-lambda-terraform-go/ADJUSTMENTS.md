# Adjustments

This directory is the `eventbridge-schedule-to-lambda-terraform-go` pattern of [aws-samples/serverless-patterns](https://github.com/aws-samples/serverless-patterns/tree/deb84afac5425dfb4cabbc3c33f7a11fa4a71555/eventbridge-schedule-to-lambda-terraform-go) at commit `deb84af`, copied with the repository's MIT-0 licence (`LICENSE`). Every change from that commit is below; nothing else differs.

| File | Change | Reason |
|---|---|---|
| `.terraform.lock.hcl` | Added: `hashicorp/aws` 6.68.0 (constraint `>= 4.64.0`, unchanged in `main.tf`) and `hashicorp/archive` 2.8.1, with checksums for linux and darwin on amd64 and arm64 | Provider pin. The pattern ships no lock file, so `>= 4.64.0` would resolve to whatever version is newest on the day of the run. 6.68.0 is the version whose `aws_scheduler_schedule` calls `docs/research/scheduler.md` 11 reads; `main.tf` uses `data "archive_file"` without declaring the provider, and 2.8.1 is the version the other patterns pin. |
| `src/main.go` | `gofmt -w`: tabs for the four-space indents, trailing blank lines removed; no other change | `just verify`'s `fmt-check` runs gofmt over every Go file in the repository. |
| `src/.golangci.yml` | Added: the lint configuration CI runs over the module, excluding the three findings in `src/main.go` | `just lint-suites` lints every Go module under `conformance/`; the exclusion keeps `src/main.go` as copied. |

The pattern keeps its `provided.al2023` runtime, its Go source (formatted only) and its `go.mod` and `go.sum`. `conformance/patterns/checks/eventbridge-schedule-to-lambda-terraform-go.sh` builds `src/bootstrap` with the README's `GOOS=linux GOARCH=amd64 go build -o bootstrap main.go`, which fetches aws-lambda-go v1.40.0, pinned by `go.sum`, from the Go module proxy; it leaves out the README's `go get`, which could move that pin, and its `zip`, whose output `main.tf` never reads (it zips `src/bootstrap` itself). The README's test reads the function's execution logs; the check moves the emulator clock past the schedule's `rate(5 minute)` and looks for the function's `Received event:` line in `/aws/lambda/EventBridgeScheduleTarget`.
