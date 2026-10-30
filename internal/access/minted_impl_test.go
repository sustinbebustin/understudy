package access_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
)

// The caller-named part of a minted pattern is escaped as an exact ARN is
// (policydoc.Literal) for every rule, not only the log group's; the
// wildcard the rule adds is not (ARCHITECTURE.md 17; formats from the
// Service Authorization Reference tables cited in minted_test.go).
func TestMintedPatternEscapesTheCallerNamedPart(t *testing.T) {
	tests := []struct {
		name, source, arn, pattern string
	}{
		{"secret name holding ?", sarSecrets, "arn:aws:secretsmanager:us-east-1:111111111111:secret:a?b-a1B2c3", "arn:aws:secretsmanager:us-east-1:111111111111:secret:a${?}b-??????"},
		{"secret name holding ${", sarSecrets, "arn:aws:secretsmanager:us-east-1:111111111111:secret:a${x}-a1B2c3", "arn:aws:secretsmanager:us-east-1:111111111111:secret:a${$}{x}-??????"},
		{"secret suffix of six hyphenated characters is the last six", sarSecrets, "arn:aws:secretsmanager:us-east-1:111111111111:secret:a-b-c-a1B2c3", "arn:aws:secretsmanager:us-east-1:111111111111:secret:a-b-c-??????"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pattern, minted := access.MintedPattern(access.Direct{}, arn(t, tc.arn))
			if !minted || pattern != tc.pattern {
				t.Errorf("MintedPattern(%s) = %q, %t; want %q, true (%s)", tc.arn, pattern, minted, tc.pattern, tc.source)
			}
		})
	}
}
