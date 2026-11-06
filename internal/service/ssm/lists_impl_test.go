package ssm_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// Without MaxResults GetParametersByPath and DescribeParameters both answer
// pages of 10; the last page carries no NextToken (ssm-parameters.md 7.4,
// 7.5; SSM-19 and SSM-20 step 01).
func TestDefaultPageSizes(t *testing.T) {
	run(t, func(h *harness) {
		for i := range 51 {
			h.put(fmt.Sprintf("/page/p%02d", i), "v")
		}
		var sizes []int
		in := &ssmapi.GetParametersByPathInput{Path: "/page"}
		for {
			out := must(h, ssmapi.OpGetParametersByPath, in)
			sizes = append(sizes, len(out.Parameters))
			if out.NextToken == nil {
				break
			}
			in.NextToken = out.NextToken
		}
		if want := []int{10, 10, 10, 10, 10, 1}; !slices.Equal(sizes, want) {
			h.Errorf("GetParametersByPath page sizes = %v, want %v", sizes, want)
		}
		sizes = sizes[:0]
		din := &ssmapi.DescribeParametersInput{}
		for {
			out := must(h, ssmapi.OpDescribeParameters, din)
			sizes = append(sizes, len(out.Parameters))
			if out.NextToken == nil {
				break
			}
			din.NextToken = out.NextToken
		}
		if want := []int{10, 10, 10, 10, 10, 1}; !slices.Equal(sizes, want) {
			h.Errorf("DescribeParameters page sizes = %v, want %v", sizes, want)
		}
	})
}

// Path / lists the root parameters, and every parameter when recursive;
// an empty listing answers Parameters [] on both operations; a token of one
// listing is InvalidNextToken on the other, naming it (ssm-parameters.md
// 7.4, 7.10; SSM-07 step 07, SSM-19 step 05, SSM-20 step 02, SSM-32).
// UNVERIFIED (SSM-19): what Path / lists recursively.
func TestRootPathEmptyListingsAndForeignTokens(t *testing.T) {
	run(t, func(h *harness) {
		for _, n := range []string{"/root1", "root2", "/deep/a", "/deep/b/c"} {
			h.put(n, "v")
		}
		list := func(recursive bool) []string {
			h.Helper()
			out := must(h, ssmapi.OpGetParametersByPath, &ssmapi.GetParametersByPathInput{Path: "/", Recursive: new(recursive)})
			return names(out.Parameters, paramName)
		}
		if got, want := list(false), []string{"root1", "root2"}; !slices.Equal(got, want) {
			h.Errorf("Path / one level = %v, want %v", got, want)
		}
		if got, want := list(true), []string{"/deep/a", "/deep/b/c", "root1", "root2"}; !slices.Equal(got, want) {
			h.Errorf("Path / recursive = %v, want %v", got, want)
		}

		empty := must(h, ssmapi.OpGetParametersByPath, &ssmapi.GetParametersByPathInput{Path: "/none"})
		if empty.Parameters == nil || len(empty.Parameters) != 0 || empty.NextToken != nil {
			h.Errorf("GetParametersByPath of an empty path = %#v, NextToken %v; want [] and none", empty.Parameters, empty.NextToken)
		}
		none := must(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Path", Values: []string{"/none"}}}})
		if none.Parameters == nil || len(none.Parameters) != 0 {
			h.Errorf("DescribeParameters of an empty path = %#v, want []", none.Parameters)
		}

		byPath := must(h, ssmapi.OpGetParametersByPath, &ssmapi.GetParametersByPathInput{Path: "/", Recursive: new(true), MaxResults: new(int32(1))})
		_, err := call(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{NextToken: byPath.NextToken})
		wantError(h.T, "DescribeParameters with a GetParametersByPath token", err, ssmapi.IdentityInvalidNextToken(),
			"The following token is invalid: "+val(byPath.NextToken)+".")
	})
}

// A path without its leading slash is SSM's ValidationException with AWS's
// path text (ssm-parameters.md 7.4; SSM-19 step 02), and a path breaking
// the naming rules otherwise answers the same (UNVERIFIED SSM-19).
func TestPathRules(t *testing.T) {
	run(t, func(h *harness) {
		for _, path := range []string{"x", "x/y", "/x//y", "/x/y//", "/x y"} {
			_, err := call(h, ssmapi.OpGetParametersByPath, &ssmapi.GetParametersByPathInput{Path: path})
			wantCode(h.T, "Path "+path, err, ssmapi.IdentityValidationException())
			if err != nil && !strings.HasPrefix(err.Message, "The parameter doesn't meet the parameter name requirements.") {
				h.Errorf("Path %s message = %q, want AWS's path text", path, err.Message)
			}
		}
	})
}

// DescribeParameters filter defaults: Path without Option is OneLevel and
// Name without Option Equals (SSM-21 steps 01, 10); a tag filter without
// Values matches any value of its key (SSM-21 step 06); BeginsWith is
// case-sensitive and Contains is not (SSM-21 steps 07-08); Name values
// compare with or without the root slash (ssm-parameters.md 7.5).
// UNVERIFIED (SSM-21): another key without Values is InvalidFilterValue.
func TestDescribeFilterDefaults(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/d/one", "v")
		h.put("/d/sub/two", "v")
		h.put("rootname", "v")
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/d/tagged", Value: "v", Type: new(ssmapi.ParameterTypeString), Tags: []ssmapi.Tag{{Key: "team", Value: "x"}}})
		describe := func(filters ...ssmapi.ParameterStringFilter) []string {
			h.Helper()
			out := must(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{ParameterFilters: filters})
			return names(out.Parameters, metaName)
		}
		f := func(key string, option *string, values ...string) ssmapi.ParameterStringFilter {
			return ssmapi.ParameterStringFilter{Key: key, Option: option, Values: values}
		}
		for _, c := range []struct {
			what   string
			filter ssmapi.ParameterStringFilter
			want   []string
		}{
			{"Path without Option", f("Path", nil, "/d"), []string{"/d/one", "/d/tagged"}},
			{"tag without Values", f("tag:team", nil), []string{"/d/tagged"}},
			{"Name BeginsWith in another case", f("Name", new("BeginsWith"), "/D/"), nil},
			{"Name Contains in another case", f("Name", new("Contains"), "ONE"), []string{"/d/one"}},
			{"Name Equals root with slash", f("Name", nil, "/rootname"), []string{"rootname"}},
			{"Name BeginsWith root without slash", f("Name", new("BeginsWith"), "root"), []string{"rootname"}},
		} {
			if got := describe(c.filter); !slices.Equal(got, c.want) {
				h.Errorf("%s = %v, want %v", c.what, got, c.want)
			}
		}
		_, err := call(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{ParameterFilters: []ssmapi.ParameterStringFilter{f("Type", nil)}})
		wantCode(h.T, "Type without Values", err, ssmapi.IdentityInvalidFilterValue())
		_, err = call(h, ssmapi.OpGetParametersByPath, &ssmapi.GetParametersByPathInput{Path: "/d", ParameterFilters: []ssmapi.ParameterStringFilter{f("Type", nil)}})
		wantCode(h.T, "GetParametersByPath Type without Values", err, ssmapi.IdentityInvalidFilterValue())
	})
}

// GetParametersByPath reads SecureString values as GetParameter does: the
// base64 ciphertext without decryption, the plaintext with it
// (ssm-parameters.md 5.3), and KeyId and Type filters compare the stored
// values (KeyId as written, switch ssm.kms.key-id-echo).
func TestGetParametersByPathValuesAndFilters(t *testing.T) {
	run(t, func(h *harness) {
		h.follows("ssm.kms.key-id-echo", "verbatim")
		key := h.createKey()
		h.putSecure("/s/custom", "c-secret", &key)
		h.putSecure("/s/default", "d-secret", nil)
		h.put("/s/plain", "p")
		read := func(decrypt bool, filters ...ssmapi.ParameterStringFilter) map[string]string {
			h.Helper()
			out := must(h, ssmapi.OpGetParametersByPath, &ssmapi.GetParametersByPathInput{Path: "/s", WithDecryption: new(decrypt), ParameterFilters: filters})
			got := map[string]string{}
			for _, p := range out.Parameters {
				got[val(p.Name)] = val(p.Value)
			}
			return got
		}
		plain := read(true)
		if plain["/s/custom"] != "c-secret" || plain["/s/default"] != "d-secret" || plain["/s/plain"] != "p" {
			h.Errorf("WithDecryption values = %v", plain)
		}
		sealed := read(false)
		if got, err := h.openWithKMS(sealed["/s/default"], arnPrefix+"s/default"); err != nil || got != "d-secret" {
			h.Errorf("value without decryption opens to %q, %v; want d-secret", got, err)
		}
		if got := read(false, ssmapi.ParameterStringFilter{Key: "KeyId", Values: []string{"alias/aws/ssm"}}); len(got) != 1 || got["/s/default"] == "" {
			h.Errorf("KeyId Equals alias/aws/ssm = %v, want only /s/default", got)
		}
		if got := read(false, ssmapi.ParameterStringFilter{Key: "KeyId", Values: []string{key}}); len(got) != 1 || got["/s/custom"] == "" {
			h.Errorf("KeyId Equals %s = %v, want only /s/custom", key, got)
		}
		if got := read(false, ssmapi.ParameterStringFilter{Key: "Type", Option: new("BeginsWith"), Values: []string{"Str"}}); len(got) != 1 || got["/s/plain"] != "p" {
			h.Errorf("Type BeginsWith Str = %v, want only /s/plain", got)
		}
	})
}
