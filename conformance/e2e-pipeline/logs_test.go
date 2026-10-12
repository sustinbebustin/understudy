package e2e_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

// logLine is one JSON line a worker prints or writes.
type logLine struct {
	Event        string `json:"event"`
	ID           string `json:"id"`
	MessageID    string `json:"messageId"`
	Function     string `json:"function"`
	ReceiveCount string `json:"receiveCount"`
}

// lambdaLogGroup is the group Lambda writes function fn's output to.
func lambdaLogGroup(fn string) string { return "/aws/lambda/" + fn }

// filterLines runs FilterLogEvents over every page and decodes each match
// as a worker log line.
func filterLines(t *testing.T, group, pattern string) ([]logLine, []logstypes.FilteredLogEvent) {
	t.Helper()
	var lines []logLine
	var events []logstypes.FilteredLogEvent
	pages := cloudwatchlogs.NewFilterLogEventsPaginator(env.logs, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(group), FilterPattern: aws.String(pattern),
	})
	for pages.HasMorePages() {
		out, err := pages.NextPage(t.Context())
		if err != nil {
			t.Fatalf("logs FilterLogEvents %s %q: %v", group, pattern, err)
		}
		for _, e := range out.Events {
			var l logLine
			if err := json.Unmarshal([]byte(strings.TrimSpace(aws.ToString(e.Message))), &l); err != nil {
				t.Errorf("FilterLogEvents %q matched %q, which is not a JSON line", pattern, aws.ToString(e.Message))
				continue
			}
			lines = append(lines, l)
			events = append(events, e)
		}
	}
	return lines, events
}

// waitLines waits until pattern matches at least n events in group.
func waitLines(t *testing.T, group, pattern string, n int) []logLine {
	t.Helper()
	var lines []logLine
	eventually(t, fmt.Sprintf("%d log events in %s match %s", n, group, pattern), func() (bool, string) {
		lines, _ = filterLines(t, group, pattern)
		return len(lines) >= n, fmt.Sprintf("%d matched", len(lines))
	})
	return lines
}

// eventPattern is the JSON filter pattern for worker lines with event and id.
func eventPattern(event, id string) string {
	return fmt.Sprintf(`{ ($.event = %q) && ($.id = %q) }`, event, id)
}

// TestLogsLambdaOutputLandsInCloudWatch finds the Go worker's line for one
// message with a JSON filter pattern, then reads its stream with
// GetLogEvents: the line sits between the START and END lines Lambda
// writes for the invocation, in a stream named after the date, version and
// environment (logs-filter-patterns.md, secrets-logs-events-alarms.md).
func TestLogsLambdaOutputLandsInCloudWatch(t *testing.T) {
	pipe(t)
	id := newID(t)
	publish(t, message{ID: id}, map[string]string{"kind": "order"})
	waitProcessed(t, goWorker, id)
	waitLines(t, lambdaLogGroup(goWorker), eventPattern("processed", id), 1)
	lines, events := filterLines(t, lambdaLogGroup(goWorker), eventPattern("processed", id))
	if len(lines) != 1 {
		t.Fatalf("FilterLogEvents matched %d processed lines for %s, want exactly 1", len(lines), id)
	}
	stream := aws.ToString(events[0].LogStreamName)
	if !regexp.MustCompile(`^\d{4}/\d{2}/\d{2}/\[\$LATEST\][0-9a-f]+$`).MatchString(stream) {
		t.Errorf("log stream %q, want YYYY/MM/DD/[$LATEST]<hex id>", stream)
	}
	out, err := env.logs.GetLogEvents(t.Context(), &cloudwatchlogs.GetLogEventsInput{
		LogGroupName: aws.String(lambdaLogGroup(goWorker)), LogStreamName: aws.String(stream), StartFromHead: aws.Bool(true),
	})
	if err != nil {
		t.Fatalf("logs GetLogEvents %s: %v", stream, err)
	}
	start, line, end := -1, -1, -1
	for i, e := range out.Events {
		msg := aws.ToString(e.Message)
		switch {
		case strings.HasPrefix(msg, "START RequestId: "):
			start = i
		case strings.Contains(msg, `"id":"`+id+`"`) && strings.Contains(msg, `"processed"`):
			line = i
		case strings.HasPrefix(msg, "END RequestId: ") && line >= 0 && end < 0:
			end = i
		}
		if line >= 0 && end >= 0 {
			break
		}
	}
	if start < 0 || line < start || end < line {
		t.Errorf("stream %s: START at %d, the processed line at %d, END at %d; want START < line < END", stream, start, line, end)
	}
}

// TestLogsPerTraceStream reads the stream the Go worker created for one
// message in its own log group, with GetLogEvents and with a term filter
// restricted to that stream.
func TestLogsPerTraceStream(t *testing.T) {
	pipe(t)
	id := newID(t)
	publish(t, message{ID: id}, map[string]string{"kind": "order"})
	rec := waitProcessed(t, goWorker, id)
	stream := "trace-" + id
	out, err := env.logs.GetLogEvents(t.Context(), &cloudwatchlogs.GetLogEventsInput{
		LogGroupName: aws.String(traceLogGroup), LogStreamName: aws.String(stream), StartFromHead: aws.Bool(true),
	})
	if err != nil {
		t.Fatalf("logs GetLogEvents %s %s: %v", traceLogGroup, stream, err)
	}
	if len(out.Events) != 1 {
		t.Fatalf("stream %s holds %d events, want 1", stream, len(out.Events))
	}
	var l logLine
	if err := json.Unmarshal([]byte(aws.ToString(out.Events[0].Message)), &l); err != nil || l.Event != "traced" || l.MessageID != rec.Record.MessageID {
		t.Errorf("stream %s event %q, want the traced line for message %s", stream, aws.ToString(out.Events[0].Message), rec.Record.MessageID)
	}
	filtered, err := env.logs.FilterLogEvents(t.Context(), &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(traceLogGroup), LogStreamNames: []string{stream}, FilterPattern: aws.String(`"traced"`),
	})
	if err != nil {
		t.Fatalf("logs FilterLogEvents %s: %v", stream, err)
	}
	if len(filtered.Events) != 1 || aws.ToString(filtered.Events[0].LogStreamName) != stream {
		t.Errorf("FilterLogEvents on stream %s matched %d events, want the one traced line", stream, len(filtered.Events))
	}
}

// TestLogsPythonWorkerOutput finds the Python worker's line for a message
// it processed.
func TestLogsPythonWorkerOutput(t *testing.T) {
	pipe(t)
	id := newID(t)
	publish(t, message{ID: id, Priority: 8}, nil)
	advance(t, pyWindow())
	rec := waitProcessed(t, pyWorker, id)
	lines := waitLines(t, lambdaLogGroup(pyWorker), eventPattern("processed", id), 1)
	if len(lines) != 1 || lines[0].MessageID != rec.Record.MessageID || lines[0].Function != pyWorker {
		t.Errorf("py-worker processed lines for %s: %+v, want one for message %s", id, lines, rec.Record.MessageID)
	}
}
