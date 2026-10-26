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
// The outcomes are a ratchet in baseline.json, which the coverage report
// reads for its evidence column. The server-driving transport lives in this
// package's test files, which may import internal/server.
//
// Fixture parsing, comparison paths, Compare and the scenario format
// (ParseScenario, Encode, ReadScenario, ListScenarios) are built. Not
// implemented yet: CheckBind, Mutate, Run and the baseline are stubs, and
// nothing imports the package.
package parity
