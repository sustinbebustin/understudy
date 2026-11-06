package topology

import (
	"strings"
	"testing"
)

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
