package ddblocal_test

import (
	"errors"
	"maps"
	"testing"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
)

// Labels.Extra goes on the DynamoDB Local container beside understudy's
// own labels, so a Testcontainers reaper selecting its session labels
// removes it with the session (testcontainers.md 9.3, 8.6; finding 3).

// sessionLabels are tc-python's and tc-node's session label
// (testcontainers.md 5.4, 6.4) and tc-go's (testcontainers.md 4.5).
func sessionLabels() map[string]string {
	return map[string]string{
		"org.testcontainers":            "true",
		"org.testcontainers.session-id": "6f1b8c2e-1d7a-4c39-9a51-0d2f3b4c5e6f",
		"org.testcontainers.sessionId":  "0d2f3b4c5e6f6f1b8c2e1d7a4c399a51",
	}
}

func TestTheContainerCarriesTheExtraLabels(t *testing.T) {
	d := daemon(t)
	d.AddImage(hubRef, docker.Image{ID: ddblocal.Digest})
	d.SetProgram(hubRef, dockertest.Exit{Code: 1})
	m, err := ddblocal.New(ddblocal.Config{
		Docker: d.Client(t),
		Labels: docker.Labels{Ownership: docker.Ownership{Instance: "inst1", Owner: "owner1"}, Test: true, Extra: sessionLabels()},
	})
	if err != nil {
		t.Fatalf("New with session labels = %v", err)
	}
	t.Cleanup(func() {
		if err := m.Stop(t.Context()); err != nil {
			t.Error(err)
		}
	})
	if !errorIs[*ddblocal.ExitedError](m.Start(t.Context())) {
		t.Fatal("Start did not run the container")
	}
	created := d.Created()
	if len(created) != 1 {
		t.Fatalf("created %d containers, want 1", len(created))
	}
	want := sessionLabels()
	maps.Copy(want, map[string]string{docker.LabelManaged: "true", docker.LabelInstance: "inst1", docker.LabelOwner: "owner1", docker.LabelTest: "1"})
	for k, v := range want {
		if got := created[0].Config.Labels[k]; got != v {
			t.Errorf("label %s = %q, want %q", k, got, v)
		}
	}
}

func TestNewRefusesAReservedExtraLabel(t *testing.T) {
	d := daemon(t)
	for _, key := range []string{docker.LabelOwner, docker.LabelTest, "understudy.dev/anything"} {
		extra := sessionLabels()
		extra[key] = "x"
		m, err := ddblocal.New(ddblocal.Config{
			Docker: d.Client(t),
			Labels: docker.Labels{Ownership: docker.Ownership{Instance: "inst1", Owner: "owner1"}, Extra: extra},
		})
		if re, ok := errors.AsType[*docker.ReservedLabelError](err); !ok || re.Key != key || m != nil {
			t.Errorf("New with extra label %q: manager returned %v, error %v; want no manager and an error holding a *docker.ReservedLabelError naming it", key, m != nil, err)
		}
	}
	if created := d.Created(); len(created) != 0 {
		t.Errorf("a refused New created %d containers", len(created))
	}
}
