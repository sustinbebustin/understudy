package delivery_test

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Mechanisms lists, in value order, exactly the values String gives a wire
// name and MarshalText accepts, so a mechanism added to the constants but
// not to the list is caught.
func TestMechanismsListsEveryDeclaredValue(t *testing.T) {
	var named, marshalled []delivery.Mechanism
	for v := range math.MaxUint8 + 1 {
		m := delivery.Mechanism(v)
		if !strings.HasPrefix(m.String(), "Mechanism(") {
			named = append(named, m)
		}
		if _, err := m.MarshalText(); err == nil {
			marshalled = append(marshalled, m)
		}
	}
	got := delivery.Mechanisms()
	if !slices.Equal(got, named) || !slices.Equal(got, marshalled) {
		t.Errorf("Mechanisms() = %v, want the values String names, %v, and MarshalText accepts, %v", got, named, marshalled)
	}
}

func TestMechanismWireNamesRoundTrip(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range delivery.Mechanisms() {
		name := m.String()
		if seen[name] {
			t.Errorf("two mechanisms share the name %q", name)
		}
		seen[name] = true
		got, err := delivery.ParseMechanism(name)
		if err != nil || got != m {
			t.Errorf("ParseMechanism(%q) = %v, %v; want %v", name, got, err, m)
		}
	}
	var target *delivery.MechanismError
	if _, err := delivery.ParseMechanism("sqs-to-sns"); !errors.As(err, &target) {
		t.Errorf("ParseMechanism(sqs-to-sns) error = %v, want *MechanismError", err)
	}
}

func TestMechanismJSON(t *testing.T) {
	type rule struct{ Mechanisms []delivery.Mechanism }
	data, err := json.Marshal(rule{Mechanisms: []delivery.Mechanism{delivery.SNSToSQS, delivery.S3ToLambda}})
	if err != nil || string(data) != `{"Mechanisms":["sns-to-sqs","s3-to-lambda"]}` {
		t.Fatalf("Marshal = %s, %v", data, err)
	}
	var out rule
	if err := json.Unmarshal(data, &out); err != nil || len(out.Mechanisms) != 2 || out.Mechanisms[1] != delivery.S3ToLambda {
		t.Fatalf("Unmarshal = %+v, %v", out, err)
	}
	if _, err := json.Marshal(rule{Mechanisms: []delivery.Mechanism{0}}); err == nil {
		t.Error("Marshal of the zero Mechanism succeeded")
	}
}

// The outbox persists a Request in snapshots, so the invocation type is
// written by its Lambda API name, not its declaration order.
func TestInvocationTypeJSON(t *testing.T) {
	type persisted struct{ Invocation delivery.InvocationType }
	for _, it := range []delivery.InvocationType{delivery.InvocationEvent, delivery.InvocationRequestResponse} {
		data, err := json.Marshal(persisted{Invocation: it})
		want := `{"Invocation":"` + it.String() + `"}`
		if err != nil || string(data) != want {
			t.Fatalf("Marshal(%v) = %s, %v; want %s", it, data, err, want)
		}
		var out persisted
		if err := json.Unmarshal(data, &out); err != nil || out.Invocation != it {
			t.Fatalf("Unmarshal(%s) = %v, %v; want %v", data, out.Invocation, err, it)
		}
	}
	if _, err := json.Marshal(delivery.InvocationType(0)); err == nil {
		t.Error("Marshal of the zero InvocationType succeeded, want an error")
	}
	var out delivery.InvocationType
	var target *delivery.InvocationTypeError
	if err := json.Unmarshal([]byte(`"DryRun"`), &out); !errors.As(err, &target) {
		t.Errorf("Unmarshal(DryRun) error = %v, want *InvocationTypeError", err)
	}
}

func TestMechanismVia(t *testing.T) {
	want := map[delivery.Mechanism]ref.Via{
		delivery.SNSToSQS:       ref.SNSDelivery,
		delivery.SNSToLambda:    ref.SNSDelivery,
		delivery.S3ToSQS:        ref.S3Notification,
		delivery.S3ToSNS:        ref.S3Notification,
		delivery.S3ToLambda:     ref.S3Notification,
		delivery.EventsToLambda: ref.EventsTarget,
		delivery.EventsToSQS:    ref.EventsTarget,
		delivery.EventsToSNS:    ref.EventsTarget,
	}
	for _, m := range delivery.Mechanisms() {
		if got := m.Via(); got != want[m] {
			t.Errorf("%v.Via() = %v, want %v", m, got, want[m])
		}
	}
}

func TestMechanismTarget(t *testing.T) {
	want := map[delivery.Mechanism]ref.ServiceID{
		delivery.SNSToSQS:       "sqs",
		delivery.SNSToLambda:    "lambda",
		delivery.S3ToSQS:        "sqs",
		delivery.S3ToSNS:        "sns",
		delivery.S3ToLambda:     "lambda",
		delivery.EventsToLambda: "lambda",
		delivery.EventsToSQS:    "sqs",
		delivery.EventsToSNS:    "sns",
	}
	for _, m := range delivery.Mechanisms() {
		if got := m.Target(); got != want[m] {
			t.Errorf("%v.Target() = %q, want %q", m, got, want[m])
		}
	}
}

func TestInvocationTypeNames(t *testing.T) {
	if delivery.InvocationEvent.String() != "Event" || delivery.InvocationRequestResponse.String() != "RequestResponse" {
		t.Errorf("InvocationType names = %s, %s", delivery.InvocationEvent, delivery.InvocationRequestResponse)
	}
}

// The wire DataType is the base type, then "." and the custom label when
// there is one (SQS and SNS message attributes: "Number.float").
func TestAttributeDataType(t *testing.T) {
	tests := []struct {
		v    delivery.AttributeValue
		want string
	}{
		{delivery.StringAttribute{Value: "a"}, "String"},
		{delivery.StringAttribute{Label: "uuid", Value: "a"}, "String.uuid"},
		{delivery.NumberAttribute{Value: "1.5"}, "Number"},
		{delivery.NumberAttribute{Label: "float", Value: "1.5"}, "Number.float"},
		{delivery.BinaryAttribute{Value: []byte{0}}, "Binary"},
		{delivery.BinaryAttribute{Label: "gzip", Value: []byte{0}}, "Binary.gzip"},
	}
	for _, tt := range tests {
		if got := tt.v.DataType(); got != tt.want {
			t.Errorf("%#v.DataType() = %q, want %q", tt.v, got, tt.want)
		}
	}
}
