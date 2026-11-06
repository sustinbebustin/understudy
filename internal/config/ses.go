package config

import (
	"net/mail"
	"strconv"
	"strings"
)

// verifiedIdentityHint is the grammar a refused ses.verifiedIdentities
// entry is told.
const verifiedIdentityHint = `entries are an exact address such as sender@example.com, a domain such as example.com, or "*@example.com" for every address of one domain`

// applySES reads the ses section: ses.verifiedIdentities classified entry
// by entry, and ses.productionAccess.
func (d *draft) applySES(s *fileSES, where string) *Error {
	if s == nil {
		return nil
	}
	for i, entry := range s.VerifiedIdentities {
		if err := d.cfg.SES.VerifiedIdentities.add(entry); err != "" {
			return &Error{Where: where + ".verifiedIdentities[" + strconv.Itoa(i) + "]", Reason: quote(entry) + " " + err, Hint: verifiedIdentityHint}
		}
	}
	if s.ProductionAccess != nil {
		d.cfg.SES.ProductionAccess = *s.ProductionAccess
	}
	return nil
}

// add classifies entry into its class (contract 6): "*@domain" is an
// address domain and an entry without "@" a domain, both lower-cased since
// domains are case-insensitive (ses.md 5.1); any other entry is an exact
// address kept as written, since addresses are case-sensitive. The reason
// a refused entry gives continues a sentence that starts with the entry;
// it is empty on success.
func (v *SESVerifiedIdentities) add(entry string) string {
	// An address's domain follows its last "@"; a quoted local part may
	// hold another.
	at := strings.LastIndex(entry, "@")
	isAddress := at >= 0
	var local, domain string
	if isAddress {
		local, domain = entry[:at], entry[at+1:]
	}
	switch {
	case !isAddress:
		if reason := domainReason(entry); reason != "" {
			return reason
		}
		v.Domains = append(v.Domains, strings.ToLower(entry))
	case local == "*":
		if reason := domainReason(domain); reason != "" {
			return "has a domain that " + reason
		}
		v.AddressDomains = append(v.AddressDomains, strings.ToLower(domain))
	case strings.Contains(entry, "*"):
		return `uses "*" other than as the whole local part of "*@domain"`
	default:
		addr, err := mail.ParseAddress(entry)
		if err != nil || addr.Name != "" || addr.Address != entry {
			return "is not a bare email address"
		}
		if reason := domainReason(domain); reason != "" {
			return "has a domain that " + reason
		}
		v.Addresses = append(v.Addresses, entry)
	}
	return ""
}

// domainReason says why name is not a domain name: DNS labels of letters,
// digits and inner hyphens, each 1 to 63 characters, 255 in all (ses.md
// 5.1); empty when it is one.
func domainReason(name string) string {
	if name == "" {
		return "is empty"
	}
	if len(name) > 255 {
		return "is longer than 255 characters"
	}
	for label := range strings.SplitSeq(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "is not a domain name: each dot-separated label is 1 to 63 letters, digits and inner hyphens"
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return "is not a domain name: each dot-separated label is 1 to 63 letters, digits and inner hyphens"
			}
		}
	}
	return ""
}
