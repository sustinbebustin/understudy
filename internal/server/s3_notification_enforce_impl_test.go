package server_test

import (
	"encoding/base64"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/server"
)

// S3 under iam.enforce: PutBucketNotificationConfiguration asks whether
// each destination's resource policy lets s3.amazonaws.com deliver from
// the bucket, with the keys a delivery sends (iam-enforcement-surfaces.md
// 8.1, 8.3, 12.4; IAM-ENF-20).

const (
	notifyQueueARN    = "arn:aws:sqs:us-east-1:000000000000:nq"
	notifyTopicARN    = "arn:aws:sns:us-east-1:000000000000:nt"
	notifyFunctionARN = "arn:aws:lambda:us-east-1:000000000000:function:nfn"
)

// notificationServer runs a server under mode holding the bucket enf-b and,
// none of them with a policy that admits S3, the queue nq, the topic nt
// with its default policy, and the function nfn.
func notificationServer(t *testing.T, mode server.IAMEnforce) (c *admin.Client, endpoint string) {
	t.Helper()
	c, endpoint, _ = enforced(t, mode)
	if w := s3Call(t, dummy, http.MethodPut, endpoint+"/enf-b", ""); w.status != http.StatusOK {
		t.Fatalf("CreateBucket = %d %s", w.status, w.body)
	}
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"nq"}`)
	mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"nt"}})
	iamAsRoot(t, endpoint, url.Values{"Action": {"CreateRole"}, "RoleName": {"nfn"}, "AssumeRolePolicyDocument": {trustOf("lambda.amazonaws.com")}})
	pkg := base64.StdEncoding.EncodeToString(zipped(t, "unused"))
	body := `{"FunctionName":"nfn","Runtime":"provided.al2023","Handler":"bootstrap","Role":"arn:aws:iam::000000000000:role/nfn","Code":{"ZipFile":"` + pkg + `"}}`
	if w := lambdaCall(t, dummy, endpoint, http.MethodPost, "/2015-03-31/functions", body); w.status != http.StatusCreated {
		t.Fatalf("CreateFunction = %d %s", w.status, w.body)
	}
	return c, endpoint
}

func queueNotification(arn string) string {
	return `<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><QueueConfiguration><Queue>` + arn +
		`</Queue><Event>s3:ObjectCreated:*</Event></QueueConfiguration></NotificationConfiguration>`
}

func topicNotification(arn string) string {
	return `<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TopicConfiguration><Topic>` + arn +
		`</Topic><Event>s3:ObjectCreated:*</Event></TopicConfiguration></NotificationConfiguration>`
}

func functionNotification(arn string) string {
	return `<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><CloudFunctionConfiguration><CloudFunction>` + arn +
		`</CloudFunction><Event>s3:ObjectCreated:*</Event></CloudFunctionConfiguration></NotificationConfiguration>`
}

// putNotification sends PutBucketNotificationConfiguration for enf-b as
// root, with x-amz-skip-destination-validation: true when skip.
func putNotification(t *testing.T, endpoint, body string, skip bool) reply {
	t.Helper()
	req := newRequest(t, http.MethodPut, endpoint+"/enf-b?notification", body)
	if skip {
		req.Header.Set("X-Amz-Skip-Destination-Validation", "true")
	}
	auth.Sign(req, []byte(body), dummy.scoped("s3"), time.Now())
	return sigv4Do(t, req)
}

// errorElements are the child elements of an S3 error body in order, as
// name=text, without RequestId and HostId.
func errorElements(t *testing.T, body string) []string {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(body))
	var (
		out   []string
		depth int
		name  string
		text  strings.Builder
	)
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("error body %q: %v", body, err)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			depth++
			name = tok.Name.Local
			text.Reset()
		case xml.CharData:
			text.Write(tok)
		case xml.EndElement:
			if depth == 2 && name != "RequestId" && name != "HostId" {
				out = append(out, name+"="+text.String())
			}
			depth--
		}
	}
}

// wantRefused checks w is the recorded 400 whose body, request and host
// ids aside, is exactly want's elements.
func wantRefused(t *testing.T, what string, w reply, want ...string) {
	t.Helper()
	got := errorElements(t, w.body)
	want = append([]string{"Code=InvalidArgument", "Message=Unable to validate the following destination configurations"}, want...)
	if w.status != http.StatusBadRequest || !slices.Equal(got, want) {
		t.Errorf("%s = %d %q, want 400 %q", what, w.status, got, want)
	}
}

// Under enforce a destination whose policy does not admit S3 refuses the
// configuration with the recorded answers
// (testdata/parity/s3/IAM-ENF/20-queue.json, 20-topic.json,
// 20-function.json; account, Region and names swapped for understudy's):
// a queue without a policy, a topic with its default policy and a function
// without a permission. The configuration is not stored.
func TestNotificationDestinationPolicyRefusesUnderEnforce(t *testing.T) {
	_, endpoint := notificationServer(t, server.IAMEnforceEnforce)
	wantRefused(t, "the queue", putNotification(t, endpoint, queueNotification(notifyQueueARN), false),
		"ArgumentName1="+notifyQueueARN, "ArgumentValue1=Permissions on the destination queue do not allow S3 to publish notifications from this bucket")
	wantRefused(t, "the topic", putNotification(t, endpoint, topicNotification(notifyTopicARN), false),
		"ArgumentName1="+notifyTopicARN, "ArgumentValue1=Permissions on the destination topic do not allow S3 to publish notifications from this bucket")
	wantRefused(t, "the function", putNotification(t, endpoint, functionNotification(notifyFunctionARN), false),
		"ArgumentName1="+notifyFunctionARN+", null", "ArgumentValue1=Not authorized to invoke function ["+notifyFunctionARN+"]")
	if w := s3Call(t, dummy, http.MethodGet, endpoint+"/enf-b?notification", ""); strings.Contains(w.body, "arn:") {
		t.Errorf("GetBucketNotificationConfiguration = %s, want no refused destination stored", w.body)
	}
}

// A destination whose policy admits s3.amazonaws.com from the bucket is
// accepted, and x-amz-skip-destination-validation skips the permission
// check as it skips the existence check (s3.md 13.1).
func TestNotificationDestinationPolicyAdmitsS3OrIsSkipped(t *testing.T) {
	_, endpoint := notificationServer(t, server.IAMEnforceEnforce)
	if w := putNotification(t, endpoint, queueNotification(notifyQueueARN), true); w.status != http.StatusOK {
		t.Errorf("the queue with the skip header = %d %s, want 200", w.status, w.body)
	}

	fromBucket := `"Condition":{"ArnLike":{"aws:SourceArn":"arn:aws:s3:::enf-b"},"StringEquals":{"aws:SourceAccount":"000000000000"}}`
	setQueuePolicy(t, endpoint, "nq", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"s3.amazonaws.com"},"Action":"sqs:SendMessage","Resource":"`+notifyQueueARN+`",`+fromBucket+`}]}`)
	mustSNS(t, endpoint, url.Values{"Action": {"SetTopicAttributes"}, "TopicArn": {notifyTopicARN}, "AttributeName": {"Policy"}, "AttributeValue": {
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"s3.amazonaws.com"},"Action":"sns:Publish","Resource":"` + notifyTopicARN + `",` + fromBucket + `}]}`,
	}})
	permission := `{"StatementId":"s3","Action":"lambda:InvokeFunction","Principal":"s3.amazonaws.com","SourceArn":"arn:aws:s3:::enf-b","SourceAccount":"000000000000"}`
	if w := lambdaCall(t, dummy, endpoint, http.MethodPost, "/2015-03-31/functions/nfn/policy", permission); w.status != http.StatusCreated {
		t.Fatalf("AddPermission = %d %s", w.status, w.body)
	}
	for what, body := range map[string]string{
		"the queue":    queueNotification(notifyQueueARN),
		"the topic":    topicNotification(notifyTopicARN),
		"the function": functionNotification(notifyFunctionARN),
	} {
		if w := putNotification(t, endpoint, body, false); w.status != http.StatusOK {
			t.Errorf("%s admitting S3 = %d %s, want 200", what, w.status, w.body)
		}
	}
}

// Under advise the configuration is stored and each destination's refusal
// journaled; under off nothing is asked.
func TestNotificationDestinationPolicyUnderAdviseAndOff(t *testing.T) {
	both := `<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TopicConfiguration><Topic>` + notifyTopicARN +
		`</Topic><Event>s3:ObjectRemoved:*</Event></TopicConfiguration><QueueConfiguration><Queue>` + notifyQueueARN +
		`</Queue><Event>s3:ObjectCreated:*</Event></QueueConfiguration></NotificationConfiguration>`
	t.Run("advise", func(t *testing.T) {
		c, endpoint := notificationServer(t, server.IAMEnforceAdvise)
		if w := putNotification(t, endpoint, both, false); w.status != http.StatusOK {
			t.Fatalf("Put under advise = %d %s, want 200", w.status, w.body)
		}
		advised := enforcementAdvisories(t, c, "access-would-deny")
		for _, want := range []string{"sqs:SendMessage", "sns:Publish"} {
			if !slices.ContainsFunc(advised, func(s string) bool { return strings.Contains(s, "s3.amazonaws.com") && strings.Contains(s, want) }) {
				t.Errorf("access-would-deny advisories = %q, want one for s3.amazonaws.com and %s", advised, want)
			}
		}
	})
	t.Run("off", func(t *testing.T) {
		c, endpoint := notificationServer(t, server.IAMEnforceOff)
		if w := putNotification(t, endpoint, both, false); w.status != http.StatusOK {
			t.Fatalf("Put under off = %d %s, want 200", w.status, w.body)
		}
		if got := enforcementAdvisories(t, c, "access-would-deny"); len(got) != 0 {
			t.Errorf("off journaled access-would-deny advisories %q, want none", got)
		}
	})
}
