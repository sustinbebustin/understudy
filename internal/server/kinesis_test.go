package server_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/coverage"
	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
	"github.com/sustinbebustin/understudy/internal/server"
)

// kinesisRequest is a Kinesis call as the AWS CLI sends it: POST /,
// awsJson1_1 and X-Amz-Target Kinesis_20131202.<Op>, signed for kinesis
// (kinesis.md 2.1, aws-doc CreateStream example).
func kinesisRequest(t *testing.T, op, contentType string, body []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost:4566/", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Amz-Target", "Kinesis_20131202."+op)
	req.Header.Set("X-Amz-Date", "20260101T000000Z")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260101/us-east-1/kinesis/aws4_request, SignedHeaders=host;x-amz-date, Signature=0")
	return req
}

func serveKinesis(t *testing.T, srv *server.Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// The wired emulator serves Kinesis: CreateStream answers 200 with an
// empty body ("HTTP 200 response with an empty HTTP body", aws-doc
// kinesis.md 2.1) and DescribeStreamSummary finds the stream (spec B1
// tracer bullet), where the edge answered 501 before.
func TestKinesisIsServed(t *testing.T) {
	srv, _ := build(t, server.Config{Docker: server.DockerDisabled{}, DynamoDB: server.DynamoDBDisabled{}})
	rec := serveKinesis(t, srv, kinesisRequest(t, "CreateStream", "application/x-amz-json-1.1", []byte(`{"StreamName":"s","ShardCount":1}`)))
	if body := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || body != "" {
		t.Fatalf("CreateStream = %d %s, want 200 with an empty body", rec.Code, rec.Body)
	}
	rec = serveKinesis(t, srv, kinesisRequest(t, "DescribeStreamSummary", "application/x-amz-json-1.1", []byte(`{"StreamName":"s"}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"StreamARN":"arn:aws:kinesis:us-east-1:000000000000:stream/s"`) {
		t.Errorf("DescribeStreamSummary = %d %s", rec.Code, rec.Body)
	}
}

// A CBOR request answers 501 in CBOR, so the Java client can parse it, a
// map of text strings {"__type": "NotImplemented", "message": ...} naming
// CBOR_ENABLED=false and -Daws.cborEnabled=false (kinesis.md 2.3, 13.4
// item 2, 13.9: never a generic parse error; contract 4; refusal golden 7:
// localstack@8b9a79f tests/aws/services/kinesis/test_kinesis.py:L775-L814,
// validated 2024-07-31, and
// localstack@8b9a79f tests/aws/services/kinesis/test_kinesis.snapshot.json:L230-L237,
// validated 2025-09-16).
func TestKinesisCBORAnswersNotImplementedInCBOR(t *testing.T) {
	srv, _ := build(t, server.Config{Docker: server.DockerDisabled{}, DynamoDB: server.DynamoDBDisabled{}})
	rec := serveKinesis(t, srv, kinesisRequest(t, "ListStreams", "application/x-amz-cbor-1.1", []byte{0xa0}))
	w := cbor.NewWriter()
	w.BeginMap()
	w.Key("__type")
	w.Text("NotImplemented")
	w.Key("message")
	w.Text("understudy does not implement the CBOR protocol for Amazon Kinesis; it serves JSON (application/x-amz-json-1.1). " +
		"Set CBOR_ENABLED=false in the environment or -Daws.cborEnabled=false on the JVM so the AWS SDK for Java sends JSON. Nothing was changed.")
	w.EndMap()
	want, err := w.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusNotImplemented || rec.Header().Get("Content-Type") != "application/x-amz-cbor-1.1" || !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("CBOR ListStreams = %d %q %x, want 501 application/x-amz-cbor-1.1 %x", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes(), want)
	}
}

// An HTTP/2 prior-knowledge preface, which net/http hands to the handler
// as method PRI and target *, answers 501 with a diagnostic naming the
// HTTP/1.1 client setting (kinesis.md 2.4, 13.9; contract 4).
func TestHTTP2PrefaceAnswersNotImplemented(t *testing.T) {
	srv, _ := build(t, server.Config{Docker: server.DockerDisabled{}, DynamoDB: server.DynamoDBDisabled{}})
	req := httptest.NewRequestWithContext(t.Context(), "PRI", "http://localhost:4566/", nil)
	req.RequestURI = "*"
	req.URL.Path = "*"
	req.Proto, req.ProtoMajor, req.ProtoMinor = "HTTP/2.0", 2, 0
	rec := serveKinesis(t, srv, req)
	want := "understudy speaks HTTP/1.1 only and received an HTTP/2 prior-knowledge preface (PRI * HTTP/2.0). Configure the client for HTTP/1.1: " +
		"with the AWS SDK for Java v2, build the async client with NettyNioAsyncHttpClient.builder().protocol(Protocol.HTTP1_1). Nothing was changed."
	if rec.Code != http.StatusNotImplemented || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" || rec.Body.String() != want {
		t.Errorf("PRI * = %d %q %q, want 501 text/plain %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String(), want)
	}
}

// The coverage report carries the 8b Kinesis notes exactly as the spec
// words them ("Coverage marks", Kinesis): 21 operations bound, enhanced
// monitoring stored-only, the rest partial; the 24 others unbound. The
// unbound list holds two refusal goldens: consumers and SubscribeToShard
// answer 501 (golden 8: localstack@8b9a79f
// tests/aws/services/kinesis/test_kinesis.snapshot.json:L89-L229; its
// L146-L188 validated 2025-08-19, the span's other dates are not in
// kinesis.md) and resource policies answer 501 (golden 9:
// localstack@8b9a79f tests/aws/services/kinesis/test_kinesis.snapshot.json:L239-L318,
// validated 2025-08-05).
func TestKinesisCoverageNotes(t *testing.T) {
	describe := "the optional fields of a fresh stream (EncryptionType NONE, StreamModeDetails, MaxRecordSizeInKiB 1024, no KeyId, WarmThroughput or ConsumerCount) and the not-found text over JSON are inferred; AWS's are UNVERIFIED (kinesis.md 4.4, finding 5)"
	tags := "a 51st tag and an aws: key answer InvalidArgumentException; AWS's code and text are UNVERIFIED (kinesis.md 4.10)"
	retention := "error texts follow kinesalite and are UNVERIFIED (kinesis.md 4.7)"
	monitoring := "shard-level metrics are stored and reported on the stream; no metrics are published to CloudWatch"
	partial := map[string]string{
		"CreateStream":                  "ON_DEMAND streams start with four shards and never scale; WarmThroughputMiBps and RecordDistributionStrategy AUTO are answered with 501 NotImplemented; a stream that would take the region's shards above 20000 is refused with LimitExceededException, whose text is understudy's and UNVERIFIED (kinesis.md 4.1), and the on-demand stream quota is not enforced; the stream is ACTIVE when CreateStream returns, where AWS reports CREATING first",
		"DeleteStream":                  "the stream is gone when DeleteStream returns, where AWS reports DELETING first",
		"DescribeStream":                describe,
		"DescribeStreamSummary":         describe,
		"ListShards":                    "the errors for NextToken combined with StreamName, StreamCreationTimestamp or ExclusiveStartShardId, or for neither stream nor token, follow kinesalite and are UNVERIFIED (kinesis.md 4.6)",
		"AddTagsToStream":               tags,
		"RemoveTagsFromStream":          tags,
		"ListTagsForStream":             tags,
		"TagResource":                   tags,
		"UntagResource":                 tags,
		"ListTagsForResource":           tags,
		"ListStreams":                   "names are listed in lexical order; AWS's order and token lifetime are UNVERIFIED (kinesis.md 4.5)",
		"IncreaseStreamRetentionPeriod": retention,
		"DecreaseStreamRetentionPeriod": retention,
		"PutRecord":                     "per-shard write limits (1 MiB/s, 1,000 records/s) are not enforced and ProvisionedThroughputExceededException comes only from fault injection; DryRun answers 501 NotImplemented; argument and record-size error texts follow kinesalite or are understudy's and are UNVERIFIED (kinesis.md 13.3)",
		"PutRecords":                    "per-shard write limits (1 MiB/s, 1,000 records/s) are not enforced; no entry fails: a fault rule fails the whole call, and entry-level fault injection is not built; argument, record-size and request-size error texts follow kinesalite or are understudy's and are UNVERIFIED (kinesis.md 13.3)",
		"GetShardIterator":              "the five calls per second per shard limit is not enforced; DryRun answers 501 NotImplemented; argument error texts follow kinesalite and are UNVERIFIED (kinesis.md 5.4, 13.3)",
		"GetRecords":                    "per-shard read limits (5 calls/s, 2 MiB/s and the 5-second wait after a 10 MiB read) are not enforced; DryRun answers 501 NotImplemented; the Limit and expired-iterator texts follow kinesalite or are understudy's and are UNVERIFIED (kinesis.md 5.6, 13.3)",
		"DescribeLimits":                "ShardLimit is 20000 and OnDemandStreamCountLimit 50 in every region; CreateStream enforces the shard limit, not the on-demand stream limit",
	}
	unbound := []string{
		"SplitShard", "MergeShards", "UpdateShardCount", "UpdateStreamMode", "UpdateMaxRecordSize", "StartStreamEncryption", "StopStreamEncryption",
		"RegisterStreamConsumer", "DescribeStreamConsumer", "ListStreamConsumers", "DeregisterStreamConsumer", "PutResourcePolicy", "GetResourcePolicy",
		"DeleteResourcePolicy", "SubscribeToShard", "UpdateStreamWarmThroughput", "UpdateStreamRecordDistributionStrategy", "DescribeAccountSettings",
		"UpdateAccountSettings", "CreateChannel", "DeleteChannel", "DescribeChannel", "ListChannels", "UpdateChannel",
	}
	got := map[string]coverage.Status{}
	for _, s := range wiredReport(t).Services {
		if s.Service != "kinesis" {
			continue
		}
		for _, op := range s.Operations {
			got[op.Name] = op.Status
		}
	}
	if len(got) != 45 {
		t.Errorf("the report lists %d Kinesis operations, want the model's 45 (kinesis.md 13.1)", len(got))
	}
	for op, note := range partial {
		p, ok := got[op].(coverage.Partial)
		if !ok || p.Note != note {
			t.Errorf("%s = %#v, want partial %q", op, got[op], note)
		}
	}
	for _, op := range []string{"EnableEnhancedMonitoring", "DisableEnhancedMonitoring"} {
		if s, ok := got[op].(coverage.StoredOnly); !ok || s.Note != monitoring {
			t.Errorf("%s = %#v, want stored-only %q", op, got[op], monitoring)
		}
	}
	for _, op := range unbound {
		if _, ok := got[op].(coverage.Unbound); !ok {
			t.Errorf("%s = %#v, want unbound", op, got[op])
		}
	}
}

// The five 8b switches are reported by the wired emulator (spec "Fidelity
// switches"; contract 3.8).
func TestKinesisSwitchesAreReported(t *testing.T) {
	names := map[string]bool{}
	for _, sw := range wiredReport(t).Switches {
		names[sw.Name] = true
	}
	for _, n := range []string{
		"kinesis.hash.even-split",
		"kinesis.hash.md5-byte-order",
		"kinesis.sequence.layout",
		"kinesis.get-records.millis-behind-latest",
		"kinesis.retention.equal-value",
	} {
		if !names[n] {
			t.Errorf("switch %s is not reported", n)
		}
	}
}
