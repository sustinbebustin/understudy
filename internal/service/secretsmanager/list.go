package secretsmanager

import (
	"context"
	"encoding/base64"
	"maps"
	"slices"
	"sort"
	"strings"

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

// page returns the page of items, sorted by key in o, that starts at token
// and holds at most size items (defaultPageSize when nil). The token is
// opaque to clients: the base64 of the next item's key, so a listing that
// changes between pages neither repeats nor skips an item still present.
func page[T any](items []T, key func(T) string, o order, size *int32, token *string) (listPage[T], *awserr.Error) {
	start := 0
	if token != nil {
		raw, err := base64.RawURLEncoding.DecodeString(*token)
		if err != nil {
			return listPage[T]{}, errInvalidNextToken()
		}
		from := string(raw)
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
		p.next = new(base64.RawURLEncoding.EncodeToString([]byte(key(items[end]))))
	}
	return p, nil
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
	secrets := readState(s, c.Scope(), func(st *State) []Secret {
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
	p, err := page(secrets, key, o, in.MaxResults, in.NextToken)
	if err != nil {
		return nil, err
	}
	out := &secretsmanagerapi.ListSecretsOutput{NextToken: p.next, SecretList: make([]secretsmanagerapi.SecretListEntry, 0, len(p.items))}
	for i := range p.items {
		sec := &p.items[i]
		out.SecretList = append(out.SecretList, secretsmanagerapi.SecretListEntry{
			ARN:                    new(sec.ARN),
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

// matchesValue applies one filter value: a prefix match, case-insensitive
// for description and all, case-sensitive otherwise (the Filter
// reference). No secret has a primary Region or an owning service, since
// understudy has neither replication nor service-owned secrets.
func matchesValue(sec Secret, key secretsmanagerapi.FilterNameStringType, v string) bool {
	switch key {
	case secretsmanagerapi.FilterNameStringTypeName:
		return strings.HasPrefix(sec.Name, v)
	case secretsmanagerapi.FilterNameStringTypeDescription:
		return hasPrefixFold(deref(sec.Description), v)
	case secretsmanagerapi.FilterNameStringTypeTagKey:
		return slices.ContainsFunc(sec.Tags, func(t Tag) bool { return strings.HasPrefix(t.Key, v) })
	case secretsmanagerapi.FilterNameStringTypeTagValue:
		return slices.ContainsFunc(sec.Tags, func(t Tag) bool { return strings.HasPrefix(t.Value, v) })
	case secretsmanagerapi.FilterNameStringTypeAll:
		return matchesAllWords(sec, v)
	case secretsmanagerapi.FilterNameStringTypePrimaryRegion, secretsmanagerapi.FilterNameStringTypeOwningService:
	}
	return false
}

// matchesAllWords is the all filter: every word of v prefixes, ignoring
// case, the name, the description or a tag key or value.
func matchesAllWords(sec Secret, v string) bool {
	attrs := make([]string, 0, 2+2*len(sec.Tags))
	attrs = append(attrs, sec.Name, deref(sec.Description))
	for _, t := range sec.Tags {
		attrs = append(attrs, t.Key, t.Value)
	}
	for word := range strings.FieldsSeq(v) {
		if !slices.ContainsFunc(attrs, func(a string) bool { return hasPrefixFold(a, word) }) {
			return false
		}
	}
	return true
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
