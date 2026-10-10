package awserr_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The expected texts follow AWS's recorded validation grammar
// (error-matrix.md section 4).
func TestCoralMessageMatchesRecordedGrammar(t *testing.T) {
	tests := []struct {
		name string
		vs   []awserr.Violation
		want string
	}{
		{
			name: "CloudWatch SetAlarmState enum, recorded 2025-09-22",
			vs: []awserr.Violation{{
				Path:       awserr.Path{"stateValue"},
				Value:      awserr.RenderedText{Text: "INVALID"},
				Constraint: awserr.Enum{Values: []string{"INSUFFICIENT_DATA", "ALARM", "OK"}},
			}},
			want: "1 validation error detected: Value 'INVALID' at 'stateValue' failed to satisfy constraint: Member must satisfy enum value set: [INSUFFICIENT_DATA, ALARM, OK]",
		},
		{
			name: "required member renders null without quotes",
			vs: []awserr.Violation{{
				Path:       awserr.Path{"tableName"},
				Value:      awserr.RenderedNull{},
				Constraint: awserr.Required{},
			}},
			want: "1 validation error detected: Value null at 'tableName' failed to satisfy constraint: Member must not be null",
		},
		{
			name: "pattern then max length on one member, indexed path",
			vs: []awserr.Violation{
				{
					Path:       awserr.Path{"targets", "1", "member", "id"},
					Value:      awserr.RenderedText{Text: "a b"},
					Constraint: awserr.Pattern{Echo: `[\.\-_A-Za-z0-9]+`},
				},
				{
					Path:       awserr.Path{"targets", "1", "member", "id"},
					Value:      awserr.RenderedText{Text: "a b"},
					Constraint: awserr.LengthMax{Max: 64},
				},
			},
			want: `2 validation errors detected: Value 'a b' at 'targets.1.member.id' failed to satisfy constraint: Member must satisfy regular expression pattern: [\.\-_A-Za-z0-9]+; ` +
				"Value 'a b' at 'targets.1.member.id' failed to satisfy constraint: Member must have length less than or equal to 64",
		},
		{
			name: "blob values are never echoed; numbers use value, not length",
			vs: []awserr.Violation{
				{Path: awserr.Path{"plaintext"}, Value: awserr.RenderedOmitted{}, Constraint: awserr.LengthMin{Min: 1}},
				{Path: awserr.Path{"numberOfBytes"}, Value: awserr.RenderedText{Text: "2048"}, Constraint: awserr.RangeMax{Max: "1024"}},
				{Path: awserr.Path{"limit"}, Value: awserr.RenderedText{Text: "0"}, Constraint: awserr.RangeMin{Min: "1"}},
			},
			want: "3 validation errors detected: Value at 'plaintext' failed to satisfy constraint: Member must have length greater than or equal to 1; " +
				"Value '2048' at 'numberOfBytes' failed to satisfy constraint: Member must have value less than or equal to 1024; " +
				"Value '0' at 'limit' failed to satisfy constraint: Member must have value greater than or equal to 1",
		},
		{
			name: "nil value renders as null",
			vs:   []awserr.Violation{{Path: awserr.Path{"x"}, Constraint: awserr.Required{}}},
			want: "1 validation error detected: Value null at 'x' failed to satisfy constraint: Member must not be null",
		},
		{
			// The inner constraints are given out of order; AWS's order wins.
			name: "Lambda CreateFunction vpcConfig.subnetIds rolled up, recorded 2025-11-25",
			vs: []awserr.Violation{{
				Path:  awserr.Path{"vpcConfig", "subnetIds"},
				Value: awserr.RenderedText{Text: "[<subnet_id_2>]"},
				Constraint: awserr.Composite{Scope: awserr.ElementScope, Of: []awserr.MemberConstraint{
					awserr.Pattern{Echo: "subnet-[0-9a-z]*"},
					awserr.LengthMin{Min: 0},
					awserr.LengthMax{Max: 1024},
				}},
			}},
			want: "1 validation error detected: Value '[<subnet_id_2>]' at 'vpcConfig.subnetIds' failed to satisfy constraint: Member must satisfy constraint: " +
				"[Member must have length less than or equal to 1024, Member must have length greater than or equal to 0, Member must satisfy regular expression pattern: subnet-[0-9a-z]*]",
		},
		{
			name: "Lambda CreateAlias routing weights map value, recorded 2025-11-25",
			vs: []awserr.Violation{{
				Path:  awserr.Path{"routingConfig", "additionalVersionWeights"},
				Value: awserr.RenderedText{Text: "{2=2.0}"},
				Constraint: awserr.Composite{Scope: awserr.MapValueScope, Of: []awserr.MemberConstraint{
					awserr.Required{},
					awserr.RangeMin{Min: "0.0"},
					awserr.RangeMax{Max: "1.0"},
				}},
			}},
			want: "1 validation error detected: Value '{2=2.0}' at 'routingConfig.additionalVersionWeights' failed to satisfy constraint: Map value must satisfy constraint: " +
				"[Member must have value less than or equal to 1.0, Member must have value greater than or equal to 0.0, Member must not be null]",
		},
		{
			name: "Lambda CreateAlias routing weights map keys, recorded 2025-11-25",
			vs: []awserr.Violation{{
				Path:  awserr.Path{"routingConfig", "additionalVersionWeights"},
				Value: awserr.RenderedText{Text: "{$LATEST=0.5}"},
				Constraint: awserr.Composite{Scope: awserr.MapKeysScope, Of: []awserr.MemberConstraint{
					awserr.LengthMax{Max: 1024},
					awserr.LengthMin{Min: 1},
					awserr.Pattern{Echo: "[0-9]+"},
				}},
			}},
			want: "1 validation error detected: Value '{$LATEST=0.5}' at 'routingConfig.additionalVersionWeights' failed to satisfy constraint: Map keys must satisfy constraint: " +
				"[Member must have length less than or equal to 1024, Member must have length greater than or equal to 1, Member must satisfy regular expression pattern: [0-9]+]",
		},
		{
			name: "Lambda CreateFunction architectures enum then not-null, recorded 2025-11-25",
			vs: []awserr.Violation{{
				Path:  awserr.Path{"architectures"},
				Value: awserr.RenderedText{Text: "[X86_64]"},
				Constraint: awserr.Composite{Scope: awserr.ElementScope, Of: []awserr.MemberConstraint{
					awserr.Required{},
					awserr.Enum{Values: []string{"x86_64", "arm64"}},
				}},
			}},
			want: "1 validation error detected: Value '[X86_64]' at 'architectures' failed to satisfy constraint: Member must satisfy constraint: " +
				"[Member must satisfy enum value set: [x86_64, arm64], Member must not be null]",
		},
	}
	for _, tt := range tests {
		if got := awserr.CoralMessage(tt.vs); got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
	}
}

// Encoding has no recorded rolled-up position; it sorts with pattern and enum,
// after the bounds and before not-null.
func TestCompositeOrdersEncodingWithPatternAndEnum(t *testing.T) {
	v := awserr.Violation{
		Path:  awserr.Path{"names"},
		Value: awserr.RenderedText{Text: "[x]"},
		Constraint: awserr.Composite{Scope: awserr.ElementScope, Of: []awserr.MemberConstraint{
			awserr.Required{},
			awserr.Encoding{},
			awserr.LengthMax{Max: 8},
		}},
	}
	want := "Value '[x]' at 'names' failed to satisfy constraint: Member must satisfy constraint: " +
		"[Member must have length less than or equal to 8, Member must contain only valid UTF-8 characters, Member must not be null]"
	if got := v.Coral(); got != want {
		t.Errorf("Coral:\n got %s\nwant %s", got, want)
	}
}

// A nil Constraint or an unknown CompositeScope is a validator bug: the text
// says so in understudy's words and carries no Go type syntax.
func TestCoralRendersValidatorBugsAsUnderstudyText(t *testing.T) {
	tests := []struct {
		name string
		v    awserr.Violation
		want string
	}{
		{
			name: "nil constraint",
			v:    awserr.Violation{Path: awserr.Path{"x"}, Value: awserr.RenderedNull{}},
			want: "Value null at 'x' failed to satisfy constraint: understudy validator bug: no constraint recorded",
		},
		{
			name: "zero composite scope",
			v: awserr.Violation{
				Path:       awserr.Path{"x"},
				Value:      awserr.RenderedNull{},
				Constraint: awserr.Composite{Of: []awserr.MemberConstraint{awserr.Required{}}},
			},
			want: "Value null at 'x' failed to satisfy constraint: Member (understudy validator bug: unknown composite scope) must satisfy constraint: [Member must not be null]",
		},
	}
	for _, tt := range tests {
		if got := tt.v.Coral(); got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
	}
}

// An empty violation list is a validator bug too: the message must not pass
// for AWS text with a count of 0.
func TestCoralMessageRendersNoViolationsAsUnderstudyText(t *testing.T) {
	const want = "understudy validator bug: validation failed with no violations recorded"
	for _, vs := range [][]awserr.Violation{nil, {}} {
		if got := awserr.CoralMessage(vs); got != want {
			t.Errorf("CoralMessage(%#v) = %q, want %q", vs, got, want)
		}
	}
}
