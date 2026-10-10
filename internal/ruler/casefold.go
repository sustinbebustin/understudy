package ruler

import (
	"slices"
	"strings"
	"unicode"
)

// caseless is an equals-ignore-case operand as Ruler compiles it
// (input.EqualsIgnoreCaseParser): one unit per UTF-16 code unit of the
// operand, accepting exactly the code unit's String.toLowerCase and
// String.toUpperCase under Locale.ROOT. Those are Java's full case mappings,
// so a unit can accept text of another length ("ß" accepts "ß" and "SS"),
// and they leave out the code unit itself when it is neither ("ǅ" accepts
// only "ǆ" and "Ǆ"). Java's UTF-8 encoder turns each half of a surrogate
// pair, taken alone, into "?", so a character above U+FFFF accepts only
// "??". The equals, prefix, suffix and anything-but forms all match against
// the same units, byte for byte.
type caseless []caseUnit

// caseUnit is the texts one operand code unit accepts: one, or two that
// differ.
type caseUnit []string

func newCaseless(s string) caseless {
	units := make(caseless, 0, len(s))
	for _, r := range s {
		if r > 0xFFFF {
			units = append(units, caseUnit{"?"}, caseUnit{"?"})
			continue
		}
		lower, upper := javaLower(r), javaUpper(r)
		if lower == upper {
			units = append(units, caseUnit{lower})
			continue
		}
		units = append(units, caseUnit{lower, upper})
	}
	return units
}

// equal reports whether all of s is a match of the units.
func (c caseless) equal(s string) bool {
	return slices.Contains(c.ends(s), len(s))
}

// prefixOf reports whether s starts with a match of the units.
func (c caseless) prefixOf(s string) bool {
	return len(c.ends(s)) > 0
}

// ends returns the offsets in s where a match of the units that starts at
// offset 0 can end. A unit's two texts can differ in length, so a match can
// end at more than one offset.
func (c caseless) ends(s string) []int {
	at := []int{0}
	for _, u := range c {
		var next []int
		for _, i := range at {
			for _, text := range u {
				if strings.HasPrefix(s[i:], text) && !slices.Contains(next, i+len(text)) {
					next = append(next, i+len(text))
				}
			}
		}
		if len(next) == 0 {
			return nil
		}
		at = next
	}
	return at
}

// suffixOf reports whether s ends with a match of the units. Ruler matches
// the suffix form on reversed bytes (SuffixEqualsIgnoreCaseParser), which is
// matching the units from the end.
func (c caseless) suffixOf(s string) bool {
	at := []int{len(s)}
	for _, v := range slices.Backward(c) {
		var next []int
		for _, i := range at {
			for _, text := range v {
				if strings.HasSuffix(s[:i], text) && !slices.Contains(next, i-len(text)) {
					next = append(next, i-len(text))
				}
			}
		}
		if len(next) == 0 {
			return false
		}
		at = next
	}
	return true
}

// javaLower is String.valueOf(r).toLowerCase(Locale.ROOT) for a character of
// the Basic Multilingual Plane: the simple mapping, except U+0130, the one
// unconditional multi-character lowercase mapping of SpecialCasing.txt. A
// lone "Σ" has no preceding letter, so the final-sigma rule does not apply.
func javaLower(r rune) string {
	if r == 'İ' {
		return "i̇"
	}
	return string(unicode.ToLower(r))
}

// javaUpper is String.valueOf(r).toUpperCase(Locale.ROOT) for a character of
// the Basic Multilingual Plane: the full mapping of SpecialCasing.txt where
// it has an unconditional one, otherwise the simple mapping.
func javaUpper(r rune) string {
	if s, ok := fullUpper[r]; ok {
		return s
	}
	return string(unicode.ToUpper(r))
}

// fullUpper holds the unconditional uppercase mappings of SpecialCasing.txt
// (Unicode 17.0.0, unchanged since 15.0.0) that differ from the simple
// mapping.
var fullUpper = map[rune]string{ //nolint:gochecknoglobals // a constant table; Go has no constant maps.
	'ß': "SS", 'ﬀ': "FF", 'ﬁ': "FI", 'ﬂ': "FL",
	'ﬃ': "FFI", 'ﬄ': "FFL", 'ﬅ': "ST", 'ﬆ': "ST",
	'և': "ԵՒ", 'ﬓ': "ՄՆ", 'ﬔ': "ՄԵ", 'ﬕ': "ՄԻ",
	'ﬖ': "ՎՆ", 'ﬗ': "ՄԽ", 'ŉ': "ʼN", 'ΐ': "Ϊ́",
	'ΰ': "Ϋ́", 'ǰ': "J̌", 'ẖ': "H̱", 'ẗ': "T̈",
	'ẘ': "W̊", 'ẙ': "Y̊", 'ẚ': "Aʾ", 'ὐ': "Υ̓",
	'ὒ': "Υ̓̀", 'ὔ': "Υ̓́", 'ὖ': "Υ̓͂", 'ᾶ': "Α͂",
	'ῆ': "Η͂", 'ῒ': "Ϊ̀", 'ΐ': "Ϊ́", 'ῖ': "Ι͂",
	'ῗ': "Ϊ͂", 'ῢ': "Ϋ̀", 'ΰ': "Ϋ́", 'ῤ': "Ρ̓",
	'ῦ': "Υ͂", 'ῧ': "Ϋ͂", 'ῶ': "Ω͂", 'ᾀ': "ἈΙ",
	'ᾁ': "ἉΙ", 'ᾂ': "ἊΙ", 'ᾃ': "ἋΙ", 'ᾄ': "ἌΙ",
	'ᾅ': "ἍΙ", 'ᾆ': "ἎΙ", 'ᾇ': "ἏΙ", 'ᾈ': "ἈΙ",
	'ᾉ': "ἉΙ", 'ᾊ': "ἊΙ", 'ᾋ': "ἋΙ", 'ᾌ': "ἌΙ",
	'ᾍ': "ἍΙ", 'ᾎ': "ἎΙ", 'ᾏ': "ἏΙ", 'ᾐ': "ἨΙ",
	'ᾑ': "ἩΙ", 'ᾒ': "ἪΙ", 'ᾓ': "ἫΙ", 'ᾔ': "ἬΙ",
	'ᾕ': "ἭΙ", 'ᾖ': "ἮΙ", 'ᾗ': "ἯΙ", 'ᾘ': "ἨΙ",
	'ᾙ': "ἩΙ", 'ᾚ': "ἪΙ", 'ᾛ': "ἫΙ", 'ᾜ': "ἬΙ",
	'ᾝ': "ἭΙ", 'ᾞ': "ἮΙ", 'ᾟ': "ἯΙ", 'ᾠ': "ὨΙ",
	'ᾡ': "ὩΙ", 'ᾢ': "ὪΙ", 'ᾣ': "ὫΙ", 'ᾤ': "ὬΙ",
	'ᾥ': "ὭΙ", 'ᾦ': "ὮΙ", 'ᾧ': "ὯΙ", 'ᾨ': "ὨΙ",
	'ᾩ': "ὩΙ", 'ᾪ': "ὪΙ", 'ᾫ': "ὫΙ", 'ᾬ': "ὬΙ",
	'ᾭ': "ὭΙ", 'ᾮ': "ὮΙ", 'ᾯ': "ὯΙ", 'ᾳ': "ΑΙ",
	'ᾼ': "ΑΙ", 'ῃ': "ΗΙ", 'ῌ': "ΗΙ", 'ῳ': "ΩΙ",
	'ῼ': "ΩΙ", 'ᾲ': "ᾺΙ", 'ᾴ': "ΆΙ", 'ῂ': "ῊΙ",
	'ῄ': "ΉΙ", 'ῲ': "ῺΙ", 'ῴ': "ΏΙ", 'ᾷ': "Α͂Ι",
	'ῇ': "Η͂Ι", 'ῷ': "Ω͂Ι",
}
