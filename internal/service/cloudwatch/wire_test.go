package cloudwatch_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/protocol/awsquery"
	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
	"github.com/sustinbebustin/understudy/internal/protocol/rpcv2cbor"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/registry"
)

const requestID = "00000000-0000-0000-0000-000000000000"

// encodeCBOR renders out, op's output, as the rpcv2Cbor codec answers.
func encodeCBOR(t *testing.T, op *registry.Operation, out smithy.Serializable) rpcv2cbor.Response {
	t.Helper()
	resp, err := rpcv2cbor.Codec{}.Encode(op, requestID, out, op.SuccessCode)
	if err != nil {
		t.Fatalf("encode %s: %s", op.ID, err.Message)
	}
	return resp
}

// encodeQuery renders out, op's output, as the awsQuery codec answers.
func encodeQuery(t *testing.T, op *registry.Operation, out smithy.Serializable) string {
	t.Helper()
	resp, err := awsquery.Codec{}.Encode(op, requestID, out, op.SuccessCode)
	if err != nil {
		t.Fatalf("encode %s: %s", op.ID, err.Message)
	}
	return string(resp.Body)
}

// member is the value of key in m, or nil when m has no such key.
func member(m cbor.Map, key string) cbor.Value {
	for _, e := range m {
		if e.Key == key {
			return e.Value
		}
	}
	return nil
}

// The aws_cloudwatch_metric_alarm sequence of tf-compute.md 6 as Terraform
// receives it over rpcv2Cbor (wire-cbor.md 9) and as boto3 and the CLI
// receive it over awsQuery: Unit outputs (PutMetricAlarm, DeleteAlarms)
// carry no CBOR body, the read carries Threshold as a double,
// ActionsEnabled, and the empty action and Dimensions lists, and a missing
// alarm's error carries its Query code.
func TestTerraformAlarmSequenceOnTheWire(t *testing.T) {
	run(t, func(h *harness) {
		put := must(h, cloudwatchapi.OpPutMetricAlarm, minimalAlarm("wired"))
		if resp := encodeCBOR(t, cloudwatchapi.OpPutMetricAlarm.Descriptor(), put); len(resp.Body) != 0 || resp.Header.Get("Content-Type") != "" {
			t.Errorf("PutMetricAlarm over CBOR = %q with Content-Type %q, want no body", resp.Body, resp.Header.Get("Content-Type"))
		}

		describe := h.describe("wired")
		arn := *describe.MetricAlarms[0].AlarmArn
		resp := encodeCBOR(t, cloudwatchapi.OpDescribeAlarms.Descriptor(), describe)
		decoded, err := cbor.Decode(resp.Body)
		if err != nil {
			t.Fatalf("DescribeAlarms over CBOR does not decode: %v", err)
		}
		root, ok := decoded.(cbor.Map)
		if !ok {
			t.Fatalf("DescribeAlarms over CBOR = %#v, want a map", decoded)
		}
		alarms, ok := member(root, "MetricAlarms").(cbor.Array)
		if !ok || len(alarms) != 1 {
			t.Fatalf("MetricAlarms = %#v, want one alarm", member(root, "MetricAlarms"))
		}
		alarm, ok := alarms[0].(cbor.Map)
		if !ok {
			t.Fatalf("the alarm = %#v, want a map", alarms[0])
		}
		if got, ok := member(alarm, "Threshold").(cbor.Float64); !ok || got != 30 {
			t.Errorf("Threshold = %#v, want the double 30", member(alarm, "Threshold"))
		}
		if got, ok := member(alarm, "ActionsEnabled").(cbor.Bool); !ok || !bool(got) {
			t.Errorf("ActionsEnabled = %#v, want true", member(alarm, "ActionsEnabled"))
		}
		for _, list := range []string{"OKActions", "AlarmActions", "InsufficientDataActions", "Dimensions"} {
			if got, ok := member(alarm, list).(cbor.Array); !ok || len(got) != 0 {
				t.Errorf("%s = %#v, want an empty list", list, member(alarm, list))
			}
		}

		query := encodeQuery(t, cloudwatchapi.OpDescribeAlarms.Descriptor(), describe)
		for _, want := range []string{
			"<Threshold>30</Threshold>",
			"<ActionsEnabled>true</ActionsEnabled>",
			"<OKActions/>",
			"<AlarmActions/>",
			"<InsufficientDataActions/>",
			"<Dimensions/>",
		} {
			if !strings.Contains(query, want) {
				t.Errorf("DescribeAlarms over awsQuery lacks %s:\n%s", want, query)
			}
		}
		// awsQuery sends the envelope even for a Unit output, which boto3
		// needs (wire-json-query.md 8.2), but no Result element.
		if q := encodeQuery(t, cloudwatchapi.OpPutMetricAlarm.Descriptor(), put); !strings.HasPrefix(q, "<PutMetricAlarmResponse") || strings.Contains(q, "PutMetricAlarmResult") {
			t.Errorf("PutMetricAlarm over awsQuery = %s, want the envelope without a Result", q)
		}

		// TagResourceOutput is an empty structure, not Unit: an empty map
		// (wire-cbor.md 3.3).
		tagged := must(h, cloudwatchapi.OpTagResource, &cloudwatchapi.TagResourceInput{ResourceARN: arn, Tags: []cloudwatchapi.Tag{{Key: "k", Value: "v"}}})
		if resp := encodeCBOR(t, cloudwatchapi.OpTagResource.Descriptor(), tagged); string(resp.Body) != "\xa0" || resp.Header.Get("Content-Type") != "application/cbor" {
			t.Errorf("TagResource over CBOR = %q with Content-Type %q, want the empty map a0", resp.Body, resp.Header.Get("Content-Type"))
		}
		deleted := must(h, cloudwatchapi.OpDeleteAlarms, &cloudwatchapi.DeleteAlarmsInput{AlarmNames: []string{"wired"}})
		if resp := encodeCBOR(t, cloudwatchapi.OpDeleteAlarms.Descriptor(), deleted); len(resp.Body) != 0 {
			t.Errorf("DeleteAlarms over CBOR = %q, want no body", resp.Body)
		}

		_, aerr := call(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "wired", StateValue: cloudwatchapi.StateValueAlarm, StateReason: "test"})
		if aerr == nil {
			t.Fatal("SetAlarmState on a deleted alarm succeeded")
		}
		// The Go client sends x-amzn-query-mode: true, under which AWS
		// answers ResourceNotFound with 404 (error-matrix.md 5.13).
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/service/GraniteServiceVersion20100801/operation/SetAlarmState", nil)
		req.Header.Set("X-Amzn-Query-Mode", "true")
		errResp := rpcv2cbor.Codec{}.EncodeError(req, cloudwatchapi.Service(), requestID, aerr)
		if got := errResp.Header.Get("X-Amzn-Query-Error"); got != "ResourceNotFound;Sender" {
			t.Errorf("x-amzn-query-error = %q, want ResourceNotFound;Sender", got)
		}
		if errResp.Status != http.StatusNotFound {
			t.Errorf("SetAlarmState on a missing alarm over CBOR in query mode: status = %d, want 404", errResp.Status)
		}
	})
}
