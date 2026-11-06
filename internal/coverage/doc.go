// Package coverage builds the coverage report, in JSON and Markdown, from a
// kernel's bindings, the protocol-test baseline and skip list, the fidelity
// switches, the overlay evidence and the parity replay baseline
// (ARCHITECTURE.md 3.4, 19).
//
// The report states protocol compliance, operation coverage and evidence as
// three separate measures and never adds them together.
//
// Decisions the architecture leaves open:
//
//   - The builder is Build(Input) Report: Go cannot name a function and a
//     type Report in one package.
//   - Overlay evidence is one row per evidence item of an entry, since an
//     entry may back different traits with different labels. A running
//     server carries no overlay files, so its report (GET
//     /_understudy/v1/coverage) says so and points to docs/coverage.md,
//     which `understudy coverage --format markdown --overlays
//     codegen/overlays` generates offline in the repository.
//   - Operations are ordered by status (full, stored-only, partial,
//     unbound), then name; switches and overlay rows by label, strongest
//     evidence first.
//   - The JSON form writes the report's sums in the tagged-union convention
//     of internal/protocol/jsonx, so ParseJSON reads back what JSON wrote
//     and a client can rebuild the Markdown from it.
//   - The evidence column needs both the overlay files and
//     internal/parity/baseline.json (`--parity`), which the binary does not
//     carry either, so a running server's page leaves it out. ParseParity
//     reads the baseline itself, since coverage does not import
//     internal/parity.
//   - A switch or overlay item with several settles entries is contradicted
//     when any of them is, since the recording that differs is a finding;
//     else aws-recorded when one is settled. An overlay item without covers
//     backs every trait of its entry, so any entry on its shape names it.
package coverage
