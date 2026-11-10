package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/server"
)

// mustKinesis sends one signed awsJson1_1 Kinesis request as the default
// account's root and returns the body; any status but 200 fails the test.
func mustKinesis(t *testing.T, endpoint, target, body string) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "Kinesis_20131202."+target)
	region, err := aws.KnownRegions().Parse("us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	auth.Sign(req, []byte(body), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: region, Service: "kinesis"}, time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s = %d %s", target, resp.StatusCode, data)
	}
	return data
}

// `understudy resources` lists each stream as kinesis, its ARN and
// ACTIVE; a snapshot saved, the server reset, and the snapshot
// loaded bring back the stream and its record's data, which the archive
// carries as a blob.
func TestAdminSnapshotKeepsKinesisRecords(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	mustKinesis(t, endpoint, "CreateStream", `{"StreamName":"s","ShardCount":1}`)
	mustKinesis(t, endpoint, "PutRecord", `{"StreamName":"s","PartitionKey":"k","Data":"aGVsbG8="}`)

	isKinesis := func(r admin.Resource) bool { return r.Service == "kinesis" }
	res, err := c.Resources(t.Context(), admin.ResourceQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want := admin.Resource{Service: "kinesis", Account: "000000000000", Region: "us-east-1", ARN: "arn:aws:kinesis:us-east-1:000000000000:stream/s", Summary: "ACTIVE"}
	if i := slices.IndexFunc(res, isKinesis); i < 0 || res[i] != want {
		t.Fatalf("resources = %+v, want %+v among them", res, want)
	}

	var archive bytes.Buffer
	if _, err := c.SaveSnapshot(t.Context(), &archive, admin.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Reset(t.Context(), admin.ResetRequest{}); err != nil {
		t.Fatal(err)
	}
	if res, err := c.Resources(t.Context(), admin.ResourceQuery{}); err != nil || slices.ContainsFunc(res, isKinesis) {
		t.Fatalf("resources after reset = %+v, %v", res, err)
	}
	info, err := c.LoadSnapshot(t.Context(), &archive)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(info.Services, "kinesis") {
		t.Fatalf("loaded services = %v, want kinesis among them", info.Services)
	}

	var it struct{ ShardIterator string }
	if err := json.Unmarshal(mustKinesis(t, endpoint, "GetShardIterator", `{"StreamName":"s","ShardId":"shardId-000000000000","ShardIteratorType":"TRIM_HORIZON"}`), &it); err != nil {
		t.Fatal(err)
	}
	var page struct{ Records []struct{ Data []byte } }
	if err := json.Unmarshal(mustKinesis(t, endpoint, "GetRecords", `{"ShardIterator":"`+it.ShardIterator+`"}`), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || string(page.Records[0].Data) != "hello" {
		t.Errorf("after load GetRecords = %+v, want the one record hello", page.Records)
	}
}
