// Package parity replays the recorded AWS exchanges under testdata/parity
// against understudy and records, per scenario step, whether understudy
// answers as AWS did (ARCHITECTURE.md 19).
//
// A scenario (testdata/parity/<svc>/<item>/scenario.json) lists the steps of
// one record-backlog item in the order they ran, the setup calls the
// recording kit made outside its capture helper, the values to bind from one
// response into later requests, the waivers, and the fidelity switches and
// overlay entries a step settles. Comparison applies the Exchange set of
// internal/normalize, so only the named normalizers and the waivers hide a
// difference; a sensitivity check proves a settled field is still compared.
// The outcomes are a ratchet in baseline.json: TestReplay fails on any
// drift from it, and just parity-baseline rewrites it. The server-driving
// transport (ServerTransport) lives in this package's test files, which may
// import internal/server.
//
// The replayer routes awsJson and awsQuery requests only; a REST step is
// skipped (rest-routing), and so is every step of a scenario that needs
// Docker when none serves the run, as under TestReplay. Nothing outside this
// package imports it: the coverage report reads baseline.json for its
// evidence column with its own reader, coverage.ParseParity, which
// TestBaselineReadsInCoverage keeps in agreement with ParseBaseline.
package parity
