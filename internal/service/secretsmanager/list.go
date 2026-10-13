package secretsmanager

import (
	"context"
	"encoding/base64"
	"maps"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// defaultPageSize is the page size of the listings when MaxResults is
// absent: the largest MaxResults allows.
const defaultPageSize = 100

// order is the direction a listing is sorted in.
type order uint8

const (
	ascending order = iota + 1
	descending
)

// listPage is one page of a listing and the token of the next.
type listPage[T any] struct {
	items []T
	next  *string
}

// tokenVersion starts every NextToken, so a token of another format is
// refused rather than misread.
const tokenVersion = "v1"

// page returns the page of items, sorted by key in o, that starts at token
// and holds at most size items (defaultPageSize when nil). listing names
// the listing and its order, and never contains "|". The token is opaque
// to clients: the base64 of the format version, listing and the next
// item's key, so a listing that changes between pages neither repeats nor
// skips an item still present, and a token of another listing or order is
// InvalidNextTokenException rather than a silent seek.
func page[T any](items []T, key func(T) string, o order, listing string, size *int32, token *string) (listPage[T], *awserr.Error) {
	prefix := tokenVersion + "|" + listing + "|"
	start := 0
	if token != nil {
		raw, err := base64.RawURLEncoding.DecodeString(*token)
		if err != nil {
			return listPage[T]{}, errInvalidNextToken()
		}
		from, ok := strings.CutPrefix(string(raw), prefix)
		if !ok {
			return listPage[T]{}, errInvalidNextToken()
		}
		start = sort.Search(len(items), func(i int) bool {
			if o == descending {
				return key(items[i]) <= from
			}
			return key(items[i]) >= from
		})
	}
	n := defaultPageSize
	if size != nil {
		n = int(*size)
	}
	end := min(start+n, len(items))
	p := listPage[T]{items: items[start:end]}
	if end < len(items) {
		p.next = new(base64.RawURLEncoding.EncodeToString([]byte(prefix + key(items[end]))))
	}
	return p, nil
}

func orderName(o order) string {
	if o == descending {
		return "desc"
	}
	return "asc"
}

// instantKey renders i at a fixed width in UTC, so keys compare as the
// instants do for every year the emulator clock can reach.
func instantKey(i clock.Instant) string {
	return i.Wire().UTC().Format("2006-01-02T15:04:05.000000000")
}

// sortKey is the listing key of a secret for SortBy: the sorted attribute,
// then the name, so ties have one order.
func sortKey(by secretsmanagerapi.SortByType) func(Secret) string {
	switch by {
	case secretsmanagerapi.SortByTypeName:
		return func(s Secret) string { return s.Name }
	case secretsmanagerapi.SortByTypeLastChangedDate:
		return func(s Secret) string { return instantKey(s.Changed) + "\x00" + s.Name }
	case secretsmanagerapi.SortByTypeLastAccessedDate:
		return func(s Secret) string {
			var at clock.Instant
			if s.Accessed != nil {
				at = *s.Accessed
			}
			return instantKey(at) + "\x00" + s.Name
		}
	case secretsmanagerapi.SortByTypeCreatedDate:
	}
	return func(s Secret) string { return instantKey(s.Created) + "\x00" + s.Name }
}

// listSecrets lists the caller's secrets that pass every filter, sorted by
// creation date unless SortBy says otherwise; secrets scheduled for
// deletion only with IncludePlannedDeletion.
func (s *Service) listSecrets(_ context.Context, c *kernel.Call, in *secretsmanagerapi.ListSecretsInput) (*secretsmanagerapi.ListSecretsOutput, *awserr.Error) {
	planned := in.IncludePlannedDeletion != nil && *in.IncludePlannedDeletion
	by := secretsmanagerapi.SortByTypeCreatedDate
	if in.SortBy != nil {
		by = *in.SortBy
	}
	o := ascending
	if in.SortOrder != nil && *in.SortOrder == secretsmanagerapi.SortOrderTypeDesc {
		o = descending
	}
	key := sortKey(by)
	scope := c.Scope()
	secrets := readState(s, scope, func(st *State) []Secret {
		var out []Secret
		for _, name := range slices.Sorted(maps.Keys(st.Secrets)) {
			sec := st.Secrets[name]
			if _, gone := sec.Lifecycle.(scheduled); gone && !planned {
				continue
			}
			if matchesAll(sec, in.Filters) {
				out = append(out, sec)
			}
		}
		return out
	})
	slices.SortFunc(secrets, func(a, b Secret) int {
		if o == descending {
			return strings.Compare(key(b), key(a))
		}
		return strings.Compare(key(a), key(b))
	})
	p, err := page(secrets, key, o, "secrets/"+string(by)+"/"+orderName(o), in.MaxResults, in.NextToken)
	if err != nil {
		return nil, err
	}
	out := &secretsmanagerapi.ListSecretsOutput{NextToken: p.next, SecretList: make([]secretsmanagerapi.SecretListEntry, 0, len(p.items))}
	for i := range p.items {
		sec := &p.items[i]
		out.SecretList = append(out.SecretList, secretsmanagerapi.SecretListEntry{
			ARN:                    new(sec.arn(scope).String()),
			Name:                   new(sec.Name),
			Description:            sec.Description,
			KmsKeyId:               sec.KMSKeyID,
			Type:                   sec.Type,
			CreatedDate:            new(sec.Created.Wire()),
			LastChangedDate:        new(sec.Changed.Wire()),
			LastAccessedDate:       wireOf(sec.Accessed),
			DeletedDate:            deletedDate(sec),
			Tags:                   renderTags(sec.Tags),
			SecretVersionsToStages: sec.versionsToStages(),
		})
	}
	return out, nil
}

// matchesAll reports a secret every filter selects. Filters combine with
// AND and a filter's values with OR; AWS documents neither, so this is the
// fidelity switch secretsmanager.list-secrets.filter-combination.
func matchesAll(sec Secret, filters []secretsmanagerapi.Filter) bool {
	for _, f := range filters {
		if f.Key != nil && len(f.Values) > 0 && !matchesFilter(sec, *f.Key, f.Values) {
			return false
		}
	}
	return true
}

// matchesFilter reports a secret one of values selects under key. A value
// that starts with "!" selects the secrets the rest does not.
func matchesFilter(sec Secret, key secretsmanagerapi.FilterNameStringType, values []string) bool {
	for _, v := range values {
		rest, negated := strings.CutPrefix(v, "!")
		if matchesValue(sec, key, rest) != negated {
			return true
		}
	}
	return false
}

// matchesValue applies one filter value: a prefix of the whole name, tag
// key or tag value, case-sensitive; words of the description or of every
// attribute, case-insensitive (the Filter reference and the user guide's
// search page). No secret has a primary Region or an owning service, since
// understudy has neither replication nor service-owned secrets.
func matchesValue(sec Secret, key secretsmanagerapi.FilterNameStringType, v string) bool {
	switch key {
	case secretsmanagerapi.FilterNameStringTypeName:
		return strings.HasPrefix(sec.Name, v)
	case secretsmanagerapi.FilterNameStringTypeDescription:
		return matchesDescription(deref(sec.Description), v)
	case secretsmanagerapi.FilterNameStringTypeTagKey:
		return slices.ContainsFunc(sec.Tags, func(t Tag) bool { return strings.HasPrefix(t.Key, v) })
	case secretsmanagerapi.FilterNameStringTypeTagValue:
		return slices.ContainsFunc(sec.Tags, func(t Tag) bool { return strings.HasPrefix(t.Value, v) })
	case secretsmanagerapi.FilterNameStringTypeAll:
		return matchesAnyWord(sec, v)
	case secretsmanagerapi.FilterNameStringTypePrimaryRegion, secretsmanagerapi.FilterNameStringTypeOwningService:
	}
	return false
}

// matchesDescription is the description filter: every space-separated word
// of v prefixes, ignoring case, some word of the description, in any order.
// So "a secret" matches "an secret" but not "asecret" (AWS-recorded,
// LocalStack test_list_secrets_filtering), and "My Description" matches
// "Description of my secret" (the user guide's search page).
func matchesDescription(desc, v string) bool {
	descWords := strings.Fields(desc)
	for word := range strings.FieldsSeq(v) {
		if !slices.ContainsFunc(descWords, func(d string) bool { return hasPrefixFold(d, word) }) {
			return false
		}
	}
	return true
}

// matchesAnyWord is the all filter: some word of v prefixes, ignoring case,
// a word of the name, the description or a tag key or value. The user
// guide's example is OR ("My_Secret matches secrets with the word my or
// secret"); the prefix match within a word is the fidelity switch
// secretsmanager.list-secrets.all-filter-match.
func matchesAnyWord(sec Secret, v string) bool {
	attrs := append(words(sec.Name), words(deref(sec.Description))...)
	for _, t := range sec.Tags {
		attrs = append(attrs, words(t.Key)...)
		attrs = append(attrs, words(t.Value)...)
	}
	return slices.ContainsFunc(words(v), func(w string) bool {
		return slices.ContainsFunc(attrs, func(a string) bool { return hasPrefixFold(a, w) })
	})
}

// words splits s into the words the all filter compares: runs of letters
// and digits, with every other character a separator, broken where a
// letter meets a digit, where a lower-case letter is followed by an
// upper-case one, and before the last upper-case letter of a run that a
// lower-case letter follows. So credsDatabase#892 is creds, Database and
// 892, as the user guide's search page has it, and DBPassword is DB and
// Password.
func words(s string) []string {
	rs := []rune(s)
	var out []string
	start := -1
	for i, r := range rs {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			if start >= 0 {
				out = append(out, string(rs[start:i]))
				start = -1
			}
		case start < 0:
			start = i
		case wordBreak(rs, i):
			out = append(out, string(rs[start:i]))
			start = i
		}
	}
	if start >= 0 {
		out = append(out, string(rs[start:]))
	}
	return out
}

// wordBreak reports a word boundary before rs[i], where rs[i-1] and rs[i]
// are both letters or digits.
func wordBreak(rs []rune, i int) bool {
	prev, cur := rs[i-1], rs[i]
	switch {
	case unicode.IsDigit(prev) != unicode.IsDigit(cur):
		return true
	case unicode.IsLower(prev) && unicode.IsUpper(cur):
		return true
	case unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(rs) && unicode.IsLower(rs[i+1]):
		return true
	}
	return false
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
