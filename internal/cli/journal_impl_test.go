package cli

import (
	"encoding/json"
	"testing"
)

// An HTTP delivery has no target ARN; its summary names the endpoint and
// the reason it was refused.
func TestDeliverySummaryNamesTheEndpoint(t *testing.T) {
	detail := json.RawMessage(`{"from":"arn:aws:sns:us-east-1:000000000000:events","to":"","endpoint":"127.0.0.1:8080","mechanism":"sns-to-sqs","attempt":1,"outcome":{"kind":"refused","reason":"no entry allows it"}}`)
	got, ok := deliverySummary(detail)
	want := "sns-to-sqs events -> 127.0.0.1:8080 attempt 1 refused: no entry allows it"
	if !ok || got != want {
		t.Errorf("deliverySummary = %q, %t; want %q", got, ok, want)
	}
}
