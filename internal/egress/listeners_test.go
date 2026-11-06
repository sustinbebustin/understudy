package egress_test

import (
	"errors"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/sustinbebustin/understudy/internal/egress"
)

// listeners is understudy's own listening addresses as the server would
// report them; a test changes them between requests.
type listeners struct {
	mu    sync.Mutex
	addrs []netip.AddrPort
	reads int
}

func (l *listeners) ListenerAddrs() []netip.AddrPort {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reads++
	return append([]netip.AddrPort(nil), l.addrs...)
}

func (l *listeners) set(addrs ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.addrs = l.addrs[:0]
	for _, a := range addrs {
		l.addrs = append(l.addrs, netip.MustParseAddrPort(a))
	}
}

// refusedAs is wantRefused for a caller that needs no more of the refusal.
func refusedAs(t *testing.T, n *network, err error, want egress.Refusal, host string, port uint16, name string) {
	t.Helper()
	if re := wantRefused(t, n, err, want, host, port, name); re.Setting != setting {
		t.Errorf("refusal names the setting %v, want %v", re.Setting, setting)
	}
}

// understudy's own listeners are refused whatever the allow-list says
// (sns-http-delivery.md 7.3): the main, TLS, container and Runtime API
// listeners, read when each request is checked, since a Runtime API
// listener comes and goes with its environment.
func TestSendRefusesTheLiveListeners(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		dns     resolver
		live    string
		url     string
		host    string
		port    uint16
		refused string
	}{
		{"a listener on a listed loopback literal", []string{"127.0.0.1:*"}, nil, "127.0.0.1:45001", "http://127.0.0.1:45001/2018-06-01/runtime/invocation/next", "127.0.0.1", 45001, ""},
		{"the TLS listener through localhost", []string{"localhost:4567"}, resolver{"localhost": to("localhost", "127.0.0.1")}, "127.0.0.1:4567", "https://localhost:4567/_understudy/sns/SigningCert", "localhost", 4567, "127.0.0.1"},
		{"the container listener on a listed private literal", []string{"172.17.0.1:*"}, nil, "172.17.0.1:4566", "http://172.17.0.1:4566/", "172.17.0.1", 4566, ""},
		{"a listener through a name resolving to a private address", []string{"bridge.example.test:4566"}, resolver{"bridge.example.test": to("bridge.example.test", "172.17.0.1")}, "172.17.0.1:4566", "http://bridge.example.test:4566/", "bridge.example.test", 4566, "172.17.0.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				n := newNetwork(map[string]*receiver{c.live: answering(http.StatusOK)})
				live := &listeners{}
				live.set(c.live)
				var dns egress.Resolver = resolver{}
				if c.dns != nil {
					dns = c.dns
				}
				cl := newClient(t, egress.Options{Dialer: n, Resolver: dns, Listeners: live}, c.entries...)
				_, err := send(t, cl, c.url, nil, "hello")
				refusedAs(t, n, err, egress.OwnListener, c.host, c.port, c.refused)
			})
		})
	}
}

// The listener addresses are read at each request's check: an address
// that becomes a listener is refused from then on, and one that stops
// being one is reached again.
func TestSendReadsTheListenersPerRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := answering(http.StatusNoContent)
		n := newNetwork(map[string]*receiver{"127.0.0.1:45002": r})
		live := &listeners{}
		cl := newClient(t, egress.Options{Dialer: n, Resolver: resolver{}, Listeners: live}, "127.0.0.1:*")

		if res, err := send(t, cl, "http://127.0.0.1:45002/hook", nil, "a"); err != nil || res.Status != http.StatusNoContent {
			t.Fatalf("Send before the listener exists = %+v, %v; want the receiver's 204", res, err)
		}
		live.set("127.0.0.1:45002")
		_, err := send(t, cl, "http://127.0.0.1:45002/hook", nil, "b")
		if re, ok := errors.AsType[*egress.RefusedError](err); !ok || re.Refusal != egress.OwnListener {
			t.Fatalf("Send while 127.0.0.1:45002 is a listener = %v, want an OwnListener refusal", err)
		}
		live.set()
		if res, err := send(t, cl, "http://127.0.0.1:45002/hook", nil, "c"); err != nil || res.Status != http.StatusNoContent {
			t.Fatalf("Send after the listener closed = %+v, %v; want the receiver's 204", res, err)
		}
		if got := len(r.requests()); got != 2 {
			t.Errorf("the receiver got %d requests, want the first and the last", got)
		}
		live.mu.Lock()
		reads := live.reads
		live.mu.Unlock()
		if reads < 3 {
			t.Errorf("ListenerAddrs was read %d times for 3 requests, want at least once per request", reads)
		}
	})
}

// The static Refuse list still applies beside the live listeners.
func TestSendRefusesBothRefuseAndListeners(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := newNetwork(map[string]*receiver{"127.0.0.1:2375": answering(http.StatusOK), "127.0.0.1:4566": answering(http.StatusOK)})
		live := &listeners{}
		live.set("127.0.0.1:4566")
		cl := newClient(t, egress.Options{Dialer: n, Resolver: resolver{}, Listeners: live, Refuse: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:2375")}}, "127.0.0.1:*")
		for _, target := range []struct {
			url  string
			port uint16
		}{{"http://127.0.0.1:2375/containers/create", 2375}, {"http://127.0.0.1:4566/", 4566}} {
			_, err := send(t, cl, target.url, nil, "{}")
			refusedAs(t, n, err, egress.OwnListener, "127.0.0.1", target.port, "")
		}
	})
}
