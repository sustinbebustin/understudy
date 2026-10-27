package server_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/server"
)

// An S3 notification to a queue needs one statement in the queue's policy:
// s3.amazonaws.com may call sqs:SendMessage with aws:SourceArn the bucket's
// ARN and aws:SourceAccount its owner's account, which S3 always sends
// (iam-enforcement-surfaces.md 8.1, 12.6, aws-doc). S3 checks that the
// destination exists by reading it (8.3), not through a call any policy
// grants, so that lookup runs as the platform: the journal marks it so, and
// neither GET /iam/usage nor the resourcePolicies of GET /iam/policy lists
// it (ARCHITECTURE.md 16).
func TestAnS3NotificationNeedsOnlySendMessageOnTheQueue(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	const (
		bucket   = "uploads"
		queueARN = "arn:aws:sqs:us-east-1:000000000000:uploads"
		root     = "arn:aws:iam::000000000000:root"
	)
	url := endpoint + "/000000000000/uploads"
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"uploads"}`)
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"s3.amazonaws.com"},"Action":"sqs:SendMessage",` +
		`"Resource":"` + queueARN + `","Condition":{"ArnLike":{"aws:SourceArn":"arn:aws:s3:::` + bucket + `"}}}]}`
	attrs, err := json.Marshal(map[string]any{"QueueUrl": url, "Attributes": map[string]string{"Policy": policy}})
	if err != nil {
		t.Fatal(err)
	}
	mustSQS(t, endpoint, "SetQueueAttributes", string(attrs))

	wantS3 := func(what string, got reply) {
		t.Helper()
		if got.status != http.StatusOK {
			t.Fatalf("%s = %d %s", what, got.status, got.body)
		}
	}
	wantS3("CreateBucket", s3Call(t, dummy, http.MethodPut, endpoint+"/"+bucket, ""))
	notification := `<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><QueueConfiguration>` +
		`<Queue>` + queueARN + `</Queue><Event>s3:ObjectCreated:*</Event></QueueConfiguration></NotificationConfiguration>`
	wantS3("PutBucketNotificationConfiguration", s3Call(t, dummy, http.MethodPut, endpoint+"/"+bucket+"?notification", notification))
	wantS3("PutObject", s3Call(t, dummy, http.MethodPut, endpoint+"/"+bucket+"/in/a.txt", "abc"))
	if settled, err := c.Settle(t.Context(), 10*time.Second); err != nil || !settled.Settled {
		t.Fatalf("settle = %+v, %v", settled, err)
	}

	got, err := c.IAMPolicy(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := []admin.ResourcePolicy{{Target: queueARN, Principal: "s3.amazonaws.com", Action: "sqs:SendMessage", Source: "arn:aws:s3:::" + bucket, SourceAccount: "000000000000"}}
	if len(got.ResourcePolicies) != len(want) || got.ResourcePolicies[0] != want[0] {
		t.Errorf("resourcePolicies = %+v, want exactly %+v", got.ResourcePolicies, want)
	}

	uses, err := c.IAMUsage(t.Context(), admin.UsageQuery{Principal: "s3.amazonaws.com"})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range uses {
		if u.Action != "sqs:SendMessage" || u.Resource != queueARN {
			t.Errorf("s3.amazonaws.com usage lists %s on %s, want only sqs:SendMessage on the queue", u.Action, u.Resource)
		}
	}

	probes, err := c.JournalEvents(t.Context(), admin.JournalQuery{Operation: "sqs.GetQueueUrl", Kind: "api-call"})
	if err != nil {
		t.Fatal(err)
	}
	if len(probes.Events) == 0 {
		t.Fatal("the journal holds no destination lookup (sqs.GetQueueUrl)")
	}
	for _, e := range probes.Events {
		var call struct {
			Principal string `json:"principal"`
			Mode      string `json:"mode"`
		}
		if err := json.Unmarshal(e.Detail, &call); err != nil {
			t.Fatal(err)
		}
		if call.Mode != "platform:s3.amazonaws.com" || call.Principal != "s3.amazonaws.com" {
			t.Errorf("destination lookup journaled with mode %q principal %q, want platform:s3.amazonaws.com as s3.amazonaws.com", call.Mode, call.Principal)
		}
	}
}
