// Command managedpolicies keeps the AWS managed policy catalogue in
// third_party/aws-managed-policies (ADR 0016, amendment "managed
// policies"; iam-evaluation.md 10):
//
//	go run ./internal/cmd/managedpolicies -verify  # offline
//	go run ./internal/cmd/managedpolicies -fetch   # network: docs.aws.amazon.com
//
// -fetch is maintainer-run on a release cadence, never at build time or in
// tests. It reads the site's robots.txt, then the AWS Managed Policy
// Reference Guide's llms.txt, then the single-file guide it links
// (reference/full.md), one request each, waiting the robots.txt
// Crawl-delay between them and sending a User-Agent that names the
// repository. It parses every policy of the single-file guide that
// llms.txt lists and writes policies.json, SOURCE.json (provenance and the
// sha256 of each document as published), LICENSE and NOTICE, SOURCE.json
// last, then runs -verify.
//
// -verify checks, offline, that the data parses and is in canonical form,
// that every document passes policydoc's identity policy rules, that
// SOURCE.json matches the data and its hashes, and that LICENSE and NOTICE
// are the ones the generator writes.
//
// On success each mode prints one line to stderr: the number of policies,
// the count of each Type and the highest default version, for the
// maintainer to compare with iam-evaluation.md 10.4.
package main
