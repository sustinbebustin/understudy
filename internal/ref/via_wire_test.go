package ref_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/ref"
)

// Vias lists every declared Via in value order, so the web UI's label
// table, generated from it, names each one (ARCHITECTURE.md 17).
func TestViasListsEveryDeclaredVia(t *testing.T) {
	named, _ := declaredVias()
	if got := ref.Vias(); !slices.Equal(got, named) {
		t.Fatalf("Vias() = %v, want %v", got, named)
	}
}

// viaNames are the wire names every Via has had: journal files carry them
// in each internal call's origin (ADR 0007) and snapshots in stored causes
// (ADR 0009), so a file written by an earlier build must still read back.
// A value added later is appended after them.
var viaNames = []struct {
	v    ref.Via
	name string
}{
	{ref.SNSDelivery, "sns-delivery"},
	{ref.LambdaESM, "lambda-esm"},
	{ref.EventsTarget, "events-target"},
	{ref.S3Notification, "s3-notification"},
	{ref.LambdaLogs, "lambda-logs"},
	{ref.DynamoDBTTL, "dynamodb-ttl"},
	{ref.Timer, "timer"},
	{ref.Snapshot, "snapshot"},
	{ref.Admin, "admin"},
	{ref.KMSForward, "kms-forward"},
	{ref.STSAssumeRole, "sts-assume-role"},
	{ref.LambdaCode, "lambda-code"},
}

// declaredVias walks the values from the first until String falls back to
// its numeric form, as TestViaWireNamesRoundTrip does.
func declaredVias() (named []ref.Via, next ref.Via) {
	v := ref.SNSDelivery
	for ; !strings.HasPrefix(v.String(), "Via("); v++ {
		named = append(named, v)
	}
	return named, v
}

func TestViaWireNamesAreStable(t *testing.T) {
	named, next := declaredVias()
	if len(named) < len(viaNames) {
		t.Fatalf("declared values %v stop before the persisted ones end", named)
	}
	for i, c := range viaNames {
		if named[i] != c.v {
			t.Errorf("declared value %d is %d, want %v: persisted values keep their place", i, named[i], c.name)
		}
		if got := c.v.String(); got != c.name {
			t.Errorf("Via %d String() = %q, want %q", c.v, got, c.name)
		}
		if got, err := c.v.MarshalText(); err != nil || string(got) != c.name {
			t.Errorf("Via %d MarshalText() = %q, %v; want %q", c.v, got, err, c.name)
		}
		var back ref.Via
		if err := back.UnmarshalText([]byte(c.name)); err != nil || back != c.v {
			t.Errorf("UnmarshalText(%q) = %v, %v; want %v", c.name, back, err, c.v)
		}
	}
	// The value after the last declared one, an end marker or not, is no
	// Via: it neither marshals nor parses.
	if _, err := next.MarshalText(); err == nil {
		t.Errorf("MarshalText of %v, after the last declared value, succeeded", next)
	}
	if v, err := ref.ParseVia(next.String()); err == nil {
		t.Errorf("ParseVia(%q) = %v, want an error", next.String(), v)
	}
}

// The error lists every declared name, the last after "and"; for the
// persisted set that is the text understudy has always written.
func TestViaErrorListsTheKnownNames(t *testing.T) {
	_, err := ref.ParseVia("carrier-pigeon")
	if _, ok := errors.AsType[*ref.ViaError](err); !ok {
		t.Fatalf("ParseVia(carrier-pigeon) error = %v, want *ViaError", err)
	}
	named, _ := declaredVias()
	names := make([]string, 0, len(named))
	for _, v := range named {
		names = append(names, v.String())
	}
	want := `"carrier-pigeon" names no internal call mechanism; known names are ` +
		strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	if got := err.Error(); got != want {
		t.Errorf("error = %q\nwant    %q", got, want)
	}
	const persisted = "sns-delivery, lambda-esm, events-target, s3-notification, lambda-logs, dynamodb-ttl, timer, snapshot, admin, kms-forward, sts-assume-role"
	if !strings.Contains(err.Error(), "known names are "+persisted) {
		t.Errorf("error = %q, want it to list the persisted names in order", err)
	}
}
