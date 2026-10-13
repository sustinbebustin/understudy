package runtimeapi_test

import (
	"net"
	"net/http"
	"net/netip"
	"testing"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// With Peers set, an environment's listener serves a peer inside the
// container network and refuses one outside it, so a LAN machine cannot
// take invocations with GET /next. The listener binds 127.0.0.1 and the
// client dials from 127.0.0.2: inside 127.0.0.0/8, outside 127.0.0.1/32.
func TestRuntimeAPIServesOnlyItsContainerNetwork(t *testing.T) {
	for _, tc := range []struct {
		name    string
		peers   netip.Prefix
		allowed bool
	}{
		{"a peer inside the network", netip.MustParsePrefix("127.0.0.0/8"), true},
		{"a peer outside it", netip.MustParsePrefix("127.0.0.1/32"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := clock.New(clock.Options{Wall: wallAt})
			if err != nil {
				t.Fatal(err)
			}
			s, err := runtimeapi.NewServer(runtimeapi.Options{
				Clock: c, Topology: topology.LinuxBridge{Gateway: netip.MustParseAddr("127.0.0.1")}, Peers: tc.peers,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			env, err := s.Open(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 2)}}).DialContext}}
			// The invocation endpoint for an unknown request id answers
			// without waiting, so an accepted peer gets a status at once.
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
				"http://"+env.Addr().String()+"/2018-06-01/runtime/invocation/unknown/response", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if got := err == nil; got != tc.allowed {
				t.Errorf("request from 127.0.0.2 with peers %s: err = %v, want served = %v", tc.peers, err, tc.allowed)
			}
		})
	}
}
