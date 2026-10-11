// Command coverage prints the coverage report that docs/coverage.md holds:
//
//	go run ./internal/cmd/coverage > docs/coverage.md
//	go run ./internal/cmd/coverage -format json
//
// It wires the emulator in process, as understudy serve does, without
// listening; asks its admin API for the report, so the bindings and
// switches are the ones a running server reports; and adds the evidence of
// every overlay entry read from codegen/overlays, which the binary does
// not carry. Run it from the module root, as just coverage-check does, or
// point -overlays at the overlay directory.
//
// It stands in for understudy coverage until the CLI has that subcommand.
package main
