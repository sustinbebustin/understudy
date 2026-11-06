package main

import (
	"errors"
	"os"
	"slices"
	"testing"

	awsmanagedpolicies "github.com/sustinbebustin/understudy/third_party/aws-managed-policies"
)

// A SOURCE.json entry for a policy policies.json does not hold is a
// problem of SOURCE.json as a whole, beside the count that no longer
// matches.
func TestVerifyReportsAnUnknownSourceEntry(t *testing.T) {
	dir, _, src := written(t)
	fsys := asMapFS(t, dir)
	src.Policies = slices.Clone(src.Policies)
	src.Policies[3].ARN = "arn:aws:iam::aws:policy/ExampleGone"
	fsys[awsmanagedpolicies.SourceFile].Data = mustEncodeSource(t, src)
	err := verify(fsys, dir)
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("verify = %v, want a *VerifyError", err)
	}
	if !slices.ContainsFunc(ve.Problems, func(p Problem) bool {
		return p.Path == awsmanagedpolicies.SourceFile && p.Policy == "" && p.Detail != ""
	}) {
		t.Errorf("problems = %+v, want one for SOURCE.json naming no policy", ve.Problems)
	}
}

// write replaces a previous run's files in place and leaves no temporary
// file behind.
func TestWriteReplacesAPreviousRun(t *testing.T) {
	dir, cat, src := written(t)
	src.Retrieved = "2027-01-02"
	if err := write(dir, cat, src); err != nil {
		t.Fatalf("second write = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(dataFiles) {
		t.Errorf("directory holds %d entries, want the %d data files", len(entries), len(dataFiles))
	}
	if err := verify(os.DirFS(dir), dir); err != nil {
		t.Errorf("verify after the second write = %v", err)
	}
}
