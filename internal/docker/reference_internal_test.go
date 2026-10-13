package docker

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestValidateReferenceAcceptsReferencesAndIDs(t *testing.T) {
	for _, ref := range []string{
		"public.ecr.aws/lambda/provided:al2023",
		"amazon/dynamodb-local",
		"localhost:5000/team/app@" + testDigest,
		"[::1]:5000/app:1.0",
		"repo:tag@" + testDigest,
		"a__b--c.d/e_f",
		testDigest,
		"0123456789ab",
	} {
		if err := ValidateReference(ref); err != nil {
			t.Errorf("ValidateReference(%q) = %v, want nil", ref, err)
		}
	}
}

func TestValidateReferenceRefusesNonReferences(t *testing.T) {
	for _, ref := range []string{
		"",
		"..",
		"../containers/abc",
		"library/../containers/abc",
		"a/./b",
		"/repo",
		"repo/",
		"Repo",
		"repo:",
		"repo:-tag",
		"repo@sha256:abc",
		"repo name",
		"repo?x=1",
		"repo#frag",
		"repo%2F..",
		strings.Repeat("a", nameMaxLength+1),
	} {
		var ierr *InvalidReferenceError
		if err := ValidateReference(ref); !errors.As(err, &ierr) || ierr.Ref != ref {
			t.Errorf("ValidateReference(%q) = %v, want *InvalidReferenceError", ref, err)
		}
	}
}

func TestValidateReferenceAcceptsTheGrammar(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		repo, tag := referenceParts(t)
		for _, ref := range []string{repo, repo + ":" + tag, repo + "@" + testDigest, repo + ":" + tag + "@" + testDigest} {
			err := ValidateReference(ref)
			if tooLong := len(repo) > nameMaxLength; tooLong != (err != nil) {
				t.Fatalf("ValidateReference(%q) = %v; name length %d, limit %d", ref, err, len(repo), nameMaxLength)
			}
		}
	})
}

func TestValidateReferenceRefusesDotSegments(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		repo, tag := referenceParts(t)
		segments := strings.Split(repo+":"+tag, "/")
		at := rapid.IntRange(0, len(segments)).Draw(t, "at")
		dots := rapid.SampledFrom([]string{".", ".."}).Draw(t, "dots")
		ref := strings.Join(slices.Insert(segments, at, dots), "/")
		if err := ValidateReference(ref); err == nil {
			t.Fatalf("ValidateReference(%q) = nil, want a dot segment refused", ref)
		}
	})
}
