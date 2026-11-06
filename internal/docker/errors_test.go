package docker_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/docker"
)

func TestIsMountNotShared(t *testing.T) {
	desktop := &docker.Error{Op: "create container", Status: 400, Message: "Mounts denied: \nThe path /srv/code is not shared from the host and is not known to Docker."}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"Docker Desktop refusal": {desktop, true},
		"wrapped":                {fmt.Errorf("start: %w", desktop), true},
		"other status":           {&docker.Error{Op: "start container", Status: 500, Message: "mounts denied: the path /x is not shared"}, true},
		"other daemon error":     {&docker.Error{Op: "create container", Status: 400, Message: "invalid mount config for type \"bind\": bind source path does not exist: /x"}, false},
		"not a daemon error":     {errors.New("Mounts denied"), false},
		"nil":                    {nil, false},
	} {
		if got := docker.IsMountNotShared(tc.err); got != tc.want {
			t.Errorf("%s: IsMountNotShared(%v) = %v, want %v", name, tc.err, got, tc.want)
		}
	}
}

// Docker Desktop 4.98.0 accepted the create of a container binding an
// unshared path and refused its start with 500 and a text/plain body, not
// the Engine's JSON error (macOS Docker Desktop run, 2026-11-06).
func TestDesktopsPlainTextStartRefusalIsMountNotShared(t *testing.T) {
	const recorded = "mounts denied: \nThe path /opt/understudy-mount-probe is not shared from the host and is not known to Docker.\n" +
		"You can configure shared paths from Docker -> Preferences... -> Resources -> File Sharing.\n" +
		"See https://docs.docker.com/go/mac-file-sharing/ for more info."
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.56")
			if _, err := io.WriteString(w, "OK"); err != nil {
				t.Error(err)
			}
		case strings.HasSuffix(r.URL.Path, "/containers/probe/start"):
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			if _, err := io.WriteString(w, recorded); err != nil {
				t.Error(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := docker.New(docker.Options{Endpoint: docker.TCP{Addr: srv.Listener.Addr().String()}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.ContainerStart(t.Context(), "probe")
	if !docker.IsMountNotShared(err) {
		t.Errorf("ContainerStart = %v, want IsMountNotShared", err)
	}
}
