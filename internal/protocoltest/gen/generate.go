// Package gen holds the synthetic services of the Smithy protocol
// compliance corpus, generated from third_party/smithy-protocol-tests by the
// generator that emits internal/api (ARCHITECTURE.md 5.6): one package per
// service, each with its types, schemas, serde, validators, binder and
// case tables (cases_gen.go), and the case vocabulary those tables use
// (case.go). Only the compliance harness imports them; the server never
// does. Their *_gen.go files are never edited by hand; regenerate with just
// generate.
//
// The case tables are cases_gen.go rather than test files, since the
// harness in internal/protocoltest runs them and Go never lets one package
// read another's test files.
package gen

//go:generate go run ../../cmd/gen -corpus
