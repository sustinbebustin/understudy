package smithy_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

func TestDerefNilGivesZero(t *testing.T) {
	type pair struct {
		A string
		B int
	}
	if got := smithy.Deref[string](nil); got != "" {
		t.Errorf("Deref[string](nil) = %q, want empty", got)
	}
	if got := smithy.Deref[pair](nil); got != (pair{}) {
		t.Errorf("Deref[pair](nil) = %+v, want zero", got)
	}
}

func TestDerefNonNilGivesValue(t *testing.T) {
	type pair struct {
		A string
		B int
	}
	s := "x"
	if got := smithy.Deref(&s); got != "x" {
		t.Errorf("Deref(&s) = %q, want x", got)
	}
	p := pair{A: "a", B: 2}
	if got := smithy.Deref(&p); got != p {
		t.Errorf("Deref(&p) = %+v, want %+v", got, p)
	}
}
