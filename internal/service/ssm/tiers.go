package ssm

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"slices"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The two stored tiers (ssm-parameters.md 4.2).
const (
	tierStandard = tier(ssmapi.ParameterTierStandard)
	tierAdvanced = tier(ssmapi.ParameterTierAdvanced)
)

// tierLimits is what one tier allows in an account and Region
// (ssm-parameters.md 4.2, 9.3).
type tierLimits struct {
	// valueChars is the largest value, counted in characters (Unicode code
	// points): 2049 x U+00E9, 4098 bytes, is a valid Standard value
	// (SSM-12 step 04).
	valueChars int
	// parameters is how many parameters of the tier a scope can hold.
	parameters int
}

// limits is t's limits.
func (t tier) limits() tierLimits {
	if t == tierAdvanced {
		return tierLimits{valueChars: 8192, parameters: 100_000}
	}
	return tierLimits{valueChars: 4096, parameters: 10_000}
}

// valueChars is the size of a value as the tier limits count it.
func valueChars(value string) int { return utf8.RuneCountInString(value) }

// resolveTier is the tier a write stores (ssm-parameters.md 4.2). current
// is the parameter's tier, nil on a create.
//
//   - Omitted: Standard on a create (the default tier; the service setting
//     that changes it is unbound), the current tier on an overwrite, so an
//     Advanced parameter stays Advanced (ssm-parameters.md 4.2; SSM-13
//     step 05).
//   - Standard on an Advanced parameter: refused, a tier is never
//     downgraded (ssm-parameters.md 12.4; SSM-13 step 04).
//   - Intelligent-Tiering: Advanced when the value is over Standard's size,
//     the parameter is already Advanced, or a new Standard parameter would
//     not fit; Standard otherwise (ssm-parameters.md 4.2; SSM-13 steps 01,
//     02, 06).
func (w write) resolveTier(st *State, current *tier) (tier, *awserr.Error) {
	requested := w.in.Tier
	switch {
	case requested == nil && current != nil:
		return *current, nil
	case requested == nil:
		return tierStandard, nil
	}
	switch *requested {
	case ssmapi.ParameterTierAdvanced:
		return tierAdvanced, nil
	case ssmapi.ParameterTierIntelligentTiering:
		switch {
		case valueChars(w.in.Value) > tierStandard.limits().valueChars, current != nil && *current == tierAdvanced:
			return tierAdvanced, nil
		case current != nil && *current == tierStandard:
			return tierStandard, nil
		case tierFull(st, tierStandard):
			return tierAdvanced, nil
		}
		return tierStandard, nil
	case ssmapi.ParameterTierStandard:
	}
	if current != nil && *current == tierAdvanced {
		return "", ssmapi.ErrValidationException("This parameter uses the advanced-parameter tier. " +
			"You can't downgrade a parameter from the advanced-parameter tier to the standard-parameter tier. " +
			"If necessary, you can delete the advanced parameter and recreate it as a standard parameter. " +
			"Be aware that standard parameters have a value limit of 4096 characters.")
	}
	return tierStandard, nil
}

// checkSize refuses a value over t's size limit in characters, with AWS's
// texts (ssm-parameters.md 4.2; SSM-12 steps 02 and 06).
func (w write) checkSize(t tier) *awserr.Error {
	if valueChars(w.in.Value) <= t.limits().valueChars {
		return nil
	}
	if t == tierAdvanced {
		return ssmapi.ErrValidationException("The specified parameter value is too large. Advanced-tier parameters support a maximum parameter value of 8192 characters.")
	}
	return ssmapi.ErrValidationException("Standard tier parameters support a maximum parameter value of 4096 characters. " +
		"To create a larger parameter value, upgrade the parameter to use the advanced-parameter tier. " +
		"For more information, see https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-store-advanced-parameters.html")
}

// checkCount refuses a write that would put a parameter in tier t when the
// scope already holds as many parameters of t as the tier allows
// (ssm-parameters.md 4.2, 9.3). A full Standard tier answers AWS's text
// (SSM-33 step 01). The Advanced tier's text is unrecorded, so it is
// understudy's own, naming the parameter and the limit.
func (w write) checkCount(st *State, t tier) *awserr.Error {
	if !tierFull(st, t) {
		return nil
	}
	limit := t.limits().parameters
	if t != tierAdvanced {
		return ssmapi.ErrParameterLimitExceeded(fmt.Sprintf(
			"You have reached the maximum number of standard parameters for this AWS account and Region (%d). "+
				"To add more parameters, either delete existing parameters or upgrade to the advanced-parameter tier (charges apply).", limit))
	}
	return ssmapi.ErrParameterLimitExceeded(fmt.Sprintf(
		"Parameter %q was not written: this account and Region already hold %d %s parameters, the most the %s tier allows. "+
			"Delete parameters this account and Region no longer need.",
		w.name.spelling, limit, t, t))
}

// tierFull reports that st holds as many parameters of tier t as the tier
// allows. It counts only when the scope holds that many parameters in all,
// so a write below the limit costs no scan.
func tierFull(st *State, t tier) bool {
	limit := t.limits().parameters
	if len(st.Parameters) < limit {
		return false
	}
	n := 0
	for _, p := range st.Parameters {
		if p.latest().Tier == t {
			n++
		}
	}
	return n >= limit
}

// allowedPattern is a compiled AllowedPattern: the pattern as given, and a
// regexp that matches only a whole value (ssm-parameters.md 4.4: 10,000 is
// refused by \d{1,4} although it holds a run of 1 to 4 digits).
type allowedPattern struct {
	source string
	whole  *regexp.Regexp
}

// structuralPatternErrors are the regexp/syntax errors no regex dialect
// accepts. A pattern that fails with one is a client error; any other
// failure is a construct Go RE2 lacks, such as a lookahead or a
// backreference (spec "Scope").
func structuralPatternErrors() [6]syntax.ErrorCode {
	return [6]syntax.ErrorCode{
		syntax.ErrMissingBracket, syntax.ErrMissingParen, syntax.ErrUnexpectedParen,
		syntax.ErrTrailingBackslash, syntax.ErrMissingRepeatArgument, syntax.ErrInvalidCharRange,
	}
}

// compilePattern compiles a non-empty AllowedPattern as Go RE2. A
// structural error answers InvalidAllowedPatternException with AWS's text
// (ssm-parameters.md 4.4; SSM-15 step 04); any other compile failure
// answers 501 (spec "Scope"), although AWS's Java dialect accepts a
// lookahead and a possessive quantifier (SSM-15 steps 02-03).
func (w write) compilePattern(op, source string) (allowedPattern, *awserr.Error) {
	// The pattern compiles alone first: wrapped, a stray ")(" or a trailing
	// backslash would pair with the anchoring group.
	_, err := regexp.Compile(source)
	if err == nil {
		var whole *regexp.Regexp
		if whole, err = regexp.Compile(`^(?:` + source + `)$`); err == nil {
			return allowedPattern{source: source, whole: whole}, nil
		}
	}
	if se, ok := errors.AsType[*syntax.Error](err); ok {
		structural := structuralPatternErrors()
		if slices.Contains(structural[:], se.Code) {
			return allowedPattern{}, ssmapi.ErrInvalidAllowedPatternException("The syntax of the allowedPattern parameter is invalid.")
		}
	}
	return allowedPattern{}, errUnsupported(op, fmt.Sprintf(
		"AllowedPattern %q, which Go RE2 cannot compile (%v), for parameter %q", source, err, w.name.spelling))
}

// checkPattern refuses a value that is not a whole match of the pattern
// (ssm-parameters.md 4.4 [D], 12.4: AWS's text names the pattern, never the
// value).
func (w write) checkPattern(p allowedPattern) *awserr.Error {
	if p.whole.MatchString(w.in.Value) {
		return nil
	}
	return ssmapi.ErrParameterPatternMismatchException("Parameter value, cannot be validated against allowedPattern: " + p.source)
}

// checkDataType refuses a DataType other than text and
// aws:ssm:integration (ssm-parameters.md 4.3); aws:ec2:image answers 501
// before this (spec "Scope"). The text is understudy's, after moto's
// (ssm-parameters.md 4.3 [E]).
func (w write) checkDataType(dataType string) *awserr.Error {
	switch dataType {
	case defaultDataType, dataTypeIntegration:
		return nil
	}
	return ssmapi.ErrValidationException(fmt.Sprintf(
		"Parameter %q was not written: DataType %q is not supported. Use text, aws:ssm:integration or aws:ec2:image; data type names are all lowercase.",
		w.name.spelling, dataType))
}

// DataTypes besides text (ssm-parameters.md 4.3).
const (
	dataTypeIntegration = "aws:ssm:integration"
	dataTypeEC2Image    = "aws:ec2:image"
)
