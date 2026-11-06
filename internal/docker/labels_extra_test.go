package docker_test

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
)

// Extra labels are the user's, added beside understudy's own so a
// Testcontainers reaper selecting its session labels also removes
// understudy's children; keys under understudy.dev/ stay understudy's
// (testcontainers.md 9.3, 8.6).

// reserved is the prefix of every label understudy sets itself
// (testcontainers.md 9.3; containers.md 9.1).
const reserved = "understudy.dev/"

// sessionLabels are labels a Testcontainers session puts on what it starts:
// tc-python's and tc-node's session label (testcontainers.md 5.4, 6.4) and
// tc-go's camel-case one (testcontainers.md 4.5).
func sessionLabels() map[string]string {
	return map[string]string{
		"org.testcontainers":            "true",
		"org.testcontainers.session-id": "6f1b8c2e-1d7a-4c39-9a51-0d2f3b4c5e6f",
		"org.testcontainers.sessionId":  "0d2f3b4c5e6f6f1b8c2e1d7a4c399a51",
	}
}

func TestReservedPrefixCoversEveryOwnLabel(t *testing.T) {
	if docker.ReservedPrefix != reserved {
		t.Errorf("ReservedPrefix = %q, want %q (testcontainers.md 9.3)", docker.ReservedPrefix, reserved)
	}
	for _, k := range []string{
		docker.LabelManaged, docker.LabelInstance, docker.LabelOwner, docker.LabelFunction,
		docker.LabelAccount, docker.LabelRegion, docker.LabelGeneration, docker.LabelTest,
	} {
		if !strings.HasPrefix(k, docker.ReservedPrefix) {
			t.Errorf("own label %q is outside ReservedPrefix %q, so a user could set it", k, docker.ReservedPrefix)
		}
	}
}

func TestLabelsMapAddsExtraBesideTheOwnLabels(t *testing.T) {
	own := docker.Ownership{Instance: "a1b2c3", Owner: "data-4566"}
	extra := sessionLabels()
	extra["com.example/team"] = "payments"
	before := maps.Clone(extra)
	got, err := docker.Labels{Ownership: own, Test: true, Extra: extra}.Map()
	if err != nil {
		t.Fatalf("Map() = %v", err)
	}
	want := maps.Clone(extra)
	maps.Copy(want, map[string]string{docker.LabelManaged: "true", docker.LabelInstance: "a1b2c3", docker.LabelOwner: "data-4566", docker.LabelTest: "1"})
	if !maps.Equal(got, want) {
		t.Errorf("Map() = %v, want the own labels plus every extra label: %v", got, want)
	}
	if !maps.Equal(extra, before) {
		t.Errorf("Map() changed Extra to %v, want it left as %v", extra, before)
	}
}

func TestLabelsMapRefusesAReservedExtraKey(t *testing.T) {
	own := docker.Ownership{Instance: "a1b2c3", Owner: "data-4566"}
	for _, key := range []string{docker.LabelManaged, docker.LabelOwner, reserved + "custom", reserved} {
		extra := sessionLabels()
		extra[key] = "x"
		got, err := docker.Labels{Ownership: own, Extra: extra}.Map()
		if re, ok := errors.AsType[*docker.ReservedLabelError](err); !ok || re.Key != key || got != nil {
			t.Errorf("Map() with extra key %q = %v, %v; want no labels and a *ReservedLabelError naming it", key, got, err)
		}
	}
	// The ownership is checked first, as before Extra existed.
	got, err := docker.Labels{Extra: map[string]string{docker.LabelManaged: "false"}}.Map()
	if _, ok := errors.AsType[*docker.OwnershipError](err); !ok || got != nil {
		t.Errorf("Map() with no ownership and a reserved extra key = %v, %v; want an *OwnershipError", got, err)
	}
}

func TestCheckExtra(t *testing.T) {
	accepted := []map[string]string{
		nil,
		{},
		sessionLabels(),
		// The prefix is understudy.dev/ with its slash, matched case-sensitively.
		{"understudy.dev": "x", "understudy.devx/a": "x", "Understudy.dev/a": "x", "io.understudy.dev/a": "x"},
	}
	for _, extra := range accepted {
		if err := docker.CheckExtra(extra); err != nil {
			t.Errorf("CheckExtra(%v) = %v, want nil", extra, err)
		}
	}
	// The first reserved key in sorted order is the one named.
	extra := map[string]string{reserved + "zeta": "", "com.example/team": "payments", reserved + "alpha": "", reserved + "mid": ""}
	if re, ok := errors.AsType[*docker.ReservedLabelError](docker.CheckExtra(extra)); !ok || re.Key != reserved+"alpha" {
		t.Errorf("CheckExtra(%v) = %v, want a *ReservedLabelError naming %salpha", extra, re, reserved)
	}
}

// The refusal reads as testcontainers.md 9.3 words it.
func TestReservedLabelErrorText(t *testing.T) {
	err := &docker.ReservedLabelError{Key: "understudy.dev/managed"}
	const want = `label key "understudy.dev/managed" is reserved for understudy's own labels; choose a key outside understudy.dev/`
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// CheckExtra refuses a map exactly when a key is under understudy.dev/,
// naming the first such key in sorted order, and Map agrees with it: an
// accepted map is added whole beside the own labels, which it cannot
// replace.
func TestCheckExtraRefusesExactlyTheReservedKeys(t *testing.T) {
	own := docker.Ownership{Instance: "i1", Owner: "o1"}
	ownMap := map[string]string{docker.LabelManaged: "true", docker.LabelInstance: "i1", docker.LabelOwner: "o1"}
	keys := rapid.OneOf(
		rapid.String(),
		rapid.StringMatching(`[a-z][a-z0-9.\-/]{0,15}`),
		rapid.StringMatching(`understudy\.dev/[a-z]{0,6}`),
		rapid.SampledFrom([]string{"understudy.dev", "Understudy.dev/x", "org.testcontainers.session-id", docker.LabelManaged}),
	)
	rapid.Check(t, func(t *rapid.T) {
		extra := rapid.MapOf(keys, rapid.String()).Draw(t, "extra")
		var bad []string
		for k := range extra {
			if strings.HasPrefix(k, reserved) {
				bad = append(bad, k)
			}
		}
		err := docker.CheckExtra(extra)
		got, merr := docker.Labels{Ownership: own, Extra: extra}.Map()
		if len(bad) == 0 {
			if err != nil || merr != nil {
				t.Fatalf("CheckExtra = %v, Map = %v; want both nil for %v", err, merr, extra)
			}
			want := maps.Clone(extra)
			maps.Copy(want, ownMap)
			if !maps.Equal(got, want) {
				t.Fatalf("Map() = %v, want %v", got, want)
			}
			return
		}
		first := slices.Min(bad)
		if re, ok := errors.AsType[*docker.ReservedLabelError](err); !ok || re.Key != first {
			t.Fatalf("CheckExtra(%v) = %v, want a *ReservedLabelError naming %q", extra, err, first)
		}
		if re, ok := errors.AsType[*docker.ReservedLabelError](merr); !ok || re.Key != first || got != nil {
			t.Fatalf("Map() = %v, %v; want no labels and a *ReservedLabelError naming %q", got, merr, first)
		}
	})
}

// Sweeps select only understudy.dev/ labels, so extra labels change nothing
// a sweep removes (testcontainers.md 9.3, "Sweep unaffected").
func TestSweepIgnoresExtraLabels(t *testing.T) {
	d, c := daemonWithImage(t, dockertest.Options{})
	ctx := t.Context()
	previous := labelMap(t, docker.Labels{Ownership: docker.Ownership{Instance: "old", Owner: "me"}, Extra: sessionLabels()})
	stranger := labelMap(t, docker.Labels{Ownership: docker.Ownership{Instance: "other", Owner: "someone-else"}, Extra: sessionLabels()})
	leftover := create(t, c, "leftover", docker.Config{Labels: previous}, docker.HostConfig{})
	foreign := create(t, c, "foreign", docker.Config{Labels: stranger}, docker.HostConfig{})
	sessionOnly := create(t, c, "session-only", docker.Config{Labels: sessionLabels()}, docker.HostConfig{})

	swept, err := c.Sweep(ctx, docker.Ownership{Instance: "new", Owner: "me"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if !slices.Equal(swept.Containers, []string{leftover}) {
		t.Errorf("swept containers = %v, want only the earlier instance's %s", swept.Containers, leftover)
	}
	for _, kept := range []string{foreign, sessionOnly} {
		if _, ok := d.Container(kept); !ok {
			t.Errorf("container %s was swept", kept)
		}
	}
}
