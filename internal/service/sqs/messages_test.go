package sqs

import (
	"strconv"
	"testing"

	"pgregory.net/rapid"
)

// Every Number value SQS accepts is a decimal: strconv reads it, so no
// run of signs or stray character gets through.
func TestValidNumberAcceptsOnlyDecimals(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		v := rapid.StringMatching(`[-+.eE0-9]{0,12}`).Draw(rt, "v")
		if !validNumber(v) {
			return
		}
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			rt.Fatalf("validNumber(%q) = true, but it is no decimal: %v", v, err)
		}
	})
}

// A valid Number takes one sign at most: a second sign, in any order,
// makes it invalid.
func TestValidNumberRefusesRepeatedSigns(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		v := rapid.StringMatching(`[0-9]{1,10}(\.[0-9]{1,5})?([eE][-+]?[0-9]{1,2})?`).Draw(rt, "v")
		signs := rapid.StringMatching(`[-+]{0,3}`).Draw(rt, "signs")
		if got, want := validNumber(signs+v), len(signs) <= 1; got != want {
			rt.Fatalf("validNumber(%q) = %v, want %v", signs+v, got, want)
		}
	})
}
