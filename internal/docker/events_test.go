package docker_test

import (
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
)

func TestEventsDeliversFilteredDieAndOOMEvents(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	ctx := t.Context()
	// From the start of the fresh daemon, so no event depends on when the
	// daemon subscribes; the filters alone select.
	stream, err := c.Events(ctx, docker.EventsOptions{Since: time.Unix(0, 1), Filters: docker.Filters{
		"type":  {"container"},
		"event": {"die", "oom"},
		"label": {docker.LabelManaged + "=true"},
	}})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	defer stream.Close()

	unmanaged := create(t, c, "unmanaged", docker.Config{}, docker.HostConfig{})
	if err := c.ContainerStart(ctx, unmanaged); err != nil {
		t.Fatal(err)
	}
	d.Exit(unmanaged, 1)
	d.SetProgram(providedImage, dockertest.Exit{Code: 137, OOMKilled: true})
	managed := create(t, c, "managed", docker.Config{Labels: labelMap(t, docker.Labels{Ownership: docker.Ownership{Instance: "i", Owner: "o"}})}, docker.HostConfig{})
	if err := c.ContainerStart(ctx, managed); err != nil {
		t.Fatal(err)
	}

	var got []docker.Event
	for len(got) < 2 {
		e, err := stream.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		got = append(got, e)
	}
	if got[0].Action != "oom" || got[1].Action != "die" || got[1].Actor.ID != managed || got[1].Actor.Attributes["exitCode"] != "137" {
		t.Errorf("events = %+v, want oom then die with exitCode 137 for the managed container only", got)
	}
	if got[1].Actor.Attributes["com.amazonaws.lambda.platform.kernel"] != "k6.1" {
		t.Errorf("die attributes = %v, want the image's labels merged in", got[1].Actor.Attributes)
	}
}

// The daemon may subscribe after Events returns, so a watcher passes a Since
// before the action it watches; earlier events stay out.
func TestEventsSinceBeforeTheWatchedActionDeliversOnlyThatAction(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	ctx := t.Context()
	create(t, c, "before", docker.Config{}, docker.HostConfig{})
	past := d.Events()
	since := time.Unix(0, past[len(past)-1].TimeNano+1)
	stream, err := c.Events(ctx, docker.EventsOptions{Filters: docker.Filters{"event": {"create"}}, Since: since})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	after := create(t, c, "after", docker.Config{}, docker.HostConfig{})
	if e, err := stream.Next(); err != nil || e.Actor.ID != after {
		t.Errorf("first event = %+v, %v; want the create after Since, not the one before it", e, err)
	}
}

func TestEventsSinceReplaysFromTheGivenInstant(t *testing.T) {
	_, c := daemonWithImage(t, dockertest.Options{})
	ctx := t.Context()
	first := create(t, c, "first", docker.Config{}, docker.HostConfig{})
	second := create(t, c, "second", docker.Config{}, docker.HostConfig{})
	stream, err := c.Events(ctx, docker.EventsOptions{Filters: docker.Filters{"event": {"create"}}, Since: time.Unix(0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	e, err := stream.Next()
	if closeErr := stream.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || e.Actor.ID != first {
		t.Fatalf("first replayed event = %+v, %v; want the first create", e, err)
	}
	stream, err = c.Events(ctx, docker.EventsOptions{Filters: docker.Filters{"event": {"create"}}, Since: time.Unix(0, e.TimeNano+1)})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if e, err := stream.Next(); err != nil || e.Actor.ID != second {
		t.Errorf("event after reconnecting since the first = %+v, %v; want the second create", e, err)
	}
}
