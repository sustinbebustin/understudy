package gosdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"
)

// Research: docs/research/sns-http-delivery.md. The receiver is
// sns_http_test.go's.

// ConfirmSubscription with the confirmation's Token confirms an http
// subscription (2.3). With RawMessageDelivery on, a publish reaches the
// endpoint as the message itself, with x-amz-sns-rawdelivery: true and the
// headers of 3.2, and its message attributes are not sent (4.5, 3.2).
func TestSNSHTTPConfirmByTokenThenRawDelivery(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sns.NewFromConfig(awsCfg)
	rcv := newHookReceiver(t)
	topic := createTopic(t, name(t, "hooks-raw"), nil)
	sub, err := client.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: aws.String(topic), Protocol: aws.String("http"), Endpoint: aws.String(rcv.URL + "/raw"), ReturnSubscriptionArn: true,
	})
	ok(t, err, "Subscribe http")
	arn := aws.ToString(sub.SubscriptionArn)

	eventually(t, 10*time.Second, 50*time.Millisecond, "the SubscriptionConfirmation", func() (bool, string) {
		return len(rcv.ofType("SubscriptionConfirmation")) == 1, "the receiver has no SubscriptionConfirmation"
	})
	token := rcv.ofType("SubscriptionConfirmation")[0].body["Token"]
	confirmed, err := client.ConfirmSubscription(ctx, &sns.ConfirmSubscriptionInput{TopicArn: aws.String(topic), Token: aws.String(token)})
	ok(t, err, "ConfirmSubscription")
	if got := aws.ToString(confirmed.SubscriptionArn); got != arn {
		t.Fatalf("ConfirmSubscription SubscriptionArn = %q, want %q", got, arn)
	}
	attrs, err := client.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{SubscriptionArn: aws.String(arn)})
	ok(t, err, "GetSubscriptionAttributes")
	if attrs.Attributes["PendingConfirmation"] != "false" {
		t.Errorf("PendingConfirmation = %q after ConfirmSubscription, want false", attrs.Attributes["PendingConfirmation"])
	}
	_, err = client.SetSubscriptionAttributes(ctx, &sns.SetSubscriptionAttributesInput{
		SubscriptionArn: aws.String(arn), AttributeName: aws.String("RawMessageDelivery"), AttributeValue: aws.String("true"),
	})
	ok(t, err, "SetSubscriptionAttributes RawMessageDelivery")

	const message = `{"order": 7, "note": "raw, not enveloped"}`
	out, err := client.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(topic), Message: aws.String(message),
		MessageAttributes: map[string]types.MessageAttributeValue{"colour": {DataType: aws.String("String"), StringValue: aws.String("teal-attribute")}},
	})
	ok(t, err, "Publish")
	eventually(t, 10*time.Second, 50*time.Millisecond, "the Notification", func() (bool, string) {
		return len(rcv.ofType("Notification")) == 1, "the receiver has no Notification"
	})
	n := rcv.ofType("Notification")[0]
	if n.raw != message {
		t.Errorf("raw Notification body = %q, want the message %q", n.raw, message)
	}
	for header, want := range map[string]string{
		"x-amz-sns-rawdelivery":      "true",
		"x-amz-sns-message-id":       aws.ToString(out.MessageId),
		"x-amz-sns-topic-arn":        topic,
		"x-amz-sns-subscription-arn": arn,
		"Content-Type":               "text/plain; charset=UTF-8",
		"User-Agent":                 "Amazon Simple Notification Service Agent",
	} {
		if got := n.header.Get(header); got != want {
			t.Errorf("raw Notification header %s = %q, want %q", header, got, want)
		}
	}
	for header, values := range n.header {
		for _, v := range values {
			if strings.Contains(v, "teal-attribute") {
				t.Errorf("raw Notification header %s = %q carries a message attribute", header, v)
			}
		}
	}
	_, err = client.Unsubscribe(ctx, &sns.UnsubscribeInput{SubscriptionArn: aws.String(arn)})
	ok(t, err, "Unsubscribe")
}
