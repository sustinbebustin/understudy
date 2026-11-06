package delivery_test

import (
	"errors"
	"strconv"
	"testing"

	"github.com/sustinbebustin/understudy/internal/delivery"
)

// A Lambda delivery carries a destination record or a dead letter, and a
// failure of each counts in its own CloudWatch metric:
// DestinationDeliveryFailures (lambda-destinations-layers.md 4.6) and
// DeadLetterErrors (5.4). The wire names are what snapshots store with a
// queued delivery.
func TestLambdaPayloadNamesAndMetrics(t *testing.T) {
	cases := []struct {
		p      delivery.LambdaPayload
		name   string
		metric string
	}{
		{delivery.DestinationRecord, "destination-record", "DestinationDeliveryFailures"},
		{delivery.DeadLetter, "dead-letter", "DeadLetterErrors"},
	}
	for _, c := range cases {
		if got := c.p.String(); got != c.name {
			t.Errorf("LambdaPayload(%d).String() = %q, want %q", c.p, got, c.name)
		}
		if got := c.p.Metric(); got != c.metric {
			t.Errorf("%s.Metric() = %q, want %q", c.name, got, c.metric)
		}
		text, err := c.p.MarshalText()
		if err != nil || string(text) != c.name {
			t.Errorf("%s.MarshalText() = %q, %v; want %q", c.name, text, err, c.name)
		}
		var back delivery.LambdaPayload
		if err := back.UnmarshalText([]byte(c.name)); err != nil || back != c.p {
			t.Errorf("UnmarshalText(%q) = %v, %v; want %v", c.name, back, err, c.p)
		}
	}
}

// An undeclared payload has no metric and no wire name, and a name no
// payload has is refused naming the known ones.
func TestLambdaPayloadRefusesUndeclaredValues(t *testing.T) {
	for _, p := range []delivery.LambdaPayload{0, delivery.DeadLetter + 1} {
		if got, want := p.String(), "LambdaPayload("+strconv.Itoa(int(p))+")"; got != want {
			t.Errorf("LambdaPayload(%d).String() = %q, want %q", p, got, want)
		}
		if got := p.Metric(); got != "" {
			t.Errorf("LambdaPayload(%d).Metric() = %q, want none", p, got)
		}
		if _, err := p.MarshalText(); !isPayloadError(err) {
			t.Errorf("LambdaPayload(%d).MarshalText() = %v, want a *LambdaPayloadError", p, err)
		}
	}
	var p delivery.LambdaPayload
	err := p.UnmarshalText([]byte("record"))
	if !isPayloadError(err) {
		t.Fatalf("UnmarshalText(record) = %v, want a *LambdaPayloadError", err)
	}
	if want := `"record" names no Lambda delivery payload; known names are destination-record and dead-letter`; err.Error() != want {
		t.Errorf("error = %q\nwant    %q", err, want)
	}
}

func isPayloadError(err error) bool {
	_, ok := errors.AsType[*delivery.LambdaPayloadError](err)
	return ok
}
