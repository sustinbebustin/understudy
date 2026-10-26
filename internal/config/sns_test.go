package config_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/config"
	"github.com/sustinbebustin/understudy/internal/egress"
)

// The allow-list grammar's hint, as every refused entry gives it
// (sns-http-delivery.md 7.2).
const allowHint = "entries are host:port, the host localhost, an IP literal ([v6] for IPv6) or a DNS name, and the port 1-65535 or *"

var snsSetting = egress.Setting{Service: "sns", Feature: "httpDelivery"}

func allowEntries(cfg config.Config) []string {
	entries := cfg.SNS.HTTPDelivery.Allow.Entries()
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.String())
	}
	return out
}

// The two SNS HTTP delivery settings follow sigv4 in the list help shows,
// keyed sns.httpDelivery.allow and sns.httpDelivery.caBundle (ADR 0015,
// sns-http-delivery.md 7.2, 7.4).
func TestSNSHTTPDeliverySettings(t *testing.T) {
	want := []config.Setting{
		{
			Key: "sns.httpDelivery.allow", Flag: "sns-http-delivery-allow", Env: "UNDERSTUDY_SNS_HTTP_DELIVERY_ALLOW", Value: "HOST:PORT,...",
			Usage: "hosts SNS delivers http and https subscriptions to, as comma-separated host:port entries: the host localhost, an IP literal ([v6] for IPv6) or a DNS name, " +
				"the port 1-65535 or * (default none: nothing is delivered). AWS hosts, link-local and metadata addresses and understudy's own listeners are refused whatever the list says",
		},
		{
			Key: "sns.httpDelivery.caBundle", Flag: "sns-http-delivery-ca-bundle", Env: "UNDERSTUDY_SNS_HTTP_DELIVERY_CA_BUNDLE", Value: "FILE",
			Usage: "PEM file of CA certificates an https subscription endpoint may chain to, beside the system roots (default none)",
		},
	}
	settings := config.Settings()
	sigv4 := slices.IndexFunc(settings, func(s config.Setting) bool { return s.Key == "sigv4" })
	allow := slices.IndexFunc(settings, func(s config.Setting) bool { return s.Key == want[0].Key })
	if sigv4 < 0 || allow < 0 {
		t.Fatalf("Settings lists sigv4 at %d and sns.httpDelivery.allow at %d, want both", sigv4, allow)
	}
	if allow < sigv4 {
		t.Errorf("sns.httpDelivery.allow is at %d, before sigv4 at %d", allow, sigv4)
	}
	if allow+1 >= len(settings) {
		t.Fatalf("Settings ends at sns.httpDelivery.allow; want sns.httpDelivery.caBundle after it")
	}
	for i, w := range want {
		if got := settings[allow+i]; got != w {
			t.Errorf("Settings()[%d] = %+v\nwant %+v", allow+i, got, w)
		}
	}
}

// The list comes from the highest source that sets it, whole: flag over
// variable over file; none allows nothing.
func TestSNSHTTPDeliveryAllowSources(t *testing.T) {
	file := files{config.DefaultConfigFile: `{"sns": {"httpDelivery": {"allow": ["127.0.0.1:8080", "localhost:*"]}}}`}
	cases := []struct {
		name string
		args []string
		env  []string
		f    files
		want []string
	}{
		{"default", nil, nil, files{}, nil},
		{"file", nil, nil, file, []string{"127.0.0.1:8080", "localhost:*"}},
		{"variable over file", nil, []string{"UNDERSTUDY_SNS_HTTP_DELIVERY_ALLOW=[::1]:9000,hooks.example.test:443"}, file, []string{"::1:9000", "hooks.example.test:443"}},
		{
			"flag over variable",
			[]string{"--sns-http-delivery-allow", "127.0.0.1:*"},
			[]string{"UNDERSTUDY_SNS_HTTP_DELIVERY_ALLOW=localhost:8080"},
			file,
			[]string{"127.0.0.1:*"},
		},
		{"an empty flag is the empty list", []string{"--sns-http-delivery-allow="}, nil, file, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := allowEntries(load(t, c.args, c.env, c.f))
			want := c.want
			// Compare through egress's own normal form.
			if len(want) > 0 {
				parsed, perr := egress.ParseAllowList(snsSetting, want)
				if perr != nil {
					t.Fatalf("ParseAllowList(%q): %v", want, perr)
				}
				want = nil
				for _, e := range parsed.Entries() {
					want = append(want, e.String())
				}
			}
			if !slices.Equal(got, want) {
				t.Errorf("allow = %q, want %q", got, want)
			}
		})
	}
	cfg := load(t, []string{"--sns-http-delivery-allow", "127.0.0.1:8080"}, nil, files{})
	if got := cfg.SNS.HTTPDelivery.Allow.Setting(); got != snsSetting {
		t.Errorf("the list's setting = %v, want %v", got, snsSetting)
	}
}

// A malformed entry, or one naming a host that is always refused, stops
// serve with the entry, egress's reason and the grammar
// (sns-http-delivery.md 7.2, 7.3).
func TestSNSHTTPDeliveryAllowRefusals(t *testing.T) {
	for _, entries := range [][]string{
		{"127.0.0.1:8080", "no-port"},
		{"sqs.us-east-1.amazonaws.com:443"},
		{"169.254.169.254:80"},
		{"localhost:0"},
	} {
		_, perr := egress.ParseAllowList(snsSetting, entries)
		if perr == nil {
			t.Fatalf("ParseAllowList(%q) accepted the list", entries)
		}
		reason := "entry " + strconv.Itoa(perr.Index) + ": " + perr.Reason
		joined := strings.Join(entries, ",")
		quoted := make([]string, len(entries))
		for i, e := range entries {
			quoted[i] = `"` + e + `"`
		}
		sources := []struct {
			args, env []string
			f         files
			where     string
		}{
			{[]string{"--sns-http-delivery-allow", joined}, nil, files{}, "--sns-http-delivery-allow"},
			{nil, []string{"UNDERSTUDY_SNS_HTTP_DELIVERY_ALLOW=" + joined}, files{}, "UNDERSTUDY_SNS_HTTP_DELIVERY_ALLOW"},
			{nil, nil, files{config.DefaultConfigFile: `{"sns": {"httpDelivery": {"allow": [` + strings.Join(quoted, ", ") + `]}}}`}, config.DefaultConfigFile + ": sns.httpDelivery.allow"},
		}
		for _, s := range sources {
			err := loadErr(t, s.args, s.env, s.f)
			if err.Where != s.where || err.Reason != reason || err.Hint != allowHint {
				t.Errorf("%q from %s: error at %q: %q (hint %q)\nwant at %q: %q (hint %q)", entries, s.where, err.Where, err.Reason, err.Hint, s.where, reason, allowHint)
			}
		}
	}
}

// The file form is nested and typed: allow is an array of strings and
// caBundle a string; the dotted key at the top level is refused, naming
// the nested form.
func TestSNSHTTPDeliveryFileForm(t *testing.T) {
	at := func(key string) string { return config.DefaultConfigFile + ": " + key }
	cases := []struct {
		name, doc, where, closest string
	}{
		{"allow as a string", `{"sns": {"httpDelivery": {"allow": "127.0.0.1:8080"}}}`, at("sns.httpDelivery.allow"), ""},
		{"an allow number", `{"sns": {"httpDelivery": {"allow": [8080]}}}`, at("sns.httpDelivery.allow"), ""},
		{"caBundle as a list", `{"sns": {"httpDelivery": {"caBundle": ["/ca.pem"]}}}`, at("sns.httpDelivery.caBundle"), ""},
		{"an unknown member", `{"sns": {"httpDelivery": {"allowed": ["127.0.0.1:8080"]}}}`, at("sns.httpDelivery.allowed"), "allow"},
	}
	for _, c := range cases {
		err := loadErr(t, nil, nil, files{config.DefaultConfigFile: c.doc})
		if err.Where != c.where || (c.closest != "" && err.Closest != c.closest) {
			t.Errorf("%s: error at %q closest %q, want at %q closest %q (%v)", c.name, err.Where, err.Closest, c.where, c.closest, err)
		}
	}
	// The dotted key at the top level is refused, and the refusal shows
	// the nested form.
	err := loadErr(t, nil, nil, files{config.DefaultConfigFile: `{"sns.httpDelivery.allow": ["127.0.0.1:8080"]}`})
	if err.Where != at("sns.httpDelivery.allow") || !strings.Contains(err.Reason+err.Hint, `"httpDelivery"`) {
		t.Errorf("the dotted key: error %+v, want it refused at that key, giving the nested form", err)
	}
	// Both members load together.
	bundle, _ := selfSignedPEM(t)
	cfg := load(t, nil, nil, files{
		"/certs/ca.pem":          bundle,
		config.DefaultConfigFile: `{"sns": {"httpDelivery": {"allow": ["127.0.0.1:8080"], "caBundle": "/certs/ca.pem"}}}`,
	})
	if got := allowEntries(cfg); !slices.Equal(got, []string{"127.0.0.1:8080"}) || len(cfg.SNS.HTTPDelivery.CABundle) != 1 {
		t.Errorf("allow %q and %d bundle certificates, want 127.0.0.1:8080 and 1", got, len(cfg.SNS.HTTPDelivery.CABundle))
	}
}

// selfSignedPEM is a CA certificate in PEM.
func selfSignedPEM(t *testing.T) (string, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "receiver CA"},
		NotBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), cert
}

// caBundle is read through Load's file reader and parsed, from the flag,
// the variable or the file; unset, there is none (sns-http-delivery.md
// 7.4).
func TestSNSHTTPDeliveryCABundle(t *testing.T) {
	const path = "/certs/receiver-ca.pem"
	bundle, cert := selfSignedPEM(t)
	for name, src := range map[string]struct {
		args, env []string
		f         files
	}{
		"flag":     {[]string{"--sns-http-delivery-ca-bundle", path}, nil, files{path: bundle}},
		"variable": {nil, []string{"UNDERSTUDY_SNS_HTTP_DELIVERY_CA_BUNDLE=" + path}, files{path: bundle}},
		"file":     {nil, nil, files{path: bundle, config.DefaultConfigFile: `{"sns": {"httpDelivery": {"caBundle": "` + path + `"}}}`}},
	} {
		got := load(t, src.args, src.env, src.f).SNS.HTTPDelivery.CABundle
		if len(got) != 1 || !got[0].Equal(cert) {
			t.Errorf("%s: CABundle = %d certificates, want the bundle's one", name, len(got))
		}
	}
	if got := load(t, nil, nil, files{}).SNS.HTTPDelivery.CABundle; got != nil {
		t.Errorf("CABundle with no setting = %v, want nil", got)
	}
	twoCerts := bundle + bundle
	if got := load(t, []string{"--sns-http-delivery-ca-bundle", path}, nil, files{path: twoCerts}).SNS.HTTPDelivery.CABundle; len(got) != 2 {
		t.Errorf("a bundle of two certificates gave %d", len(got))
	}
}

func TestSNSHTTPDeliveryCABundleRefusals(t *testing.T) {
	const path = "/certs/receiver-ca.pem"
	broken := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")}))
	cases := []struct {
		name         string
		f            files
		reasonPrefix string
		hint         string
	}{
		{"unreadable", files{}, `cannot read the CA bundle "` + path + `": `, "check the path, or remove the setting"},
		{"no certificate", files{path: "just text\n"}, `"` + path + `" holds no PEM certificate`, ""},
		{"a block that does not parse", files{path: broken}, `"` + path + `": certificate `, ""},
	}
	for _, c := range cases {
		err := loadErr(t, []string{"--sns-http-delivery-ca-bundle", path}, nil, c.f)
		if err.Where != "--sns-http-delivery-ca-bundle" || !strings.HasPrefix(err.Reason, c.reasonPrefix) {
			t.Errorf("%s: error at %q: %q, want at --sns-http-delivery-ca-bundle starting %q", c.name, err.Where, err.Reason, c.reasonPrefix)
		}
		if c.hint != "" && err.Hint != c.hint {
			t.Errorf("%s: hint %q, want %q", c.name, err.Hint, c.hint)
		}
		if c.name == "a block that does not parse" && !strings.Contains(err.Reason, "does not parse: ") {
			t.Errorf("%s: reason %q does not say the certificate does not parse", c.name, err.Reason)
		}
	}
	err := loadErr(t, nil, []string{"UNDERSTUDY_SNS_HTTP_DELIVERY_CA_BUNDLE=" + path}, files{})
	if err.Where != "UNDERSTUDY_SNS_HTTP_DELIVERY_CA_BUNDLE" {
		t.Errorf("an unreadable bundle from the variable is refused at %q", err.Where)
	}
	err = loadErr(t, nil, nil, files{config.DefaultConfigFile: `{"sns": {"httpDelivery": {"caBundle": "` + path + `"}}}`})
	if err.Where != config.DefaultConfigFile+": sns.httpDelivery.caBundle" {
		t.Errorf("an unreadable bundle from the file is refused at %q", err.Where)
	}
}
