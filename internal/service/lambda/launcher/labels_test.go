package launcher_test

import (
	"errors"
	"maps"
	"testing"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
)

// ExtraLabels are added to every function container beside understudy's
// own, so a Testcontainers reaper selecting its session labels removes the
// containers a containerized understudy started (testcontainers.md 9.3,
// 8.6; finding 3).

// sessionLabels are tc-python's and tc-node's session label
// (testcontainers.md 5.4, 6.4) and tc-go's (testcontainers.md 4.5).
func sessionLabels() map[string]string {
	return map[string]string{
		"org.testcontainers":            "true",
		"org.testcontainers.session-id": "6f1b8c2e-1d7a-4c39-9a51-0d2f3b4c5e6f",
		"org.testcontainers.sessionId":  "0d2f3b4c5e6f6f1b8c2e1d7a4c399a51",
	}
}

func TestStartAddsTheExtraLabels(t *testing.T) {
	d := newDaemon(t)
	extra := sessionLabels()
	l, err := launcher.New(d.Client(t), launcher.Options{
		Ownership: docker.Ownership{Instance: instance, Owner: owner}, Test: true,
		Topology: bridge(), EndpointPort: 4566, ExtraLabels: extra,
	})
	if err != nil {
		t.Fatalf("New with session labels = %v", err)
	}
	c, err := l.Start(t.Context(), spec(t), slot())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, ok := d.Container(c.ID)
	if !ok {
		t.Fatal("container not on the daemon")
	}
	want := maps.Clone(extra)
	maps.Copy(want, map[string]string{
		docker.LabelManaged: "true", docker.LabelInstance: instance, docker.LabelOwner: owner,
		docker.LabelFunction: "worker", docker.LabelAccount: "123456789012", docker.LabelRegion: "eu-west-1",
		docker.LabelGeneration: "3", docker.LabelTest: "1",
	})
	for k, v := range want {
		if got.Config.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, got.Config.Labels[k], v)
		}
	}
}

func TestNewRefusesAReservedExtraLabel(t *testing.T) {
	d := newDaemon(t)
	for _, key := range []string{docker.LabelManaged, docker.LabelFunction, "understudy.dev/anything"} {
		extra := sessionLabels()
		extra[key] = "x"
		l, err := launcher.New(d.Client(t), launcher.Options{
			Ownership: docker.Ownership{Instance: instance, Owner: owner}, Test: true,
			Topology: bridge(), EndpointPort: 4566, ExtraLabels: extra,
		})
		if re, ok := errors.AsType[*docker.ReservedLabelError](err); !ok || re.Key != key || l != nil {
			t.Errorf("New with extra label %q: launcher returned %v, error %v; want no launcher and a *docker.ReservedLabelError naming it", key, l != nil, err)
		}
	}
	if created := d.Created(); len(created) != 0 {
		t.Errorf("a refused New created %d containers", len(created))
	}
}
