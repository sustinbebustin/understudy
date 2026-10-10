package fault

import (
	"regexp"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func TestARNGlobMatchesTheArchitectureExamples(t *testing.T) {
	cases := []struct {
		glob, arn string
		want      bool
	}{
		{"arn:aws:sqs:*:*:orders-*", "arn:aws:sqs:us-east-1:000000000000:orders-dlq", true},
		{"arn:aws:sqs:*:*:orders-*", "arn:aws:sqs:us-east-1:000000000000:payments", false},
		{"arn:aws:s3:::bucket/*", "arn:aws:s3:::bucket/a/b/c.txt", true},
		{"arn:aws:lambda:*:*:function:worker?", "arn:aws:lambda:eu-west-1:123456789012:function:worker1", true},
		{"arn:aws:lambda:*:*:function:worker?", "arn:aws:lambda:eu-west-1:123456789012:function:worker12", false},
		{"arn:aws:s3:::caf?", "arn:aws:s3:::café", true},
		{"*", "arn:aws:sns:us-east-1:000000000000:topic", true},
		{"arn:aws:sqs:us-east-1:000000000000:q", "arn:aws:sqs:us-east-1:000000000000:q2", false},
		{"", "arn:aws:sqs:us-east-1:000000000000:anything", true},
	}
	for _, c := range cases {
		if got := NewARNGlob(c.glob).Match(c.arn); got != c.want {
			t.Errorf("NewARNGlob(%q).Match(%q) = %v, want %v", c.glob, c.arn, got, c.want)
		}
	}
}

func TestCanMatchARNRejectsGlobsWithoutTheARNPrefix(t *testing.T) {
	cases := []struct {
		glob string
		want bool
	}{
		{"", true},
		{"*", true},
		{"arn:aws:sqs:*:*:orders-*", true},
		{"*:orders-*", true},
		{"a?n:*", true},
		{"ar*", true},
		{"orders-*", false},
		{"ar", false},
		{"arn", false},
		{"ARN:*", false},
	}
	for _, c := range cases {
		if got := NewARNGlob(c.glob).canMatchARN(); got != c.want {
			t.Errorf("NewARNGlob(%q).canMatchARN() = %v, want %v", c.glob, got, c.want)
		}
	}
}

func TestCanMatchARNNeverRejectsAGlobThatMatches(t *testing.T) {
	char := rapid.SampledFrom([]rune{'a', 'r', 'n', ':', 's', 'é', '*', '?'})
	rapid.Check(t, func(t *rapid.T) {
		glob := string(rapid.SliceOfN(char, 1, 8).Draw(t, "glob"))
		arn := arnPrefix + string(rapid.SliceOfN(char, 0, 8).Draw(t, "rest"))
		if NewARNGlob(glob).Match(arn) && !NewARNGlob(glob).canMatchARN() {
			t.Fatalf("NewARNGlob(%q) matches %q but canMatchARN is false", glob, arn)
		}
	})
}

// referenceMatch translates the glob to an anchored regular expression: the
// independent oracle for the hand-written matcher.
func referenceMatch(t *rapid.T, glob, arn string) bool {
	var re strings.Builder
	re.WriteString(`(?s)\A`)
	for _, r := range glob {
		switch r {
		case '*':
			re.WriteString(".*")
		case '?':
			re.WriteString(".")
		default:
			re.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	re.WriteString(`\z`)
	compiled, err := regexp.Compile(re.String())
	if err != nil {
		t.Fatalf("reference regexp for %q: %v", glob, err)
	}
	return compiled.MatchString(arn)
}

func TestARNGlobAgreesWithReferenceMatcher(t *testing.T) {
	// A small alphabet makes matches common; é and 日 exercise multi-byte ?.
	alphabet := []rune{'a', 'b', ':', '/', '-', '.', 'é', '日'}
	char := rapid.SampledFrom(alphabet)
	rapid.Check(t, func(t *rapid.T) {
		arnRunes := rapid.SliceOfN(char, 0, 12).Draw(t, "arn")
		arn := "arn:aws:sqs:" + string(arnRunes)
		globChar := rapid.OneOf(char, rapid.Just('*'), rapid.Just('?'))
		var glob string
		if rapid.Bool().Draw(t, "derived") {
			// Derive the glob from the ARN so matches are frequent.
			src := []rune(arn)
			out := make([]rune, 0, len(src))
			for _, r := range src {
				switch rapid.IntRange(0, 5).Draw(t, "edit") {
				case 0:
					out = append(out, '?')
				case 1:
					out = append(out, '*')
				case 2:
					// drop the character
				default:
					out = append(out, r)
				}
			}
			glob = string(out)
			if glob == "" {
				// The empty glob is the zero ARNGlob, which matches
				// everything; the reference regexp would match only "".
				glob = "*"
			}
		} else {
			glob = "arn:aws:sqs:" + string(rapid.SliceOfN(globChar, 1, 10).Draw(t, "glob"))
		}
		got := NewARNGlob(glob).Match(arn)
		if want := referenceMatch(t, glob, arn); got != want {
			t.Fatalf("NewARNGlob(%q).Match(%q) = %v, reference says %v", glob, arn, got, want)
		}
	})
}
