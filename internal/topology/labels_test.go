package topology_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// The Docker Desktop probe is a container understudy creates, so it carries
// the extra labels as every other one does (testcontainers.md 9.3).
func TestDetectDesktopProbeCarriesTheExtraLabels(t *testing.T) {
	d := desktopProbing(t, ahostsv4("192.168.65.254"))
	labels := probeLabels()
	labels.Extra = map[string]string{"org.testcontainers": "true", "org.testcontainers.session-id": "6f1b8c2e-1d7a-4c39-9a51-0d2f3b4c5e6f"}
	if _, err := new(topology.Detector).Detect(t.Context(), d.Client(t), topology.Options{ProbeImage: probeImage, ProbeLabels: labels, Root: onHost()}); err != nil {
		t.Fatal(err)
	}
	probes := d.Created()
	if len(probes) != 1 {
		t.Fatalf("daemon recorded %d created containers, want the one probe", len(probes))
	}
	want := map[string]string{
		"org.testcontainers": "true", "org.testcontainers.session-id": "6f1b8c2e-1d7a-4c39-9a51-0d2f3b4c5e6f",
		docker.LabelManaged: "true", docker.LabelOwner: "o", docker.LabelInstance: "i",
	}
	for k, v := range want {
		if got := probes[0].Config.Labels[k]; got != v {
			t.Errorf("probe label %s = %q, want %q", k, got, v)
		}
	}
}
