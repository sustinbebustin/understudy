package server

import (
	"io"
	"net/netip"
	"testing"

	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
)

// Where the main listener already holds the address function containers
// reach understudy at, on every interface or on exactly that address, no
// container listener opens: a second one would collide with it, so
// functions call the main listener (ADR 0011).
func TestFunctionsShareAMainListenerOnTheirAddress(t *testing.T) {
	gateway := netip.MustParseAddr("127.0.0.1")
	for _, bind := range []string{"0.0.0.0:0", "127.0.0.1:0"} {
		t.Run(bind, func(t *testing.T) {
			// A bridge whose gateway is loopback stands in for docker0: the
			// host holds the address, so a container listener could open.
			daemon := dockertest.New(t, dockertest.Options{BridgeSubnet: netip.MustParsePrefix("127.0.0.0/8")})
			srv, err := New(Config{Docker: DockerSocket{Path: daemon.Socket()}, LambdaNetwork: NetworkBridge{Gateway: gateway}, LogOutput: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := srv.Close(); err != nil {
					t.Errorf("Close = %v", err)
				}
			})
			main, err := listen(t.Context(), netip.MustParseAddrPort(bind))
			if err != nil {
				t.Fatal(err)
			}
			if err := srv.begin(); err != nil {
				t.Fatal(err)
			}
			sess, err := srv.start(t.Context(), main, nil)
			t.Cleanup(func() {
				if err := srv.shutdown(t.Context(), sess); err != nil {
					t.Errorf("shutdown = %v", err)
				}
			})
			if err != nil {
				t.Fatalf("start = %v", err)
			}
			if _, err := srv.wired.host.lambda(t.Context()); err != nil {
				t.Fatalf("building the function runtime with the main listener on %s = %v, want functions to share it", main.Addr(), err)
			}
			sess.ls.mu.Lock()
			served := len(sess.ls.open)
			sess.ls.mu.Unlock()
			if served != 1 {
				t.Errorf("listeners served with the main listener on %s = %d, want only the main listener", main.Addr(), served)
			}
		})
	}
}
