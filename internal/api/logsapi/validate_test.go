package logsapi

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
)

// TestTooManyLogEventsAreReportedWithoutAValue: AWS reports a list of
// structures over its length limit with no value
// (testdata/parity/logs/R10/06-10001-events.json).
func TestTooManyLogEventsAreReportedWithoutAValue(t *testing.T) {
	in := &PutLogEventsInput{LogGroupName: "g", LogStreamName: "s", LogEvents: make([]InputLogEvent, 10001)}
	for i := range in.LogEvents {
		in.LogEvents[i] = InputLogEvent{Timestamp: 1, Message: "m"}
	}
	const want = "1 validation error detected: Value at 'logEvents' failed to satisfy constraint: Member must have length less than or equal to 10000"
	if got := awserr.CoralMessage(in.Validate()); got != want {
		t.Fatalf("message = %s\nwant      %s", got, want)
	}
}
