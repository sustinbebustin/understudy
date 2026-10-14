package cbor_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
)

// vector is a golden body from wire-cbor.md section 8: G vectors are what
// aws-sdk-go-v2 cloudwatch v1.73.0 sent or accepted, S vectors the Smithy
// 1.74.0 rpcv2Cbor corpus. want is its diagnostic notation (see diag).
type vector struct {
	name, hex, want string
}

const alarmARN = `"arn:aws:cloudwatch:us-east-1:000000000000:alarm:tf-alarm"`

// requestVectors are bodies clients send, in every length form: the Go
// client's indefinite maps and lists, the corpus's indefinite strings and
// keys, half floats and non-minimal widths.
var requestVectors = []vector{
	{
		"G01 PutMetricAlarm", "bf6e416374696f6e73456e61626c6564f56c416c61726d416374696f6e739f782461726e3a6177733a736e733a75732d656173742d313a3030303030303030303030303a74ff70416c61726d4465736372697074696f6e646465736369416c61726d4e616d656874662d616c61726d72436f6d70617269736f6e4f70657261746f7274477265617465725468616e5468726573686f6c646a44696d656e73696f6e739fbf644e616d656951756575654e616d656556616c75656171ffff714576616c756174696f6e506572696f6473016a4d65747269634e616d657822417070726f78696d6174654e756d6265724f664d6573736167657356697369626c65694e616d657370616365674157532f53515366506572696f6419012c695374617469737469636353756d64546167739fbf634b657963656e766556616c756563646576ffbf634b6579647465616d6556616c75656178ffff695468726573686f6c64fb40240000000000007054726561744d697373696e6744617461676d697373696e67ff",
		`{"ActionsEnabled": true, "AlarmActions": ["arn:aws:sns:us-east-1:000000000000:t"], "AlarmDescription": "desc", "AlarmName": "tf-alarm", "ComparisonOperator": "GreaterThanThreshold", "Dimensions": [{"Name": "QueueName", "Value": "q"}], "EvaluationPeriods": 1, "MetricName": "ApproximateNumberOfMessagesVisible", "Namespace": "AWS/SQS", "Period": 300, "Statistic": "Sum", "Tags": [{"Key": "env", "Value": "dev"}, {"Key": "team", "Value": "x"}], "Threshold": 10_3, "TreatMissingData": "missing"}`,
	},
	{"G02 DescribeAlarms empty", "bfff", `{}`},
	{
		"G03 DescribeAlarms by name", "bf6a416c61726d4e616d65739f6874662d616c61726dff6a416c61726d54797065739f6b4d6574726963416c61726dffff",
		`{"AlarmNames": ["tf-alarm"], "AlarmTypes": ["MetricAlarm"]}`,
	},
	{
		"G04 DescribeAlarms paging", "bf6a4d61785265636f7264731864694e657874546f6b656e63746f6bff",
		`{"MaxRecords": 100, "NextToken": "tok"}`,
	},
	{
		"G05 ListTagsForResource", "bf6b5265736f7572636541524e783861726e3a6177733a636c6f756477617463683a75732d656173742d313a3030303030303030303030303a616c61726d3a74662d616c61726dff",
		`{"ResourceARN": ` + alarmARN + `}`,
	},
	{
		"G06 TagResource", "bf6b5265736f7572636541524e783861726e3a6177733a636c6f756477617463683a75732d656173742d313a3030303030303030303030303a616c61726d3a74662d616c61726d64546167739fbf634b657963656e766556616c756563646576ffffff",
		`{"ResourceARN": ` + alarmARN + `, "Tags": [{"Key": "env", "Value": "dev"}]}`,
	},
	{
		"G07 UntagResource", "bf6b5265736f7572636541524e783861726e3a6177733a636c6f756477617463683a75732d656173742d313a3030303030303030303030303a616c61726d3a74662d616c61726d675461674b6579739f63656e76ffff",
		`{"ResourceARN": ` + alarmARN + `, "TagKeys": ["env"]}`,
	},
	{"G08 DeleteAlarms", "bf6a416c61726d4e616d65739f6874662d616c61726dffff", `{"AlarmNames": ["tf-alarm"]}`},
	{
		"G09 PutMetricData", "bf6a4d6574726963446174619fbf6a44696d656e73696f6e739fbf644e616d656553746167656556616c756563646576ffff6a4d65747269634e616d65644a6f62736954696d657374616d70c1fb41da55cd6947df3b64556e697465436f756e746556616c7565fb3ff8000000000000ffff694e616d65737061636563417070ff",
		`{"MetricData": [{"Dimensions": [{"Name": "Stage", "Value": "dev"}], "MetricName": "Jobs", "Timestamp": 1(1.767323045123e+09_3), "Unit": "Count", "Value": 1.5_3}], "Namespace": "App"}`,
	},
	{
		"G10 GetMetricData", "bf67456e6454696d65c1fb41da55d0ed47df3b6d4d617844617461706f696e74731864714d657472696344617461517565726965739fbf624964626d316a4d657472696353746174bf664d6574726963bf6a44696d656e73696f6e739fbf644e616d656553746167656556616c756563646576ffff6a4d65747269634e616d65644a6f6273694e616d65737061636563417070ff66506572696f64183c64537461746353756dff6a52657475726e44617461f5ffff665363616e42797354696d657374616d7044657363656e64696e6769537461727454696d65c1fb41da55cd6947df3bff",
		`{"EndTime": 1(1.767326645123e+09_3), "MaxDatapoints": 100, "MetricDataQueries": [{"Id": "m1", "MetricStat": {"Metric": {"Dimensions": [{"Name": "Stage", "Value": "dev"}], "MetricName": "Jobs", "Namespace": "App"}, "Period": 60, "Stat": "Sum"}, "ReturnData": true}], "ScanBy": "TimestampDescending", "StartTime": 1(1.767323045123e+09_3)}`,
	},
	{"S01 empty_input", "bfff", `{}`},
	{
		"S02 indefinite keys and strings", "aa696279746556616c7565057f66646f75626c656556616c7565fffb3ffe395810624dd37f6566616c736567426f6f6c65616e6556616c7565fff46a666c6f617456616c7565fa40f400006c696e746567657256616c7565190100696c6f6e6756616c75651926916a73686f727456616c75651926aa7f66737472696e676556616c7565ff6673696d706c657074727565426f6f6c65616e56616c7565f569626c6f6256616c756543666f6f",
		`{"byteValue": 5, "doubleValue": 1.889_3, "falseBooleanValue": false, "floatValue": 7.625_2, "integerValue": 256, "longValue": 9873, "shortValue": 9898, "stringValue": "simple", "trueBooleanValue": true, "blobValue": h'666f6f'}`,
	},
	{
		"S03 upcasting", "bf6b646f75626c6556616c7565f93e006a666c6f617456616c7565f947a06c696e746567657256616c75651838696c6f6e6756616c75651901006a73686f727456616c75650aff",
		`{"doubleValue": 1.5_2, "floatValue": 7.625_2, "integerValue": 56, "longValue": 256, "shortValue": 10}`,
	},
	{
		"S04 NaN", "bf6b646f75626c6556616c7565fb7ff80000000000006a666c6f617456616c7565fa7fc00000ff",
		`{"doubleValue": NaN_3, "floatValue": NaN_2}`,
	},
	{"S06 null member", "bf6b737472696e6756616c7565f6ff", `{"stringValue": null}`},
	{
		"S07 indefinite byte string", "a169626c6f6256616c75655f5822416e206578616d706c6520696e646566696e6974652d6279746520737472696e672c51206368756e6b6564206f6e20636f6d6d61ff",
		`{"blobValue": h'416e206578616d706c6520696e646566696e6974652d6279746520737472696e672c206368756e6b6564206f6e20636f6d6d61'}`,
	},
	{
		"S08 indefinite strings in a definite list", "a16a737472696e674c697374837f781d416e206578616d706c6520696e646566696e69746520737472696e672c772077686963682077696c6c206265206368756e6b65642c6e206f6e206561636820636f6d6d61ff7f7835416e6f74686572206578616d706c6520696e646566696e69746520737472696e672077697468206f6e6c79206f6e65206368756e6bff7654686973206973206120706c61696e20737472696e67",
		`{"stringList": ["An example indefinite string, which will be chunked, on each comma", "Another example indefinite string with only one chunk", "This is a plain string"]}`,
	},
	{
		"S12 extra fields", "bf696279746556616c7565056b646f75626c6556616c7565fb3ffe395810624dd37166616c7365426f6f6c65616e56616c7565f46a666c6f617456616c7565fa40f400006b65787472614f626a656374bf73696e646566696e6974654c656e6774684d6170bf6b77697468416e41727261799f010203ffff71646566696e6974654c656e6774684d6170a3727769746841446566696e697465417272617983010203781d616e64536f6d65496e646566696e6974654c656e677468537472696e67781f74686174206861732c206265656e206368756e6b6564206f6e20636f6d6d616c6e6f726d616c537472696e6763666f6f6a73686f727456616c756519270f6e736f6d654f746865724669656c6476746869732073686f756c6420626520736b6970706564ff6c696e746567657256616c7565190100696c6f6e6756616c75651926916a73686f727456616c75651926aa6b737472696e6756616c75656673696d706c657074727565426f6f6c65616e56616c7565f569626c6f6256616c756543666f6fff",
		`{"byteValue": 5, "doubleValue": 1.889_3, "falseBooleanValue": false, "floatValue": 7.625_2, "extraObject": {"indefiniteLengthMap": {"withAnArray": [1, 2, 3]}, "definiteLengthMap": {"withADefiniteArray": [1, 2, 3], "andSomeIndefiniteLengthString": "that has, been chunked on comma", "normalString": "foo"}, "shortValue": 9999, "someOtherField": "this should be skipped"}, "integerValue": 256, "longValue": 9873, "shortValue": 9898, "stringValue": "simple", "trueBooleanValue": true, "blobValue": h'666f6f'}`,
	},
	{
		"S13 union", "a268636f6e74656e7473a16b737472696e6756616c756563666f6f6a6f7468657256616c756563626172",
		`{"contents": {"stringValue": "foo"}, "otherValue": "bar"}`,
	},
}

// canonicalResponses are the response bodies the Go client accepted, all
// already in the form the encoder emits: definite lengths, minimal widths.
var canonicalResponses = []vector{
	{"G12 empty structure", "a0", `{}`},
	{"G13 DescribeAlarms none", "a16c4d6574726963416c61726d7380", `{"MetricAlarms": []}`},
	{
		"G14 DescribeAlarms one", "a16c4d6574726963416c61726d7381b369416c61726d4e616d656874662d616c61726d68416c61726d41726e783861726e3a6177733a636c6f756477617463683a75732d656173742d313a3030303030303030303030303a616c61726d3a74662d616c61726d70416c61726d4465736372697074696f6e64646573636e416374696f6e73456e61626c6564f56c416c61726d416374696f6e7381782461726e3a6177733a736e733a75732d656173742d313a3030303030303030303030303a74694f4b416374696f6e738077496e73756666696369656e7444617461416374696f6e73806a537461746556616c756571494e53554646494349454e545f444154417553746174655570646174656454696d657374616d70c1fb41da55cd6947df3b7822416c61726d436f6e66696775726174696f6e5570646174656454696d657374616d70c1fb41da55cd6947df3b6a4d65747269634e616d657822417070726f78696d6174654e756d6265724f664d6573736167657356697369626c65694e616d657370616365674157532f535153695374617469737469636353756d6a44696d656e73696f6e7381a2644e616d656951756575654e616d656556616c7565617166506572696f6419012c714576616c756174696f6e506572696f647301695468726573686f6c64fb402400000000000072436f6d70617269736f6e4f70657261746f7274477265617465725468616e5468726573686f6c647054726561744d697373696e6744617461676d697373696e67",
		`{"MetricAlarms": [{"AlarmName": "tf-alarm", "AlarmArn": ` + alarmARN + `, "AlarmDescription": "desc", "ActionsEnabled": true, "AlarmActions": ["arn:aws:sns:us-east-1:000000000000:t"], "OKActions": [], "InsufficientDataActions": [], "StateValue": "INSUFFICIENT_DATA", "StateUpdatedTimestamp": 1(1.767323045123e+09_3), "AlarmConfigurationUpdatedTimestamp": 1(1.767323045123e+09_3), "MetricName": "ApproximateNumberOfMessagesVisible", "Namespace": "AWS/SQS", "Statistic": "Sum", "Dimensions": [{"Name": "QueueName", "Value": "q"}], "Period": 300, "EvaluationPeriods": 1, "Threshold": 10_3, "ComparisonOperator": "GreaterThanThreshold", "TreatMissingData": "missing"}]}`,
	},
	{
		"G15 ListTagsForResource", "a1645461677382a2634b657963656e766556616c756563646576a2634b6579647465616d6556616c75656178",
		`{"Tags": [{"Key": "env", "Value": "dev"}, {"Key": "team", "Value": "x"}]}`,
	},
	{
		"G16 GetMetricData", "a1714d657472696344617461526573756c747381a5624964626d31654c6162656c644a6f62736a537461747573436f646568436f6d706c6574656a54696d657374616d707382c1fb41da55cd68000000c1fb41da55cd770000006656616c75657382fb3ff8000000000000fb4000000000000000",
		`{"MetricDataResults": [{"Id": "m1", "Label": "Jobs", "StatusCode": "Complete", "Timestamps": [1(1.76732304e+09_3), 1(1.7673231e+09_3)], "Values": [1.5_3, 2_3]}]}`,
	},
	{
		"G17 ResourceNotFound", "a2665f5f747970657829636f6d2e616d617a6f6e6177732e636c6f75647761746368235265736f757263654e6f74466f756e64676d6573736167656f416c61726d206e6f7420666f756e64",
		`{"__type": "com.amazonaws.cloudwatch#ResourceNotFound", "message": "Alarm not found"}`,
	},
	{
		"G18 ResourceNotFoundException", "a4665f5f747970657832636f6d2e616d617a6f6e6177732e636c6f75647761746368235265736f757263654e6f74466f756e64457863657074696f6e674d65737361676571556e6b6e6f776e207265736f757263652e6c5265736f757263655479706565616c61726d6a5265736f757263654964783861726e3a6177733a636c6f756477617463683a75732d656173742d313a3030303030303030303030303a616c61726d3a74662d616c61726d",
		`{"__type": "com.amazonaws.cloudwatch#ResourceNotFoundException", "Message": "Unknown resource.", "ResourceType": "alarm", "ResourceId": ` + alarmARN + `}`,
	},
	{
		"G19 InvalidNextToken", "a2665f5f747970657829636f6d2e616d617a6f6e6177732e636c6f7564776174636823496e76616c69644e657874546f6b656e676d6573736167657819546865206e65787420746f6b656e20697320696e76616c6964",
		`{"__type": "com.amazonaws.cloudwatch#InvalidNextToken", "message": "The next token is invalid"}`,
	},
	{
		"G20 InternalServiceFault", "a2665f5f74797065782d636f6d2e616d617a6f6e6177732e636c6f7564776174636823496e7465726e616c536572766963654661756c74674d6573736167656e696e7465726e616c206572726f72",
		`{"__type": "com.amazonaws.cloudwatch#InternalServiceFault", "Message": "internal error"}`,
	},
	{
		"G21 ValidationException", "a2665f5f74797065782c636f6d2e616d617a6f6e6177732e636c6f756477617463682356616c69646174696f6e457863657074696f6e676d6573736167656962616420696e707574",
		`{"__type": "com.amazonaws.cloudwatch#ValidationException", "message": "bad input"}`,
	},
	{
		"S13 union", "a268636f6e74656e7473a16b737472696e6756616c756563666f6f6a6f7468657256616c756563626172",
		`{"contents": {"stringValue": "foo"}, "otherValue": "bar"}`,
	},
}

// otherResponses are corpus responses in forms the encoder does not emit:
// indefinite lengths, a non-minimal map head, a half float.
var otherResponses = []vector{
	{"S05 half subnormal", "a16576616c7565f90050", `{"value": 4.7683716e-06_2}`}, // 1.25 * 2^-18 exactly
	{
		"S09 InvalidGreeting", "bf665f5f74797065782e736d697468792e70726f746f636f6c74657374732e727063763243626f7223496e76616c69644772656574696e67674d657373616765624869ff",
		`{"__type": "smithy.protocoltests.rpcv2Cbor#InvalidGreeting", "Message": "Hi"}`,
	},
	{
		"S10 CustomCodeError, non-minimal map head", "b90002665f5f74797065782b6177732e70726f746f636f6c74657374732e727063763263626f7223437573746f6d436f64654572726f72676d657373616765624869",
		`{"__type": "aws.protocoltests.rpcv2cbor#CustomCodeError", "message": "Hi"}`,
	},
	{"S11 fractional seconds", "bf686461746574696d65c1fb41cc37db380fbe77ff", `{"datetime": 1(9.46845296123e+08_3)}`},
	{"G22 member in a Unit output", "a1617801", `{"x": 1}`},
	{"G24 empty indefinite map", "bfff", `{}`},
}

func TestDecodeGoldenVectors(t *testing.T) {
	for _, set := range [][]vector{requestVectors, canonicalResponses, otherResponses} {
		for _, v := range set {
			t.Run(v.name, func(t *testing.T) {
				got, err := cbor.Decode(unhex(t, v.hex))
				if err != nil {
					t.Fatalf("Decode: %v", err)
				}
				if d := diag(got); d != v.want {
					t.Errorf("decoded\n got %s\nwant %s", d, v.want)
				}
			})
		}
	}
}

// The encoder's canonical form is exactly what the canonical response
// vectors hold, byte for byte.
func TestEncodeReproducesCanonicalResponses(t *testing.T) {
	for _, v := range canonicalResponses {
		t.Run(v.name, func(t *testing.T) {
			body := unhex(t, v.hex)
			val, err := cbor.Decode(body)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			got, err := cbor.Encode(val)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if !bytes.Equal(got, body) {
				t.Errorf("Encode\n got %x\nwant %x", got, body)
			}
		})
	}
}

// Every request vector re-encodes to the canonical form, which decodes to
// the same tree (wire-cbor.md section 7, property list).
func TestReencodedRequestVectorsDecodeAlike(t *testing.T) {
	for _, set := range [][]vector{requestVectors, otherResponses} {
		for _, v := range set {
			t.Run(v.name, func(t *testing.T) {
				val, err := cbor.Decode(unhex(t, v.hex))
				if err != nil {
					t.Fatalf("Decode: %v", err)
				}
				canonical, err := cbor.Encode(val)
				if err != nil {
					t.Fatalf("Encode: %v", err)
				}
				again, err := cbor.Decode(canonical)
				if err != nil {
					t.Fatalf("Decode of the canonical form %x: %v", canonical, err)
				}
				if diag(again) != v.want {
					t.Errorf("canonical form decodes to %s, want %s", diag(again), v.want)
				}
			})
		}
	}
}

func TestReadTimestampOfGoClientVector(t *testing.T) {
	// G09's Timestamp member: 1(1.767323045123e+09_3).
	r := cbor.NewReader(unhex(t, "c1fb41da55cd6947df3b"))
	got, err := r.ReadTimestamp()
	if err != nil {
		t.Fatalf("ReadTimestamp: %v", err)
	}
	if want := time.UnixMilli(1767323045123).UTC(); !got.Equal(want) {
		t.Errorf("ReadTimestamp = %v, want %v", got, want)
	}
}

func TestReadTimestampForms(t *testing.T) {
	tests := []struct {
		name, hex string
		want      time.Time
	}{
		{"unsigned integer", "c11a5a0b3c00", time.Unix(1510685696, 0).UTC()},
		{"negative integer", "c120", time.Unix(-1, 0).UTC()},
		{"single float", "c1fa3fc00000", time.UnixMilli(1500).UTC()},
		{"half float", "c1f93e00", time.UnixMilli(1500).UTC()},
		{"S11 fractional seconds", "c1fb41cc37db380fbe77", time.UnixMilli(946845296123).UTC()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cbor.NewReader(unhex(t, tt.hex)).ReadTimestamp()
			if err != nil {
				t.Fatalf("ReadTimestamp: %v", err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("ReadTimestamp = %v, want %v", got, tt.want)
			}
		})
	}
}

// Smithy requires tag 1; an untagged number, another tag or tag 1 around
// text is a type mismatch (wire-cbor.md 3.6 and 5.1), and a timestamp
// beyond the years 0001 to 9999 or not finite is out of range.
func TestReadTimestampRefusals(t *testing.T) {
	tests := []struct {
		name, hex string
		mismatch  bool
	}{
		{"bare double", "fb41da55cd6947df3b", true},
		{"bare integer", "1a5a0b3c00", true},
		{"tag 0 text", "c074323031372d31312d31345431383a35343a35365a", true},
		{"tag 1 text", "c16161", true},
		{"NaN", "c1fb7ff8000000000000", false},
		{"infinity", "c1f97c00", false},
		{"year 10000", "c11b0000003afff44180", false},
		{"before year 1", "c13b0000000e7791f700", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cbor.NewReader(unhex(t, tt.hex)).ReadTimestamp()
			var mismatch *cbor.MismatchError
			var outOfRange *cbor.RangeError
			switch {
			case tt.mismatch && !errors.As(err, &mismatch):
				t.Errorf("ReadTimestamp error = %v, want a *MismatchError", err)
			case !tt.mismatch && !errors.As(err, &outOfRange):
				t.Errorf("ReadTimestamp error = %v, want a *RangeError", err)
			}
		})
	}
}
