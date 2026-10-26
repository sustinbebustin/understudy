package main

import "github.com/sustinbebustin/understudy/internal/policycatalogue"

// parseFull reads every policy idx lists out of the single-file guide
// (iam-evaluation.md 10.3, 10.4), in idx order. Not implemented yet: it refuses.
func parseFull([]byte, index) ([]policycatalogue.Policy, error) {
	return nil, &notImplementedError{op: "parsing full.md"}
}
