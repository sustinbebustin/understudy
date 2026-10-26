package main

import (
	"fmt"
	"io/fs"
	"strings"
)

// Problem is one way the catalogue directory differs from what the
// generator writes.
type Problem struct {
	// Path is "policies.json", "SOURCE.json", "LICENSE" or "NOTICE".
	Path string
	// Policy is the policy's ARN, "" when the problem is not one policy's.
	Policy string
	Detail string
}

// VerifyError lists every problem -verify found, so one run reports them
// all.
type VerifyError struct {
	Dir      string
	Problems []Problem
}

func (e *VerifyError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s is not the catalogue the generator writes (%d problems):", e.Dir, len(e.Problems))
	for _, p := range e.Problems {
		if p.Policy != "" {
			fmt.Fprintf(&b, "\n  %s: %s: %s", p.Path, p.Policy, p.Detail)
		} else {
			fmt.Fprintf(&b, "\n  %s: %s", p.Path, p.Detail)
		}
	}
	b.WriteString("\nThe files are generator output and are never edited by hand; restore them with git checkout, or re-run 'go run ./internal/cmd/managedpolicies -fetch' to refresh them on purpose.")
	return b.String()
}

// verify checks the catalogue directory fsys, named dir in messages,
// offline. Not implemented yet: it refuses.
func verify(fs.FS, string) error {
	return &notImplementedError{op: "verifying the catalogue"}
}
