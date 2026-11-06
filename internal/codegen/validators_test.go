package codegen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// overlayValidators reads every dev.understudy#patternValidator of
// codegen/overlays by shape id.
func overlayValidators(t *testing.T) map[string]func(string) bool {
	t.Helper()
	paths, err := filepath.Glob("../../codegen/overlays/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no overlays: %v", err)
	}
	out := map[string]func(string) bool{}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Shapes map[string]struct {
				Traits map[string]json.RawMessage `json:"traits"`
			} `json:"shapes"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		for id, s := range doc.Shapes {
			v, ok := s.Traits["dev.understudy#patternValidator"]
			if !ok {
				continue
			}
			var pv struct{ AllOf, NoneOf []string }
			if err := json.Unmarshal(v, &pv); err != nil {
				t.Fatalf("%s %s: %v", p, id, err)
			}
			all, none := compileAll(t, pv.AllOf), compileAll(t, pv.NoneOf)
			out[id] = func(s string) bool {
				for _, re := range all {
					if !re.MatchString(s) {
						return false
					}
				}
				for _, re := range none {
					if re.MatchString(s) {
						return false
					}
				}
				return true
			}
		}
	}
	return out
}

func compileAll(t *testing.T, ps []string) []*regexp.Regexp {
	t.Helper()
	out := make([]*regexp.Regexp, len(ps))
	for i, p := range ps {
		out[i] = regexp.MustCompile(p)
	}
	return out
}

// ECMA-262 \s and line terminators beyond ASCII, and two characters that
// are neither, spelled by code point.
var (
	nbsp               = string(rune(0x00A0))
	ideographicSpace   = string(rune(0x3000))
	byteOrderMark      = string(rune(0xFEFF))
	lineSeparator      = string(rune(0x2028))
	paragraphSeparator = string(rune(0x2029))
	// ecmaWhitespace is every \s character that is not a line terminator:
	// TAB, VT, FF, ZWNBSP and the Zs category.
	ecmaWhitespace = string([]rune{
		'\t', '\v', '\f', ' ', 0x00A0, 0x1680,
		0x2000, 0x2001, 0x2002, 0x2003, 0x2004, 0x2005, 0x2006, 0x2007, 0x2008, 0x2009, 0x200A,
		0x202F, 0x205F, 0x3000, 0xFEFF,
	})
	// nextLine is a line terminator in Unicode but not in ECMA-262, and
	// mongolianVowelSeparator left Zs in Unicode 6.3.
	nextLine                = string(rune(0x0085))
	mongolianVowelSeparator = string(rune(0x180E))
)

// TestPatternValidatorsAgreeWithTheirLookarounds: each hand validator of a
// pattern RE2 cannot compile (smithy-models.md 6.2), for a lookaround, a
// backreference or a repeat count above 1000, accepts and rejects what the
// ECMA-262 pattern does. The strings probe each lookaround, each
// backreference and each repeat bound the validator splits or drops. They
// are BMP-only: ECMA without the u flag counts UTF-16 units where the
// validators count code points, as the overlays' evidence records.
func TestPatternValidatorsAgreeWithTheirLookarounds(t *testing.T) {
	validators := overlayValidators(t)
	type probes struct{ accept, reject []string }
	// ^(?!\s*$).+$
	notBlank := probes{
		accept: []string{"a", " a ", "id-1", nbsp + "x", "x" + ecmaWhitespace, ecmaWhitespace + "x", nextLine, mongolianVowelSeparator},
		reject: []string{
			"", " ", "\t " + nbsp, ideographicSpace + byteOrderMark, ecmaWhitespace,
			"a\nb", "a" + lineSeparator, "\r", paragraphSeparator, " " + paragraphSeparator,
		},
	}
	// ^.{1,100000}$, whose bound the validator leaves to the shape's
	// @length, so no string runs past it.
	oneLine := probes{
		accept: []string{"a", " ", ecmaWhitespace, nextLine, strings.Repeat("a", 100000)},
		reject: []string{"", "\n", "a\nb", "\r", "a" + lineSeparator, paragraphSeparator},
	}
	const (
		role     = "arn:aws:iam::123456789012:role/"
		s3Object = "https://bucket.s3.amazonaws.com/"
	)
	tests := map[string]probes{
		"com.amazonaws.eventbridge#PathParameter": notBlank,
		// ^[a-z0-9]([a-z0-9]|-(?!-)){1,61}[a-z0-9]$
		"com.amazonaws.iam#accountAliasType": {
			accept: []string{"abc", "my-alias", "a-b-c", "a1-2b", strings.Repeat("a", 63)},
			reject: []string{"ab", "a--b", "-ab", "ab-", "ABC", "a_b", strings.Repeat("a", 64)},
		},
		// ^(?!^[0-9]+$)([a-zA-Z0-9-_]+)$
		"com.amazonaws.lambda#Alias": {
			accept: []string{"live", "1a", "v-1", "_", "12-"},
			reject: []string{"", "123", "0", "a b", "a.b", "$LATEST"},
		},
		// ^(^(?!(system\x2e)))(^[_a-zA-Z0-9])([^$]*)$
		"com.amazonaws.lambda#CollectionName": {
			accept: []string{"orders", "_x", "a.b", "system", "systemx.y", "a\nb"},
			reject: []string{"", "system.users", "$x", "a$b", ".a", "-a"},
		},
		// ^(arn:...:lambda:)?(<region>:)?(\d{12}:)?(function:)?([a-zA-Z0-9-_]{1,64})(:((?!\d+$)[0-9a-zA-Z-_]+))?$
		"com.amazonaws.lambda#FunctionUrlFunctionName": {
			accept: []string{
				"my-function", "my-function:live", "fn:1a",
				"arn:aws:lambda:us-east-1:123456789012:function:my-function",
				"arn:aws:lambda:us-gov-west-1:123456789012:function:fn:prod",
				"123456789012:function:fn",
			},
			reject: []string{"", "fn:123", "fn:", "a b", "arn:aws:lambda:us-east-1:123456789012:function:fn:1", strings.Repeat("f", 65)},
		},
		// ^((?!^\d+$)^[0-9a-zA-Z-_]+$)$
		"com.amazonaws.lambda#FunctionUrlQualifier": {
			accept: []string{"live", "v1", "1a", "-"},
			reject: []string{"", "1", "123", "$LATEST", "a b"},
		},
		// ^[0-9A-Za-z\.\-_]*(?<!\.)$
		"com.amazonaws.lambda#S3Bucket": {
			accept: []string{"", "bucket", "my.bucket", "a.b-c_d", "x"},
			reject: []string{"bucket.", ".", "a..", "a b", "a/b"},
		},
		// ^(?=[/\.\-_A-Za-z0-9]+)((?!aws\.).*)|(\$(\.[\w_-]+(\[(\d+|\*)\])*)*)$
		"com.amazonaws.scheduler#Source": {
			accept: []string{
				"a", "aw", "aws", "awsx", "aws_", "aws\n", "aws" + lineSeparator, "AWS.events", "my.app", "/path", ".x", "-x", "_x", "9",
				"a b", "a\nb", "aw\n", // the first alternative has no $
				"$", "$.detail", "$.detail.items[0]", "$.a[*].b", " $.a", "aws.events$", "aws.x$.a[1]", // the second has no ^
			},
			reject: []string{"", "aws.", "aws.events", " a", "@x", "\n", "éx", "$x", "$.a b", "$.a[x]", "$.a[1", "aws.events$x"},
		},
		// ^(?=.*[a-zA-Z_\-\.])[a-zA-Z0-9_\-\.]+$
		"com.amazonaws.sfn#CharacterRestrictedName": {
			accept: []string{"a", "1a", "a1", "-", ".", "_", "1.2", "123-", "my-state_machine.v1"},
			reject: []string{"", "1", "123", "a b", "a$", "a:b", "a\n", "1\n", "café"},
		},
		"com.amazonaws.ssm#AlarmName": notBlank,
		// ^arn:aws[a-z0-9-]*:iam::\d{12}:role\/[\w-\/.@+=,]{1,1017}$
		"com.amazonaws.ssm#CloudConnectorIamRoleArn": {
			accept: []string{
				role + "r", role + "my-role", role + "path/to/role", role + "a.b@c+d=e,f_g", role + "-", role + "/",
				"arn:aws-us-gov:iam::123456789012:role/r", "arn:aws-cn:iam::123456789012:role/r",
				role + strings.Repeat("r", 1000), role + strings.Repeat("r", 1001), role + strings.Repeat("r", 1017),
			},
			reject: []string{
				"", role, role + strings.Repeat("r", 1018), role + "a b", role + "a:b", role + "r\n", role + "café",
				"arn:aws:iam::12345678901:role/r", "arn:aws:iam::123456789012:user/r", "arn:aws:iam:us-east-1:123456789012:role/r",
			},
		},
		"com.amazonaws.ssm#DocumentReviewComment":           notBlank,
		"com.amazonaws.ssm#InstancePropertyFilterValue":     oneLine,
		"com.amazonaws.ssm#InstancePropertyStringFilterKey": oneLine,
		"com.amazonaws.ssm#MetadataKey":                     notBlank,
		"com.amazonaws.ssm#OpsItemCategory":                 notBlank,
		"com.amazonaws.ssm#OpsItemDataKey":                  notBlank,
		"com.amazonaws.ssm#OpsItemSeverity":                 notBlank,
		"com.amazonaws.ssm#OpsItemSource":                   notBlank,
		"com.amazonaws.ssm#OpsItemTitle":                    notBlank,
		"com.amazonaws.ssm#OpsMetadataFilterKey":            notBlank,
		"com.amazonaws.ssm#OpsMetadataResourceId":           notBlank,
		"com.amazonaws.ssm#Policy":                          notBlank,
		"com.amazonaws.ssm#RegistrationMetadataKey":         notBlank,
		"com.amazonaws.ssm#RegistrationMetadataValue":       notBlank,
		// ^([\-]?\d{4}(?!\d{2}\b))((-?)((0[1-9]|1[0-2])(\3([12]\d|0[1-9]|3[01]))?|W([0-4]\d|5[0-2])(-?[1-7])?|(00[1-9]|0[1-9]\d|[12]\d{2}|3([0-5]\d|6[1-6])))([T\s]((([01]\d|2[0-3])((:?)[0-5]\d)?|24\:?00)([\.,]\d(?!:))?)?(\17[0-5]\d([\.,]\d)?)?([zZ]|([\-])([01]\d|2[0-3]):?([0-5]\d)?)?)?)?$
		"com.amazonaws.ssm#StringDateTime": {
			accept: []string{
				"2020", "-2020", "2020-01", "2020-01-15", "20200115", "2020-01T10",
				"2020011", "202001T10", // (?!\d{2}\b) passes a seventh digit or a word character after six
				"2020-001", "2020-366", "2020-W01", "2020W01", "2020-W01-1", "2020W011",
				"2020-01-15T10", "2020-01-15T10:30", "2020-01-15T1030", "2020-01-15T10:30:45", "2020-01-15T103045", // \17 is ":" or ""
				"2020-01-15T10:30:45.5", "2020-01-15T10:30,5", "2020-01-15T10.5", "2020-01-15T1030.545",
				"2020-01-15T10.530", "2020-01-15T24:0030", // \17 unset matches the empty string
				"2020-01-15T24:00", "2020-01-15T2400", "2020-01-15T",
				"2020-01-15T10:30Z", "2020-01-15T10:30z", "2020-01-15T10:30-05:00", "2020-01-15T10:30-0500", "2020-01-15T10:30-05", "20200115T103045Z",
				"2020-01-15 10:30", "2020-01-15\n10:30", "2020-01-15" + nbsp + "10:30", "2020-01-15" + lineSeparator + "10:30",
				"2020-01-15" + ideographicSpace + "10", "2020-01-15" + byteOrderMark + "10",
			},
			reject: []string{
				"", "-", "2020 10:00", "2020T10", "2020-13", "2020-00", "2020-01-32", "2020-W53", "2020-367", "2020-000",
				"202001", "-202001", "202001 10", "202001" + lineSeparator + "10", // (?!\d{2}\b)
				"2020-0115", "202001-15", // \3
				"2020-01-15T10:3045", "2020-01-15T1030:45", "2020-01-15T24:00:30", // \17
				"2020-01-15T10:30.5:45", "2020-01-15T10.5:30", "2020-01-15T10:30.545", // (?!:), and \17 after a fraction
				"2020-01-15T24:30", "2020-01-15T25:00", "2020-01-15T10:60", "2020-01-15T10:30:45.12", "2020-01-15T10:30+05:00",
				"2020-01-15" + nextLine + "10:30", "2020-01-15" + mongolianVowelSeparator + "10",
			},
		},
		// ^https:\/\/[-a-zA-Z0-9@:%._\+~#=]{1,253}\.s3(\.[a-z\d-]{9,16})?\.amazonaws\.com\/.{1,2000}$
		"com.amazonaws.ssm#TargetLocationsURL": {
			accept: []string{
				s3Object + "x", s3Object + "path/to/file.json", "https://my-bucket.s3.us-east-1.amazonaws.com/key", s3Object + "a b",
				s3Object + strings.Repeat("x", 1000), s3Object + strings.Repeat("x", 1001), s3Object + strings.Repeat("x", 2000),
			},
			reject: []string{
				"", s3Object, s3Object + strings.Repeat("x", 2001),
				s3Object + "a\nb", s3Object + "\r", s3Object + "a" + lineSeparator, s3Object + paragraphSeparator,
				"http://bucket.s3.amazonaws.com/x", "https://bucket.s3.us-east.amazonaws.com/x", "https://bucket.amazonaws.com/x",
			},
		},
	}
	if len(validators) != len(tests) {
		t.Errorf("the overlays carry %d pattern validators, the test checks %d", len(validators), len(tests))
	}
	for id, tt := range tests {
		match, ok := validators[id]
		if !ok {
			t.Errorf("no overlay validator for %s", id)
			continue
		}
		for _, s := range tt.accept {
			if !match(s) {
				t.Errorf("%s rejects %q, which its ECMA pattern accepts", id, s)
			}
		}
		for _, s := range tt.reject {
			if match(s) {
				t.Errorf("%s accepts %q, which its ECMA pattern rejects", id, s)
			}
		}
	}
}
