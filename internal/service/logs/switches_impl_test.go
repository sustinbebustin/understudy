package logs

import (
	"maps"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/logsfilter"
)

// AWS answers x-amzn-log-group-authz: outdated on each group-level action
// it allowed through a log-group:<name>:* statement, and not on the
// stream-level ones (testdata/parity/logs/IAM-ENF/42-wild-*.json;
// secrets-logs-events-alarms.md 3.10). understudy does not send the
// header, so a switch names the deviation on every recorded operation.
func TestOutdatedAuthzHeaderSwitchNamesTheRecordedGroupActions(t *testing.T) {
	t.Parallel()
	sw := outdatedAuthzHeader()
	if sw.Value != "not-sent" || sw.Record != "IAM-ENF-42" {
		t.Errorf("switch %s = %q record %q, want not-sent recorded by IAM-ENF-42", sw.Name, sw.Value, sw.Record)
	}
	want := []string{"logs.CreateLogGroup", "logs.DeleteLogGroup", "logs.DescribeLogStreams", "logs.FilterLogEvents", "logs.PutRetentionPolicy"}
	if !slices.Equal(sw.Ops, want) {
		t.Errorf("switch %s ops = %v, want the group-level steps of IAM-ENF-42 %v", sw.Name, sw.Ops, want)
	}
}

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
