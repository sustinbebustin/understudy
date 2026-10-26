package egress_test

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"unicode"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/egress"
)

// setting is SNS HTTP delivery's allow-list, the one ADR 0015 names.
var setting = egress.Setting{Service: "sns", Feature: "httpDelivery"}

// The allow-list key has one shape, <service>.<feature>.allow (ADR 0015:
// sns.httpDelivery.allow).
func TestSettingNamesTheKey(t *testing.T) {
	if got := setting.String(); got != "sns.httpDelivery.allow" {
		t.Errorf("String() = %q, want sns.httpDelivery.allow", got)
	}
}

// The allow-list is empty by default, so nothing is reached (ADR 0015,
// sns-http-delivery.md 7.1).
func TestTheZeroAllowListAllowsNothing(t *testing.T) {
	var a egress.AllowList
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "example.test"} {
		if e, ok := a.Match(host, 80); ok {
			t.Errorf("zero AllowList Match(%s, 80) = %v", host, e)
		}
	}
	if got := a.Entries(); len(got) != 0 {
		t.Errorf("zero AllowList Entries() = %v", got)
	}
}

func parse(t *testing.T, entries ...string) egress.AllowList {
	t.Helper()
	a, err := egress.ParseAllowList(setting, entries)
	if err != nil {
		t.Fatalf("ParseAllowList(%q): %v", entries, err)
	}
	return a
}

// Entries are host:port with host localhost, an IPv4 literal, a bracketed
// IPv6 literal or a DNS name, and port 1-65535 or * (sns-http-delivery.md
// 7.2). Hosts are compared lower-cased, without a trailing dot or brackets,
// IPv4-mapped IPv6 unmapped (7.2), so entries are kept in that form.
func TestParseAllowListNormalizesEntries(t *testing.T) {
	cases := []struct {
		entry string
		want  egress.Entry
		text  string
	}{
		{"localhost:9000", egress.Entry{Host: "localhost", Port: 9000}, "localhost:9000"},
		{"LocalHost:9000", egress.Entry{Host: "localhost", Port: 9000}, "localhost:9000"},
		{"127.0.0.1:8080", egress.Entry{Host: "127.0.0.1", Port: 8080}, "127.0.0.1:8080"},
		{"127.0.0.1:*", egress.Entry{Host: "127.0.0.1", Port: egress.AnyPort}, "127.0.0.1:*"},
		{"[::1]:*", egress.Entry{Host: "::1", Port: egress.AnyPort}, "[::1]:*"},
		{"[0:0:0:0:0:0:0:1]:8080", egress.Entry{Host: "::1", Port: 8080}, "[::1]:8080"},
		{"[::ffff:127.0.0.1]:80", egress.Entry{Host: "127.0.0.1", Port: 80}, "127.0.0.1:80"},
		{"Hooks.Example.TEST.:443", egress.Entry{Host: "hooks.example.test", Port: 443}, "hooks.example.test:443"},
		{"host-1.example.test:1", egress.Entry{Host: "host-1.example.test", Port: 1}, "host-1.example.test:1"},
		{"10.0.0.5:65535", egress.Entry{Host: "10.0.0.5", Port: 65535}, "10.0.0.5:65535"},
		// Listing a private or loopback literal is how it is allowed (7.3,
		// 7.4).
		{"192.168.1.2:80", egress.Entry{Host: "192.168.1.2", Port: 80}, "192.168.1.2:80"},
		{"[fd12:3456::1]:80", egress.Entry{Host: "fd12:3456::1", Port: 80}, "[fd12:3456::1]:80"},
	}
	for _, c := range cases {
		a := parse(t, c.entry)
		if got := a.Entries(); !slices.Equal(got, []egress.Entry{c.want}) {
			t.Errorf("ParseAllowList(%q).Entries() = %v, want %v", c.entry, got, c.want)
			continue
		}
		if got := a.Entries()[0].String(); got != c.text {
			t.Errorf("Entry(%q).String() = %q, want %q", c.entry, got, c.text)
		}
		if a.Setting() != setting {
			t.Errorf("Setting() = %v, want %v", a.Setting(), setting)
		}
	}
}

func TestParseAllowListKeepsOrderAndDuplicates(t *testing.T) {
	a := parse(t, "127.0.0.1:8080", "localhost:*", "127.0.0.1:8080")
	want := []egress.Entry{{Host: "127.0.0.1", Port: 8080}, {Host: "localhost", Port: egress.AnyPort}, {Host: "127.0.0.1", Port: 8080}}
	if got := a.Entries(); !slices.Equal(got, want) {
		t.Errorf("Entries() = %v, want %v", got, want)
	}
}

// refusedEntries are entries the grammar does not produce (7.2: no host
// wildcards, no CIDR ranges), and hosts no entry may name: AWS endpoints
// and the always-refused addresses (7.3). A refused entry stops the load
// with the entry and the grammar in the error, never silently ignored (7.2).
var refusedEntries = []string{
	"",
	"localhost",
	"localhost:",
	":80",
	"localhost:0",
	"localhost:65536",
	"localhost:-1",
	"localhost:http",
	"localhost:80:80",
	"*.example.test:80",
	"*:80",
	"10.0.0.0/8:80",
	"::1:80",
	"[::1]",
	"[127.0.0.1]:80",
	"http://localhost:80",
	"localhost:80/path",
	"user@localhost:80",
	"under_score.example.test:80",
	"local host:80",
	"sqs.us-east-1.amazonaws.com:443",
	"amazonaws.com:443",
	"abc123.lambda-url.us-east-1.on.aws:443",
	"abc123.execute-api.com:443",
	"sqs.us-east-1.api.aws:443",
	"169.254.169.254:80",
	"[fd00:ec2::254]:80",
	"169.254.170.2:80",
	"169.254.10.10:80",
	"[fe80::1]:80",
	"0.0.0.0:80",
	"[::]:80",
	"224.0.0.1:80",
	"[ff02::1]:80",
	"[::ffff:169.254.169.254]:80",
}

func TestParseAllowListRefusesEntries(t *testing.T) {
	for _, entry := range refusedEntries {
		entries := []string{"127.0.0.1:8080", entry}
		a, err := egress.ParseAllowList(setting, entries)
		if err == nil {
			t.Errorf("ParseAllowList(%q) accepted %q", entries, entry)
			continue
		}
		if err.Setting != setting || err.Index != 1 || err.Entry != entry {
			t.Errorf("ParseAllowList(%q) = %+v, want the setting, index 1 and the entry as given", entries, err)
		}
		msg := err.Error()
		for _, want := range []string{setting.String(), "host:port"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q does not name %q", msg, want)
			}
		}
		if got := a.Entries(); len(got) != 0 {
			t.Errorf("ParseAllowList(%q) kept %v; a refused list parses nothing", entries, got)
		}
	}
}

// Matching compares the normalized host and the port, or any port for *;
// there are no host wildcards (7.2). The scheme is not an input: it does
// not matter.
func TestMatch(t *testing.T) {
	a := parse(t, "localhost:9000", "127.0.0.1:8080", "127.0.0.1:*", "[::1]:8080", "example.test:80")
	cases := []struct {
		host string
		port uint16
		want egress.Entry
		ok   bool
	}{
		{"localhost", 9000, egress.Entry{Host: "localhost", Port: 9000}, true},
		{"LOCALHOST.", 9000, egress.Entry{Host: "localhost", Port: 9000}, true},
		{"localhost", 9001, egress.Entry{}, false},
		// The first entry that allows the request is reported.
		{"127.0.0.1", 8080, egress.Entry{Host: "127.0.0.1", Port: 8080}, true},
		{"127.0.0.1", 1, egress.Entry{Host: "127.0.0.1", Port: egress.AnyPort}, true},
		{"::ffff:127.0.0.1", 5, egress.Entry{Host: "127.0.0.1", Port: egress.AnyPort}, true},
		{"::1", 8080, egress.Entry{Host: "::1", Port: 8080}, true},
		{"[::1]", 8080, egress.Entry{Host: "::1", Port: 8080}, true},
		{"0:0:0:0:0:0:0:1", 8080, egress.Entry{Host: "::1", Port: 8080}, true},
		{"::1", 8081, egress.Entry{}, false},
		{"Example.Test.", 80, egress.Entry{Host: "example.test", Port: 80}, true},
		{"hooks.example.test", 80, egress.Entry{}, false},
		{"127.0.0.2", 8080, egress.Entry{}, false},
	}
	for _, c := range cases {
		got, ok := a.Match(c.host, c.port)
		if ok != c.ok || got != c.want {
			t.Errorf("Match(%q, %d) = %v, %t; want %v, %t", c.host, c.port, got, ok, c.want, c.ok)
		}
	}
}

// IsAWSName is true for a name equal to or under any partition's DNS or
// dual-stack suffix (sns-http-delivery.md 7.3, from aws-sdk-go-v2's
// partitions.json), the whole aws top-level domain, which holds on.aws,
// and execute-api.com.
func TestIsAWSName(t *testing.T) {
	aws := []string{
		"amazonaws.com", "sqs.us-east-1.amazonaws.com", "SQS.US-EAST-1.AMAZONAWS.COM.",
		"api.aws", "sqs.us-east-1.api.aws",
		"amazonaws.com.cn", "s3.cn-north-1.amazonaws.com.cn",
		"api.amazonwebservices.com.cn", "sqs.cn-north-1.api.amazonwebservices.com.cn",
		"amazonaws.eu", "sqs.eusc-de-east-1.amazonaws.eu", "api.amazonwebservices.eu",
		"c2s.ic.gov", "sqs.us-iso-east-1.c2s.ic.gov", "api.aws.ic.gov",
		"sc2s.sgov.gov", "api.aws.scloud",
		"cloud.adc-e.uk", "sqs.eu-isoe-west-1.cloud.adc-e.uk", "api.cloud-aws.adc-e.uk",
		"csp.hci.ic.gov", "api.aws.hci.ic.gov",
		"aws", "on.aws", "abc123.lambda-url.us-east-1.on.aws",
		"execute-api.com", "petstore-demo-endpoint.execute-api.com",
	}
	for _, name := range aws {
		if !egress.IsAWSName(name) {
			t.Errorf("IsAWSName(%q) = false, want true", name)
		}
	}
	other := []string{
		"", "localhost", "example.com", "notamazonaws.com", "amazonaws.com.evil.example", "amazonaws.co",
		"aws.example", "myaws", "api.aws.example", "execute-api.com.example", "my-execute-api.com",
		"amazonwebservices.com", "api.amazonwebservices.com", "com.cn", "eu", "ic.gov", "hci.ic.gov",
		"sgov.gov", "scloud", "adc-e.uk", "uk", "127.0.0.1", "::1",
	}
	for _, name := range other {
		if egress.IsAWSName(name) {
			t.Errorf("IsAWSName(%q) = true, want false", name)
		}
	}
}

// awsSuffixes are 7.3's hard-refused suffixes plus execute-api.com.
var awsSuffixes = []string{
	"amazonaws.com", "api.aws", "amazonaws.com.cn", "api.amazonwebservices.com.cn",
	"amazonaws.eu", "api.amazonwebservices.eu", "c2s.ic.gov", "api.aws.ic.gov",
	"sc2s.sgov.gov", "api.aws.scloud", "cloud.adc-e.uk", "api.cloud-aws.adc-e.uk",
	"csp.hci.ic.gov", "api.aws.hci.ic.gov", "aws", "execute-api.com",
}

func genLabels() *rapid.Generator[[]string] {
	return rapid.SliceOfN(rapid.StringMatching(`[a-z0-9]([a-z0-9-]{0,6}[a-z0-9])?`), 1, 3)
}

// randomCase upper-cases each letter of s with a drawn coin.
func randomCase(t *rapid.T, s, label string) string {
	var b strings.Builder
	for i, r := range s {
		if rapid.Bool().Draw(t, fmt.Sprintf("%s %d upper", label, i)) {
			r = unicode.ToUpper(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

func TestIsAWSNameHoldsUnderEverySuffix(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		name := strings.Join(genLabels().Draw(t, "labels"), ".") + "." + rapid.SampledFrom(awsSuffixes).Draw(t, "suffix")
		name = randomCase(t, name, "name")
		if rapid.Bool().Draw(t, "trailing dot") {
			name += "."
		}
		if !egress.IsAWSName(name) {
			t.Fatalf("IsAWSName(%q) = false, want true", name)
		}
	})
}

func TestIsAWSNameIsFalseOutsideTheSuffixes(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		name := strings.Join(genLabels().Draw(t, "labels"), ".") + "." + rapid.SampledFrom([]string{"test", "example", "invalid", "localhost", "internal", "lan"}).Draw(t, "tld")
		if egress.IsAWSName(name) {
			t.Fatalf("IsAWSName(%q) = true, want false", name)
		}
	})
}

// forbidden reports the addresses 7.3 refuses whatever the list says.
func forbidden(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() ||
		a == netip.MustParseAddr("169.254.169.254") || a == netip.MustParseAddr("fd00:ec2::254") || a == netip.MustParseAddr("169.254.170.2")
}

// genEntry draws a well-formed entry and the Entry it parses to.
func genEntry() *rapid.Generator[struct {
	text string
	want egress.Entry
}] {
	type out = struct {
		text string
		want egress.Entry
	}
	return rapid.Custom(func(t *rapid.T) out {
		var host, norm string
		switch rapid.IntRange(0, 3).Draw(t, "host kind") {
		case 0:
			host, norm = randomCase(t, "localhost", "localhost"), "localhost"
		case 1:
			b := rapid.SliceOfN(rapid.Byte(), 4, 4).Draw(t, "ipv4")
			a := netip.AddrFrom4([4]byte(b))
			if forbidden(a) {
				t.Skip("an always-refused address")
			}
			host, norm = a.String(), a.String()
		case 2:
			b := rapid.SliceOfN(rapid.Byte(), 16, 16).Draw(t, "ipv6")
			a := netip.AddrFrom16([16]byte(b))
			if forbidden(a) {
				t.Skip("an always-refused address")
			}
			host, norm = "["+a.StringExpanded()+"]", a.Unmap().String()
		default:
			labels := genLabels().Draw(t, "labels")
			norm = strings.Join(labels, ".") + ".test"
			host = randomCase(t, norm, "name")
			if rapid.Bool().Draw(t, "trailing dot") {
				host += "."
			}
		}
		if rapid.Bool().Draw(t, "any port") {
			return out{host + ":*", egress.Entry{Host: norm, Port: egress.AnyPort}}
		}
		p := rapid.Uint16Range(1, 65535).Draw(t, "port")
		return out{fmt.Sprintf("%s:%d", host, p), egress.Entry{Host: norm, Port: egress.Port(p)}}
	})
}

// Every well-formed entry parses to its normalized form, prints in a form
// that parses back to it, and matches its own host and port (7.2).
func TestWellFormedEntriesParseAndMatch(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		e := genEntry().Draw(t, "entry")
		a, err := egress.ParseAllowList(setting, []string{e.text})
		if err != nil {
			t.Fatalf("ParseAllowList(%q): %v", e.text, err)
		}
		if got := a.Entries(); !slices.Equal(got, []egress.Entry{e.want}) {
			t.Fatalf("ParseAllowList(%q).Entries() = %v, want %v", e.text, got, e.want)
		}
		again, err := egress.ParseAllowList(setting, []string{e.want.String()})
		if err != nil || !slices.Equal(again.Entries(), []egress.Entry{e.want}) {
			t.Fatalf("ParseAllowList(%q), the printed entry, = %v, %v; want %v", e.want.String(), again.Entries(), err, e.want)
		}
		port := uint16(e.want.Port)
		if e.want.Port == egress.AnyPort {
			port = rapid.Uint16Range(1, 65535).Draw(t, "request port")
		}
		if got, ok := a.Match(e.want.Host, port); !ok || got != e.want {
			t.Fatalf("Match(%q, %d) = %v, %t; want %v", e.want.Host, port, got, ok, e.want)
		}
		if e.want.Port != egress.AnyPort {
			other := rapid.Uint16Range(1, 65535).Filter(func(p uint16) bool { return p != port }).Draw(t, "other port")
			if got, ok := a.Match(e.want.Host, other); ok {
				t.Fatalf("Match(%q, %d) = %v, want no match for another port", e.want.Host, other, got)
			}
		}
	})
}

// One refused entry anywhere refuses the list and names its index.
func TestARefusedEntryNamesItsIndex(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		good := rapid.SliceOfN(genEntry(), 0, 4).Draw(t, "good")
		entries := make([]string, 0, len(good)+1)
		for _, g := range good {
			entries = append(entries, g.text)
		}
		i := rapid.IntRange(0, len(entries)).Draw(t, "index")
		bad := rapid.SampledFrom(refusedEntries).Draw(t, "bad")
		entries = slices.Insert(entries, i, bad)
		_, err := egress.ParseAllowList(setting, entries)
		if err == nil {
			t.Fatalf("ParseAllowList(%q) accepted %q", entries, bad)
		}
		if err.Index != i || err.Entry != bad {
			t.Fatalf("ParseAllowList(%q) refused entry %d %q, want %d %q", entries, err.Index, err.Entry, i, bad)
		}
	})
}
