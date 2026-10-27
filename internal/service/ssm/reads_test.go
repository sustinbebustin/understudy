package ssm_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// A selector picks a version (name:<n>) or a label (name:<label>); the
// answer names the bare parameter and echoes the selector with its leading
// colon (ssm-parameters.md 6.3, aws-doc sysman-paramstore-labels.html: the
// CLI example shows "Name": "<parameter-name>" with "Selector":
// ":<label-name>"). A version or label that does not exist is
// ParameterVersionNotFound, never a fallback to the latest (6.1 [D]; 2.2
// [M]).
func TestGetParameterSelectors(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/sel/a", "v1")
		h.overwrite("/sel/a", "v2")
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/sel/a", ParameterVersion: new(int64(1)), Labels: []string{"prod"}})
		for _, c := range []struct {
			ask, selector, value string
			version              int64
		}{
			{"/sel/a:1", ":1", "v1", 1},
			{"/sel/a:2", ":2", "v2", 2},
			{"/sel/a:prod", ":prod", "v1", 1},
		} {
			got := h.mustGet(c.ask, false)
			if val(got.Name) != "/sel/a" || val(got.Selector) != c.selector || val(got.Value) != c.value || got.Version != c.version {
				h.Errorf("GetParameter %s = Name %q Selector %q Value %q Version %d, want /sel/a %q %q %d",
					c.ask, val(got.Name), val(got.Selector), val(got.Value), got.Version, c.selector, c.value, c.version)
			}
			many := must(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: []string{c.ask}})
			if len(many.Parameters) != 1 || val(many.Parameters[0].Selector) != c.selector || val(many.Parameters[0].Value) != c.value {
				h.Errorf("GetParameters [%s] = %+v, want Selector %q Value %q", c.ask, many.Parameters, c.selector, c.value)
			}
		}
		if got := h.mustGet("/sel/a", false); got.Selector != nil || val(got.Value) != "v2" {
			h.Errorf("GetParameter without a selector = Selector %v Value %q, want no Selector and the latest v2", got.Selector, val(got.Value))
		}
		for _, ask := range []string{"/sel/a:3", "/sel/a:staging"} {
			err := h.get(ask)
			wantCode(h.T, "GetParameter "+ask, err, ssmapi.IdentityParameterVersionNotFound())
		}
	})
}

// GetParameters lists what it found in alphabetical order ("The results
// for GetParameters requests are listed in alphabetical order",
// ssm-parameters.md 7.3 [M][D]) and the names it did not find in
// InvalidParameters (doc example; G5-G6), which is [] and present when
// every name was found (7.10; third-party-recorded G5) since Powertools
// indexes it without a default (10.3 [C]).
func TestGetParametersOrderAndInvalid(t *testing.T) {
	run(t, func(h *harness) {
		for _, n := range []string{"/g/c", "/g/a", "/g/b"} {
			h.put(n, "v"+n)
		}
		out := must(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: []string{"/g/c", "/g/a", "EC2DevServerTypo", "/g/b"}})
		if got := names(out.Parameters, paramName); !slices.Equal(got, []string{"/g/a", "/g/b", "/g/c"}) {
			h.Errorf("Parameters = %v, want /g/a /g/b /g/c", got)
		}
		if !slices.Equal(out.InvalidParameters, []string{"EC2DevServerTypo"}) {
			h.Errorf("InvalidParameters = %v, want [EC2DevServerTypo]", out.InvalidParameters)
		}
		for _, p := range out.Parameters {
			if val(p.Value) != "v"+val(p.Name) {
				h.Errorf("%s Value = %q", val(p.Name), val(p.Value))
			}
		}
		all := must(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: []string{"/g/b", "/g/a"}})
		if all.InvalidParameters == nil || len(all.InvalidParameters) != 0 {
			h.Errorf("InvalidParameters with every name found = %#v, want [] present", all.InvalidParameters)
		}
	})
}

// GetParametersByPath (ssm-parameters.md 7.4): without Recursive it lists
// one level, the two direct children of /x and not a deeper one or a
// sibling tree ([T] parameters_by_path_data_source_test.go:L20-L66); a
// trailing slash is the same path (API example /Branch312/Dev/; [T]
// L98-L115); Recursive lists every parameter under the path. Items name
// the parameter as stored and carry no Selector.
func TestGetParametersByPathLevels(t *testing.T) {
	run(t, func(h *harness) {
		for _, n := range []string{"/x/a", "/x/b", "/x/y/c", "/x/y/z/d", "/xz/a", "/top"} {
			h.put(n, "v")
		}
		list := func(path string, recursive bool) []string {
			h.Helper()
			var out []string
			in := &ssmapi.GetParametersByPathInput{Path: path, Recursive: new(recursive)}
			for {
				page := must(h, ssmapi.OpGetParametersByPath, in)
				for _, p := range page.Parameters {
					if p.Selector != nil {
						h.Errorf("%s item %s carries Selector %q", path, val(p.Name), *p.Selector)
					}
					out = append(out, val(p.Name))
				}
				if page.NextToken == nil {
					return out
				}
				in.NextToken = page.NextToken
			}
		}
		oneLevel := []string{"/x/a", "/x/b"}
		recursive := []string{"/x/a", "/x/b", "/x/y/c", "/x/y/z/d"}
		for _, path := range []string{"/x", "/x/"} {
			if got := list(path, false); !slices.Equal(sortedCopy(got), oneLevel) {
				h.Errorf("Path %s one level = %v, want %v", path, got, oneLevel)
			}
			if got := list(path, true); !slices.Equal(sortedCopy(got), recursive) {
				h.Errorf("Path %s recursive = %v, want %v", path, got, recursive)
			}
		}
	})
}

// Under switch ssm.list.order (name-ascending), GetParametersByPath and
// DescribeParameters list in byte order of names across pages of any
// MaxResults; the last page carries no NextToken (ssm-parameters.md 7.6:
// "If there are no additional items to return, the string is empty";
// 12.5 item 3).
func TestListOrderAcrossPages(t *testing.T) {
	run(t, func(h *harness) {
		h.follows("ssm.list.order", "name-ascending")
		want := []string{"/o/A", "/o/B", "/o/a", "/o/a.b", "/o/a/x", "/o/b", "/o/b-c", "/o/z/z/z"}
		for _, i := range []int{5, 2, 7, 0, 3, 6, 1, 4} {
			h.put(want[i], "v")
		}
		for _, size := range []int32{1, 2, 3, 10} {
			var got []string
			in := &ssmapi.GetParametersByPathInput{Path: "/o", Recursive: new(true), MaxResults: new(size)}
			for {
				page := must(h, ssmapi.OpGetParametersByPath, in)
				if len(page.Parameters) > int(size) {
					h.Errorf("MaxResults %d answered %d items", size, len(page.Parameters))
				}
				got = append(got, names(page.Parameters, paramName)...)
				if page.NextToken == nil {
					break
				}
				in.NextToken = page.NextToken
			}
			if !slices.Equal(got, want) {
				h.Errorf("GetParametersByPath MaxResults %d = %v, want %v", size, got, want)
			}

			got = nil
			din := &ssmapi.DescribeParametersInput{MaxResults: new(size), ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Path", Option: new("Recursive"), Values: []string{"/o"}}}}
			for {
				page := must(h, ssmapi.OpDescribeParameters, din)
				got = append(got, names(page.Parameters, metaName)...)
				if page.NextToken == nil {
					break
				}
				din.NextToken = page.NextToken
			}
			if !slices.Equal(got, want) {
				h.Errorf("DescribeParameters MaxResults %d = %v, want %v", size, got, want)
			}
		}
	})
}

// A NextToken that is not one the listing issued is InvalidNextToken
// ("The specified token isn't valid.", ssm-parameters.md 7.10 [M]), on each
// paginated operation.
func TestGarbageNextTokenIsInvalidNextToken(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/tok/a", "v")
		garbage := new("not-a-token")
		wantCode(h.T, "GetParametersByPath", errOf(call(h, ssmapi.OpGetParametersByPath, &ssmapi.GetParametersByPathInput{Path: "/tok", NextToken: garbage})), ssmapi.IdentityInvalidNextToken())
		wantCode(h.T, "DescribeParameters", errOf(call(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{NextToken: garbage})), ssmapi.IdentityInvalidNextToken())
		wantCode(h.T, "GetParameterHistory", errOf(call(h, ssmapi.OpGetParameterHistory, &ssmapi.GetParameterHistoryInput{Name: "/tok/a", NextToken: garbage})), ssmapi.IdentityInvalidNextToken())
	})
}

// GetParametersByPath filters (ssm-parameters.md 7.4, aws-doc
// API_GetParametersByPath and API_ParameterStringFilter): keys Type, KeyId
// and Label only, so tag, DataType, Name, Path and Tier are
// InvalidFilterKey; Label takes Equals only (InvalidFilterOption). A Label
// filter returns the labeled version of each parameter, not the latest
// (the labels guide's example shows versions 3, 2 and 2,
// sysman-paramstore-labels.html; [R-weak] test_ssm.py:L134-L151).
func TestGetParametersByPathFilters(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/f/plain", "p1")
		h.overwrite("/f/plain", "p2")
		h.putSecure("/f/secret", "s1", nil)
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/f/plain", ParameterVersion: new(int64(1)), Labels: []string{"latest"}})

		byPath := func(filters ...ssmapi.ParameterStringFilter) *ssmapi.GetParametersByPathOutput {
			h.Helper()
			return must(h, ssmapi.OpGetParametersByPath, &ssmapi.GetParametersByPathInput{Path: "/f", ParameterFilters: filters})
		}
		for _, key := range []string{"tag:env", "DataType", "Name", "Path", "Tier"} {
			_, err := call(h, ssmapi.OpGetParametersByPath, &ssmapi.GetParametersByPathInput{Path: "/f", ParameterFilters: []ssmapi.ParameterStringFilter{{Key: key, Values: []string{"x"}}}})
			wantCode(h.T, "filter key "+key, err, ssmapi.IdentityInvalidFilterKey())
		}
		_, err := call(h, ssmapi.OpGetParametersByPath, &ssmapi.GetParametersByPathInput{Path: "/f", ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Label", Option: new("BeginsWith"), Values: []string{"lat"}}}})
		wantCode(h.T, "Label BeginsWith", err, ssmapi.IdentityInvalidFilterOption())

		out := byPath(ssmapi.ParameterStringFilter{Key: "Label", Option: new("Equals"), Values: []string{"latest"}})
		if len(out.Parameters) != 1 || val(out.Parameters[0].Name) != "/f/plain" || val(out.Parameters[0].Value) != "p1" || out.Parameters[0].Version != 1 {
			h.Errorf("Label Equals latest = %+v, want /f/plain version 1 p1", out.Parameters)
		}
		out = byPath(ssmapi.ParameterStringFilter{Key: "Type", Option: new("Equals"), Values: []string{"SecureString"}})
		if got := names(out.Parameters, paramName); !slices.Equal(got, []string{"/f/secret"}) {
			h.Errorf("Type Equals SecureString = %v, want [/f/secret]", got)
		}
	})
}

// DescribeParameters metadata (ssm-parameters.md 7.5, 12.5 item 3): Name,
// ARN, Type, Version (latest), the resolved Tier, DataType and Policies,
// which is [] and present when there are none (7.10, DescribeParameters doc
// example); Description and AllowedPattern absent when unset; KeyId only
// for SecureString (5.4 [M]). Terraform's exact request returns exactly
// one entry and no NextToken.
func TestDescribeParametersMetadata(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/m/bare", "v")
		h.overwrite("/m/bare", "w")
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/m/full", Value: "7", Type: new(ssmapi.ParameterTypeString), Description: new("d"), AllowedPattern: new(`\d`), Tier: new(ssmapi.ParameterTierAdvanced)})
		out := must(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{
			ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Name", Option: new("Equals"), Values: []string{"/m/bare"}}},
		})
		if len(out.Parameters) != 1 || out.NextToken != nil {
			h.Fatalf("Terraform's request = %d entries, NextToken %v; want one entry and no NextToken", len(out.Parameters), out.NextToken)
		}
		bare := out.Parameters[0]
		switch {
		case val(bare.Name) != "/m/bare" || val(bare.ARN) != arnPrefix+"m/bare" || val(bare.Type) != ssmapi.ParameterTypeString || bare.Version != 2:
			h.Errorf("bare = Name %q ARN %q Type %q Version %d", val(bare.Name), val(bare.ARN), val(bare.Type), bare.Version)
		case val(bare.Tier) != ssmapi.ParameterTierStandard || val(bare.DataType) != "text":
			h.Errorf("bare = Tier %q DataType %q, want Standard text", val(bare.Tier), val(bare.DataType))
		case bare.Policies == nil || len(bare.Policies) != 0:
			h.Errorf("bare Policies = %#v, want [] present", bare.Policies)
		case bare.Description != nil || bare.AllowedPattern != nil || bare.KeyId != nil:
			h.Errorf("bare = Description %v AllowedPattern %v KeyId %v, want all absent", bare.Description, bare.AllowedPattern, bare.KeyId)
		case bare.LastModifiedDate == nil:
			h.Error("bare has no LastModifiedDate")
		}
		full := h.mustDescribeOne("/m/full")
		if val(full.Description) != "d" || val(full.AllowedPattern) != `\d` || val(full.Tier) != ssmapi.ParameterTierAdvanced {
			h.Errorf("full = Description %q AllowedPattern %q Tier %q", val(full.Description), val(full.AllowedPattern), val(full.Tier))
		}
	})
}

// DescribeParameters filters (ssm-parameters.md 7.5, aws-doc
// API_ParameterStringFilter): every key but Label (InvalidFilterKey);
// options Equals and BeginsWith, Contains for Name, Recursive and OneLevel
// for Path, and an option a key does not take is InvalidFilterOption;
// KeyId alias/aws/ssm finds the default-key SecureStrings (parameter-search
// doc); filters combine by AND, as the doc example of Type=String with a
// Name value. Shared lists nothing, since no parameter can be shared
// (coverage note), and the deprecated Filters member answers 501.
func TestDescribeParametersFilters(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/df/app/one", "v")
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/df/app/two", Value: "a,b", Type: new(ssmapi.ParameterTypeStringList), Tags: []ssmapi.Tag{{Key: "env", Value: "dev"}}})
		h.putSecure("/df/app/deep/three", "v", nil)
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/df/other", Value: "v", Type: new(ssmapi.ParameterTypeString), Tier: new(ssmapi.ParameterTierAdvanced)})

		describe := func(filters ...ssmapi.ParameterStringFilter) []string {
			h.Helper()
			var got []string
			in := &ssmapi.DescribeParametersInput{ParameterFilters: filters}
			for {
				page := must(h, ssmapi.OpDescribeParameters, in)
				got = append(got, names(page.Parameters, metaName)...)
				if page.NextToken == nil {
					return sortedCopy(got)
				}
				in.NextToken = page.NextToken
			}
		}
		f := func(key, option string, values ...string) ssmapi.ParameterStringFilter {
			return ssmapi.ParameterStringFilter{Key: key, Option: new(option), Values: values}
		}
		for _, c := range []struct {
			what    string
			filters []ssmapi.ParameterStringFilter
			want    []string
		}{
			{"Name BeginsWith", []ssmapi.ParameterStringFilter{f("Name", "BeginsWith", "/df/app/")}, []string{"/df/app/deep/three", "/df/app/one", "/df/app/two"}},
			{"Name Contains", []ssmapi.ParameterStringFilter{f("Name", "Contains", "deep")}, []string{"/df/app/deep/three"}},
			{"Type Equals", []ssmapi.ParameterStringFilter{f("Type", "Equals", "StringList")}, []string{"/df/app/two"}},
			{"KeyId Equals alias/aws/ssm", []ssmapi.ParameterStringFilter{f("KeyId", "Equals", "alias/aws/ssm")}, []string{"/df/app/deep/three"}},
			{"Path OneLevel", []ssmapi.ParameterStringFilter{f("Path", "OneLevel", "/df/app")}, []string{"/df/app/one", "/df/app/two"}},
			{"Path Recursive", []ssmapi.ParameterStringFilter{f("Path", "Recursive", "/df/app")}, []string{"/df/app/deep/three", "/df/app/one", "/df/app/two"}},
			{"Tier Equals", []ssmapi.ParameterStringFilter{f("Tier", "Equals", "Advanced")}, []string{"/df/other"}},
			{"DataType Equals", []ssmapi.ParameterStringFilter{f("DataType", "Equals", "text"), f("Name", "BeginsWith", "/df/other")}, []string{"/df/other"}},
			{"tag Equals", []ssmapi.ParameterStringFilter{f("tag:env", "Equals", "dev")}, []string{"/df/app/two"}},
			{"Type AND Path", []ssmapi.ParameterStringFilter{f("Type", "Equals", "String"), f("Path", "Recursive", "/df")}, []string{"/df/app/one", "/df/other"}},
		} {
			if got := describe(c.filters...); !slices.Equal(got, c.want) {
				h.Errorf("%s = %v, want %v", c.what, got, c.want)
			}
		}

		_, err := call(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{ParameterFilters: []ssmapi.ParameterStringFilter{f("Label", "Equals", "x")}})
		wantCode(h.T, "DescribeParameters Label", err, ssmapi.IdentityInvalidFilterKey())
		for _, bad := range []ssmapi.ParameterStringFilter{f("Type", "Contains", "String"), f("Name", "Recursive", "/df"), f("Path", "Equals", "/df")} {
			_, err := call(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{ParameterFilters: []ssmapi.ParameterStringFilter{bad}})
			wantCode(h.T, "DescribeParameters "+bad.Key+" "+val(bad.Option), err, ssmapi.IdentityInvalidFilterOption())
		}

		shared := must(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{Shared: new(true)})
		if shared.Parameters == nil || len(shared.Parameters) != 0 {
			h.Errorf("Shared = %#v, want []", shared.Parameters)
		}
		_, err = call(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{Filters: []ssmapi.ParametersFilter{{Key: "Name", Values: []string{"/df/other"}}}})
		wantNotImplemented(h.T, "the deprecated Filters", err, "Filters")
	})
}

func sortedCopy(ss []string) []string {
	out := slices.Clone(ss)
	slices.Sort(out)
	return out
}
