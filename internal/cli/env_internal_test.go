package cli

import (
	"crypto/x509"
	"net"
	"testing"
)

// env --tls installs only a root whose critical name constraints keep it
// to localhost and loopback addresses; any looser or missing constraint
// refuses the certificate.
func TestConstrainedToLoopback(t *testing.T) {
	loopback := func() []*net.IPNet {
		return []*net.IPNet{
			{IP: net.IPv4(127, 0, 0, 0).To4(), Mask: net.CIDRMask(8, 32)},
			{IP: net.IPv6loopback, Mask: net.CIDRMask(128, 128)},
		}
	}
	understudy := func() *x509.Certificate {
		return &x509.Certificate{PermittedDNSDomains: []string{"localhost"}, PermittedIPRanges: loopback(), PermittedDNSDomainsCritical: true}
	}
	if !constrainedToLoopback(understudy()) {
		t.Fatal("understudy's own constraints were refused")
	}
	for name, change := range map[string]func(*x509.Certificate){
		"no constraints":       func(c *x509.Certificate) { *c = x509.Certificate{} },
		"not critical":         func(c *x509.Certificate) { c.PermittedDNSDomainsCritical = false },
		"another domain":       func(c *x509.Certificate) { c.PermittedDNSDomains = append(c.PermittedDNSDomains, "example.com") },
		"no IP constraint":     func(c *x509.Certificate) { c.PermittedIPRanges = nil },
		"a wider address":      func(c *x509.Certificate) { c.PermittedIPRanges[0].Mask = net.CIDRMask(0, 32) },
		"an excluded instead":  func(c *x509.Certificate) { c.ExcludedDNSDomains = []string{"example.com"} },
		"no domain constraint": func(c *x509.Certificate) { c.PermittedDNSDomains = nil },
	} {
		c := understudy()
		change(c)
		if constrainedToLoopback(c) {
			t.Errorf("%s: accepted", name)
		}
	}
}
