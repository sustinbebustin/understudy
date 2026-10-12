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
// (lambda-events.md 6.2) with its change left to each test, at eventVersion
// 2.1 as the recordings show.
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

func TestObjectCreatedPutMatchesTheDocumentedRecord(t *testing.T) {
	const golden = `{"Records":[{"eventVersion":"2.1","eventSource":"aws:s3","awsRegion":"us-west-2","eventTime":"1970-01-01T00:00:00.000Z","eventName":"ObjectCreated:Put","userIdentity":{"principalId":"AIDAJDPLRKLG7UEXAMPLE"},"requestParameters":{"sourceIPAddress":"172.16.0.1"},"responseElements":{"x-amz-request-id":"C3D13FE58DE4C810","x-amz-id-2":"FMyUVURIY8/IgAtTv8xRjskZQpcIZ9KG4V5Wp6S7S/JRWeUWerMUE5JgHvANOjpD"},"s3":{"s3SchemaVersion":"1.0","configurationId":"testConfigRule","bucket":{"name":"amzn-s3-demo-bucket","ownerIdentity":{"principalId":"A3NL1KOZZKExample"},"arn":"arn:aws:s3:::amzn-s3-demo-bucket"},"object":{"key":"HappyFace.jpg","size":1024,"eTag":"d41d8cd98f00b204e9800998ecf8427e","versionId":"096fKKXTRTtl3on89fVO.nfljtsv6qko","sequencer":"0055AED6DCD90281E5"}}}]}`
	ev := documented(notify.Created{Action: notify.CreatedPut, Size: 1024, ETag: "d41d8cd98f00b204e9800998ecf8427e", Sequencer: exampleSequencer})
	ev.VersionID = "096fKKXTRTtl3on89fVO.nfljtsv6qko"
	got, err := notify.Record(ev, "testConfigRule")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != golden {
		t.Fatalf("record =\n%s\nwant\n%s", got, golden)
	}
}

// A removal carries only the key and the sequencer: no size and no eTag
// (AWS-recorded, lambda-events.md 6.2), and no versionId on an unversioned
// bucket.
func TestObjectRemovedDeleteMatchesTheRecordedRecord(t *testing.T) {
	const golden = `{"Records":[{"eventVersion":"2.1","eventSource":"aws:s3","awsRegion":"us-west-2","eventTime":"1970-01-01T00:00:00.000Z","eventName":"ObjectRemoved:Delete","userIdentity":{"principalId":"AIDAJDPLRKLG7UEXAMPLE"},"requestParameters":{"sourceIPAddress":"172.16.0.1"},"responseElements":{"x-amz-request-id":"C3D13FE58DE4C810","x-amz-id-2":"FMyUVURIY8/IgAtTv8xRjskZQpcIZ9KG4V5Wp6S7S/JRWeUWerMUE5JgHvANOjpD"},"s3":{"s3SchemaVersion":"1.0","configurationId":"testConfigRule","bucket":{"name":"amzn-s3-demo-bucket","ownerIdentity":{"principalId":"A3NL1KOZZKExample"},"arn":"arn:aws:s3:::amzn-s3-demo-bucket"},"object":{"key":"HappyFace.jpg","sequencer":"0055AED6DCD90281E5"}}}]}`
	got, err := notify.Record(documented(notify.Removed{Action: notify.RemovedDelete, Sequencer: exampleSequencer}), "testConfigRule")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != golden {
		t.Fatalf("record =\n%s\nwant\n%s", got, golden)
	}
}

// A tagging event is eventVersion 2.3 and carries the eTag but no size or
// sequencer (AWS-recorded, s3.md 13.2).
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
	if r.EventVersion != "2.3" || r.EventName != "ObjectTagging:Put" {
		t.Fatalf("eventVersion, eventName = %q, %q; want 2.3, ObjectTagging:Put", r.EventVersion, r.EventName)
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
