package filterpolicy

import (
	"maps"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/ruler"
)

// switchTable holds a row for each SNS profile switch and no other, so no
// row outlives the switch it describes.
func TestSwitchTableHoldsARowPerProfileSwitch(t *testing.T) {
	t.Parallel()
	meta := ruler.Switches(ruler.SNS{})
	names := make([]string, 0, len(meta))
	for _, sw := range meta {
		names = append(names, sw.Name)
	}
	slices.Sort(names)
	if rows := slices.Sorted(maps.Keys(switchTable())); !slices.Equal(rows, names) {
		t.Errorf("switchTable rows = %v, want the profile's switches %v", rows, names)
	}
}
