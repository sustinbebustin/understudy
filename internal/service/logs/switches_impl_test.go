package logs

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/evidence"
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

// logs/R10 recorded a term found as a substring, with no word boundary:
// FilterLogEvents with the pattern ERROR (03-filter-01.json) listed
// exactly the messages of 02-put-log-events.json that contain ERROR, such
// as ERRORS, MY_ERROR and ERRORx, and not error. So
// logs.filter.term-boundary is aws-recorded at substring (ADR 0003,
// amendment 2026-11-13).
func TestTermBoundaryIsRecordedSubstring(t *testing.T) {
	t.Parallel()
	read := func(step string, into any) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "parity", "logs", "R10", step+".json"))
		if err != nil {
			t.Fatalf("read logs/R10/%s.json: %v", step, err)
		}
		var capture struct {
			Exchanges []struct {
				Request  struct{ Body string } `json:"request"`
				Response struct{ Body string } `json:"response"`
			} `json:"exchanges"`
		}
		if err := json.Unmarshal(raw, &capture); err != nil || len(capture.Exchanges) != 1 {
			t.Fatalf("logs/R10/%s.json holds no single exchange (%v)", step, err)
		}
		body := capture.Exchanges[0].Request.Body
		if step != "02-put-log-events" {
			body = capture.Exchanges[0].Response.Body
		}
		if err := json.Unmarshal([]byte(body), into); err != nil {
			t.Fatalf("logs/R10/%s.json body: %v", step, err)
		}
	}
	var put struct {
		LogEvents []struct{ Message string } `json:"logEvents"`
	}
	var listed struct {
		Events []struct{ Message string } `json:"events"`
	}
	read("02-put-log-events", &put)
	read("03-filter-01", &listed)
	// The service's filter options are the zero value New leaves them at.
	var s Service
	f, err := s.filter.Compile("ERROR")
	if err != nil {
		t.Fatalf("Compile(ERROR): %v", err)
	}
	var got []string
	for _, e := range put.LogEvents {
		if logsfilter.Match(f, e.Message) {
			got = append(got, e.Message)
		}
	}
	want := make([]string, 0, len(listed.Events))
	for _, e := range listed.Events {
		want = append(want, e.Message)
	}
	if !slices.Equal(got, want) {
		t.Errorf("ERROR matches %q, AWS listed %q", got, want)
	}
	sw := filterSwitches()["logs.filter.term-boundary"]
	if v := s.filter.TermBoundary.String(); v != "substring" || sw.label != evidence.AWSRecorded {
		t.Errorf("logs.filter.term-boundary = %q labelled %s, want substring, aws-recorded", v, sw.label)
	}
}
