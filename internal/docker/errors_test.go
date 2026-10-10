package docker_test

import (
	"errors"
	"fmt"
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
