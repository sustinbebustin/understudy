package server_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/server"
)

// dockerLabels keys under understudy.dev/ are understudy's own: New refuses
// one as a configuration refusal of the setting, and serves nothing
// (testcontainers.md 9.3).
func TestNewRefusesAReservedDockerLabel(t *testing.T) {
	for _, engine := range []server.Docker{server.DockerDisabled{}, server.DockerSocket{Path: "/nonexistent/docker.sock"}} {
		srv, err := server.New(server.Config{
			DockerLabels: map[string]string{"org.testcontainers.session-id": "s1", docker.LabelManaged: "false"},
			Docker:       engine, LogOutput: io.Discard,
		})
		if err == nil {
			_ = srv.Close()
		}
		se, ok := errors.AsType[*server.SettingError](err)
		if !ok || se.Setting != "dockerLabels" {
			t.Fatalf("New with Docker %T and a reserved label = %v, want a *SettingError for dockerLabels", engine, err)
		}
		if re, ok := errors.AsType[*docker.ReservedLabelError](err); !ok || re.Key != docker.LabelManaged {
			t.Errorf("New with a reserved label = %v, want a *docker.ReservedLabelError naming %s inside", err, docker.LabelManaged)
		}
		if msg := err.Error(); !strings.HasPrefix(msg, "config: dockerLabels: ") || !strings.Contains(msg, docker.LabelManaged) {
			t.Errorf("New with a reserved label = %q, want it to read as a configuration refusal of dockerLabels naming the key", msg)
		}
	}
}

// Labels outside understudy.dev/ are accepted and change nothing else
// about serving.
func TestServerAcceptsSessionDockerLabels(t *testing.T) {
	c, _ := adminServer(t, server.Config{
		DockerLabels: map[string]string{"org.testcontainers": "true", "org.testcontainers.session-id": "s1", "com.example/team": "payments"},
		Docker:       server.DockerDisabled{},
	})
	h, err := c.Health(t.Context())
	if err != nil || !h.Ready {
		t.Fatalf("health = %+v, %v; want ready", h, err)
	}
}
