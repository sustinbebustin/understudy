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
// domain (contract 6). ses.productionAccess defaults to false.
func TestSESVerifiedIdentitiesAreClassified(t *testing.T) {
	cfg := load(t, nil, nil, files{config.DefaultConfigFile: `{"ses": {"verifiedIdentities": ["Sender@example.com", "Example.ORG", "*@Example.net"]}}`})
	v := cfg.SES.VerifiedIdentities
	if !slices.Equal(v.Addresses, []string{"Sender@example.com"}) || !slices.Equal(v.Domains, []string{"example.org"}) ||
		!slices.Equal(v.AddressDomains, []string{"example.net"}) {
		t.Errorf("verifiedIdentities = %+v", v)
	}
	if cfg.SES.ProductionAccess {
		t.Error("ses.productionAccess defaults to true, want false")
	}
	if cfg := load(t, nil, nil, files{config.DefaultConfigFile: `{"ses": {"productionAccess": true}}`}); !cfg.SES.ProductionAccess {
		t.Error("ses.productionAccess true did not load")
	}
}

// Any other entry is refused at load, naming it (contract 6).
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
