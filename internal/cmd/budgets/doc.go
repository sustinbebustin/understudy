// Command budgets measures the budgets of ARCHITECTURE.md 5.7 and fails when
// one is over its ceiling or cannot be measured:
//
//	go run ./internal/cmd/budgets    # also: just budgets
//
// It builds the stripped linux/amd64 binary (-trimpath, -ldflags "-s -w",
// CGO_ENABLED=0) and measures its size; starts it with understudy serve
// --listen 127.0.0.1:0 --ready-file and times the first 200 from
// /_understudy/v1/health, from process start; reads VmRSS from
// /proc/<pid>/status after a second of idling; times go build ./... with a
// fresh, empty GOCACHE and golangci-lint run ./...; and reads the journal
// ring defaults. It prints one table and exits 1 when a row fails. A breach
// is fixed by the role pruning of ARCHITECTURE.md 5.4 or by tightening the
// closure, never by raising the ceiling.
//
// Decisions the architecture leaves open:
//
//   - The served binary has no Docker: DOCKER_HOST points at a socket that
//     does not exist, so the idle RSS never counts a container engine
//     client's connections.
//   - Idle RSS is VmRSS one second after the first 200, the steady state an
//     idle emulator settles to; the startup allocations are already counted.
//   - The binary runs on the host it was built for. On a host that is not
//     linux/amd64 a second, native build is run; its size is not measured.
//     RSS comes from /proc, so the tool runs on Linux only.
//   - The lint row times a complete golangci-lint run, findings or not:
//     whether the tree is lint-clean is just lint's to say. The run uses the
//     caller's lint cache, as the CI job does.
package main
