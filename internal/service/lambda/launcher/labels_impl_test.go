package launcher_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
)

// New keeps its own copy of ExtraLabels: a change to the caller's map
// afterwards reaches no container (testcontainers.md 9.3).
func TestNewCopiesTheExtraLabels(t *testing.T) {
	d := newDaemon(t)
	extra := map[string]string{"org.testcontainers.session-id": "s1"}
	l, err := launcher.New(d.Client(t), launcher.Options{
		Ownership: docker.Ownership{Instance: instance, Owner: owner}, Test: true,
		Topology: bridge(), EndpointPort: 4566, ExtraLabels: extra,
	})
	if err != nil {
		t.Fatal(err)
	}
	extra["org.testcontainers.session-id"] = "s2"
	extra[docker.LabelManaged] = "false"
	c, err := l.Start(t.Context(), spec(t), slot())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, ok := d.Container(c.ID)
	if !ok {
		t.Fatal("container not on the daemon")
	}
	if v := got.Config.Labels["org.testcontainers.session-id"]; v != "s1" {
		t.Errorf("session label = %q, want s1, the value New was given", v)
	}
	if v := got.Config.Labels[docker.LabelManaged]; v != "true" {
		t.Errorf("%s = %q, want true", docker.LabelManaged, v)
	}
}
