package notify_test

import (
	"encoding/json"
	"maps"
	"net/url"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/service/s3/notify"
)

// documented is the object event of the S3 content-structure example
// (lambda-events.md 6.2) with its change left to each test.
func documented(change notify.Change) notify.Event {
	return notify.Event{
		Time:   time.Unix(0, 0).UTC(),
		Region: "us-west-2",
		Bucket: notify.Bucket{
			Name:  "amzn-s3-demo-bucket",
			ARN:   aws.ARN{Partition: "aws", Service: "s3", Resource: "amzn-s3-demo-bucket"},
			Owner: "A3NL1KOZZKExample",
		},
		Key:    "HappyFace.jpg",
		Change: change,
		Request: notify.Request{
			ID:        "C3D13FE58DE4C810",
			HostID:    "FMyUVURIY8/IgAtTv8xRjskZQpcIZ9KG4V5Wp6S7S/JRWeUWerMUE5JgHvANOjpD",
			Principal: "AIDAJDPLRKLG7UEXAMPLE",
			SourceIP:  "172.16.0.1",
		},
	}
}

const exampleSequencer notify.Sequencer = 0x55AED6DCD90281E5

// recorded is an event as R17 recorded it (sqs/R17/02-events-*.json): a
// role session's call on a bucket in us-east-2, with the per-event fields
// left to each case.
func recorded(at time.Time, bucket, requestID, hostID string, change notify.Change) notify.Event {
	return notify.Event{
		Time:   at,
		Region: "us-east-2",
		Bucket: notify.Bucket{
			Name:  bucket,
			ARN:   aws.ARN{Partition: "aws", Service: "s3", Resource: bucket},
			Owner: "AEXAMPLE000009",
		},
		Change: change,
		Request: notify.Request{
			ID:        requestID,
			HostID:    hostID,
			Principal: "AWS:AROARZPUZDIKAAAAAAACA:00000000-0000-0000-0000-000000000003",
			SourceIP:  "198.51.100.100",
		},
	}
}

// r17 is an instant of R17's events, 2026-10-14 07:07:ss.ms UTC.
func r17(second, ms int) time.Time {
	return time.Date(2026, time.October, 14, 7, 7, second, ms*int(time.Millisecond), time.UTC)
}

// Each record is the body S3 delivered to SQS in R17, byte for byte:
// eventVersion 2.6, AWS's key order, the empty-object eTag on a delete
// marker, and hasObjectAnnotation after a copy's sequencer.
func TestRecordsMatchTheR17Records(t *testing.T) {
	for name, tc := range map[string]struct {
		ev       notify.Event
		key      string
		version  string
		configID string
		golden   string
	}{
		// sqs/R17/02-events-2.json
		"ObjectCreated:Put": {
			ev: recorded(r17(45, 557), "usr-rec-20261014-ev", "GYNBJ1SRNVBCV8YY",
				"wY6U2xs0cPOStW72e9IcHUw6Rv0Vvmgw88Jd/QeHGGga5nXyrCvoNL4lHlHO89S+SBs/q3y3QmouL4wupRszKqfofFL3fXYn",
				notify.Created{Action: notify.CreatedPut, Size: 5, ETag: "5d41402abc4b2a76b9719d911017c592", Sequencer: 0x6ACF2A417B72EE95}),
			key:      "a b/ü+1.txt",
			configID: "ps",
			golden:   `{"Records":[{"eventVersion":"2.6","eventSource":"aws:s3","awsRegion":"us-east-2","eventTime":"2026-10-14T07:07:45.557Z","eventName":"ObjectCreated:Put","userIdentity":{"principalId":"AWS:AROARZPUZDIKAAAAAAACA:00000000-0000-0000-0000-000000000003"},"requestParameters":{"sourceIPAddress":"198.51.100.100"},"responseElements":{"x-amz-request-id":"GYNBJ1SRNVBCV8YY","x-amz-id-2":"wY6U2xs0cPOStW72e9IcHUw6Rv0Vvmgw88Jd/QeHGGga5nXyrCvoNL4lHlHO89S+SBs/q3y3QmouL4wupRszKqfofFL3fXYn"},"s3":{"s3SchemaVersion":"1.0","configurationId":"ps","bucket":{"name":"usr-rec-20261014-ev","ownerIdentity":{"principalId":"AEXAMPLE000009"},"arn":"arn:aws:s3:::usr-rec-20261014-ev"},"object":{"key":"a+b/%C3%BC%2B1.txt","size":5,"eTag":"5d41402abc4b2a76b9719d911017c592","sequencer":"006ACF2A417B72EE95"}}}]}`,
		},
		// sqs/R17/02-events-3.json
		"ObjectCreated:Copy": {
			ev: recorded(r17(56, 858), "usr-rec-20261014-ev-v", "NQTFJJ3RKSBR3PPT",
				"VBClJ8X2J99UyorXSHSDm8jjMg9RR9BAu66RT3CPnCAXHk/LRhUJ0Jtk7lYLeX4mI3ZFgIouGCfe8hNj5XwooB3nR33jlFQHMZooLBYW6mo=",
				notify.Created{Action: notify.CreatedCopy, Size: 5, ETag: "5d41402abc4b2a76b9719d911017c592", Sequencer: 0x6ACF2A4CCFF09D11}),
			key:      "copy",
			version:  "5ZmtzXDLMSf5lrLkWAN7zJ3Z1MzEQ79s",
			configID: "q",
			golden:   `{"Records":[{"eventVersion":"2.6","eventSource":"aws:s3","awsRegion":"us-east-2","eventTime":"2026-10-14T07:07:56.858Z","eventName":"ObjectCreated:Copy","userIdentity":{"principalId":"AWS:AROARZPUZDIKAAAAAAACA:00000000-0000-0000-0000-000000000003"},"requestParameters":{"sourceIPAddress":"198.51.100.100"},"responseElements":{"x-amz-request-id":"NQTFJJ3RKSBR3PPT","x-amz-id-2":"VBClJ8X2J99UyorXSHSDm8jjMg9RR9BAu66RT3CPnCAXHk/LRhUJ0Jtk7lYLeX4mI3ZFgIouGCfe8hNj5XwooB3nR33jlFQHMZooLBYW6mo="},"s3":{"s3SchemaVersion":"1.0","configurationId":"q","bucket":{"name":"usr-rec-20261014-ev-v","ownerIdentity":{"principalId":"AEXAMPLE000009"},"arn":"arn:aws:s3:::usr-rec-20261014-ev-v"},"object":{"key":"copy","size":5,"eTag":"5d41402abc4b2a76b9719d911017c592","versionId":"5ZmtzXDLMSf5lrLkWAN7zJ3Z1MzEQ79s","sequencer":"006ACF2A4CCFF09D11","hasObjectAnnotation":false}}}]}`,
		},
		// sqs/R17/02-events-1.json
		"ObjectCreated:CompleteMultipartUpload": {
			ev: recorded(r17(56, 280), "usr-rec-20261014-ev-v", "NQT0V8EEBC5167YV",
				"pAQx6bOdm0oPh0TJFPQDNIDw1CcDuvaysif8m91E3MAQGaFfVUp5iNjY0WHU1FxMfkhvdRFTdE9Tk2nAlkTJwzqKdxir5J73",
				notify.Created{Action: notify.CreatedCompleteMultipartUpload, Size: 5242880, ETag: "81485c0e873d222199469076b60f30e9-1", Sequencer: 0x6ACF2A4A9C030DFF}),
			key:      "mp",
			version:  "_yoZJVEe6v1PGluqtQPKrWbYFg_FaODV",
			configID: "q",
			golden:   `{"Records":[{"eventVersion":"2.6","eventSource":"aws:s3","awsRegion":"us-east-2","eventTime":"2026-10-14T07:07:56.280Z","eventName":"ObjectCreated:CompleteMultipartUpload","userIdentity":{"principalId":"AWS:AROARZPUZDIKAAAAAAACA:00000000-0000-0000-0000-000000000003"},"requestParameters":{"sourceIPAddress":"198.51.100.100"},"responseElements":{"x-amz-request-id":"NQT0V8EEBC5167YV","x-amz-id-2":"pAQx6bOdm0oPh0TJFPQDNIDw1CcDuvaysif8m91E3MAQGaFfVUp5iNjY0WHU1FxMfkhvdRFTdE9Tk2nAlkTJwzqKdxir5J73"},"s3":{"s3SchemaVersion":"1.0","configurationId":"q","bucket":{"name":"usr-rec-20261014-ev-v","ownerIdentity":{"principalId":"AEXAMPLE000009"},"arn":"arn:aws:s3:::usr-rec-20261014-ev-v"},"object":{"key":"mp","size":5242880,"eTag":"81485c0e873d222199469076b60f30e9-1","versionId":"_yoZJVEe6v1PGluqtQPKrWbYFg_FaODV","sequencer":"006ACF2A4A9C030DFF"}}}]}`,
		},
		// sqs/R17/02-events-1.json
		"ObjectRemoved:DeleteMarkerCreated": {
			ev: recorded(r17(59, 690), "usr-rec-20261014-ev-v", "4481M9ER9D4REVSV",
				"bGJ0CivAdUUNqhqg/+IuYzgFpF/XC4wIbxStgOzqD010b3SRJKPE+jjHxHNi+GRI6K6l5jCa9P2FhlhFA5sxRaduIuCVnjI97Y3HvmVsEVM=",
				notify.Removed{Action: notify.RemovedDeleteMarkerCreated, Sequencer: 0x6ACF2A4FABAE61EF}),
			key:      "d2",
			version:  "gKoDFTmrHTAhS9oM65xDEI3P_3xjxL2I",
			configID: "q",
			golden:   `{"Records":[{"eventVersion":"2.6","eventSource":"aws:s3","awsRegion":"us-east-2","eventTime":"2026-10-14T07:07:59.690Z","eventName":"ObjectRemoved:DeleteMarkerCreated","userIdentity":{"principalId":"AWS:AROARZPUZDIKAAAAAAACA:00000000-0000-0000-0000-000000000003"},"requestParameters":{"sourceIPAddress":"198.51.100.100"},"responseElements":{"x-amz-request-id":"4481M9ER9D4REVSV","x-amz-id-2":"bGJ0CivAdUUNqhqg/+IuYzgFpF/XC4wIbxStgOzqD010b3SRJKPE+jjHxHNi+GRI6K6l5jCa9P2FhlhFA5sxRaduIuCVnjI97Y3HvmVsEVM="},"s3":{"s3SchemaVersion":"1.0","configurationId":"q","bucket":{"name":"usr-rec-20261014-ev-v","ownerIdentity":{"principalId":"AEXAMPLE000009"},"arn":"arn:aws:s3:::usr-rec-20261014-ev-v"},"object":{"key":"d2","eTag":"d41d8cd98f00b204e9800998ecf8427e","versionId":"gKoDFTmrHTAhS9oM65xDEI3P_3xjxL2I","sequencer":"006ACF2A4FABAE61EF"}}}]}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			ev := tc.ev
			ev.Key, ev.VersionID = tc.key, tc.version
			got, err := notify.Record(ev, tc.configID)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.golden {
				t.Fatalf("record =\n%s\nwant\n%s", got, tc.golden)
			}
		})
	}
}

// A removal carries only the key and the sequencer: no size and no eTag
// (AWS-recorded, lambda-events.md 6.2), and no versionId on an unversioned
// bucket. R17 recorded no ObjectRemoved:Delete record, so its eventVersion
// is the one version S3 documents for every event type since 2.4 (aws-doc,
// s3-ssec-object-lock.md 7).
func TestObjectRemovedDeleteMatchesTheRecordedRecord(t *testing.T) {
	const golden = `{"Records":[{"eventVersion":"2.6","eventSource":"aws:s3","awsRegion":"us-west-2","eventTime":"1970-01-01T00:00:00.000Z","eventName":"ObjectRemoved:Delete","userIdentity":{"principalId":"AIDAJDPLRKLG7UEXAMPLE"},"requestParameters":{"sourceIPAddress":"172.16.0.1"},"responseElements":{"x-amz-request-id":"C3D13FE58DE4C810","x-amz-id-2":"FMyUVURIY8/IgAtTv8xRjskZQpcIZ9KG4V5Wp6S7S/JRWeUWerMUE5JgHvANOjpD"},"s3":{"s3SchemaVersion":"1.0","configurationId":"testConfigRule","bucket":{"name":"amzn-s3-demo-bucket","ownerIdentity":{"principalId":"A3NL1KOZZKExample"},"arn":"arn:aws:s3:::amzn-s3-demo-bucket"},"object":{"key":"HappyFace.jpg","sequencer":"0055AED6DCD90281E5"}}}]}`
	got, err := notify.Record(documented(notify.Removed{Action: notify.RemovedDelete, Sequencer: exampleSequencer}), "testConfigRule")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != golden {
		t.Fatalf("record =\n%s\nwant\n%s", got, golden)
	}
}

// A tagging event carries the eTag but no size or sequencer (AWS-recorded,
// s3.md 13.2). Its eventVersion is 2.6: S3 has used one version for every
// event type since 2.4 (aws-doc, s3-ssec-object-lock.md 7), which supersedes
// the 2.3 of the 2025 recording; R17 recorded no tagging event.
func TestObjectTaggingCarriesTheETagWithoutSizeOrSequencer(t *testing.T) {
	got, err := notify.Record(documented(notify.Tagged{Action: notify.TaggingPut, ETag: "d41d8cd98f00b204e9800998ecf8427e"}), "c")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Records []struct {
			EventVersion string `json:"eventVersion"`
			EventName    string `json:"eventName"`
			S3           struct {
				Object map[string]any `json:"object"`
			} `json:"s3"`
		}
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	r := doc.Records[0]
	if r.EventVersion != "2.6" || r.EventName != "ObjectTagging:Put" {
		t.Fatalf("eventVersion, eventName = %q, %q; want 2.6, ObjectTagging:Put", r.EventVersion, r.EventName)
	}
	if want := map[string]any{"key": "HappyFace.jpg", "eTag": "d41d8cd98f00b204e9800998ecf8427e"}; !maps.Equal(r.S3.Object, want) {
		t.Fatalf("object = %v, want %v", r.S3.Object, want)
	}
}

// Every declared action names an event type of the S3 model, and a value
// outside them names none, so it cannot pass as a real event.
func TestActionsNameModelEvents(t *testing.T) {
	for _, c := range []notify.Change{
		notify.Created{Action: notify.CreatedPut},
		notify.Created{Action: notify.CreatedPost},
		notify.Created{Action: notify.CreatedCopy},
		notify.Created{Action: notify.CreatedCompleteMultipartUpload},
		notify.Removed{Action: notify.RemovedDelete},
		notify.Removed{Action: notify.RemovedDeleteMarkerCreated},
		notify.Tagged{Action: notify.TaggingPut},
		notify.Tagged{Action: notify.TaggingDelete},
	} {
		if e := s3api.Event("s3:" + c.Name()); !e.Known() {
			t.Errorf("%s is not an event type of the model", e)
		}
	}
	for _, c := range []notify.Change{notify.Created{}, notify.Removed{}, notify.Tagged{}} {
		if e := s3api.Event("s3:" + c.Name()); e.Known() {
			t.Errorf("the zero action names the model event %s", e)
		}
	}
}

// An empty object still reports its size: 0 is a size, not an absent one.
func TestCreatedEmptyObjectReportsSizeZero(t *testing.T) {
	got, err := notify.Record(documented(notify.Created{Action: notify.CreatedPut, ETag: "d41d8cd98f00b204e9800998ecf8427e"}), "c")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"size":0,`) {
		t.Fatalf("record %s has no size 0", got)
	}
}

func TestTestEventMatchesTheDocumentedMessage(t *testing.T) {
	const golden = `{"Service":"Amazon S3","Event":"s3:TestEvent","Time":"2014-10-13T15:57:02.089Z","Bucket":"amzn-s3-demo-bucket","RequestId":"5582815E1AEA5ADF","HostId":"8cLeGAmw098X5cv4Zkwcmo8vvZa3eH3eKxsPzbB9wrR+YstdA6Knx4Ip8EXAMPLE"}`
	at := time.Date(2014, time.October, 13, 15, 57, 2, 89_000_000, time.UTC)
	got, err := notify.TestEvent("amzn-s3-demo-bucket", at, notify.Request{ID: "5582815E1AEA5ADF", HostID: "8cLeGAmw098X5cv4Zkwcmo8vvZa3eH3eKxsPzbB9wrR+YstdA6Knx4Ip8EXAMPLE"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != golden {
		t.Fatalf("test event =\n%s\nwant\n%s", got, golden)
	}
}

// The key is form-encoded except for '/': a space is '+', '@' is %40
// (AWS-recorded a@b -> a%40b) and a slash stays (AWS-recorded
// testupload/dir1/testfile.txt).
func TestKeyEncodingMatchesTheRecordings(t *testing.T) {
	for key, want := range map[string]string{
		"a@b":                          "a%40b",
		"testupload/dir1/testfile.txt": "testupload/dir1/testfile.txt",
		"red flower.jpg":               "red+flower.jpg",
		"café":                         "caf%C3%A9",
	} {
		if got := notify.EncodeKey(key); got != want {
			t.Errorf("EncodeKey(%q) = %q, want %q", key, got, want)
		}
	}
}

// Decoding an encoded key as a form value gives the key back, and the
// encoding holds no character a form decoder would read differently.
func TestKeyEncodingRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		key := rapid.String().Draw(t, "key")
		enc := notify.EncodeKey(key)
		got, err := url.QueryUnescape(enc)
		if err != nil {
			t.Fatalf("QueryUnescape(%q): %v", enc, err)
		}
		if got != key {
			t.Fatalf("QueryUnescape(EncodeKey(%q)) = %q", key, got)
		}
		if i := strings.IndexFunc(enc, func(r rune) bool {
			return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.*/%+", r)
		}); i >= 0 {
			t.Fatalf("EncodeKey(%q) = %q holds %q", key, enc, enc[i])
		}
	})
}
