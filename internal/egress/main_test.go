package egress_test

import (
	"os"
	"testing"
)

// proxy is an address no test network listens on. Every test runs with the
// proxy variables pointing at it, set before net/http first reads and caches
// them, so a client that honoured them would dial it, which the in-memory
// network records (sns-http-delivery.md 7.4: Proxy is nil, so HTTP_PROXY and
// HTTPS_PROXY cannot route a delivery off the machine).
const proxy = "203.0.113.77:3128"

func TestMain(m *testing.M) {
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		if err := os.Setenv(name, "http://"+proxy); err != nil {
			panic(err)
		}
	}
	for _, name := range []string{"NO_PROXY", "no_proxy"} {
		if err := os.Unsetenv(name); err != nil {
			panic(err)
		}
	}
	m.Run()
}
