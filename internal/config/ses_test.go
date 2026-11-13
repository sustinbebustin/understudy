package config_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/config"
)

// ses.verifiedIdentities classifies each entry: an exact address kept as
// written (addresses are case-sensitive, ses.md 5.1), a domain lower-cased
// (domains are case-insensitive, ses.md 5.1), and "*@domain" as an address
// domain. ses.productionAccess defaults to false.
func TestSESVerifiedIdentitiesAreClassified(t *testing.T) {
	cfg := load(t, nil, nil, files{config.DefaultConfigFile: `{"ses": {"verifiedIdentities": ["Sender@example.com", "Example.ORG", "*@Example.net"]}}`})
	v := cfg.SES.VerifiedIdentities
	if !slices.Equal(v.Addresses, []string{"Sender@example.com"}) || !slices.Equal(v.Domains, []string{"example.org"}) ||
		!slices.Equal(v.AddressDomains, []string{"example.net"}) {
		t.Errorf("verifiedIdentities = %+v", v)
	}
	if got := cfg.SES.ProductionAccess; got.All || len(got.Pairs) != 0 {
		t.Errorf("ses.productionAccess defaults to %+v, want the sandbox everywhere", got)
	}
	if cfg := load(t, nil, nil, files{config.DefaultConfigFile: `{"ses": {"productionAccess": true}}`}); !cfg.SES.ProductionAccess.All {
		t.Error("ses.productionAccess true did not load")
	}
	if cfg := load(t, nil, nil, files{config.DefaultConfigFile: `{"ses": {"productionAccess": false}}`}); cfg.SES.ProductionAccess.All {
		t.Error("ses.productionAccess false loaded as true")
	}
}

// A list of {account, region} entries lifts the sandbox for those pairs
// only; a region may be a configured extra region.
func TestSESProductionAccessList(t *testing.T) {
	doc := `{"regions": ["local-1"], "ses": {"productionAccess": [
		{"account": "111111111111", "region": "us-east-1"},
		{"account": "222222222222", "region": "local-1"}]}}`
	got := load(t, nil, nil, files{config.DefaultConfigFile: doc}).SES.ProductionAccess
	if got.All {
		t.Error("a list took every account and Region out of the sandbox")
	}
	pairs := make([]string, 0, len(got.Pairs))
	for _, p := range got.Pairs {
		pairs = append(pairs, p.Account.String()+"/"+p.Region.String())
	}
	if want := []string{"111111111111/us-east-1", "222222222222/local-1"}; !slices.Equal(pairs, want) {
		t.Errorf("pairs = %q, want %q", pairs, want)
	}
}

// A value that is neither a bool nor a list, and an entry with a missing or
// malformed account or Region or an unknown member, is refused at ses.productionAccess
// naming the entry.
func TestSESProductionAccessListRefusals(t *testing.T) {
	at := config.DefaultConfigFile + ": ses.productionAccess"
	for name, tc := range map[string]struct{ value, entry string }{
		"object":         {`{"account": "111111111111", "region": "us-east-1"}`, ""},
		"number":         {`1`, ""},
		"non-object":     {`["yes"]`, "[0]"},
		"missing acct":   {`[{"region": "us-east-1"}]`, "[0]"},
		"missing region": {`[{"account": "111111111111"}]`, "[0]"},
		"short acct":     {`[{"account": "1234", "region": "us-east-1"}]`, "[0]"},
		"unknown region": {`[{"account": "111111111111", "region": "mars-1"}]`, "[0]"},
		"unknown member": {`[{"account": "111111111111", "region": "us-east-1", "x": 1}]`, "[0]"},
		"second entry":   {`[{"account": "111111111111", "region": "us-east-1"}, {"account": "2", "region": "us-east-1"}]`, "[1]"},
	} {
		err := loadErr(t, nil, nil, files{config.DefaultConfigFile: `{"ses": {"productionAccess": ` + tc.value + `}}`})
		if err.Where != at+tc.entry {
			t.Errorf("%s: error at %q, want %q (%+v)", name, err.Where, at+tc.entry, err)
		}
	}
}

// Any other entry is refused at load, naming it.
func TestSESVerifiedIdentitiesRefusals(t *testing.T) {
	at := config.DefaultConfigFile + ": ses.verifiedIdentities"
	for _, entry := range []string{"", "a@", "*", "sender*@example.com", "Name <a@example.com>", "a b@example.com", "*@"} {
		doc := `{"ses": {"verifiedIdentities": ["` + entry + `"]}}`
		err := loadErr(t, nil, nil, files{config.DefaultConfigFile: doc})
		if !strings.HasPrefix(err.Where, at) || !strings.Contains(err.Reason+err.Hint, `"`+entry+`"`) && !strings.Contains(err.Reason, entry) {
			t.Errorf("entry %q: error %+v, want it refused at %s naming the entry", entry, err, at)
		}
	}
	err := loadErr(t, nil, nil, files{config.DefaultConfigFile: `{"ses": {"productionAccess": "yes"}}`})
	if err.Where != config.DefaultConfigFile+": ses.productionAccess" {
		t.Errorf("productionAccess \"yes\": error at %q, want at ses.productionAccess", err.Where)
	}
}
