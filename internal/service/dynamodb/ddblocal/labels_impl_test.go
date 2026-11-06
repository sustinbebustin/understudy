package ddblocal_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
)

// New keeps its own copy of Labels.Extra: a change to the caller's map
// afterwards, a reserved key among them, reaches no container
// (testcontainers.md 9.3).
func TestNewCopiesTheExtraLabels(t *testing.T) {
	d := daemon(t)
	d.AddImage(hubRef, docker.Image{ID: ddblocal.Digest})
	d.SetProgram(hubRef, dockertest.Exit{Code: 1})
	extra := map[string]string{"org.testcontainers.session-id": "s1"}
	m, err := ddblocal.New(ddblocal.Config{
		Docker: d.Client(t),
		Labels: docker.Labels{Ownership: docker.Ownership{Instance: "inst1", Owner: "owner1"}, Extra: extra},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Stop(t.Context()); err != nil {
			t.Error(err)
		}
	})
	extra["org.testcontainers.session-id"] = "s2"
	extra[docker.LabelOwner] = "someone-else"
	if !errorIs[*ddblocal.ExitedError](m.Start(t.Context())) {
		t.Fatal("Start did not run the container")
	}
	created := d.Created()
	if len(created) != 1 {
		t.Fatalf("created %d containers, want 1", len(created))
	}
	labels := created[0].Config.Labels
	if v := labels["org.testcontainers.session-id"]; v != "s1" {
		t.Errorf("session label = %q, want s1, the value New was given", v)
	}
	if v := labels[docker.LabelOwner]; v != "owner1" {
		t.Errorf("%s = %q, want owner1", docker.LabelOwner, v)
	}
}
