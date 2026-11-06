package events

import (
	"maps"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/ruler"
)

// rulerSwitches holds a row for each EventBridge profile switch and no
// other, so no row outlives the switch it describes.
func TestRulerSwitchesHoldARowPerProfileSwitch(t *testing.T) {
	t.Parallel()
	meta := ruler.Switches(ruler.EventBridge{})
	names := make([]string, 0, len(meta))
	for _, sw := range meta {
		names = append(names, sw.Name)
	}
	slices.Sort(names)
	if rows := slices.Sorted(maps.Keys(rulerSwitches())); !slices.Equal(rows, names) {
		t.Errorf("rulerSwitches rows = %v, want the profile's switches %v", rows, names)
	}
}
