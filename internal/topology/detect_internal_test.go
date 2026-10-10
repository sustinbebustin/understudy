package topology

import (
	"net/netip"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// getent ahostsv4 prints each address once per socket type, naming the
// host only on the address's first line.
func TestParseGetentReturnsTheFirstIPv4Address(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		addr := rapid.Custom(func(t *rapid.T) netip.Addr {
			return netip.AddrFrom4([4]byte(rapid.SliceOfN(rapid.Byte(), 4, 4).Draw(t, "octets")))
		})
		addrs := rapid.SliceOfN(addr, 1, 3).Draw(t, "addrs")
		name := rapid.StringMatching(`[a-z][a-z0-9.-]{0,20}`).Draw(t, "name")
		sep := rapid.StringMatching(`[ \t]{1,4}`).Draw(t, "sep")
		var out strings.Builder
		for _, a := range addrs {
			out.WriteString(a.String() + sep + "STREAM " + name + "\n")
			out.WriteString(a.String() + sep + "DGRAM  \n")
			out.WriteString(a.String() + sep + "RAW    \n")
		}
		got, err := parseGetent(out.String())
		if err != nil || got != addrs[0] {
			t.Fatalf("parseGetent(%q) = %v, %v; want %v", out.String(), got, err, addrs[0])
		}
	})
}

func TestParseGetentNeverAcceptsGarbage(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		out := rapid.String().Draw(t, "out")
		if got, err := parseGetent(out); err == nil && !got.Is4() {
			t.Fatalf("parseGetent(%q) = %v without error, want an IPv4 address or an error", out, got)
		}
	})
}

func TestContainerIDInPath(t *testing.T) {
	id := strings.Repeat("ab12", 16)
	cases := []struct {
		path string
		want string
	}{
		{"/var/lib/docker/containers/" + id + "/hostname", id},
		{"/var/lib/docker/containers/" + id[:63] + "/hostname", ""},
		{"/var/lib/docker/containers/" + strings.ToUpper(id) + "/hostname", ""},
		// Podman: the first "/containers/" segment is not followed by an ID.
		{"/run/containers/storage/overlay-containers/" + id + "/userdata/hostname", id},
		{"/home/u/.local/share/containers/storage/overlay-containers/" + id + "/userdata/hosts", id},
		{"/var/lib/docker/containers/" + id, ""},
		{"/etc/hostname", ""},
	}
	for _, tc := range cases {
		got, ok := containerIDInPath(tc.path)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("containerIDInPath(%q) = %q, %v; want %q", tc.path, got, ok, tc.want)
		}
	}
}
