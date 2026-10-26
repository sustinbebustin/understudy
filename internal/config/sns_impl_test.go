package config_test

import (
	"encoding/json"
	"encoding/pem"
	"slices"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/config"
)

// entryText draws an allow-list entry in a form the grammar accepts
// (sns-http-delivery.md 7.2), such as compose's and Kubernetes' service
// names, written as an operator would write it.
func entryText() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		host := rapid.SampledFrom([]string{
			"localhost", "127.0.0.1", "[::1]", "10.0.0.7", "receiver", "receiver.default.svc.cluster.local", "hooks.example.test", "[fd12::7]",
		}).Draw(t, "host")
		port := "*"
		if rapid.Bool().Draw(t, "exact port") {
			port = strconv.Itoa(rapid.IntRange(1, 65535).Draw(t, "port"))
		}
		return host + ":" + port
	})
}

// Whichever single source carries sns.httpDelivery.allow, the list is the
// same: the flag and variable's comma-separated text and the file's array
// differ only in precedence.
func TestSNSHTTPDeliveryAllowSourcesAgree(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		entries := rapid.SliceOfN(entryText(), 1, 5).Draw(t, "entries")
		text := strings.Join(entries, ",")
		array, err := json.Marshal(entries)
		if err != nil {
			t.Fatal(err)
		}
		fromFlag, ferr := config.Load([]string{"--sns-http-delivery-allow", text}, nil, files{}.read)
		fromEnv, eerr := config.Load(nil, []string{"UNDERSTUDY_SNS_HTTP_DELIVERY_ALLOW=" + text}, files{}.read)
		fromFile, lerr := config.Load(nil, nil, files{config.DefaultConfigFile: `{"sns": {"httpDelivery": {"allow": ` + string(array) + `}}}`}.read)
		if ferr != nil || eerr != nil || lerr != nil {
			t.Fatalf("%q: flag %v, variable %v, file %v", entries, ferr, eerr, lerr)
		}
		want := allowEntries(fromFlag)
		if len(want) != len(entries) {
			t.Fatalf("%q loaded as %q, want one entry each", entries, want)
		}
		if got := allowEntries(fromEnv); !slices.Equal(got, want) {
			t.Fatalf("variable %q = %q, flag = %q", text, got, want)
		}
		if got := allowEntries(fromFile); !slices.Equal(got, want) {
			t.Fatalf("file %s = %q, flag = %q", array, got, want)
		}
	})
}

// A bracketed IPv6 entry from the variable replaces the file's list whole
// (sns-http-delivery.md 7.2: an IPv6 host is bracketed).
func TestSNSHTTPDeliveryAllowVariableReplacesTheFile(t *testing.T) {
	f := files{config.DefaultConfigFile: `{"sns": {"httpDelivery": {"allow": ["127.0.0.1:8080", "localhost:*"]}}}`}
	cfg := load(t, nil, []string{"UNDERSTUDY_SNS_HTTP_DELIVERY_ALLOW=[::1]:9000,hooks.example.test:443"}, f)
	if got, want := allowEntries(cfg), []string{"[::1]:9000", "hooks.example.test:443"}; !slices.Equal(got, want) {
		t.Errorf("allow = %q, want %q", got, want)
	}
}

// Entries are taken as written: the file's array is not split on commas,
// and text around a comma is not trimmed, so neither is read as a
// different list than the one written.
func TestSNSHTTPDeliveryAllowEntriesAreTakenAsWritten(t *testing.T) {
	err := loadErr(t, nil, nil, files{config.DefaultConfigFile: `{"sns": {"httpDelivery": {"allow": ["127.0.0.1:1,127.0.0.1:2"]}}}`})
	if err.Where != config.DefaultConfigFile+": sns.httpDelivery.allow" || !strings.HasPrefix(err.Reason, "entry 0: ") {
		t.Errorf("a file entry holding a comma = %+v, want entry 0 refused", *err)
	}
	err = loadErr(t, []string{"--sns-http-delivery-allow", "127.0.0.1:8080, localhost:9000"}, nil, files{})
	if err.Where != "--sns-http-delivery-allow" || !strings.HasPrefix(err.Reason, `entry 1: the host " localhost"`) {
		t.Errorf("an entry after a comma and a space = %+v, want entry 1 refused with its space", *err)
	}
}

// A value of the wrong JSON type inside the list is named by its setting,
// as the flag would be, with the entry's index; elsewhere an array
// element's path is written [i], as an unknown key's is.
func TestWrongTypesInArraysNameTheElement(t *testing.T) {
	cases := []struct {
		name, file string
		want       config.Error
	}{
		{
			"an allow entry", `{"sns": {"httpDelivery": {"allow": ["127.0.0.1:8080", 8080]}}}`,
			config.Error{Where: config.DefaultConfigFile + ": sns.httpDelivery.allow", Reason: "entry 1: is a JSON number; expected a string"},
		},
		{
			"a hot-reload member", `{"lambda": {"hotReload": [{"function": "f", "dir": "/x"}, {"function": 7, "dir": "/y"}]}}`,
			config.Error{Where: config.DefaultConfigFile + ": lambda.hotReload[1].function", Reason: "is a JSON number; expected a string"},
		},
		{
			"a region", `{"regions": ["local-1", true]}`,
			config.Error{Where: config.DefaultConfigFile + ": regions[1]", Reason: "is a JSON bool; expected a string"},
		},
	}
	for _, c := range cases {
		if err := loadErr(t, nil, nil, files{config.DefaultConfigFile: c.file}); *err != c.want {
			t.Errorf("%s: %s refused as %+v, want %+v", c.name, c.file, *err, c.want)
		}
	}
}

// A nested setting's dotted key at the top level is refused with the
// nested form it takes, and a typo of it there is told that key.
func TestSNSHTTPDeliveryDottedKeys(t *testing.T) {
	cases := []struct {
		name, file string
		want       config.Error
	}{
		{
			"allow", `{"sns.httpDelivery.allow": ["127.0.0.1:8080"]}`,
			config.Error{
				Where: config.DefaultConfigFile + ": sns.httpDelivery.allow", Reason: `is not a top-level key; the setting is "allow" in the "sns.httpDelivery" section`,
				Hint: `write it as {"sns": {"httpDelivery": {"allow": ...}}}`,
			},
		},
		{
			"caBundle", `{"sns.httpDelivery.caBundle": "/ca.pem"}`,
			config.Error{
				Where: config.DefaultConfigFile + ": sns.httpDelivery.caBundle", Reason: `is not a top-level key; the setting is "caBundle" in the "sns.httpDelivery" section`,
				Hint: `write it as {"sns": {"httpDelivery": {"caBundle": ...}}}`,
			},
		},
		{
			"a typo", `{"sns.httpDelivery.alow": ["127.0.0.1:8080"]}`,
			config.Error{Where: config.DefaultConfigFile + ": sns.httpDelivery.alow", Reason: "is not a configuration key", Closest: "sns.httpDelivery.allow", Hint: "fix or remove the key"},
		},
	}
	for _, c := range cases {
		if err := loadErr(t, nil, nil, files{config.DefaultConfigFile: c.file}); *err != c.want {
			t.Errorf("%s: %s refused as %+v, want %+v", c.name, c.file, *err, c.want)
		}
	}
}

// The CA bundle's other PEM blocks, such as a key kept beside the
// certificates, are skipped, and a certificate that does not parse is
// counted among the certificates only, from 0.
func TestSNSHTTPDeliveryCABundleCountsCertificates(t *testing.T) {
	const path = "/certs/ca.pem"
	good, cert := selfSignedPEM(t)
	key := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a key either")}))
	got := load(t, []string{"--sns-http-delivery-ca-bundle", path}, nil, files{path: key + good}).SNS.HTTPDelivery.CABundle
	if len(got) != 1 || !got[0].Equal(cert) {
		t.Errorf("a key block then a certificate gave %d certificates, want the one", len(got))
	}
	broken := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")}))
	err := loadErr(t, []string{"--sns-http-delivery-ca-bundle", path}, nil, files{path: key + good + broken})
	if want := `"` + path + `": certificate 1 does not parse: `; !strings.HasPrefix(err.Reason, want) {
		t.Errorf("the second certificate broken: reason %q, want it to start %q", err.Reason, want)
	}
	err = loadErr(t, nil, []string{}, files{path: good, config.DefaultConfigFile: `{"sns": {"httpDelivery": {"caBundle": ""}}}`})
	if err.Where != config.DefaultConfigFile+": sns.httpDelivery.caBundle" || err.Reason != "is empty" {
		t.Errorf("an empty caBundle = %+v, want it refused as empty", *err)
	}
}
