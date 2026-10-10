// Command vendor keeps third_party/api-models-aws a verbatim, verifiable copy
// of the twelve phase 1 Smithy models at one pinned upstream commit.
//
//	go run ./internal/cmd/vendor -verify   # offline: recompute every hash
//	go run ./internal/cmd/vendor -fetch    # network: download and record
//
// -fetch reads the GitHub tree of the pinned commit once, downloads each model
// file plus LICENSE and NOTICE from the raw endpoint, checks every download's
// git blob SHA-1 against its tree entry, writes the bytes unchanged and
// records them in UPSTREAM.json. -verify recomputes the git blob SHA-1 and the
// SHA-256 of every recorded file offline; just generate runs it before
// go generate. Bumping the pin means changing pinnedCommit and running -fetch.
package main
