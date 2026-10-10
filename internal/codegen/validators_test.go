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

// ECMA-262 \s and line terminators beyond ASCII, spelled by code point.
var (
	nbsp             = string(rune(0x00A0))
	ideographicSpace = string(rune(0x3000))
	byteOrderMark    = string(rune(0xFEFF))
	lineSeparator    = string(rune(0x2028))
)

// TestPatternValidatorsAgreeWithTheirLookarounds: each hand validator of
// the seven lookaround patterns RE2 cannot compile (smithy-models.md 6.2)
// accepts and rejects what the ECMA-262 pattern does. The strings probe
// each lookaround's boundary.
func TestPatternValidatorsAgreeWithTheirLookarounds(t *testing.T) {
	validators := overlayValidators(t)
	tests := map[string]struct{ accept, reject []string }{
		// ^(?!\s*$).+$
		"com.amazonaws.eventbridge#PathParameter": {
			accept: []string{"a", " a ", "id-1", nbsp + "x"},
			reject: []string{"", " ", "\t " + nbsp, ideographicSpace + byteOrderMark, "a\nb", "a" + lineSeparator, "\r"},
		},
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
