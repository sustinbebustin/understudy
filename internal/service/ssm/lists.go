package ssm

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/paging"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Page sizes without MaxResults: 10 for both (ssm-parameters.md 7.4, 7.5;
// SSM-19 and SSM-20 step 01, 10 entries and a NextToken for 12).
const (
	byPathPageSize   = 10
	describePageSize = 10
)

// The Parameter Store listings, each named inside the NextTokens it issues.
// Both list in byte order of canonical names (switch ssm.list.order).
const (
	listingByPath   paging.Listing = "ssm.parameters-by-path"
	listingDescribe paging.Listing = "ssm.describe-parameters"
)

// pageOf sorts items by key and cuts the page that starts at token, of
// maxResults items (def without it; the generated range check has bounded
// it). A token no page of l issued is InvalidNextToken naming the token
// (ssm-parameters.md 7.10; SSM-32).
func pageOf[T any](l paging.Listing, items []T, key func(T) string, maxResults *int32, def int, token *string) (paging.Page[T], *awserr.Error) {
	n := def
	if maxResults != nil {
		n = int(*maxResults)
	}
	p, err := paging.SortedKeyset(l, items, key, n, token)
	if err != nil {
		return paging.Page[T]{}, ssmapi.ErrInvalidNextToken("The following token is invalid: " + *token + ".")
	}
	return p, nil
}

// predicate reports whether a listing keeps parameter p read at version v.
type predicate func(p *Parameter, v Version) bool

// option is a ParameterStringFilter Option (ssm-parameters.md 7.4, 7.5).
type option string

const (
	optEquals     option = "Equals"
	optBeginsWith option = "BeginsWith"
	optContains   option = "Contains"
	optOneLevel   option = "OneLevel"
	optRecursive  option = "Recursive"
)

// keySpec is what one filter key accepts in one operation: its options,
// the first of them the default, and how a filter of it is built.
type keySpec struct {
	options []option
	// valueless is a key whose filter may omit Values.
	valueless bool
	build     func(o option, values []string) (predicate, *awserr.Error)
}

// keySpecs maps a filter key to its spec in one operation; ok false is a
// key the operation does not take.
type keySpecs func(key string) (keySpec, bool)

// filterKeys is the filter keys one operation takes: their specs, and the
// list AWS's InvalidFilterKey text names, in AWS's order
// (ssm-parameters.md 7.4, 7.5; SSM-01 step 05, SSM-19 step 03).
type filterKeys struct {
	specs keySpecs
	named string
}

// parseFilters turns ParameterStringFilters into predicates, judging each
// filter's key, then its option, then its values. Filters combine by AND
// (ssm-parameters.md 7.5, the doc example of Type=String with a Name
// value) and the values of one filter by OR. UNVERIFIED (SSM-21),
// ssm-parameters.md 7.5: values ORed, and a filter without Values answers
// InvalidFilterValue.
func parseFilters(op string, fs []ssmapi.ParameterStringFilter, keys filterKeys) ([]predicate, *awserr.Error) {
	out := make([]predicate, 0, len(fs))
	for _, f := range fs {
		spec, ok := keys.specs(f.Key)
		if !ok {
			return nil, ssmapi.ErrInvalidFilterKey("The following filter key is not valid: " + f.Key + ". Valid filter keys include: " + keys.named + ".")
		}
		o := spec.options[0]
		if f.Option != nil {
			o = option(*f.Option)
			if !slices.Contains(spec.options, o) {
				return nil, errFilterOption(*f.Option, spec.options)
			}
		}
		if len(f.Values) == 0 && !spec.valueless {
			return nil, ssmapi.ErrInvalidFilterValue(fmt.Sprintf(
				"The %s filter of ssm %s has no Values. Give it at least one value to compare with.", f.Key, op))
		}
		p, err := spec.build(o, f.Values)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// errFilterOption is AWS's answer for an option the key does not take,
// naming the key's options in alphabetical order (ssm-parameters.md 7.4,
// 7.5; SSM-19 step 04 [Equals], SSM-21 step 09 [BeginsWith, Equals]).
// UNVERIFIED (SSM-21): the order for Name's three options and Path's two.
func errFilterOption(got string, valid []option) *awserr.Error {
	names := make([]string, len(valid))
	for i, o := range valid {
		names[i] = string(o)
	}
	slices.Sort(names)
	return ssmapi.ErrInvalidFilterOption("The following filter option is not valid: " + got + ". Valid options include: [" + strings.Join(names, ", ") + "].")
}

// keepsAll reports whether every filter keeps p at v.
func keepsAll(filters []predicate, p *Parameter, v Version) bool {
	for _, f := range filters {
		if !f(p, v) {
			return false
		}
	}
	return true
}

// textFilter builds a filter comparing a text field of a parameter with
// each value: equal, a prefix of it or contained in it. A field absent for
// a parameter (KeyId of a String, an unset tag) matches nothing. Equals and
// BeginsWith are case-sensitive, Contains is not (option.compare).
func textFilter(field func(p *Parameter, v Version) (string, bool)) func(option, []string) (predicate, *awserr.Error) {
	return func(o option, values []string) (predicate, *awserr.Error) {
		return func(p *Parameter, v Version) bool {
			got, ok := field(p, v)
			return ok && slices.ContainsFunc(values, func(want string) bool { return o.compare(got, want) })
		}, nil
	}
}

// compare applies a text option: Equals, BeginsWith or Contains. BeginsWith
// is case-sensitive and Contains ignores case (ssm-parameters.md 7.5;
// SSM-21 steps 07-08). UNVERIFIED (SSM-21): Equals is case-sensitive. The
// Path options never reach it (pathFilter).
func (o option) compare(got, want string) bool {
	switch o {
	case optBeginsWith:
		return strings.HasPrefix(got, want)
	case optContains:
		return strings.Contains(strings.ToLower(got), strings.ToLower(want))
	case optEquals, optOneLevel, optRecursive:
	}
	return got == want
}

func typeField(_ *Parameter, v Version) (string, bool)     { return string(v.Type), true }
func tierField(_ *Parameter, v Version) (string, bool)     { return string(v.Tier), true }
func dataTypeField(_ *Parameter, v Version) (string, bool) { return v.DataType, true }

// keyIDField is a SecureString's KeyId as written, alias/aws/ssm for the
// default key (switch ssm.kms.key-id-echo; the search guide's KeyId
// alias/aws/ssm, ssm-parameters.md 7.5).
func keyIDField(_ *Parameter, v Version) (string, bool) {
	k := keyIDOf(v.Value)
	if k == nil {
		return "", false
	}
	return *k, true
}

// slashed is a name with a leading slash, so that /foo and foo compare as
// the one parameter they name (ssm-parameters.md 3.2).
func slashed(n string) string {
	if strings.HasPrefix(n, "/") {
		return n
	}
	return "/" + n
}

// nameFilter compares names (ssm-parameters.md 7.5): Equals and BeginsWith
// on the names with a leading slash, so Terraform's Name Equals /foo finds
// the root parameter foo ([T] PR #49339); Contains on the canonical name.
func nameFilter(o option, values []string) (predicate, *awserr.Error) {
	wants := make([]string, len(values))
	for i, raw := range values {
		wants[i] = strings.Trim(raw, " ")
		if o != optContains {
			wants[i] = slashed(wants[i])
		}
	}
	return func(p *Parameter, _ Version) bool {
		got := p.Name
		if o != optContains {
			got = slashed(got)
		}
		return slices.ContainsFunc(wants, func(want string) bool { return o.compare(got, want) })
	}, nil
}

// tagFilter matches parameters carrying tag key with one of values, or
// with any value when the filter has none. UNVERIFIED (SSM-21),
// ssm-parameters.md 7.5: a tag filter without Values.
func tagFilter(key string) func(option, []string) (predicate, *awserr.Error) {
	field := textFilter(func(p *Parameter, _ Version) (string, bool) {
		i := slices.IndexFunc(p.Tags, func(t Tag) bool { return t.Key == key })
		if i < 0 {
			return "", false
		}
		return p.Tags[i].Value, true
	})
	return func(o option, values []string) (predicate, *awserr.Error) {
		if len(values) == 0 {
			return func(p *Parameter, _ Version) bool {
				return slices.ContainsFunc(p.Tags, func(t Tag) bool { return t.Key == key })
			}, nil
		}
		return field(o, values)
	}
}

// labelFilter keeps the version that carries one of the labels
// (ssm-parameters.md 6.2, 7.4).
func labelFilter(_ option, labels []string) (predicate, *awserr.Error) {
	return func(p *Parameter, v Version) bool {
		return slices.ContainsFunc(labels, func(l string) bool {
			n, ok := p.Labels[l]
			return ok && n == v.Number
		})
	}, nil
}

// pathPrefix is a hierarchy path as the prefix of the names under it: "/"
// for the root, else the path with one trailing slash.
type pathPrefix string

// textPathRequirements is AWS's answer for a path that breaks the naming
// rules, backslashes included (ssm-parameters.md 7.4; SSM-19 step 02, and
// SSM-23 step 07 for a path under /aws/).
const textPathRequirements = `The parameter doesn't meet the parameter name requirements. The parameter name must begin with a forward slash "/". ` +
	`It can't be prefixed with \"aws\" or \"ssm\" (case-insensitive). It must use only letters, numbers, or the following symbols: . (period), - (hyphen), _ (underscore). ` +
	`Special characters are not allowed. All sub-paths, if specified, must use the forward slash symbol "/". Valid example: /get/parameters2-/by1./path0_.`

// parsePath reads a GetParametersByPath Path or a DescribeParameters Path
// filter value (ssm-parameters.md 7.4): it begins with a slash, one
// trailing slash is accepted (/x/ is /x; API example /Branch312/Dev/), and
// otherwise it follows the naming rules (ssm-parameters.md 3.1); a path that
// does not answers ValidationException with AWS's path text. UNVERIFIED
// (SSM-19), ssm-parameters.md 7.4: that text for a defect other than a
// missing leading slash or a reserved prefix, and for a DescribeParameters
// Path filter.
func parsePath(raw string, a asker) (pathPrefix, *awserr.Error) {
	p := strings.Trim(raw, " ")
	switch {
	case p == "/":
		return "/", nil
	case !strings.HasPrefix(p, "/"):
		return "", ssmapi.ErrValidationException(textPathRequirements)
	}
	if _, err := parseName(strings.TrimSuffix(p, "/"), a); err != nil {
		return "", ssmapi.ErrValidationException(textPathRequirements)
	}
	p = strings.TrimSuffix(p, "/")
	return pathPrefix(p + "/"), nil
}

// holds reports whether the parameter canonical is under the path: its
// direct child, or at any depth when recursive. Path / holds the root
// parameters, and every parameter when recursive. UNVERIFIED (SSM-19),
// ssm-parameters.md 7.4: what Path / lists.
func (pp pathPrefix) holds(canonical string, recursive bool) bool {
	rest, ok := strings.CutPrefix(slashed(canonical), string(pp))
	return ok && rest != "" && (recursive || !strings.Contains(rest, "/"))
}

// pathFilter is DescribeParameters' Path filter: OneLevel (understudy's
// default, UNVERIFIED SSM-21) or Recursive under any of the paths.
func pathFilter(a asker) func(option, []string) (predicate, *awserr.Error) {
	return func(o option, values []string) (predicate, *awserr.Error) {
		paths := make([]pathPrefix, len(values))
		for i, raw := range values {
			pp, err := parsePath(raw, a)
			if err != nil {
				return nil, err
			}
			paths[i] = pp
		}
		return func(p *Parameter, _ Version) bool {
			return slices.ContainsFunc(paths, func(pp pathPrefix) bool { return pp.holds(p.Name, o == optRecursive) })
		}, nil
	}
}

// byPathKeys are GetParametersByPath's filter keys: Type, KeyId and Label,
// Label with Equals only (ssm-parameters.md 7.4).
func byPathKeys() filterKeys {
	return filterKeys{named: "[Type, KeyId, Label]", specs: func(key string) (keySpec, bool) {
		text := []option{optEquals, optBeginsWith}
		switch key {
		case "Type":
			return keySpec{options: text, build: textFilter(typeField)}, true
		case "KeyId":
			return keySpec{options: text, build: textFilter(keyIDField)}, true
		case "Label":
			return keySpec{options: []option{optEquals}, build: labelFilter}, true
		}
		return keySpec{}, false
	}}
}

// describeKeys are DescribeParameters' filter keys: every key but Label;
// Contains for Name, OneLevel and Recursive for Path (ssm-parameters.md
// 7.5). Name defaults to Equals (the search guide; SSM-21 step 10); Path to
// OneLevel (SSM-21 step 01). AWS's InvalidFilterKey text names no tag: key.
func describeKeys(a asker) filterKeys {
	return filterKeys{named: "[Path, Name, Type, KeyId, Tier, DataType]", specs: func(key string) (keySpec, bool) {
		text := []option{optEquals, optBeginsWith}
		switch key {
		case "Name":
			return keySpec{options: []option{optEquals, optBeginsWith, optContains}, build: nameFilter}, true
		case "Path":
			return keySpec{options: []option{optOneLevel, optRecursive}, build: pathFilter(a)}, true
		case "Type":
			return keySpec{options: text, build: textFilter(typeField)}, true
		case "KeyId":
			return keySpec{options: text, build: textFilter(keyIDField)}, true
		case "Tier":
			return keySpec{options: text, build: textFilter(tierField)}, true
		case "DataType":
			return keySpec{options: text, build: textFilter(dataTypeField)}, true
		}
		if tag, ok := strings.CutPrefix(key, "tag:"); ok {
			return keySpec{options: text, valueless: true, build: tagFilter(tag)}, true
		}
		return keySpec{}, false
	}}
}

// listed is the version of p a GetParametersByPath result carries: the
// latest, or with a Label filter the newest retained version the filters
// keep (ssm-parameters.md 7.4: the labeled version, not the latest). ok is
// false when the filters keep no version. UNVERIFIED (SSM-19): which
// version answers when one filter names labels on several versions.
func (p *Parameter) listed(filters []predicate, labeled bool) (Version, bool) {
	if !labeled {
		v := p.latest()
		return v, keepsAll(filters, p, v)
	}
	for i := range slices.Backward(p.Versions) {
		if keepsAll(filters, p, p.Versions[i]) {
			return p.Versions[i], true
		}
	}
	return Version{}, false
}

// getParametersByPath lists the parameters under a path, one level or
// recursively (ssm-parameters.md 7.4), in byte order of canonical names
// (switch ssm.list.order). Items name the parameter canonically and carry
// no Selector, a Label filter's included (UNVERIFIED SSM-19). Values are
// decrypted when asked, after the state is read, and a KMS failure fails
// the call.
func (s *Service) getParametersByPath(ctx context.Context, c *kernel.Call, in *ssmapi.GetParametersByPathInput) (*ssmapi.GetParametersByPathOutput, *awserr.Error) {
	const op = "GetParametersByPath"
	if err := checkReserved(op, in.Path); err != nil {
		return nil, err
	}
	scope := c.Scope()
	path, err := parsePath(in.Path, askerOf(c))
	if err != nil {
		return nil, err
	}
	filters, err := parseFilters(op, in.ParameterFilters, byPathKeys())
	if err != nil {
		return nil, err
	}
	labeled := slices.ContainsFunc(in.ParameterFilters, func(f ssmapi.ParameterStringFilter) bool { return f.Key == "Label" })
	recursive := in.Recursive != nil && *in.Recursive
	hits := store.View(s.cells, scope, func(st *State) []found {
		var out []found
		for _, p := range st.Parameters {
			if !path.holds(p.Name, recursive) {
				continue
			}
			if v, ok := p.listed(filters, labeled); ok {
				out = append(out, found{name: name{canonical: p.Name, spelling: p.Name}, sel: noSelector{}, v: v})
			}
		}
		return out
	})
	page, err := pageOf(listingByPath, hits, func(f found) string { return f.name.canonical }, in.MaxResults, byPathPageSize, in.NextToken)
	if err != nil {
		return nil, err
	}
	decrypt := in.WithDecryption != nil && *in.WithDecryption
	out := &ssmapi.GetParametersByPathOutput{Parameters: make([]ssmapi.Parameter, 0, len(page.Items)), NextToken: page.Next}
	for i := range page.Items {
		p, err := s.render(ctx, c, scope, page.Items[i], decrypt)
		if err != nil {
			return nil, err
		}
		out.Parameters = append(out.Parameters, p)
	}
	return out, nil
}

// describeParameters lists parameter metadata, the latest version of each
// parameter the filters keep (ssm-parameters.md 7.5), in byte order of
// canonical names (switch ssm.list.order). Shared lists nothing, since no
// parameter can be shared. Filters with ParameterFilters is refused
// (SSM-21 step 04); the deprecated Filters alone answers 501, AWS's prefix
// match of its Name (SSM-21 step 03) not being served.
func (s *Service) describeParameters(_ context.Context, c *kernel.Call, in *ssmapi.DescribeParametersInput) (*ssmapi.DescribeParametersOutput, *awserr.Error) {
	const op = "DescribeParameters"
	for _, f := range in.ParameterFilters {
		if f.Key != "Name" && f.Key != "Path" {
			continue
		}
		if err := checkReserved(op, f.Values...); err != nil {
			return nil, err
		}
	}
	switch {
	case len(in.Filters) > 0 && len(in.ParameterFilters) > 0:
		return nil, ssmapi.ErrValidationException("You can use either Filters or ParameterFilters in a single request.")
	case len(in.Filters) > 0:
		return nil, errUnsupported(op, "the deprecated Filters member (use ParameterFilters)")
	}
	scope := c.Scope()
	filters, err := parseFilters(op, in.ParameterFilters, describeKeys(askerOf(c)))
	if err != nil {
		return nil, err
	}
	if in.Shared != nil && *in.Shared {
		return &ssmapi.DescribeParametersOutput{Parameters: []ssmapi.ParameterMetadata{}}, nil
	}
	hits := store.View(s.cells, scope, func(st *State) []Parameter {
		var out []Parameter
		for _, p := range st.Parameters {
			if keepsAll(filters, &p, p.latest()) {
				out = append(out, p)
			}
		}
		return out
	})
	page, err := pageOf(listingDescribe, hits, func(p Parameter) string { return p.Name }, in.MaxResults, describePageSize, in.NextToken)
	if err != nil {
		return nil, err
	}
	out := &ssmapi.DescribeParametersOutput{Parameters: make([]ssmapi.ParameterMetadata, 0, len(page.Items)), NextToken: page.Next}
	for _, p := range page.Items {
		out.Parameters = append(out.Parameters, metadataOf(scope, &p))
	}
	return out, nil
}

// metadataOf is p's latest version as DescribeParameters reports it
// (ssm-parameters.md 7.5, 12.5): KeyId only for a SecureString (switch
// ssm.kms.key-id-echo), Description and AllowedPattern absent when unset,
// Policies [] (7.10).
func metadataOf(scope aws.Scope, p *Parameter) ssmapi.ParameterMetadata {
	v := p.latest()
	return ssmapi.ParameterMetadata{
		Name: new(p.Name), ARN: new(parameterARN(scope, p.Name).String()), Type: new(ssmapi.ParameterType(v.Type)), KeyId: keyIDOf(v.Value),
		LastModifiedDate: new(v.LastModified.Wire()), LastModifiedUser: new(v.LastModifiedUser),
		Description: v.Description, AllowedPattern: v.AllowedPattern, Version: v.Number,
		Tier: new(ssmapi.ParameterTier(v.Tier)), Policies: []ssmapi.ParameterInlinePolicy{}, DataType: new(v.DataType),
	}
}
