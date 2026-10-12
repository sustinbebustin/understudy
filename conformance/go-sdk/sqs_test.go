package gosdk_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Research: docs/research/phase1/sqs.md, error-matrix.md 5.1,
// sigv4-checksums-endpoints.md 4.7.

// createQueue creates a queue and deletes it when the test ends.
func createQueue(t *testing.T, queueName string, attrs map[string]string) string {
	t.Helper()
	client := sqs.NewFromConfig(awsCfg)
	out, err := client.CreateQueue(t.Context(), &sqs.CreateQueueInput{QueueName: aws.String(queueName), Attributes: attrs})
	ok(t, err, "CreateQueue "+queueName)
	t.Cleanup(func() { _, _ = client.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: out.QueueUrl}) })
	return aws.ToString(out.QueueUrl)
}

// queueArn returns the ARN of the queue at queueURL.
func queueArn(t *testing.T, queueURL string) string {
	t.Helper()
	out, err := sqs.NewFromConfig(awsCfg).GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(queueURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	ok(t, err, "GetQueueAttributes QueueArn")
	return out.Attributes["QueueArn"]
}

// queueAttrs returns every attribute of the queue.
func queueAttrs(t *testing.T, queueURL string) map[string]string {
	t.Helper()
	out, err := sqs.NewFromConfig(awsCfg).GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(queueURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameAll},
	})
	ok(t, err, "GetQueueAttributes All")
	return out.Attributes
}

// receive collects messages from the queue until it has want of them or
// timeout passes, long polling one second at a time. It does not delete
// them.
func receive(t *testing.T, queueURL string, want int, timeout time.Duration) []types.Message {
	t.Helper()
	client := sqs.NewFromConfig(awsCfg)
	var got []types.Message
	deadline := time.Now().Add(timeout)
	for len(got) < want && time.Now().Before(deadline) {
		out, err := client.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(queueURL),
			MaxNumberOfMessages:         10,
			WaitTimeSeconds:             1,
			MessageAttributeNames:       []string{"All"},
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
		})
		ok(t, err, "ReceiveMessage")
		got = append(got, out.Messages...)
	}
	return got
}

// drain receives and deletes every message the queue delivers within
// timeout, returning them.
func drain(t *testing.T, queueURL string, timeout time.Duration) []types.Message {
	t.Helper()
	client := sqs.NewFromConfig(awsCfg)
	var got []types.Message
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := client.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
			MessageAttributeNames:       []string{"All"},
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
		})
		ok(t, err, "ReceiveMessage")
		for _, m := range out.Messages {
			_, err := client.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: m.ReceiptHandle})
			ok(t, err, "DeleteMessage")
		}
		got = append(got, out.Messages...)
	}
	return got
}

// bodies returns the bodies of messages, in order.
func bodies(msgs []types.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, aws.ToString(m.Body))
	}
	return out
}

func md5Hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// sqsAttributesMD5 is the MD5 SQS reports over message attributes, built
// as the SQS developer guide and the Java SDK's reference implementation
// describe (sqs.md 6.3).
func sqsAttributesMD5(attrs map[string]types.MessageAttributeValue) string {
	var buf bytes.Buffer
	field := func(b []byte) {
		_ = binary.Write(&buf, binary.BigEndian, uint32(len(b)))
		buf.Write(b)
	}
	for _, k := range slices.Sorted(maps.Keys(attrs)) {
		v := attrs[k]
		field([]byte(k))
		field([]byte(aws.ToString(v.DataType)))
		if strings.HasPrefix(aws.ToString(v.DataType), "Binary") {
			buf.WriteByte(2)
			field(v.BinaryValue)
		} else {
			buf.WriteByte(1)
			field([]byte(aws.ToString(v.StringValue)))
		}
	}
	return md5Hex(buf.Bytes())
}

func str(dataType, value string) types.MessageAttributeValue {
	return types.MessageAttributeValue{DataType: aws.String(dataType), StringValue: aws.String(value)}
}

// TestSQSAttributeMD5Vectors keeps the suite's own MD5 helper honest
// against vectors recorded from AWS (sqs.md 6.3); it needs no server.
func TestSQSAttributeMD5Vectors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		attrs map[string]types.MessageAttributeValue
		want  string
	}{
		{map[string]types.MessageAttributeValue{"k": str("String", "x")}, "a45daa70926828a2f0a1db3418e6feb4"},
		{map[string]types.MessageAttributeValue{"General": str("String", "Kenobi"), "Hello": str("String", "There"), "Help.Me": str("String", "Me")}, "4c360f3fdafd970e05fae2f149d997f5"},
		{map[string]types.MessageAttributeValue{"timestamp": str("Number", "1493147359900")}, "235c5c510d26fb653d073faed50ae77c"},
		{map[string]types.MessageAttributeValue{"attr1": {DataType: aws.String("Binary"), BinaryValue: []byte("traceparent\x1e00-774062d6c37081a5a0b9b5b88e30627c-2d2482211f6489da-01")}}, "8cbe4db156db8a94db8b801b7addb984"},
	} {
		if got := sqsAttributesMD5(tc.attrs); got != tc.want {
			t.Errorf("sqsAttributesMD5(%v) = %s, want %s", slices.Sorted(maps.Keys(tc.attrs)), got, tc.want)
		}
	}
}

func TestSQSQueueLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sqs.NewFromConfig(awsCfg)
	acct := account(t)
	queueName := name("lifecycle")
	queueURL := createQueue(t, queueName, nil)

	if !strings.HasSuffix(queueURL, "/"+acct+"/"+queueName) {
		t.Errorf("QueueUrl = %q, want it to end in /%s/%s", queueURL, acct, queueName)
	}

	t.Run("default attributes", func(t *testing.T) {
		attrs := queueAttrs(t, queueURL)
		wantKeys := []string{
			"ApproximateNumberOfMessages", "ApproximateNumberOfMessagesDelayed", "ApproximateNumberOfMessagesNotVisible",
			"CreatedTimestamp", "DelaySeconds", "LastModifiedTimestamp", "MaximumMessageSize", "MessageRetentionPeriod",
			"QueueArn", "ReceiveMessageWaitTimeSeconds", "SqsManagedSseEnabled", "VisibilityTimeout",
		}
		if got := slices.Sorted(maps.Keys(attrs)); !slices.Equal(got, wantKeys) {
			t.Errorf("All attributes of a fresh standard queue = %v, want exactly %v", got, wantKeys)
		}
		want := map[string]string{
			"ApproximateNumberOfMessages": "0", "ApproximateNumberOfMessagesDelayed": "0", "ApproximateNumberOfMessagesNotVisible": "0",
			"DelaySeconds": "0", "MaximumMessageSize": "1048576", "MessageRetentionPeriod": "345600",
			"QueueArn": "arn:aws:sqs:" + region() + ":" + acct + ":" + queueName, "ReceiveMessageWaitTimeSeconds": "0",
			"SqsManagedSseEnabled": "true", "VisibilityTimeout": "30",
		}
		for k, v := range want {
			if attrs[k] != v {
				t.Errorf("attribute %s = %q, want %q", k, attrs[k], v)
			}
		}
		for _, k := range []string{"CreatedTimestamp", "LastModifiedTimestamp"} {
			sec, err := strconv.ParseInt(attrs[k], 10, 64)
			if err != nil || time.Since(time.Unix(sec, 0)).Abs() > time.Hour {
				t.Errorf("%s = %q, want epoch seconds near now", k, attrs[k])
			}
		}
	})

	t.Run("named attributes only", func(t *testing.T) {
		out, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
			QueueUrl:       aws.String(queueURL),
			AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameVisibilityTimeout, types.QueueAttributeNameDelaySeconds},
		})
		ok(t, err, "GetQueueAttributes")
		if got := slices.Sorted(maps.Keys(out.Attributes)); !slices.Equal(got, []string{"DelaySeconds", "VisibilityTimeout"}) {
			t.Errorf("GetQueueAttributes(VisibilityTimeout, DelaySeconds) = %v", got)
		}
		_, err = client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: aws.String(queueURL), AttributeNames: []types.QueueAttributeName{"Foobar"}})
		errAs[*types.InvalidAttributeName](t, err, "GetQueueAttributes of an unknown name")
		wantMessage(t, err, "GetQueueAttributes of an unknown name", "Unknown Attribute Foobar.")
	})

	t.Run("CreateQueue idempotency", func(t *testing.T) {
		again, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(queueName)})
		ok(t, err, "CreateQueue again without attributes")
		if aws.ToString(again.QueueUrl) != queueURL {
			t.Errorf("CreateQueue again returned %q, want %q", aws.ToString(again.QueueUrl), queueURL)
		}
		same, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(queueName), Attributes: map[string]string{"VisibilityTimeout": "30"}})
		ok(t, err, "CreateQueue again with an equal attribute")
		if aws.ToString(same.QueueUrl) != queueURL {
			t.Errorf("CreateQueue with an equal attribute returned %q", aws.ToString(same.QueueUrl))
		}
		_, err = client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(queueName), Attributes: map[string]string{"DelaySeconds": "5"}})
		e := typedErr[*types.QueueNameExists](t, err, "CreateQueue with a different attribute")
		if e.ErrorCode() != "QueueAlreadyExists" {
			t.Errorf("error code = %q, want QueueAlreadyExists", e.ErrorCode())
		}
		wantStatus(t, err, "CreateQueue with a different attribute", 400)
		wantMessage(t, err, "CreateQueue with a different attribute", "A queue already exists with the same name and a different value for attribute DelaySeconds")
	})

	t.Run("GetQueueUrl and ListQueues", func(t *testing.T) {
		got, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(queueName)})
		ok(t, err, "GetQueueUrl")
		if aws.ToString(got.QueueUrl) != queueURL {
			t.Errorf("GetQueueUrl = %q, want %q", aws.ToString(got.QueueUrl), queueURL)
		}
		_, err = client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name("missing"))})
		e := typedErr[*types.QueueDoesNotExist](t, err, "GetQueueUrl of a missing queue")
		if e.ErrorCode() != "AWS.SimpleQueueService.NonExistentQueue" {
			t.Errorf("error code = %q, want AWS.SimpleQueueService.NonExistentQueue", e.ErrorCode())
		}
		wantStatus(t, err, "GetQueueUrl of a missing queue", 400)
		wantMessage(t, err, "GetQueueUrl of a missing queue", "The specified queue does not exist.")

		prefix := name("list")
		createQueue(t, prefix+"-a", nil)
		createQueue(t, prefix+"-b", nil)
		list, err := client.ListQueues(ctx, &sqs.ListQueuesInput{QueueNamePrefix: aws.String(prefix)})
		ok(t, err, "ListQueues")
		if len(list.QueueUrls) != 2 {
			t.Errorf("ListQueues(prefix) = %v, want the 2 queues", list.QueueUrls)
		}
		page, err := client.ListQueues(ctx, &sqs.ListQueuesInput{QueueNamePrefix: aws.String(prefix), MaxResults: aws.Int32(1)})
		ok(t, err, "ListQueues MaxResults 1")
		if len(page.QueueUrls) != 1 || page.NextToken == nil {
			t.Errorf("ListQueues MaxResults 1 = %v with NextToken %v, want one URL and a token", page.QueueUrls, page.NextToken)
		}
		none, err := client.ListQueues(ctx, &sqs.ListQueuesInput{QueueNamePrefix: aws.String(name("nothing"))})
		ok(t, err, "ListQueues with no match")
		if len(none.QueueUrls) != 0 {
			t.Errorf("ListQueues with no match = %v", none.QueueUrls)
		}
	})

	t.Run("SetQueueAttributes", func(t *testing.T) {
		_, err := client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{QueueUrl: aws.String(queueURL), Attributes: map[string]string{"VisibilityTimeout": "45", "DelaySeconds": "1"}})
		ok(t, err, "SetQueueAttributes")
		attrs := queueAttrs(t, queueURL)
		if attrs["VisibilityTimeout"] != "45" || attrs["DelaySeconds"] != "1" {
			t.Errorf("after SetQueueAttributes VisibilityTimeout %q, DelaySeconds %q", attrs["VisibilityTimeout"], attrs["DelaySeconds"])
		}
		_, err = client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{QueueUrl: aws.String(queueURL), Attributes: map[string]string{"DelaySeconds": "1000"}})
		errAs[*types.InvalidAttributeValue](t, err, "SetQueueAttributes DelaySeconds 1000")
		wantMessage(t, err, "SetQueueAttributes DelaySeconds 1000", "Invalid value for the parameter DelaySeconds.")
		_, err = client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{QueueUrl: aws.String(queueURL), Attributes: map[string]string{"FifoQueue": "true"}})
		errAs[*types.InvalidAttributeName](t, err, "SetQueueAttributes FifoQueue on a standard queue")
		wantMessage(t, err, "SetQueueAttributes FifoQueue on a standard queue", "Unknown Attribute FifoQueue.")

		policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"*"}]}`
		_, err = client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{QueueUrl: aws.String(queueURL), Attributes: map[string]string{"Policy": policy}})
		ok(t, err, "SetQueueAttributes Policy")
		wantJSONEqual(t, "Policy attribute", queueAttrs(t, queueURL)["Policy"], policy)
		_, err = client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{QueueUrl: aws.String(queueURL), Attributes: map[string]string{"Policy": ""}})
		ok(t, err, "SetQueueAttributes Policy empty")
		if p, present := queueAttrs(t, queueURL)["Policy"]; present {
			t.Errorf("Policy %q is still listed after setting it to the empty string", p)
		}
	})

	t.Run("tags", func(t *testing.T) {
		_, err := client.TagQueue(ctx, &sqs.TagQueueInput{QueueUrl: aws.String(queueURL), Tags: map[string]string{"team": "platform", "Team": "other", "empty": ""}})
		ok(t, err, "TagQueue")
		tags, err := client.ListQueueTags(ctx, &sqs.ListQueueTagsInput{QueueUrl: aws.String(queueURL)})
		ok(t, err, "ListQueueTags")
		if !maps.Equal(tags.Tags, map[string]string{"team": "platform", "Team": "other", "empty": ""}) {
			t.Errorf("ListQueueTags = %v", tags.Tags)
		}
		_, err = client.TagQueue(ctx, &sqs.TagQueueInput{QueueUrl: aws.String(queueURL), Tags: map[string]string{"team": "core"}})
		ok(t, err, "TagQueue overwrite")
		_, err = client.UntagQueue(ctx, &sqs.UntagQueueInput{QueueUrl: aws.String(queueURL), TagKeys: []string{"Team", "empty", "never-set"}})
		ok(t, err, "UntagQueue including an unknown key")
		tags, err = client.ListQueueTags(ctx, &sqs.ListQueueTagsInput{QueueUrl: aws.String(queueURL)})
		ok(t, err, "ListQueueTags")
		if !maps.Equal(tags.Tags, map[string]string{"team": "core"}) {
			t.Errorf("after overwrite and untag ListQueueTags = %v, want team=core", tags.Tags)
		}
	})

	t.Run("permissions", func(t *testing.T) {
		qarn := queueArn(t, queueURL)
		label := "crossaccountpermission"
		_, err := client.AddPermission(ctx, &sqs.AddPermissionInput{
			QueueUrl: aws.String(queueURL), Label: aws.String(label),
			AWSAccountIds: []string{"111111111111", "222222222222"}, Actions: []string{"ReceiveMessage"},
		})
		ok(t, err, "AddPermission")
		want := `{"Version":"2008-10-17","Id":"` + qarn + `/SQSDefaultPolicy","Statement":[{"Sid":"` + label + `","Effect":"Allow","Principal":{"AWS":["arn:aws:iam::111111111111:root","arn:aws:iam::222222222222:root"]},"Action":"SQS:ReceiveMessage","Resource":"` + qarn + `"}]}`
		wantJSONEqual(t, "Policy after AddPermission", queueAttrs(t, queueURL)["Policy"], want)
		_, err = client.AddPermission(ctx, &sqs.AddPermissionInput{
			QueueUrl: aws.String(queueURL), Label: aws.String(label),
			AWSAccountIds: []string{"333333333333"}, Actions: []string{"SendMessage"},
		})
		apiErr(t, err, "AddPermission with an existing label", "InvalidParameterValue")
		wantMessage(t, err, "AddPermission with an existing label", "Value "+label+" for parameter Label is invalid. Reason: Already exists.")
		_, err = client.RemovePermission(ctx, &sqs.RemovePermissionInput{QueueUrl: aws.String(queueURL), Label: aws.String(label + "2")})
		apiErr(t, err, "RemovePermission of an unknown label", "InvalidParameterValue")
		wantMessage(t, err, "RemovePermission of an unknown label", "Value "+label+"2 for parameter Label is invalid. Reason: can't find label.")
		_, err = client.RemovePermission(ctx, &sqs.RemovePermissionInput{QueueUrl: aws.String(queueURL), Label: aws.String(label)})
		ok(t, err, "RemovePermission")
		if p, present := queueAttrs(t, queueURL)["Policy"]; present {
			t.Errorf("Policy %q is still listed after removing its last statement", p)
		}
	})

	t.Run("DeleteQueue", func(t *testing.T) {
		gone := name("gone")
		goneURL := createQueue(t, gone, nil)
		_, err := client.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(goneURL)})
		ok(t, err, "DeleteQueue")
		_, err = client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(gone)})
		errAs[*types.QueueDoesNotExist](t, err, "GetQueueUrl after DeleteQueue")
		_, err = client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(goneURL), MessageBody: aws.String("x")})
		errAs[*types.QueueDoesNotExist](t, err, "SendMessage to a deleted queue")
		// AWS refuses a queue of the same name for 60 seconds after a delete.
		_, err = client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(gone)})
		e := typedErr[*types.QueueDeletedRecently](t, err, "CreateQueue within 60 s of DeleteQueue")
		if e.ErrorCode() != "AWS.SimpleQueueService.QueueDeletedRecently" {
			t.Errorf("error code = %q", e.ErrorCode())
		}
		wantMessage(t, err, "CreateQueue within 60 s of DeleteQueue", "You must wait 60 seconds after deleting a queue before you can create another with the same name.")
	})
}

func TestSQSSendReceive(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sqs.NewFromConfig(awsCfg)
	queueURL := createQueue(t, name("send"), nil)

	attrs := map[string]types.MessageAttributeValue{
		"General": str("String", "Kenobi"),
		"Hello":   str("String", "There"),
		"Help.Me": str("String", "Me"),
		"count":   str("Number.int", "42"),
		"blob":    {DataType: aws.String("Binary"), BinaryValue: []byte{0, 1, 2, 0xff}},
	}
	body := "line one\r\nline two &amp; <tag> é中"
	trace := "Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad8;Sampled=1"
	sent, err := client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(queueURL), MessageBody: aws.String(body), MessageAttributes: attrs,
		MessageSystemAttributes: map[string]types.MessageSystemAttributeValue{
			"AWSTraceHeader": {DataType: aws.String("String"), StringValue: aws.String(trace)},
		},
	})
	ok(t, err, "SendMessage")
	if got := aws.ToString(sent.MD5OfMessageBody); got != md5Hex([]byte(body)) {
		t.Errorf("MD5OfMessageBody = %s, want %s", got, md5Hex([]byte(body)))
	}
	if got, want := aws.ToString(sent.MD5OfMessageAttributes), sqsAttributesMD5(attrs); got != want {
		t.Errorf("MD5OfMessageAttributes = %s, want %s", got, want)
	}
	if got := aws.ToString(sent.MD5OfMessageSystemAttributes); got != "5ae4d5d7636402d80f4eb6d213245a88" {
		t.Errorf("MD5OfMessageSystemAttributes = %s, want the recorded 5ae4d5d7636402d80f4eb6d213245a88", got)
	}
	if !uuidPattern.MatchString(aws.ToString(sent.MessageId)) || sent.SequenceNumber != nil {
		t.Errorf("SendMessage MessageId %q, SequenceNumber %v; want a UUID and no sequence number", aws.ToString(sent.MessageId), sent.SequenceNumber)
	}

	plain, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String("message")})
	ok(t, err, "SendMessage without attributes")
	if aws.ToString(plain.MD5OfMessageBody) != "78e731027d8fd50ed642340b7c9a63b3" || plain.MD5OfMessageAttributes != nil || plain.MD5OfMessageSystemAttributes != nil {
		t.Errorf("plain SendMessage MD5s = body %s, attributes %v, system %v; want the recorded body MD5 and no others",
			aws.ToString(plain.MD5OfMessageBody), plain.MD5OfMessageAttributes, plain.MD5OfMessageSystemAttributes)
	}

	before := time.Now().Add(-time.Minute)
	out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 2,
		MessageAttributeNames:       []string{"All"},
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
	})
	ok(t, err, "ReceiveMessage")
	if len(out.Messages) != 2 {
		t.Fatalf("ReceiveMessage returned %d messages, want 2", len(out.Messages))
	}
	var m types.Message
	for _, x := range out.Messages {
		if aws.ToString(x.MessageId) == aws.ToString(sent.MessageId) {
			m = x
		}
	}
	if aws.ToString(m.Body) != body {
		t.Errorf("received Body = %q, want the byte-exact %q", aws.ToString(m.Body), body)
	}
	if aws.ToString(m.MD5OfBody) != md5Hex([]byte(body)) || aws.ToString(m.MD5OfMessageAttributes) != sqsAttributesMD5(attrs) {
		t.Errorf("received MD5OfBody %s, MD5OfMessageAttributes %s", aws.ToString(m.MD5OfBody), aws.ToString(m.MD5OfMessageAttributes))
	}
	if !maps.EqualFunc(m.MessageAttributes, attrs, func(a, b types.MessageAttributeValue) bool {
		return aws.ToString(a.DataType) == aws.ToString(b.DataType) && aws.ToString(a.StringValue) == aws.ToString(b.StringValue) && bytes.Equal(a.BinaryValue, b.BinaryValue)
	}) {
		t.Errorf("received MessageAttributes = %+v, want what was sent", m.MessageAttributes)
	}
	if m.ReceiptHandle == nil || aws.ToString(m.ReceiptHandle) == "" {
		t.Error("no ReceiptHandle")
	}
	a := m.Attributes
	if a["ApproximateReceiveCount"] != "1" || a["SenderId"] == "" || a["AWSTraceHeader"] != trace {
		t.Errorf("system attributes = %v, want ApproximateReceiveCount 1, a SenderId and the AWSTraceHeader", a)
	}
	for _, k := range []string{"SentTimestamp", "ApproximateFirstReceiveTimestamp"} {
		ms, err := strconv.ParseInt(a[k], 10, 64)
		if err != nil || time.UnixMilli(ms).Before(before) || time.UnixMilli(ms).After(time.Now().Add(time.Minute)) {
			t.Errorf("%s = %q, want epoch milliseconds near now", k, a[k])
		}
	}
	for _, k := range []string{"SequenceNumber", "MessageGroupId", "MessageDeduplicationId"} {
		if v, present := a[k]; present {
			t.Errorf("standard-queue message carries FIFO attribute %s = %q", k, v)
		}
	}

	t.Run("attribute name filters", func(t *testing.T) {
		qURL := createQueue(t, name("filters"), nil)
		sel := map[string]types.MessageAttributeValue{"General": str("String", "Kenobi"), "Hello": str("String", "There"), "Help.Me": str("String", "Me")}
		_, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(qURL), MessageBody: aws.String("filters"), MessageAttributes: sel})
		ok(t, err, "SendMessage")
		got, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(qURL), WaitTimeSeconds: 2, VisibilityTimeout: 0,
			MessageAttributeNames: []string{"Hel.*"}, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameSenderId},
		})
		ok(t, err, "ReceiveMessage with a prefix filter")
		if len(got.Messages) != 1 {
			t.Fatalf("got %d messages", len(got.Messages))
		}
		gm := got.Messages[0]
		if names := slices.Sorted(maps.Keys(gm.MessageAttributes)); !slices.Equal(names, []string{"Hello", "Help.Me"}) {
			t.Errorf("MessageAttributeNames [Hel.*] returned %v, want [Hello Help.Me]", names)
		}
		if aws.ToString(gm.MD5OfMessageAttributes) != "83fee93c1bcd8b9a5a923ffacdc636c7" {
			t.Errorf("MD5OfMessageAttributes over the returned subset = %s, want the recorded 83fee93c1bcd8b9a5a923ffacdc636c7", aws.ToString(gm.MD5OfMessageAttributes))
		}
		if names := slices.Sorted(maps.Keys(gm.Attributes)); !slices.Equal(names, []string{"SenderId"}) {
			t.Errorf("MessageSystemAttributeNames [SenderId] returned %v", names)
		}
	})

	t.Run("delete", func(t *testing.T) {
		for _, x := range out.Messages {
			_, err := client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: x.ReceiptHandle})
			ok(t, err, "DeleteMessage")
		}
		_, err := client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: out.Messages[0].ReceiptHandle})
		ok(t, err, "DeleteMessage with an already-deleted handle")
		_, err = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String("garbage")})
		errAs[*types.ReceiptHandleIsInvalid](t, err, "DeleteMessage with a garbage handle")
		wantMessage(t, err, "DeleteMessage with a garbage handle", `The input receipt handle "garbage" is not a valid receipt handle.`)
		empty, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), WaitTimeSeconds: 1})
		ok(t, err, "ReceiveMessage of an empty queue")
		if len(empty.Messages) != 0 {
			t.Errorf("queue still delivers %d messages after the deletes", len(empty.Messages))
		}
	})
}

func TestSQSSendValidation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sqs.NewFromConfig(awsCfg)
	queueURL := createQueue(t, name("validation"), map[string]string{"MaximumMessageSize": "1024"})

	_, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String("")})
	apiErr(t, err, "SendMessage with an empty body", "MissingParameter")
	wantStatus(t, err, "SendMessage with an empty body", 400)
	wantMessage(t, err, "SendMessage with an empty body", "The request must contain the parameter MessageBody.")

	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String(strings.Repeat("a", 1025))})
	apiErr(t, err, "SendMessage over MaximumMessageSize", "InvalidParameterValue")
	wantMessage(t, err, "SendMessage over MaximumMessageSize", "One or more parameters are invalid. Reason: Message must be shorter than 1024 bytes.")

	// Body plus attribute name, type and value count toward the limit.
	atLimit := strings.Repeat("a", 1024-len("k")-len("String")-len("x"))
	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(queueURL), MessageBody: aws.String(atLimit),
		MessageAttributes: map[string]types.MessageAttributeValue{"k": str("String", "x")},
	})
	ok(t, err, "SendMessage exactly at the limit counting attributes")
	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(queueURL), MessageBody: aws.String(atLimit + "a"),
		MessageAttributes: map[string]types.MessageAttributeValue{"k": str("String", "x")},
	})
	apiErr(t, err, "SendMessage one byte over the limit counting attributes", "InvalidParameterValue")

	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String("bad \x01 char")})
	errAs[*types.InvalidMessageContents](t, err, "SendMessage with a control character")

	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(queueURL), MessageBody: aws.String("x"),
		MessageAttributes: map[string]types.MessageAttributeValue{"ErrorDetails": str("String", "")},
	})
	apiErr(t, err, "SendMessage with an empty String attribute", "InvalidParameterValue")
	wantMessage(t, err, "SendMessage with an empty String attribute", "Message (user) attribute 'ErrorDetails' must contain a non-empty value of type 'String'.")

	_, err = client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 11})
	apiErr(t, err, "ReceiveMessage MaxNumberOfMessages 11", "InvalidParameterValue")
	wantMessage(t, err, "ReceiveMessage MaxNumberOfMessages 11", "Value 11 for parameter MaxNumberOfMessages is invalid. Reason: Must be between 1 and 10, if provided.")

	_, err = client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), WaitTimeSeconds: 21})
	apiErr(t, err, "ReceiveMessage WaitTimeSeconds 21", "InvalidParameterValue")
	wantMessage(t, err, "ReceiveMessage WaitTimeSeconds 21", "Value 21 for parameter WaitTimeSeconds is invalid. Reason: Must be >= 0 and <= 20, if provided.")
}

func TestSQSBatches(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sqs.NewFromConfig(awsCfg)
	queueURL := createQueue(t, name("batch"), map[string]string{"MaximumMessageSize": "1024"})

	sent, err := client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{QueueUrl: aws.String(queueURL), Entries: []types.SendMessageBatchRequestEntry{
		{Id: aws.String("ok-1"), MessageBody: aws.String("first")},
		{Id: aws.String("ok-2"), MessageBody: aws.String("second"), MessageAttributes: map[string]types.MessageAttributeValue{"k": str("String", "x")}},
		{Id: aws.String("too-big"), MessageBody: aws.String(strings.Repeat("b", 1025))},
	}})
	ok(t, err, "SendMessageBatch")
	if len(sent.Successful) != 2 || len(sent.Failed) != 1 {
		t.Fatalf("SendMessageBatch Successful %d, Failed %d; want 2 and 1", len(sent.Successful), len(sent.Failed))
	}
	for _, s := range sent.Successful {
		want := map[string]string{"ok-1": "first", "ok-2": "second"}[aws.ToString(s.Id)]
		if aws.ToString(s.MD5OfMessageBody) != md5Hex([]byte(want)) {
			t.Errorf("entry %s MD5OfMessageBody = %s", aws.ToString(s.Id), aws.ToString(s.MD5OfMessageBody))
		}
		if aws.ToString(s.Id) == "ok-2" && aws.ToString(s.MD5OfMessageAttributes) != "a45daa70926828a2f0a1db3418e6feb4" {
			t.Errorf("entry ok-2 MD5OfMessageAttributes = %s, want the recorded a45daa70926828a2f0a1db3418e6feb4", aws.ToString(s.MD5OfMessageAttributes))
		}
	}
	f := sent.Failed[0]
	if aws.ToString(f.Id) != "too-big" || !f.SenderFault || aws.ToString(f.Code) != "InvalidParameterValue" {
		t.Errorf("Failed entry = {Id %q, SenderFault %t, Code %q}, want too-big, true, InvalidParameterValue", aws.ToString(f.Id), f.SenderFault, aws.ToString(f.Code))
	}

	t.Run("request-level errors", func(t *testing.T) {
		var eleven []types.SendMessageBatchRequestEntry
		for i := range 11 {
			eleven = append(eleven, types.SendMessageBatchRequestEntry{Id: aws.String(fmt.Sprint("m", i)), MessageBody: aws.String("x")})
		}
		_, err := client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{QueueUrl: aws.String(queueURL), Entries: eleven})
		e := typedErr[*types.TooManyEntriesInBatchRequest](t, err, "SendMessageBatch of 11 entries")
		if e.ErrorCode() != "AWS.SimpleQueueService.TooManyEntriesInBatchRequest" {
			t.Errorf("error code = %q", e.ErrorCode())
		}
		wantMessage(t, err, "SendMessageBatch of 11 entries", "Maximum number of entries per request are 10. You have sent 11.")

		_, err = client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{QueueUrl: aws.String(queueURL), Entries: []types.SendMessageBatchRequestEntry{
			{Id: aws.String("same"), MessageBody: aws.String("a")}, {Id: aws.String("same"), MessageBody: aws.String("b")},
		}})
		errAs[*types.BatchEntryIdsNotDistinct](t, err, "SendMessageBatch with duplicate ids")

		_, err = client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{QueueUrl: aws.String(queueURL), Entries: []types.SendMessageBatchRequestEntry{
			{Id: aws.String("invalid:id"), MessageBody: aws.String("a")},
		}})
		errAs[*types.InvalidBatchEntryId](t, err, "SendMessageBatch with an invalid id")
		wantMessage(t, err, "SendMessageBatch with an invalid id", "A batch entry id can only contain alphanumeric characters, hyphens and underscores. It can be at most 80 letters long.")

		_, err = client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{QueueUrl: aws.String(queueURL), Entries: []types.SendMessageBatchRequestEntry{}})
		errAs[*types.EmptyBatchRequest](t, err, "SendMessageBatch with no entries")

		big := createQueue(t, name("batch-big"), nil)
		half := strings.Repeat("c", 524288)
		_, err = client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{QueueUrl: aws.String(big), Entries: []types.SendMessageBatchRequestEntry{
			{Id: aws.String("a"), MessageBody: aws.String(half)}, {Id: aws.String("b"), MessageBody: aws.String(half + "c")},
		}})
		errAs[*types.BatchRequestTooLong](t, err, "SendMessageBatch of 1048577 bytes")
		wantMessage(t, err, "SendMessageBatch of 1048577 bytes", "Batch requests cannot be longer than 1048576 bytes. You have sent 1048577 bytes.")
	})

	t.Run("visibility and delete batches", func(t *testing.T) {
		msgs := receive(t, queueURL, 2, 5*time.Second)
		if len(msgs) != 2 {
			t.Fatalf("received %d messages, want 2", len(msgs))
		}
		vis, err := client.ChangeMessageVisibilityBatch(ctx, &sqs.ChangeMessageVisibilityBatchInput{QueueUrl: aws.String(queueURL), Entries: []types.ChangeMessageVisibilityBatchRequestEntry{
			{Id: aws.String("a"), ReceiptHandle: msgs[0].ReceiptHandle, VisibilityTimeout: 0},
			{Id: aws.String("b"), ReceiptHandle: msgs[1].ReceiptHandle, VisibilityTimeout: 0},
		}})
		ok(t, err, "ChangeMessageVisibilityBatch")
		if len(vis.Successful) != 2 || len(vis.Failed) != 0 {
			t.Errorf("ChangeMessageVisibilityBatch Successful %d, Failed %d", len(vis.Successful), len(vis.Failed))
		}
		again := receive(t, queueURL, 2, 5*time.Second)
		if len(again) != 2 {
			t.Fatalf("after visibility 0, received %d messages, want both again", len(again))
		}
		del, err := client.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{QueueUrl: aws.String(queueURL), Entries: []types.DeleteMessageBatchRequestEntry{
			{Id: aws.String("a"), ReceiptHandle: again[0].ReceiptHandle},
			{Id: aws.String("b"), ReceiptHandle: again[1].ReceiptHandle},
		}})
		ok(t, err, "DeleteMessageBatch")
		if len(del.Successful) != 2 || len(del.Failed) != 0 {
			t.Errorf("DeleteMessageBatch Successful %d, Failed %d", len(del.Successful), len(del.Failed))
		}
		gone, err := client.ChangeMessageVisibilityBatch(ctx, &sqs.ChangeMessageVisibilityBatchInput{QueueUrl: aws.String(queueURL), Entries: []types.ChangeMessageVisibilityBatchRequestEntry{
			{Id: aws.String("a"), ReceiptHandle: again[0].ReceiptHandle, VisibilityTimeout: 10},
		}})
		ok(t, err, "ChangeMessageVisibilityBatch of a deleted message")
		if len(gone.Failed) != 1 || aws.ToString(gone.Failed[0].Code) != "ReceiptHandleIsInvalid" {
			t.Errorf("ChangeMessageVisibilityBatch of a deleted message Failed = %+v, want one ReceiptHandleIsInvalid entry", gone.Failed)
		}
	})
}

func TestSQSVisibilityAndDelay(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sqs.NewFromConfig(awsCfg)
	queueURL := createQueue(t, name("visibility"), nil)

	_, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String("hide me")})
	ok(t, err, "SendMessage")
	first, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(queueURL), WaitTimeSeconds: 2, VisibilityTimeout: 2,
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
	})
	ok(t, err, "ReceiveMessage")
	if len(first.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(first.Messages))
	}
	hidden, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL)})
	ok(t, err, "ReceiveMessage while in flight")
	if len(hidden.Messages) != 0 {
		t.Error("an in-flight message was delivered again before its visibility timeout")
	}
	attrs := queueAttrs(t, queueURL)
	if attrs["ApproximateNumberOfMessages"] != "0" || attrs["ApproximateNumberOfMessagesNotVisible"] != "1" {
		t.Errorf("in flight: ApproximateNumberOfMessages %s, NotVisible %s; want 0 and 1", attrs["ApproximateNumberOfMessages"], attrs["ApproximateNumberOfMessagesNotVisible"])
	}
	if attrs["VisibilityTimeout"] != "30" {
		t.Errorf("a receive's VisibilityTimeout changed the queue attribute to %s", attrs["VisibilityTimeout"])
	}
	time.Sleep(2500 * time.Millisecond)
	second, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(queueURL), WaitTimeSeconds: 2,
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
	})
	ok(t, err, "ReceiveMessage after the visibility timeout")
	if len(second.Messages) != 1 {
		t.Fatalf("after the visibility timeout got %d messages, want the message again", len(second.Messages))
	}
	m := second.Messages[0]
	if m.Attributes["ApproximateReceiveCount"] != "2" || aws.ToString(m.ReceiptHandle) == aws.ToString(first.Messages[0].ReceiptHandle) {
		t.Errorf("redelivery ApproximateReceiveCount %q, same receipt handle %t; want 2 and a new handle",
			m.Attributes["ApproximateReceiveCount"], aws.ToString(m.ReceiptHandle) == aws.ToString(first.Messages[0].ReceiptHandle))
	}

	t.Run("ChangeMessageVisibility", func(t *testing.T) {
		_, err := client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(queueURL), ReceiptHandle: m.ReceiptHandle, VisibilityTimeout: 0})
		ok(t, err, "ChangeMessageVisibility 0")
		again, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), WaitTimeSeconds: 1})
		ok(t, err, "ReceiveMessage")
		if len(again.Messages) != 1 {
			t.Fatal("ChangeMessageVisibility 0 did not make the message receivable at once")
		}
		_, err = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: again.Messages[0].ReceiptHandle})
		ok(t, err, "DeleteMessage")
		_, err = client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String("garbage"), VisibilityTimeout: 10})
		errAs[*types.ReceiptHandleIsInvalid](t, err, "ChangeMessageVisibility with a garbage handle")
	})

	t.Run("message delay", func(t *testing.T) {
		_, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String("later"), DelaySeconds: 2})
		ok(t, err, "SendMessage DelaySeconds 2")
		attrs := queueAttrs(t, queueURL)
		if attrs["ApproximateNumberOfMessagesDelayed"] != "1" {
			t.Errorf("ApproximateNumberOfMessagesDelayed = %s, want 1", attrs["ApproximateNumberOfMessagesDelayed"])
		}
		early, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL)})
		ok(t, err, "ReceiveMessage during the delay")
		if len(early.Messages) != 0 {
			t.Error("a delayed message was delivered during its delay")
		}
		// A long poll in progress receives the message when its delay ends.
		start := time.Now()
		late, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), WaitTimeSeconds: 10})
		ok(t, err, "long poll across the delay")
		if len(late.Messages) != 1 || aws.ToString(late.Messages[0].Body) != "later" {
			t.Fatalf("long poll across the delay got %d messages", len(late.Messages))
		}
		if waited := time.Since(start); waited > 6*time.Second {
			t.Errorf("long poll returned after %s; the message was due after about 2 s", waited)
		}
		_, _ = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: late.Messages[0].ReceiptHandle})
	})
}

func TestSQSLongPolling(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sqs.NewFromConfig(awsCfg)
	queueURL := createQueue(t, name("longpoll"), nil)

	t.Run("empty queue waits", func(t *testing.T) {
		start := time.Now()
		out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), WaitTimeSeconds: 2})
		ok(t, err, "ReceiveMessage WaitTimeSeconds 2")
		if len(out.Messages) != 0 {
			t.Errorf("got %d messages from an empty queue", len(out.Messages))
		}
		if waited := time.Since(start); waited < 1800*time.Millisecond || waited > 6*time.Second {
			t.Errorf("an empty long poll of 2 s returned after %s", waited)
		}
	})

	t.Run("returns as soon as a message arrives", func(t *testing.T) {
		go func() {
			time.Sleep(time.Second)
			_, _ = sqs.NewFromConfig(awsCfg).SendMessage(context.Background(), &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String("arrives")})
		}()
		start := time.Now()
		out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), WaitTimeSeconds: 10, MaxNumberOfMessages: 10})
		ok(t, err, "ReceiveMessage WaitTimeSeconds 10")
		if len(out.Messages) != 1 {
			t.Fatalf("long poll got %d messages, want the one sent during the wait", len(out.Messages))
		}
		if waited := time.Since(start); waited > 5*time.Second {
			t.Errorf("long poll returned after %s; it should return as soon as the message arrives, without waiting to fill MaxNumberOfMessages", waited)
		}
		_, _ = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: out.Messages[0].ReceiptHandle})
	})

	t.Run("queue ReceiveMessageWaitTimeSeconds applies", func(t *testing.T) {
		waitURL := createQueue(t, name("longpoll-attr"), map[string]string{"ReceiveMessageWaitTimeSeconds": "2"})
		start := time.Now()
		out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(waitURL)})
		ok(t, err, "ReceiveMessage without WaitTimeSeconds")
		if len(out.Messages) != 0 {
			t.Errorf("got %d messages from an empty queue", len(out.Messages))
		}
		if waited := time.Since(start); waited < 1800*time.Millisecond {
			t.Errorf("a receive on a queue with ReceiveMessageWaitTimeSeconds 2 returned after %s; it should long poll", waited)
		}
	})
}

func TestSQSPurge(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sqs.NewFromConfig(awsCfg)
	queueName := name("purge")
	queueURL := createQueue(t, queueName, nil)
	for i := range 3 {
		_, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String(fmt.Sprint("m", i)), DelaySeconds: int32(i * 30)})
		ok(t, err, "SendMessage")
	}
	inflight, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), WaitTimeSeconds: 1})
	ok(t, err, "ReceiveMessage")
	if len(inflight.Messages) != 1 {
		t.Fatalf("got %d messages, want the one undelayed message", len(inflight.Messages))
	}
	_, err = client.PurgeQueue(ctx, &sqs.PurgeQueueInput{QueueUrl: aws.String(queueURL)})
	ok(t, err, "PurgeQueue")
	attrs := queueAttrs(t, queueURL)
	for _, k := range []string{"ApproximateNumberOfMessages", "ApproximateNumberOfMessagesNotVisible", "ApproximateNumberOfMessagesDelayed"} {
		if attrs[k] != "0" {
			t.Errorf("after PurgeQueue %s = %s, want 0 (purge removes available, in-flight and delayed messages)", k, attrs[k])
		}
	}
	_, err = client.PurgeQueue(ctx, &sqs.PurgeQueueInput{QueueUrl: aws.String(queueURL)})
	e := typedErr[*types.PurgeQueueInProgress](t, err, "a second PurgeQueue within 60 s")
	if e.ErrorCode() != "AWS.SimpleQueueService.PurgeQueueInProgress" {
		t.Errorf("error code = %q", e.ErrorCode())
	}
	wantStatus(t, err, "a second PurgeQueue within 60 s", 403)
	wantMessage(t, err, "a second PurgeQueue within 60 s", "Only one PurgeQueue operation on "+queueName+" is allowed every 60 seconds.")
}

func TestSQSFifo(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sqs.NewFromConfig(awsCfg)

	t.Run("queue creation", func(t *testing.T) {
		_, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name("nosuffix")), Attributes: map[string]string{"FifoQueue": "true"}})
		apiErr(t, err, "CreateQueue FifoQueue without .fifo", "InvalidParameterValue")
		_, err = client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name("std") + ".fifo")})
		apiErr(t, err, "CreateQueue of a standard queue named .fifo", "InvalidParameterValue")
		_, err = client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name("std")), Attributes: map[string]string{"ContentBasedDeduplication": "true"}})
		errAs[*types.InvalidAttributeName](t, err, "CreateQueue of a standard queue with ContentBasedDeduplication")

		fifoURL := createQueue(t, name("attrs")+".fifo", map[string]string{"FifoQueue": "true"})
		attrs := queueAttrs(t, fifoURL)
		for k, v := range map[string]string{"FifoQueue": "true", "DeduplicationScope": "queue", "FifoThroughputLimit": "perQueue", "ContentBasedDeduplication": "false"} {
			if attrs[k] != v {
				t.Errorf("fresh FIFO queue %s = %q, want %q", k, attrs[k], v)
			}
		}
		_, err = client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{QueueUrl: aws.String(fifoURL), Attributes: map[string]string{"FifoQueue": "false"}})
		errAs[*types.InvalidAttributeValue](t, err, "SetQueueAttributes FifoQueue false on a FIFO queue")
		wantMessage(t, err, "SetQueueAttributes FifoQueue false on a FIFO queue", "Invalid value for the parameter FifoQueue. Reason: Modifying queue type is not supported.")
	})

	t.Run("send validation", func(t *testing.T) {
		fifoURL := createQueue(t, name("validate")+".fifo", map[string]string{"FifoQueue": "true"})
		_, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(fifoURL), MessageBody: aws.String("x"), MessageDeduplicationId: aws.String("d")})
		apiErr(t, err, "FIFO SendMessage without MessageGroupId", "MissingParameter")
		wantMessage(t, err, "FIFO SendMessage without MessageGroupId", "The request must contain the parameter MessageGroupId.")
		_, err = client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(fifoURL), MessageBody: aws.String("x"), MessageGroupId: aws.String("g")})
		apiErr(t, err, "FIFO SendMessage without a deduplication id", "InvalidParameterValue")
		wantMessage(t, err, "FIFO SendMessage without a deduplication id", "The queue should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly")
		_, err = client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(fifoURL), MessageBody: aws.String("x"), MessageGroupId: aws.String("g"), MessageDeduplicationId: aws.String("d"), DelaySeconds: 2})
		apiErr(t, err, "FIFO SendMessage with DelaySeconds", "InvalidParameterValue")
		wantMessage(t, err, "FIFO SendMessage with DelaySeconds", "Value 2 for parameter DelaySeconds is invalid. Reason: The request include parameter that is not valid for this queue type.")
		_, err = client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(fifoURL), MessageBody: aws.String("x"), MessageGroupId: aws.String("group 123"), MessageDeduplicationId: aws.String("d")})
		apiErr(t, err, "FIFO SendMessage with a space in MessageGroupId", "InvalidParameterValue")
		wantMessage(t, err, "FIFO SendMessage with a space in MessageGroupId", "Value group 123 for parameter MessageGroupId is invalid. Reason: MessageGroupId can only include alphanumeric and punctuation characters. 1 to 128 in length.")
		_, err = client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{QueueUrl: aws.String(fifoURL), Entries: []types.SendMessageBatchRequestEntry{
			{Id: aws.String("a"), MessageBody: aws.String("x"), MessageDeduplicationId: aws.String("d")},
		}})
		apiErr(t, err, "FIFO SendMessageBatch entry without MessageGroupId", "InvalidParameterValue")
		wantMessage(t, err, "FIFO SendMessageBatch entry without MessageGroupId", "The request must contain the parameter MessageGroupId.")
	})

	t.Run("group ordering and blocking", func(t *testing.T) {
		fifoURL := createQueue(t, name("order")+".fifo", map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "true"})
		var seqs []string
		for _, s := range []struct{ group, body string }{{"g1", "g1-m1"}, {"g2", "g2-m1"}, {"g1", "g1-m2"}, {"g1", "g1-m3"}} {
			out, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(fifoURL), MessageBody: aws.String(s.body), MessageGroupId: aws.String(s.group)})
			ok(t, err, "SendMessage "+s.body)
			if out.SequenceNumber == nil {
				t.Errorf("FIFO SendMessage %s returned no SequenceNumber", s.body)
			}
			if s.group == "g1" {
				seqs = append(seqs, aws.ToString(out.SequenceNumber))
			}
		}
		if !sequencesIncrease(seqs) {
			t.Errorf("g1 SequenceNumbers %v do not strictly increase", seqs)
		}
		out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(fifoURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
		})
		ok(t, err, "ReceiveMessage")
		var bodies []string
		for _, m := range out.Messages {
			bodies = append(bodies, aws.ToString(m.Body))
			if m.Attributes["MessageGroupId"] == "" || m.Attributes["SequenceNumber"] == "" || m.Attributes["MessageDeduplicationId"] == "" {
				t.Errorf("FIFO message %s lacks MessageGroupId, SequenceNumber or MessageDeduplicationId: %v", aws.ToString(m.Body), m.Attributes)
			}
		}
		// A receive drains one group in send order before the next; the
		// order between groups is unspecified.
		g1First := []string{"g1-m1", "g1-m2", "g1-m3", "g2-m1"}
		g2First := []string{"g2-m1", "g1-m1", "g1-m2", "g1-m3"}
		if !slices.Equal(bodies, g1First) && !slices.Equal(bodies, g2First) {
			t.Errorf("FIFO receive order = %v, want each group contiguous and in send order", bodies)
		}
		// Both groups are now in flight; nothing more may be delivered.
		_, err = client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(fifoURL), MessageBody: aws.String("g1-m4"), MessageGroupId: aws.String("g1")})
		ok(t, err, "SendMessage g1-m4")
		blocked, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(fifoURL), MaxNumberOfMessages: 10})
		ok(t, err, "ReceiveMessage while both groups are in flight")
		if len(blocked.Messages) != 0 {
			t.Errorf("a group with in-flight messages delivered %d more", len(blocked.Messages))
		}
		for _, m := range out.Messages {
			_, err := client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(fifoURL), ReceiptHandle: m.ReceiptHandle})
			ok(t, err, "DeleteMessage")
		}
		next, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(fifoURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1})
		ok(t, err, "ReceiveMessage after deleting the in-flight messages")
		if len(next.Messages) != 1 || aws.ToString(next.Messages[0].Body) != "g1-m4" {
			t.Errorf("after the deletes got %v, want g1-m4", next.Messages)
		}
	})

	t.Run("deduplication", func(t *testing.T) {
		fifoURL := createQueue(t, name("dedup")+".fifo", map[string]string{"FifoQueue": "true"})
		for _, group := range []string{"g1", "g2"} {
			_, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(fifoURL), MessageBody: aws.String("dup-" + group), MessageGroupId: aws.String(group), MessageDeduplicationId: aws.String("same-id")})
			ok(t, err, "SendMessage with a repeated deduplication id")
		}
		got := drain(t, fifoURL, 2*time.Second)
		if len(got) != 1 || aws.ToString(got[0].Body) != "dup-g1" {
			t.Errorf("with DeduplicationScope queue, the same id in two groups delivered %d messages, want only the first", len(got))
		}
		_, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(fifoURL), MessageBody: aws.String("after delete"), MessageGroupId: aws.String("g1"), MessageDeduplicationId: aws.String("same-id")})
		ok(t, err, "SendMessage with the id after the original was deleted")
		if got := drain(t, fifoURL, 2*time.Second); len(got) != 0 {
			t.Errorf("a duplicate sent after the original was deleted was delivered: %d messages", len(got))
		}

		groupURL := createQueue(t, name("dedup-group")+".fifo", map[string]string{"FifoQueue": "true", "DeduplicationScope": "messageGroup"})
		for _, group := range []string{"g1", "g2"} {
			_, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(groupURL), MessageBody: aws.String("dup-" + group), MessageGroupId: aws.String(group), MessageDeduplicationId: aws.String("same-id")})
			ok(t, err, "SendMessage")
		}
		if got := drain(t, groupURL, 2*time.Second); len(got) != 2 {
			t.Errorf("with DeduplicationScope messageGroup, the same id in two groups delivered %d messages, want 2", len(got))
		}

		cbdURL := createQueue(t, name("cbd")+".fifo", map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "true"})
		for range 2 {
			_, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(cbdURL), MessageBody: aws.String("same body"), MessageGroupId: aws.String("g")})
			ok(t, err, "SendMessage with content-based deduplication")
		}
		if got := drain(t, cbdURL, 2*time.Second); len(got) != 1 {
			t.Errorf("content-based deduplication delivered %d copies of one body, want 1", len(got))
		}
	})

	t.Run("expired receipt handle", func(t *testing.T) {
		fifoURL := createQueue(t, name("expired")+".fifo", map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "true"})
		_, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(fifoURL), MessageBody: aws.String("x"), MessageGroupId: aws.String("g")})
		ok(t, err, "SendMessage")
		out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(fifoURL), VisibilityTimeout: 1, WaitTimeSeconds: 1})
		ok(t, err, "ReceiveMessage")
		if len(out.Messages) != 1 {
			t.Fatal("no message")
		}
		time.Sleep(1500 * time.Millisecond)
		handle := aws.ToString(out.Messages[0].ReceiptHandle)
		_, err = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(fifoURL), ReceiptHandle: aws.String(handle)})
		apiErr(t, err, "FIFO DeleteMessage with an expired handle", "InvalidParameterValue")
		wantMessage(t, err, "FIFO DeleteMessage with an expired handle", "Value "+handle+" for parameter ReceiptHandle is invalid. Reason: The receipt handle has expired.")
	})

	t.Run("ReceiveRequestAttemptId", func(t *testing.T) {
		fifoURL := createQueue(t, name("attempt")+".fifo", map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "true"})
		_, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(fifoURL), MessageBody: aws.String("retry me"), MessageGroupId: aws.String("g")})
		ok(t, err, "SendMessage")
		in := &sqs.ReceiveMessageInput{QueueUrl: aws.String(fifoURL), WaitTimeSeconds: 1, ReceiveRequestAttemptId: aws.String("attempt-1")}
		first, err := client.ReceiveMessage(ctx, in)
		ok(t, err, "ReceiveMessage")
		retry, err := client.ReceiveMessage(ctx, in)
		ok(t, err, "ReceiveMessage retry with the same ReceiveRequestAttemptId")
		if len(first.Messages) != 1 || len(retry.Messages) != 1 || aws.ToString(first.Messages[0].ReceiptHandle) != aws.ToString(retry.Messages[0].ReceiptHandle) {
			t.Errorf("a retry with the same ReceiveRequestAttemptId returned %d messages; want the same message and receipt handle", len(retry.Messages))
		}
	})
}

func TestSQSDeadLetterQueues(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sqs.NewFromConfig(awsCfg)
	dlqURL := createQueue(t, name("dlq"), nil)
	dlqArn := queueArn(t, dlqURL)
	srcURL := createQueue(t, name("src"), map[string]string{
		"RedrivePolicy": `{"deadLetterTargetArn":"` + dlqArn + `","maxReceiveCount":"1"}`,
	})
	srcArn := queueArn(t, srcURL)

	var policy struct {
		DeadLetterTargetArn string `json:"deadLetterTargetArn"`
		MaxReceiveCount     any    `json:"maxReceiveCount"`
	}
	if err := json.Unmarshal([]byte(queueAttrs(t, srcURL)["RedrivePolicy"]), &policy); err != nil {
		t.Fatalf("RedrivePolicy is not JSON: %v", err)
	}
	if policy.DeadLetterTargetArn != dlqArn || policy.MaxReceiveCount != float64(1) {
		t.Errorf("RedrivePolicy = %+v, want the DLQ ARN and maxReceiveCount as the JSON number 1", policy)
	}

	_, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name("badredrive")), Attributes: map[string]string{
		"RedrivePolicy": `{"deadLetterTargetArn":"` + dlqArn + `"}`,
	}})
	apiErr(t, err, "CreateQueue with a RedrivePolicy lacking maxReceiveCount", "InvalidParameterValue")

	sent, err := client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(srcURL), MessageBody: aws.String("poison"),
		MessageAttributes: map[string]types.MessageAttributeValue{"k": str("String", "x")},
	})
	ok(t, err, "SendMessage")
	first, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(srcURL), WaitTimeSeconds: 1, VisibilityTimeout: 1})
	ok(t, err, "first ReceiveMessage")
	if len(first.Messages) != 1 {
		t.Fatal("the message was not delivered once")
	}
	time.Sleep(1500 * time.Millisecond)
	second, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(srcURL), WaitTimeSeconds: 1})
	ok(t, err, "second ReceiveMessage")
	if len(second.Messages) != 0 {
		t.Errorf("with maxReceiveCount 1 the message was delivered a second time")
	}
	moved := receive(t, dlqURL, 1, 10*time.Second)
	if len(moved) != 1 {
		t.Fatalf("the DLQ holds %d messages, want the moved message", len(moved))
	}
	m := moved[0]
	if aws.ToString(m.MessageId) != aws.ToString(sent.MessageId) || aws.ToString(m.Body) != "poison" || aws.ToString(m.MessageAttributes["k"].StringValue) != "x" {
		t.Errorf("moved message id %q body %q attributes %v; want the original id, body and attributes", aws.ToString(m.MessageId), aws.ToString(m.Body), m.MessageAttributes)
	}
	if m.Attributes["DeadLetterQueueSourceArn"] != srcArn || m.Attributes["ApproximateReceiveCount"] != "2" {
		t.Errorf("moved message DeadLetterQueueSourceArn %q, ApproximateReceiveCount %q; want %s and 2", m.Attributes["DeadLetterQueueSourceArn"], m.Attributes["ApproximateReceiveCount"], srcArn)
	}

	sources, err := client.ListDeadLetterSourceQueues(ctx, &sqs.ListDeadLetterSourceQueuesInput{QueueUrl: aws.String(dlqURL)})
	ok(t, err, "ListDeadLetterSourceQueues")
	if !slices.Equal(sources.QueueUrls, []string{srcURL}) {
		t.Errorf("ListDeadLetterSourceQueues = %v, want [%s]", sources.QueueUrls, srcURL)
	}

	t.Run("message move task", func(t *testing.T) {
		destURL := createQueue(t, name("dest"), nil)
		destArn := queueArn(t, destURL)
		_, err := client.StartMessageMoveTask(ctx, &sqs.StartMessageMoveTaskInput{SourceArn: aws.String(srcArn), DestinationArn: aws.String(destArn)})
		apiErr(t, err, "StartMessageMoveTask from a queue that is not a DLQ", "InvalidParameterValue")
		wantMessage(t, err, "StartMessageMoveTask from a queue that is not a DLQ", "Source queue must be configured as a Dead Letter Queue.")

		_, err = client.CancelMessageMoveTask(ctx, &sqs.CancelMessageMoveTaskInput{TaskHandle: aws.String("garbage")})
		apiErr(t, err, "CancelMessageMoveTask with a garbage handle", "InvalidParameterValue")
		wantMessage(t, err, "CancelMessageMoveTask with a garbage handle", "Value for parameter TaskHandle is invalid.")

		// The moved message is still in flight from the receive above.
		_, err = client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(dlqURL), ReceiptHandle: m.ReceiptHandle, VisibilityTimeout: 0})
		ok(t, err, "ChangeMessageVisibility 0 on the DLQ message")
		task, err := client.StartMessageMoveTask(ctx, &sqs.StartMessageMoveTaskInput{SourceArn: aws.String(dlqArn), DestinationArn: aws.String(destArn)})
		ok(t, err, "StartMessageMoveTask")
		if aws.ToString(task.TaskHandle) == "" {
			t.Fatal("StartMessageMoveTask returned no TaskHandle")
		}
		eventually(t, 15*time.Second, 200*time.Millisecond, "the move task to complete", func() (bool, string) {
			list, err := client.ListMessageMoveTasks(ctx, &sqs.ListMessageMoveTasksInput{SourceArn: aws.String(dlqArn)})
			if err != nil {
				return false, err.Error()
			}
			if len(list.Results) == 0 {
				return false, "ListMessageMoveTasks lists no task"
			}
			r := list.Results[0]
			return r.Status != nil && *r.Status == "COMPLETED" && r.ApproximateNumberOfMessagesMoved == 1 && aws.ToInt64(r.ApproximateNumberOfMessagesToMove) == 1,
				fmt.Sprintf("newest task Status %v, moved %d of %d", aws.ToString(r.Status), r.ApproximateNumberOfMessagesMoved, aws.ToInt64(r.ApproximateNumberOfMessagesToMove))
		})
		if got := receive(t, destURL, 1, 5*time.Second); len(got) != 1 || aws.ToString(got[0].Body) != "poison" {
			t.Errorf("the destination holds %d messages after the move, want the redriven message", len(got))
		}
	})
}

// sequencesIncrease reports whether decimal sequence numbers strictly
// increase.
func sequencesIncrease(seqs []string) bool {
	norm := regexp.MustCompile(`^0+`)
	for i := 1; i < len(seqs); i++ {
		a, b := norm.ReplaceAllString(seqs[i-1], ""), norm.ReplaceAllString(seqs[i], "")
		if len(a) > len(b) || (len(a) == len(b) && a >= b) {
			return false
		}
	}
	return true
}
