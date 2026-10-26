package logs

import (
	"maps"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/logsfilter"
)

// filterSwitches holds a row for each logsfilter switch and no other, so no
// row outlives the switch it describes.
func TestFilterSwitchesHoldARowPerLogsfilterSwitch(t *testing.T) {
	t.Parallel()
	meta := logsfilter.Options{}.Switches()
	names := make([]string, 0, len(meta))
	for _, sw := range meta {
		names = append(names, sw.Name)
	}
	slices.Sort(names)
	if rows := slices.Sorted(maps.Keys(filterSwitches())); !slices.Equal(rows, names) {
		t.Errorf("filterSwitches rows = %v, want logsfilter's switches %v", rows, names)
	}
}
