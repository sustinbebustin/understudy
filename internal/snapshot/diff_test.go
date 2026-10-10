package snapshot_test

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/snapshot"
	"github.com/sustinbebustin/understudy/internal/store"
)

// archive builds an archive with a Running clock at at, journal counter
// next, and the given sections at version 1.
func archive(t *testing.T, at string, next ref.EventID, sections map[string]string) *snapshot.Archive {
	t.Helper()
	a := &snapshot.Archive{Manifest: snapshot.Manifest{
		Format:      snapshot.Format,
		Writer:      "understudy v0.1.0",
		Clock:       snapshot.Clock{Mode: clock.Running{}, At: instant(t, at)},
		JournalNext: next,
		Blobs:       snapshot.EmbedBlobs,
	}}
	for _, path := range slices.Sorted(maps.Keys(sections)) {
		a.Sections = append(a.Sections, store.Section{Path: path, Version: 1, Data: []byte(sections[path])})
	}
	return a
}

func diff(t *testing.T, a, b *snapshot.Archive, normalize bool) []snapshot.Difference {
	t.Helper()
	ds, err := snapshot.Diff(a, b, normalize)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	return ds
}

func lines(ds []snapshot.Difference) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.String()
	}
	return out
}

func assertDiff(t *testing.T, got []snapshot.Difference, want ...string) {
	t.Helper()
	if g := lines(got); !slices.Equal(g, want) {
		t.Errorf("diff\n got %q\nwant %q", g, want)
	}
}

const t0 = "2026-10-09T12:00:00Z"

func TestDiffReportsAChangedQueueAttribute(t *testing.T) {
	t.Parallel()
	const path = "services/sqs/000000000000/us-east-1.json"
	a := archive(t, t0, 1, map[string]string{path: `{"queues":{"orders":{"attributes":{"DelaySeconds":"0","VisibilityTimeout":"30"}}}}`})
	b := archive(t, t0, 1, map[string]string{path: `{"queues":{"orders":{"attributes":{"DelaySeconds":"0","VisibilityTimeout":"60"}}}}`})
	assertDiff(t, diff(t, a, b, false),
		`~ services/sqs/000000000000/us-east-1.json /data/queues/orders/attributes/VisibilityTimeout: "30" -> "60"`)
	assertDiff(t, diff(t, a, b, true),
		`~ services/sqs/<account>/us-east-1.json#1 /data/queues/orders/attributes/VisibilityTimeout: "30" -> "60"`)
}

func TestDiffReportsAnAddedBucket(t *testing.T) {
	t.Parallel()
	a := archive(t, t0, 1, map[string]string{
		"services/s3/index.json":        `{"buckets":{"logs":"000000000000/us-east-1"}}`,
		"services/s3/buckets/logs.json": `{"versioning":"Enabled"}`,
	})
	b := archive(t, t0, 1, map[string]string{
		"services/s3/index.json":           `{"buckets":{"logs":"000000000000/us-east-1","uploads":"000000000000/eu-west-1"}}`,
		"services/s3/buckets/logs.json":    `{"versioning":"Enabled"}`,
		"services/s3/buckets/uploads.json": `{}`,
	})
	assertDiff(t, diff(t, a, b, false),
		`+ services/s3/buckets/uploads.json`,
		`+ services/s3/index.json /data/buckets/uploads: "000000000000/eu-west-1"`)
	assertDiff(t, diff(t, b, a, false),
		`- services/s3/buckets/uploads.json`,
		`- services/s3/index.json /data/buckets/uploads: "000000000000/eu-west-1"`)
}

// stsExport returns identity's persisted form after a seeded run under
// account mints a Lambda execution-role session for function, expiring at
// expiry.
func stsExport(t *testing.T, account, function, expiry string) string {
	t.Helper()
	acct, err := aws.ParseAccountID(account)
	if err != nil {
		t.Fatal(err)
	}
	fn, err := aws.ParseARN("arn:aws:lambda:us-east-1:" + account + ":function:" + function)
	if err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339, expiry)
	if err != nil {
		t.Fatal(err)
	}
	src := ids.NewSeeded(7)
	s := identity.New(src, acct)
	role := identity.AssumedRole{
		Account: acct,
		Role:    identity.RoleRef{Name: function + "-role", ID: string(src.RoleID())},
		Session: function,
		Source:  identity.FromExecutionEnvironment{Function: fn, Env: "env-1"},
	}
	if _, err := s.MintTemporary(role, at); err != nil {
		t.Fatal(err)
	}
	data, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Two runs of one workload under different accounts, hosts and wall clocks
// differ only in what the closed normalizer set masks.
func TestDiffNormalizeMasksRunSpecificValues(t *testing.T) {
	t.Parallel()
	a := archive(t, t0, 17, map[string]string{
		"services/sqs/111111111111/us-east-1.json": `{"queues":{"orders":{` +
			`"url":"http://sqs.us-east-1.localhost:4566/111111111111/orders",` +
			`"arn":"arn:aws:sqs:us-east-1:111111111111:orders",` +
			`"inflight":{"AQEBzZ0aAbC1dEf2gHi3jKl4mNo5pQr6sTu7":{"body":"a"},"AQEBqQ9wErT8yUi7oP6aS5dF4gH3jK2lZ1xC":{"body":"b"}},` +
			`"lastRequestId":"0b6d1c5e-2f3a-4b8c-9d0e-1f2a3b4c5d6e","cause":{"parent":12,"trace":3}}}}`,
		"services/sts/state.json": stsExport(t, "111111111111", "worker", "2026-10-09T13:00:01Z"),
		"services/logs/111111111111/us-east-1.json": `{"events":[` +
			`"START RequestId: 6f1c2a3b-0000-4000-8000-000000000001 Version: $LATEST",` +
			`"REPORT RequestId: 6f1c2a3b-0000-4000-8000-000000000001\tDuration: 12.34 ms\tBilled Duration: 13 ms\tMemory Size: 128 MB"]}`,
	})
	b := archive(t, "2026-10-09T12:07:13.5Z", 99, map[string]string{
		"services/sqs/222222222222/us-east-1.json": `{"queues":{"orders":{` +
			`"url":"http://127.0.0.1:39001/222222222222/orders",` +
			`"arn":"arn:aws:sqs:us-east-1:222222222222:orders",` +
			`"inflight":{"AQEBmMnNbBvVcCxXzZaAsSdDfFgGhHjJkKlL":{"body":"b"},"AQEBpPoOiIuUyYtTrReEwWqQ1234567890ab":{"body":"a"}},` +
			`"lastRequestId":"9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d","cause":{"parent":40,"trace":31}}}}`,
		"services/sts/state.json": stsExport(t, "222222222222", "worker", "2026-10-09T13:07:14Z"),
		"services/logs/222222222222/us-east-1.json": `{"events":[` +
			`"START RequestId: 0d0d0d0d-1111-4000-8000-000000000002 Version: $LATEST",` +
			`"REPORT RequestId: 0d0d0d0d-1111-4000-8000-000000000002\tDuration: 3.01 ms\tBilled Duration: 4 ms\tMemory Size: 128 MB"]}`,
	})
	assertDiff(t, diff(t, a, b, true))
	if got := diff(t, a, b, false); len(got) == 0 {
		t.Fatal("archives from different runs compare equal without normalization")
	}
	// Normalization masks only what it names: a changed body still shows.
	c := archive(t, t0, 17, map[string]string{"services/sqs/333333333333/us-east-1.json": `{"queues":{"orders":{"inflight":{"AQEBzZ0aAbC1dEf2gHi3jKl4mNo5pQr6sTu7":{"body":"c"}}}}}`})
	d := archive(t, t0, 17, map[string]string{"services/sqs/444444444444/us-east-1.json": `{"queues":{"orders":{"inflight":{"AQEBmMnNbBvVcCxXzZaAsSdDfFgGhHjJkKlL":{"body":"d"}}}}}`})
	assertDiff(t, diff(t, c, d, true),
		`~ services/sqs/<account>/us-east-1.json#1 /data/queues/orders/inflight/<receipt-handle>#1/body: "c" -> "d"`)
}

// sqsSection is an SQS section in the shape sqs.State serializes to, with
// one message sent by a root caller of account and enqueued by event
// enqueued, and received once under handle receipt.
func sqsSection(account, receipt string, enqueued int) string {
	return fmt.Sprintf(`{"queues":{"orders":{"name":"orders",`+
		`"created":"2026-10-09T12:00:00Z","modified":"2026-10-09T12:00:00Z",`+
		`"attributes":{"delaySeconds":0,"maximumMessageSize":262144,"messageRetentionPeriod":345600,`+
		`"receiveMessageWaitTimeSeconds":0,"visibilityTimeout":30,"sqsManagedSseEnabled":true,`+
		`"redrivePolicy":"{\"deadLetterTargetArn\":\"arn:aws:sqs:us-east-1:%[1]s:orders-dlq\",\"maxReceiveCount\":3}"},`+
		`"messages":[{"id":"5fd8c6a4-8a4e-4f6b-9a1f-2a3b4c5d6e7f","body":"order 1","bodyMD5":"2c6e0a1b3d4f5e6a7b8c9d0e1f2a3b4c",`+
		`"senderId":"%[1]s","enqueuedEventId":%[3]d,"sent":"2026-10-09T12:00:01Z","visibleAt":"2026-10-09T12:00:31Z",`+
		`"receiveCount":1,"firstReceived":"2026-10-09T12:00:01Z","receipt":"%[2]s"}]}}}`,
		account, receipt, enqueued)
}

// Two runs of one SQS workload under different accounts and journal
// interleavings differ in the sender account and the enqueuing event, which
// the normalizers mask as the state serializes them.
func TestDiffNormalizeMasksSQSMessageIdentity(t *testing.T) {
	t.Parallel()
	a := archive(t, t0, 18, map[string]string{
		"services/sqs/111111111111/us-east-1.json": sqsSection("111111111111", "AQEBzZ0aAbC1dEf2gHi3jKl4mNo5pQr6sTu7", 17),
	})
	b := archive(t, t0, 18, map[string]string{
		"services/sqs/222222222222/us-east-1.json": sqsSection("222222222222", "AQEBmMnNbBvVcCxXzZaAsSdDfFgGhHjJkKlL", 42),
	})
	assertDiff(t, diff(t, a, b, true))
	if got := diff(t, a, b, false); len(got) == 0 {
		t.Fatal("archives from different runs compare equal without normalization")
	}
}

// A lone masked key is numbered like the first of several, so a run with one
// more in-flight message shows one added value.
func TestDiffNormalizeLinesUpALoneMaskedKey(t *testing.T) {
	t.Parallel()
	const path = "services/sqs/000000000000/us-east-1.json"
	a := archive(t, t0, 1, map[string]string{path: `{"inflight":{"AQEBzZ0aAbC1dEf2gHi3jKl4mNo5pQr6sTu7":{"body":"a"}}}`})
	b := archive(t, t0, 1, map[string]string{path: `{"inflight":{` +
		`"AQEBmMnNbBvVcCxXzZaAsSdDfFgGhHjJkKlL":{"body":"a"},"AQEBpPoOiIuUyYtTrReEwWqQ1234567890ab":{"body":"b"}}}`})
	assertDiff(t, diff(t, a, b, true),
		`+ services/sqs/<account>/us-east-1.json#1 /data/inflight/<receipt-handle>#2: {"body":"b"}`)

	// Likewise for entries: a run with a second account adds one section.
	c := archive(t, t0, 1, map[string]string{"services/iam/111111111111.json": `{"roles":{}}`})
	d := archive(t, t0, 1, map[string]string{
		"services/iam/222222222222.json": `{"roles":{}}`,
		"services/iam/333333333333.json": `{"users":{}}`,
	})
	assertDiff(t, diff(t, c, d, true),
		`+ services/iam/<account>.json#2`)
}

// The S3 bucket index maps names to store.ScopeKeys values, whose account is
// masked; a bucket's own name is kept even when it is 12 digits.
func TestDiffNormalizeMasksScopeKeysButNotBucketNames(t *testing.T) {
	t.Parallel()
	a := archive(t, t0, 1, map[string]string{
		"services/s3/index.json":                `{"buckets":{"123456789012":"111111111111/us-east-1"}}`,
		"services/s3/buckets/123456789012.json": `{"versioning":"Enabled"}`,
	})
	b := archive(t, t0, 1, map[string]string{
		"services/s3/index.json":                `{"buckets":{"123456789012":"222222222222/us-east-1"}}`,
		"services/s3/buckets/123456789012.json": `{"versioning":"Suspended"}`,
	})
	assertDiff(t, diff(t, a, b, true),
		`~ services/s3/buckets/123456789012.json /data/versioning: "Enabled" -> "Suspended"`)
}

// Tag keys are the user's, not member names of state, so a tag named like a
// masked member still compares under normalization; its value's text is
// normalized as any string is.
func TestDiffNormalizeComparesTagsWhateverTheirKey(t *testing.T) {
	t.Parallel()
	const path = "services/sqs/000000000000/us-east-1.json"
	a := archive(t, t0, 1, map[string]string{path: `{"queues":{"orders":{"tags":{` +
		`"Expiry":"2026-12-31","LastRequestId":"x","Owner":"arn:aws:iam::111111111111:role/a"}}}}`})
	b := archive(t, t0, 1, map[string]string{path: `{"queues":{"orders":{"tags":{` +
		`"Expiry":"2027-06-30","LastRequestId":"y","Owner":"arn:aws:iam::222222222222:role/a"}}}}`})
	assertDiff(t, diff(t, a, b, true),
		`~ services/sqs/<account>/us-east-1.json#1 /data/queues/orders/tags/Expiry: "2026-12-31" -> "2027-06-30"`,
		`~ services/sqs/<account>/us-east-1.json#1 /data/queues/orders/tags/LastRequestId: "x" -> "y"`)

	// The names compare in any case: Lambda environment Variables and S3
	// user Metadata.
	const fn = "services/lambda/000000000000/us-east-1.json"
	c := archive(t, t0, 1, map[string]string{fn: `{"functions":{"f":{"Variables":{"RequestId":"a"},"METADATA":{"x-amz-meta-accountid":"111111111111"}}}}`})
	d := archive(t, t0, 1, map[string]string{fn: `{"functions":{"f":{"Variables":{"RequestId":"b"},"METADATA":{"x-amz-meta-accountid":"222222222222"}}}}`})
	assertDiff(t, diff(t, c, d, true),
		`~ services/lambda/<account>/us-east-1.json#1 /data/functions/f/METADATA/x-amz-meta-accountid: "111111111111" -> "222222222222"`,
		`~ services/lambda/<account>/us-east-1.json#1 /data/functions/f/Variables/RequestId: "a" -> "b"`)
}

// A record keyed by a user-chosen name that matches a user map's name, such
// as a queue called "tags" or an S3 object keyed "Metadata", still has its
// members masked by name, because a user map holds strings only.
func TestDiffNormalizeMasksARecordNamedLikeAUserMap(t *testing.T) {
	t.Parallel()
	const path = "services/sqs/000000000000/us-east-1.json"
	a := archive(t, t0, 1, map[string]string{path: `{"queues":{"tags":{"name":"tags","enqueuedEventId":17}},` +
		`"objects":{"Metadata":{"size":1,"lastRequestId":"r-1"}}}`})
	b := archive(t, t0, 1, map[string]string{path: `{"queues":{"tags":{"name":"tags","enqueuedEventId":42}},` +
		`"objects":{"Metadata":{"size":1,"lastRequestId":"r-2"}}}`})
	assertDiff(t, diff(t, a, b, true))
}

func TestDiffWithoutNormalizeShowsEveryRunSpecificValue(t *testing.T) {
	t.Parallel()
	const path = "services/sqs/000000000000/us-east-1.json"
	a := archive(t, t0, 17, map[string]string{path: `{"url":"http://localhost:4566/000000000000/q"}`})
	b := archive(t, "2026-10-09T12:00:05Z", 18, map[string]string{path: `{"url":"http://localhost:4570/000000000000/q"}`})
	assertDiff(t, diff(t, a, b, false),
		`~ manifest.json /clock/at: "2026-10-09T12:00:00Z" -> "2026-10-09T12:00:05Z"`,
		`~ manifest.json /journalNext: 17 -> 18`,
		`~ services/sqs/000000000000/us-east-1.json /data/url: "http://localhost:4566/000000000000/q" -> "http://localhost:4570/000000000000/q"`)
	assertDiff(t, diff(t, a, b, true))
}

// A frozen clock's time is emulator state, not a wall reading, so it is
// compared even under normalization.
func TestDiffNormalizeKeepsAFrozenClock(t *testing.T) {
	t.Parallel()
	a, b := archive(t, t0, 1, nil), archive(t, "2026-10-09T12:00:05Z", 1, nil)
	a.Manifest.Clock.Mode, b.Manifest.Clock.Mode = clock.Frozen{}, clock.Frozen{}
	assertDiff(t, diff(t, a, b, true),
		`~ manifest.json /clock/at: "2026-10-09T12:00:00Z" -> "2026-10-09T12:00:05Z"`)
}

func TestDiffComparesSectionVersionsBlobsAndNDJSON(t *testing.T) {
	t.Parallel()
	const items = "services/dynamodb/000000000000/us-east-1/locks.ndjson"
	a := archive(t, t0, 1, map[string]string{items: "{\"LockID\":\"a\"}\n{\"LockID\":\"b\"}\n"})
	b := archive(t, t0, 1, map[string]string{items: "{\"LockID\":\"a\"}\n{\"LockID\":\"c\"}\n{\"LockID\":\"d\"}\n"})
	bs := blobs{}
	body := bs.add(t, "body")
	b.Sections[0].Version = 2
	b.Sections[0].Blobs = []string{body}
	assertDiff(t, diff(t, a, b, false),
		`+ blobs/sha256/`+body,
		`+ `+items+` /blobs/0: "`+body+`"`,
		`~ `+items+` /data/1/LockID: "b" -> "c"`,
		`+ `+items+` /data/2: {"LockID":"d"}`,
		`~ `+items+` /version: 1 -> 2`)
}

// Diff compares archives Read returned; a manifest no archive could carry is
// an error, not a value difference.
func TestDiffRefusesAnInvalidManifest(t *testing.T) {
	t.Parallel()
	good, bad := archive(t, t0, 1, nil), archive(t, t0, 1, nil)
	bad.Manifest.Clock.Mode = nil
	for name, pair := range map[string][2]*snapshot.Archive{"first": {bad, good}, "second": {good, bad}} {
		_, err := snapshot.Diff(pair[0], pair[1], false)
		if !errors.As(err, new(*snapshot.ManifestError)) || !strings.Contains(err.Error(), "the "+name+" archive") {
			t.Errorf("%s archive invalid: error %v; want a *ManifestError naming the %s archive", name, err, name)
		}
	}
}
