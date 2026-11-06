package launcher_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/docker"
)

// When the daemon refuses the bind mounts, the retry copies the layer tree
// to /opt after the code, as tar copy does from the start
// (lambda-destinations-layers.md 11.5).
func TestStartCopiesTheLayerTreeWhenTheDaemonRefusesTheMounts(t *testing.T) {
	const denied = "Mounts denied: \nThe path /srv/understudy/code is not shared from the host and is not known to Docker."
	d := newDaemon(t)
	l := newLauncher(t, d, bridge())
	d.FailNext("POST /containers/create", http.StatusBadRequest, denied)
	c, err := l.Start(t.Context(), withLayers(t, layerTree(t)), slot())
	if err != nil {
		t.Fatalf("Start = %v, want a retry with the code and layers copied in", err)
	}
	got, _ := d.Container(c.ID)
	if !slices.Equal(got.HostConfig.Mounts, []docker.Mount{tmpVolume}) || got.HostConfig.ReadonlyRootfs {
		t.Errorf("Mounts = %+v, ReadonlyRootfs = %v; the retry needs no bind mounts and a writable root", got.HostConfig.Mounts, got.HostConfig.ReadonlyRootfs)
	}
	uploads := d.Uploads(c.ID)
	paths := make([]string, len(uploads))
	for i, u := range uploads {
		paths[i] = u.Path
	}
	if !slices.Equal(paths, []string{"/var/task", "/"}) {
		t.Fatalf("uploads at %v, want the code at /var/task, then the layer tree at /", paths)
	}
	if names, _ := entries(t, uploads[1].Data); !slices.Contains(names, "opt/python/greet.py") {
		t.Errorf("layer archive entries = %v, want opt/python/greet.py among them", names)
	}
}

// The code archive keeps its entries at its root, without the layer
// tree's opt prefix.
func TestCodeArchiveHasNoPrefix(t *testing.T) {
	d := newDaemon(t)
	l := newLauncher(t, d, bridge())
	d.FailNext("POST /containers/create", http.StatusBadRequest, "Mounts denied: \nThe path /x is not shared from the host and is not known to Docker.")
	s := withLayers(t, layerTree(t))
	c, err := l.Start(t.Context(), s, slot())
	if err != nil {
		t.Fatal(err)
	}
	if names, _ := entries(t, d.Uploads(c.ID)[0].Data); !slices.Equal(names, []string{"handler.py"}) {
		t.Errorf("code archive entries = %v, want the code tree's handler.py alone", names)
	}
}
