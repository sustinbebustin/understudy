package docker_test

import (
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
)

func TestSweepRemovesOnlyThisOwnersEarlierInstances(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	ctx := t.Context()
	previous := labelMap(t, docker.Labels{Ownership: docker.Ownership{Instance: "old", Owner: "me"}})
	current := labelMap(t, docker.Labels{Ownership: docker.Ownership{Instance: "new", Owner: "me"}})
	stranger := labelMap(t, docker.Labels{Ownership: docker.Ownership{Instance: "other", Owner: "someone-else"}})

	leftRunning := create(t, c, "left-running", docker.Config{Labels: previous}, docker.HostConfig{})
	if err := c.ContainerStart(ctx, leftRunning); err != nil {
		t.Fatal(err)
	}
	leftStopped := create(t, c, "left-stopped", docker.Config{Labels: previous}, docker.HostConfig{})
	live := create(t, c, "live", docker.Config{Labels: current}, docker.HostConfig{})
	foreign := create(t, c, "foreign", docker.Config{Labels: stranger}, docker.HostConfig{})
	unmanaged := create(t, c, "unmanaged", docker.Config{}, docker.HostConfig{})
	oldNet, err := c.NetworkCreate(ctx, "old-net", docker.NetworkOptions{Labels: previous})
	if err != nil {
		t.Fatal(err)
	}
	foreignNet, err := c.NetworkCreate(ctx, "foreign-net", docker.NetworkOptions{Labels: stranger})
	if err != nil {
		t.Fatal(err)
	}
	liveNet, err := c.NetworkCreate(ctx, "live-net", docker.NetworkOptions{Labels: current})
	if err != nil {
		t.Fatal(err)
	}
	// An earlier instance's network that a sibling still uses stays.
	busyOldNet, err := c.NetworkCreate(ctx, "busy-old-net", docker.NetworkOptions{Labels: previous})
	if err != nil {
		t.Fatal(err)
	}
	sibling := create(t, c, "sibling", docker.Config{Labels: stranger}, docker.HostConfig{NetworkMode: "busy-old-net"})
	if err := c.ContainerStart(ctx, sibling); err != nil {
		t.Fatal(err)
	}

	swept, err := c.Sweep(ctx, docker.Ownership{Instance: "new", Owner: "me"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got := slices.Sorted(slices.Values(swept.Containers)); !slices.Equal(got, slices.Sorted(slices.Values([]string{leftRunning, leftStopped}))) {
		t.Errorf("swept containers = %v, want the previous instance's two", got)
	}
	if !slices.Equal(swept.Networks, []string{oldNet}) {
		t.Errorf("swept networks = %v, want [%s]", swept.Networks, oldNet)
	}
	for _, kept := range []string{live, foreign, unmanaged, sibling} {
		if _, ok := d.Container(kept); !ok {
			t.Errorf("container %s was swept", kept)
		}
	}
	for name, id := range map[string]string{
		"another owner's network":                    foreignNet,
		"the current instance's network":             liveNet,
		"an earlier instance's network still in use": busyOldNet,
	} {
		if _, err := c.NetworkInspect(ctx, id); err != nil {
			t.Errorf("%s was swept: %v", name, err)
		}
	}
}

func TestSweepRefusesAnIncompleteOwnership(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	labels := labelMap(t, docker.Labels{Ownership: docker.Ownership{Instance: "old", Owner: "me"}})
	leftover := create(t, c, "leftover", docker.Config{Labels: labels}, docker.HostConfig{})
	for _, own := range []docker.Ownership{{Owner: "me"}, {Instance: "new"}, {}} {
		swept, err := c.Sweep(t.Context(), own)
		if oe, ok := errors.AsType[*docker.OwnershipError](err); !ok || oe.Ownership != own {
			t.Errorf("Sweep(%+v) = %v, want an *OwnershipError naming it", own, err)
		}
		if len(swept.Containers) != 0 || len(swept.Networks) != 0 {
			t.Errorf("Sweep(%+v) swept %+v, want nothing", own, swept)
		}
	}
	if _, ok := d.Container(leftover); !ok {
		t.Error("a refused sweep removed a container")
	}
}

func TestLabelsMap(t *testing.T) {
	own := docker.Ownership{Instance: "a1b2c3", Owner: "data-4566"}
	cases := []struct {
		name   string
		labels docker.Labels
		want   map[string]string
	}{
		{
			name:   "ownership only",
			labels: docker.Labels{Ownership: own},
			want:   map[string]string{docker.LabelManaged: "true", docker.LabelInstance: "a1b2c3", docker.LabelOwner: "data-4566"},
		},
		{
			name:   "function environment in a test",
			labels: docker.Labels{Ownership: own, Function: &docker.FunctionLabels{Function: "worker", Account: "000000000000", Region: "us-east-1", Generation: 3}, Test: true},
			want: map[string]string{
				docker.LabelManaged: "true", docker.LabelInstance: "a1b2c3", docker.LabelOwner: "data-4566",
				docker.LabelFunction: "worker", docker.LabelAccount: "000000000000", docker.LabelRegion: "us-east-1",
				docker.LabelGeneration: "3", docker.LabelTest: "1",
			},
		},
	}
	for _, tc := range cases {
		if got, err := tc.labels.Map(); err != nil || !maps.Equal(got, tc.want) {
			t.Errorf("%s: Map() = %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
}

func TestLabelsMapRefusesAnIncompleteOwnership(t *testing.T) {
	for _, own := range []docker.Ownership{{Owner: "me"}, {Instance: "new"}, {}} {
		got, err := docker.Labels{Ownership: own, Test: true}.Map()
		if oe, ok := errors.AsType[*docker.OwnershipError](err); !ok || oe.Ownership != own || got != nil {
			t.Errorf("Labels{Ownership: %+v}.Map() = %v, %v; want no labels and an *OwnershipError naming it", own, got, err)
		}
	}
}
