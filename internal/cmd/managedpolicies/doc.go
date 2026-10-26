// Command managedpolicies keeps the AWS managed policy catalogue in
// third_party/aws-managed-policies (ADR 0016, amendment "managed
// policies"; iam-evaluation.md 10):
//
//	go run ./internal/cmd/managedpolicies -verify  # offline
//	go run ./internal/cmd/managedpolicies -pack    # offline
//	go run ./internal/cmd/managedpolicies -fetch   # network: docs.aws.amazon.com
//
// -fetch is maintainer-run on a release cadence, never at build time or in
// tests. It reads the site's robots.txt, then the AWS Managed Policy
// Reference Guide's llms.txt, then the single-file guide it links
// (reference/full.md), one request each, waiting the robots.txt
// Crawl-delay between them and sending a User-Agent that names the
// repository. It parses every policy of the single-file guide that
// llms.txt lists and writes policies.json, policies.json.gz, SOURCE.json
// (provenance and the sha256 of each document as published), LICENSE and
// NOTICE, SOURCE.json last, then runs -verify.
//
// policies.json is the reviewable source of truth; policies.json.gz is its
// exact bytes compressed with compress/gzip at BestCompression and an empty
// header, so the output is deterministic, and is what the binary embeds.
// -pack rewrites the two derived files without the network,
// policies.json.gz from policies.json and LICENSE from the generator's
// template, exactly as -fetch writes them, then runs -verify. A missing
// policies.json.gz stops every package that embeds it from building, this
// command included: restore it with git checkout, or create it empty and
// run -pack.
//
// -verify checks, offline, that the data parses and is in canonical form,
// that every document parses as a policy and is an identity policy
// (Version 2012-10-17, no principal), that policies.json.gz decompresses to
// exactly policies.json, that SOURCE.json matches the data and its hashes,
// and that LICENSE and NOTICE are the ones the generator writes. The
// CreatePolicy rules policydoc applies to a customer's document are not
// applied: AWS publishes its managed policies without that call, and some
// of them break those rules (iam-evaluation.md 10.6).
//
// On success each mode prints one line to stderr: the number of policies,
// the count of each Type and the highest default version, for the
// maintainer to compare with iam-evaluation.md 10.4.
package main
