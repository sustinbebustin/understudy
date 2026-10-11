package ref_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/sustinbebustin/understudy/internal/ref"
)

func TestViaWireNamesRoundTrip(t *testing.T) {
	all := []ref.Via{
		ref.SNSDelivery, ref.LambdaESM, ref.EventsTarget, ref.S3Notification, ref.LambdaLogs,
		ref.DynamoDBTTL, ref.Timer, ref.Snapshot, ref.Admin, ref.KMSForward, ref.STSAssumeRole,
	}
	seen := map[string]bool{}
	for _, v := range all {
		name := v.String()
		if seen[name] {
			t.Errorf("%d and another Via share the name %q", v, name)
		}
		seen[name] = true
		got, err := ref.ParseVia(name)
		if err != nil || got != v {
			t.Errorf("ParseVia(%q) = %v, %v; want %v", name, got, err, v)
		}
	}
}

func TestViaJSON(t *testing.T) {
	data, err := json.Marshal(ref.Internal{Via: ref.KMSForward})
	if err != nil || string(data) != `{"Via":"kms-forward"}` {
		t.Fatalf("Marshal = %s, %v", data, err)
	}
	var out ref.Internal
	if err := json.Unmarshal(data, &out); err != nil || out.Via != ref.KMSForward {
		t.Fatalf("Unmarshal = %+v, %v", out, err)
	}
	if _, err := json.Marshal(ref.Internal{}); err == nil {
		t.Error("Marshal of the zero Via succeeded, want an error")
	}
	var target *ref.ViaError
	if err := json.Unmarshal([]byte(`{"Via":"carrier-pigeon"}`), &out); !errors.As(err, &target) {
		t.Errorf("Unmarshal of an unknown name: error = %v, want *ViaError", err)
	}
}

func TestListenerKindJSON(t *testing.T) {
	for _, k := range []ref.ListenerKind{ref.ListenerMain, ref.ListenerContainer} {
		data, err := json.Marshal(k)
		if err != nil || string(data) != `"`+k.String()+`"` {
			t.Fatalf("Marshal(%v) = %s, %v", k, data, err)
		}
		var out ref.ListenerKind
		if err := json.Unmarshal(data, &out); err != nil || out != k {
			t.Fatalf("Unmarshal(%s) = %v, %v; want %v", data, out, err, k)
		}
	}
	if _, err := json.Marshal(ref.ListenerKind(0)); err == nil {
		t.Error("Marshal of the zero ListenerKind succeeded, want an error")
	}
	var out ref.ListenerKind
	var target *ref.ListenerKindError
	if err := json.Unmarshal([]byte(`"sidecar"`), &out); !errors.As(err, &target) {
		t.Errorf("Unmarshal of an unknown name: error = %v, want *ListenerKindError", err)
	}
}

func TestOperationIDString(t *testing.T) {
	op := ref.OperationID{Service: "sqs", Name: "SendMessage"}
	if got := op.String(); got != "sqs.SendMessage" {
		t.Errorf("String() = %q", got)
	}
}
