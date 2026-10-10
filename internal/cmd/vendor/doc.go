// Command vendor keeps the generator's third-party inputs verifiable (ADR
// 0016): the twelve phase 1 Smithy models at one pinned upstream commit, the
// Smithy protocol compliance corpus and the operation to IAM action table
// derived from the AWS Service Reference.
//
//	go run ./internal/cmd/vendor -verify             # offline: check all three
//	go run ./internal/cmd/vendor -fetch              # network: the models
//	go run ./internal/cmd/vendor -service-reference  # network: the actions table
//
// -fetch reads the GitHub tree of the pinned commit once, downloads each model
// file plus LICENSE and NOTICE from the raw endpoint, checks every download's
// git blob SHA-1 against its tree entry, writes the bytes unchanged and
// records them in third_party/api-models-aws/UPSTREAM.json. Bumping the pin
// means changing pinnedCommit and running -fetch.
//
// -service-reference downloads the Service Reference index and the document
// of each pinned service prefix and writes only the derived
// third_party/aws-service-reference/actions.json: each operation's authorized
// actions, each action's annotations, and the document's Version,
// modification time and body SHA-256. The raw documents state no licence, so
// they are not vendored.
//
// The corpus, third_party/smithy-protocol-tests/model.json, is produced by a
// human with the self-contained Smithy CLI (its README gives the recipe);
// this command only verifies it against PIN.json.
//
// -verify recomputes the hashes of every recorded model, checks PIN.json and
// the corpus AST, and checks that the actions table has an entry for every
// operation of every pinned model, all offline; just generate runs it before
// go generate.
package main
