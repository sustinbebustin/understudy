package egress_test

import (
	"net/http"
	"testing"
	"testing/synctest"

	"github.com/sustinbebustin/understudy/internal/egress"
)

// A listener reported as an IPv4-mapped IPv6 address is the IPv4 address
// dialed, so it is refused as understudy's own (sns-http-delivery.md 7.3).
func TestSendRefusesAMappedListener(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := newNetwork(map[string]*receiver{"127.0.0.1:45003": answering(http.StatusOK)})
		live := &listeners{}
		live.set("[::ffff:127.0.0.1]:45003")
		cl := newClient(t, egress.Options{Dialer: n, Resolver: resolver{}, Listeners: live}, "127.0.0.1:*")
		_, err := send(t, cl, "http://127.0.0.1:45003/hook", nil, "hello")
		refusedAs(t, n, err, egress.OwnListener, "127.0.0.1", 45003, "")
	})
}
