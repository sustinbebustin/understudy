package delivery_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/delivery"
)

// mechanismNames are the wire names of every mechanism, in value order:
// fault rules name them (docs/guide/faults.md), the journal writes them on
// each delivery (docs/guide/journal.md) and snapshots store them with
// queued deliveries (ADR 0009). A mechanism added later is appended after
// them.
var mechanismNames = []struct {
	m    delivery.Mechanism
	name string
}{
	{delivery.SNSToSQS, "sns-to-sqs"},
	{delivery.SNSToLambda, "sns-to-lambda"},
	{delivery.S3ToSQS, "s3-to-sqs"},
	{delivery.S3ToSNS, "s3-to-sns"},
	{delivery.S3ToLambda, "s3-to-lambda"},
	{delivery.EventsToLambda, "events-to-lambda"},
	{delivery.EventsToSQS, "events-to-sqs"},
	{delivery.EventsToSNS, "events-to-sns"},
	{delivery.EventsToEvents, "events-to-events"},
	{delivery.SNSToHTTP, "sns-to-http"},
	{delivery.LambdaToSQS, "lambda-to-sqs"},
	{delivery.LambdaToSNS, "lambda-to-sns"},
	{delivery.LambdaToLambda, "lambda-to-lambda"},
	{delivery.LambdaToEvents, "lambda-to-events"},
	{delivery.LambdaToS3, "lambda-to-s3"},
}

func TestMechanismWireNamesAreStable(t *testing.T) {
	all := delivery.Mechanisms()
	if len(all) < len(mechanismNames) {
		t.Fatalf("Mechanisms() = %v stops before the persisted ones end", all)
	}
	for i, c := range mechanismNames {
		if all[i] != c.m {
			t.Errorf("Mechanisms()[%d] = %v, want %s: persisted mechanisms keep their place", i, all[i], c.name)
		}
		if got := c.m.String(); got != c.name {
			t.Errorf("Mechanism %d String() = %q, want %q", c.m, got, c.name)
		}
		if got, err := c.m.MarshalText(); err != nil || string(got) != c.name {
			t.Errorf("Mechanism %d MarshalText() = %q, %v; want %q", c.m, got, err, c.name)
		}
		if got, err := delivery.ParseMechanism(c.name); err != nil || got != c.m {
			t.Errorf("ParseMechanism(%q) = %v, %v; want %v", c.name, got, err, c.m)
		}
	}
	// The value after the last declared one, an end marker or not, is no
	// mechanism.
	next := all[len(all)-1] + 1
	if !strings.HasPrefix(next.String(), "Mechanism(") {
		t.Errorf("String() of %d, after the last declared mechanism, = %q, want the numeric fallback", next, next.String())
	}
	if _, err := next.MarshalText(); err == nil {
		t.Errorf("MarshalText of %d, after the last declared mechanism, succeeded", next)
	}
}

// The error lists every declared name, the last after "and"; for the
// persisted set that is the text understudy has always written.
func TestMechanismErrorListsTheKnownNames(t *testing.T) {
	_, err := delivery.ParseMechanism("sqs-to-sns")
	if _, ok := errors.AsType[*delivery.MechanismError](err); !ok {
		t.Fatalf("ParseMechanism(sqs-to-sns) error = %v, want *MechanismError", err)
	}
	all := delivery.Mechanisms()
	names := make([]string, 0, len(all))
	for _, m := range all {
		names = append(names, m.String())
	}
	want := `"sqs-to-sns" names no delivery mechanism; known names are ` +
		strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	if got := err.Error(); got != want {
		t.Errorf("error = %q\nwant    %q", got, want)
	}
	const persisted = "sns-to-sqs, sns-to-lambda, s3-to-sqs, s3-to-sns, s3-to-lambda, events-to-lambda, events-to-sqs"
	if !strings.Contains(err.Error(), "known names are "+persisted) {
		t.Errorf("error = %q, want it to list the persisted names in order", err)
	}
}
