package gosdk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// Research: docs/research/sns.md, error-matrix.md 5.3.

// createTopic creates a topic and deletes it when the test ends.
func createTopic(t *testing.T, topicName string, attrs map[string]string) string {
	t.Helper()
	client := sns.NewFromConfig(awsCfg)
	out, err := client.CreateTopic(t.Context(), &sns.CreateTopicInput{Name: aws.String(topicName), Attributes: attrs})
	ok(t, err, "CreateTopic "+topicName)
	t.Cleanup(func() { _, _ = client.DeleteTopic(context.Background(), &sns.DeleteTopicInput{TopicArn: out.TopicArn}) })
	return aws.ToString(out.TopicArn)
}

// subscribeQueue subscribes a new queue to the topic and returns the
// queue URL and the subscription ARN.
func subscribeQueue(t *testing.T, topicArn, queueName string, queueAttrs, subAttrs map[string]string) (string, string) {
	t.Helper()
	queueURL := createQueue(t, queueName, queueAttrs)
	out, err := sns.NewFromConfig(awsCfg).Subscribe(t.Context(), &sns.SubscribeInput{
		TopicArn: aws.String(topicArn), Protocol: aws.String("sqs"), Endpoint: aws.String(queueArn(t, queueURL)),
		Attributes: subAttrs, ReturnSubscriptionArn: true,
	})
	ok(t, err, "Subscribe sqs")
	return queueURL, aws.ToString(out.SubscriptionArn)
}

// envelope decodes an SNS-to-SQS notification body.
func envelope(t *testing.T, body string) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("SQS body is not an SNS JSON envelope: %v: %s", err, body)
	}
	return env
}

var snsTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

func TestSNSTopics(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sns.NewFromConfig(awsCfg)
	acct := account(t)
	topicName := name("topic")
	topicArn := createTopic(t, topicName, nil)
	if want := "arn:aws:sns:" + region() + ":" + acct + ":" + topicName; topicArn != want {
		t.Errorf("TopicArn = %q, want %q", topicArn, want)
	}

	t.Run("CreateTopic idempotency and names", func(t *testing.T) {
		again, err := client.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(topicName)})
		ok(t, err, "CreateTopic again")
		if aws.ToString(again.TopicArn) != topicArn {
			t.Errorf("CreateTopic again = %q, want %q", aws.ToString(again.TopicArn), topicArn)
		}
		_, err = client.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(topicName), Attributes: map[string]string{"DisplayName": "different"}})
		errAs[*types.InvalidParameterException](t, err, "CreateTopic of an existing name with different attributes")
		wantMessage(t, err, "CreateTopic of an existing name with different attributes", "Invalid parameter: Attributes Reason: Topic already exists with different attributes")
		for _, bad := range []string{"bad name", name("std") + ".fifo", strings.Repeat("a", 257)} {
			_, err = client.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(bad)})
			e := typedErr[*types.InvalidParameterException](t, err, "CreateTopic named "+bad)
			if e.ErrorCode() != "InvalidParameter" {
				t.Errorf("error code = %q, want InvalidParameter", e.ErrorCode())
			}
			wantStatus(t, err, "CreateTopic named "+bad, 400)
			wantMessage(t, err, "CreateTopic named "+bad, "Invalid parameter: Topic Name")
		}
	})

	t.Run("default attributes", func(t *testing.T) {
		out, err := client.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: aws.String(topicArn)})
		ok(t, err, "GetTopicAttributes")
		a := out.Attributes
		for k, v := range map[string]string{"DisplayName": "", "Owner": acct, "SubscriptionsConfirmed": "0", "SubscriptionsDeleted": "0", "SubscriptionsPending": "0", "TopicArn": topicArn} {
			if got, present := a[k]; !present || got != v {
				t.Errorf("attribute %s = %q (present %t), want %q", k, got, present, v)
			}
		}
		if _, present := a["FifoTopic"]; present {
			t.Error("a standard topic lists FifoTopic")
		}
		if a["EffectiveDeliveryPolicy"] == "" {
			t.Error("EffectiveDeliveryPolicy is missing")
		}
		var policy struct {
			Version   string
			Id        string
			Statement []struct {
				Sid       string
				Effect    string
				Principal map[string]string
				Action    []string
				Resource  string
				Condition map[string]map[string]string
			}
		}
		if err := json.Unmarshal([]byte(a["Policy"]), &policy); err != nil {
			t.Fatalf("Policy is not the default JSON policy: %v: %s", err, a["Policy"])
		}
		if policy.Version != "2008-10-17" || policy.Id != "__default_policy_ID" || len(policy.Statement) != 1 {
			t.Fatalf("default Policy = %s", a["Policy"])
		}
		s := policy.Statement[0]
		if s.Sid != "__default_statement_ID" || s.Effect != "Allow" || s.Principal["AWS"] != "*" || s.Resource != topicArn ||
			s.Condition["StringEquals"]["AWS:SourceOwner"] != acct || !slices.Contains(s.Action, "SNS:Publish") || len(s.Action) != 8 {
			t.Errorf("default Policy statement = %+v", s)
		}
	})

	t.Run("SetTopicAttributes", func(t *testing.T) {
		_, err := client.SetTopicAttributes(ctx, &sns.SetTopicAttributesInput{TopicArn: aws.String(topicArn), AttributeName: aws.String("DisplayName"), AttributeValue: aws.String("Conformance")})
		ok(t, err, "SetTopicAttributes DisplayName")
		out, err := client.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: aws.String(topicArn)})
		ok(t, err, "GetTopicAttributes")
		if out.Attributes["DisplayName"] != "Conformance" {
			t.Errorf("DisplayName = %q", out.Attributes["DisplayName"])
		}
		_, err = client.SetTopicAttributes(ctx, &sns.SetTopicAttributesInput{TopicArn: aws.String(topicArn), AttributeName: aws.String("FifoTopic"), AttributeValue: aws.String("true")})
		errAs[*types.InvalidParameterException](t, err, "SetTopicAttributes FifoTopic")
		wantMessage(t, err, "SetTopicAttributes FifoTopic", "Invalid parameter: AttributeName")
	})

	t.Run("missing topic", func(t *testing.T) {
		missing := "arn:aws:sns:" + region() + ":" + acct + ":" + name("missing")
		_, err := client.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: aws.String(missing)})
		e := typedErr[*types.NotFoundException](t, err, "GetTopicAttributes of a missing topic")
		if e.ErrorCode() != "NotFound" {
			t.Errorf("error code = %q, want NotFound", e.ErrorCode())
		}
		wantStatus(t, err, "GetTopicAttributes of a missing topic", 404)
		wantMessage(t, err, "GetTopicAttributes of a missing topic", "Topic does not exist")
		_, err = client.Publish(ctx, &sns.PublishInput{TopicArn: aws.String(missing), Message: aws.String("x")})
		errAs[*types.NotFoundException](t, err, "Publish to a missing topic")
		_, err = client.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: aws.String(missing)})
		ok(t, err, "DeleteTopic of a missing topic (idempotent)")
	})

	t.Run("ListTopics", func(t *testing.T) {
		var arns []string
		p := sns.NewListTopicsPaginator(client, &sns.ListTopicsInput{})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			ok(t, err, "ListTopics")
			for _, tp := range page.Topics {
				arns = append(arns, aws.ToString(tp.TopicArn))
			}
		}
		if !slices.Contains(arns, topicArn) {
			t.Errorf("ListTopics does not list %s", topicArn)
		}
	})

	t.Run("tags", func(t *testing.T) {
		_, err := client.TagResource(ctx, &sns.TagResourceInput{ResourceArn: aws.String(topicArn), Tags: []types.Tag{{Key: aws.String("team"), Value: aws.String("platform")}, {Key: aws.String("env"), Value: aws.String("")}}})
		ok(t, err, "TagResource")
		tags, err := client.ListTagsForResource(ctx, &sns.ListTagsForResourceInput{ResourceArn: aws.String(topicArn)})
		ok(t, err, "ListTagsForResource")
		if len(tags.Tags) != 2 {
			t.Errorf("ListTagsForResource = %+v, want 2 tags", tags.Tags)
		}
		_, err = client.UntagResource(ctx, &sns.UntagResourceInput{ResourceArn: aws.String(topicArn), TagKeys: []string{"env"}})
		ok(t, err, "UntagResource")
		tags, err = client.ListTagsForResource(ctx, &sns.ListTagsForResourceInput{ResourceArn: aws.String(topicArn)})
		ok(t, err, "ListTagsForResource")
		if len(tags.Tags) != 1 || aws.ToString(tags.Tags[0].Key) != "team" {
			t.Errorf("after UntagResource ListTagsForResource = %+v", tags.Tags)
		}
		_, err = client.TagResource(ctx, &sns.TagResourceInput{ResourceArn: aws.String(topicArn), Tags: []types.Tag{{Key: aws.String("dup"), Value: aws.String("a")}, {Key: aws.String("dup"), Value: aws.String("b")}}})
		errAs[*types.InvalidParameterException](t, err, "TagResource with duplicate keys")
		wantMessage(t, err, "TagResource with duplicate keys", "Invalid parameter: Duplicated keys are not allowed.")
	})

	t.Run("permissions", func(t *testing.T) {
		_, err := client.AddPermission(ctx, &sns.AddPermissionInput{TopicArn: aws.String(topicArn), Label: aws.String("cross"), AWSAccountId: []string{"111111111111"}, ActionName: []string{"Publish"}})
		ok(t, err, "AddPermission")
		out, err := client.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: aws.String(topicArn)})
		ok(t, err, "GetTopicAttributes")
		if !strings.Contains(out.Attributes["Policy"], `"cross"`) {
			t.Errorf("Policy after AddPermission lacks the cross statement: %s", out.Attributes["Policy"])
		}
		_, err = client.AddPermission(ctx, &sns.AddPermissionInput{TopicArn: aws.String(topicArn), Label: aws.String("cross"), AWSAccountId: []string{"111111111111"}, ActionName: []string{"Publish"}})
		errAs[*types.InvalidParameterException](t, err, "AddPermission with an existing label")
		wantMessage(t, err, "AddPermission with an existing label", "Invalid parameter: Statement already exists")
		_, err = client.AddPermission(ctx, &sns.AddPermissionInput{TopicArn: aws.String(topicArn), Label: aws.String("scope"), AWSAccountId: []string{"111111111111"}, ActionName: []string{"CreateTopic"}})
		errAs[*types.InvalidParameterException](t, err, "AddPermission of an action outside the topic scope")
		wantMessage(t, err, "AddPermission of an action outside the topic scope", "Invalid parameter: Policy statement action out of service scope!")
		_, err = client.RemovePermission(ctx, &sns.RemovePermissionInput{TopicArn: aws.String(topicArn), Label: aws.String("cross")})
		ok(t, err, "RemovePermission")
		out, err = client.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: aws.String(topicArn)})
		ok(t, err, "GetTopicAttributes")
		if strings.Contains(out.Attributes["Policy"], `"cross"`) {
			t.Errorf("Policy after RemovePermission still has the cross statement")
		}
	})

	t.Run("FIFO topic attributes", func(t *testing.T) {
		fifoArn := createTopic(t, name("fifo")+".fifo", map[string]string{"FifoTopic": "true"})
		out, err := client.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: aws.String(fifoArn)})
		ok(t, err, "GetTopicAttributes")
		if out.Attributes["FifoTopic"] != "true" || out.Attributes["ContentBasedDeduplication"] != "false" {
			t.Errorf("FIFO topic FifoTopic %q, ContentBasedDeduplication %q; want true, false", out.Attributes["FifoTopic"], out.Attributes["ContentBasedDeduplication"])
		}
		_, err = client.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(name("nosuffix")), Attributes: map[string]string{"FifoTopic": "true"}})
		errAs[*types.InvalidParameterException](t, err, "CreateTopic FifoTopic without .fifo")
		wantMessage(t, err, "CreateTopic FifoTopic without .fifo", "Invalid parameter: Topic Name")
	})
}

func TestSNSSubscriptions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sns.NewFromConfig(awsCfg)
	acct := account(t)
	topicArn := createTopic(t, name("subs"), nil)
	queueURL, subArn := subscribeQueue(t, topicArn, name("subs-q"), nil, nil)
	qarn := queueArn(t, queueURL)

	if !strings.HasPrefix(subArn, topicArn+":") {
		t.Errorf("SubscriptionArn = %q, want %s:<id>", subArn, topicArn)
	}

	t.Run("GetSubscriptionAttributes", func(t *testing.T) {
		out, err := client.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{SubscriptionArn: aws.String(subArn)})
		ok(t, err, "GetSubscriptionAttributes")
		for k, v := range map[string]string{
			"ConfirmationWasAuthenticated": "true", "Endpoint": qarn, "Owner": acct, "PendingConfirmation": "false",
			"Protocol": "sqs", "RawMessageDelivery": "false", "SubscriptionArn": subArn, "TopicArn": topicArn,
		} {
			if out.Attributes[k] != v {
				t.Errorf("subscription attribute %s = %q, want %q", k, out.Attributes[k], v)
			}
		}
		for _, k := range []string{"FilterPolicy", "FilterPolicyScope"} {
			if v, present := out.Attributes[k]; present {
				t.Errorf("%s = %q is listed although no filter policy is set", k, v)
			}
		}
	})

	t.Run("Subscribe idempotency and validation", func(t *testing.T) {
		again, err := client.Subscribe(ctx, &sns.SubscribeInput{TopicArn: aws.String(topicArn), Protocol: aws.String("sqs"), Endpoint: aws.String(qarn), ReturnSubscriptionArn: true})
		ok(t, err, "Subscribe again")
		if aws.ToString(again.SubscriptionArn) != subArn {
			t.Errorf("Subscribe of the same endpoint returned %q, want %q", aws.ToString(again.SubscriptionArn), subArn)
		}
		_, err = client.Subscribe(ctx, &sns.SubscribeInput{
			TopicArn: aws.String(topicArn), Protocol: aws.String("sqs"), Endpoint: aws.String(qarn),
			Attributes: map[string]string{"RawMessageDelivery": "true"},
		})
		errAs[*types.InvalidParameterException](t, err, "Subscribe of the same endpoint with different attributes")
		wantMessage(t, err, "Subscribe of the same endpoint with different attributes", "Invalid parameter: Attributes Reason: Subscription already exists with different attributes")
		_, err = client.Subscribe(ctx, &sns.SubscribeInput{TopicArn: aws.String(topicArn), Protocol: aws.String("test-protocol"), Endpoint: aws.String("x")})
		errAs[*types.InvalidParameterException](t, err, "Subscribe with an unknown protocol")
		wantMessage(t, err, "Subscribe with an unknown protocol", "Invalid parameter: Amazon SNS does not support this protocol string: test-protocol")
		_, err = client.Subscribe(ctx, &sns.SubscribeInput{TopicArn: aws.String(topicArn), Protocol: aws.String("sqs"), Endpoint: aws.String(queueURL)})
		errAs[*types.InvalidParameterException](t, err, "Subscribe sqs with a queue URL")
		wantMessage(t, err, "Subscribe sqs with a queue URL", "Invalid parameter: SQS endpoint ARN")
		_, err = client.Subscribe(ctx, &sns.SubscribeInput{TopicArn: aws.String("arn:aws:sns:" + region() + ":" + acct + ":" + name("missing")), Protocol: aws.String("sqs"), Endpoint: aws.String(qarn)})
		errAs[*types.NotFoundException](t, err, "Subscribe to a missing topic")
		wantMessage(t, err, "Subscribe to a missing topic", "Topic does not exist")
		fifoQ := createQueue(t, name("fifo-sub")+".fifo", map[string]string{"FifoQueue": "true"})
		_, err = client.Subscribe(ctx, &sns.SubscribeInput{TopicArn: aws.String(topicArn), Protocol: aws.String("sqs"), Endpoint: aws.String(queueArn(t, fifoQ))})
		errAs[*types.InvalidParameterException](t, err, "Subscribe a FIFO queue to a standard topic")
		wantMessage(t, err, "Subscribe a FIFO queue to a standard topic", "Invalid parameter: Invalid parameter: Endpoint Reason: FIFO SQS Queues can not be subscribed to standard SNS topics")
	})

	t.Run("listing", func(t *testing.T) {
		byTopic, err := client.ListSubscriptionsByTopic(ctx, &sns.ListSubscriptionsByTopicInput{TopicArn: aws.String(topicArn)})
		ok(t, err, "ListSubscriptionsByTopic")
		found := slices.ContainsFunc(byTopic.Subscriptions, func(s types.Subscription) bool {
			return aws.ToString(s.SubscriptionArn) == subArn && aws.ToString(s.Protocol) == "sqs" && aws.ToString(s.Endpoint) == qarn &&
				aws.ToString(s.Owner) == acct && aws.ToString(s.TopicArn) == topicArn
		})
		if !found {
			t.Errorf("ListSubscriptionsByTopic = %+v, want %s", byTopic.Subscriptions, subArn)
		}
		var all []string
		p := sns.NewListSubscriptionsPaginator(client, &sns.ListSubscriptionsInput{})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			ok(t, err, "ListSubscriptions")
			for _, s := range page.Subscriptions {
				all = append(all, aws.ToString(s.SubscriptionArn))
			}
		}
		if !slices.Contains(all, subArn) {
			t.Errorf("ListSubscriptions does not list %s", subArn)
		}
	})

	t.Run("SetSubscriptionAttributes validation", func(t *testing.T) {
		set := func(attr, value string) error {
			_, err := client.SetSubscriptionAttributes(ctx, &sns.SetSubscriptionAttributesInput{SubscriptionArn: aws.String(subArn), AttributeName: aws.String(attr), AttributeValue: aws.String(value)})
			return err
		}
		cases := []struct{ attr, value, msg string }{
			{"RawMessageDelivery", "wrongvalue", "Invalid parameter: RawMessageDelivery: Invalid value [wrongvalue]. Must be true or false."},
			{"FilterPolicyScope", "RandomValue", "Invalid parameter: FilterPolicyScope: Invalid value [RandomValue]. Please use either MessageBody or MessageAttributes"},
			{"Owner", "x", "Invalid parameter: AttributeName"},
			{"FilterPolicy", `{"key":[]}`, "Invalid parameter: FilterPolicy: Empty arrays are not allowed"},
			{"FilterPolicy", `{"a":["1"],"b":["1"],"c":["1"],"d":["1"],"e":["1"],"f":["1"]}`, "Invalid parameter: FilterPolicy: Filter policy can not have more than 5 keys"},
			{"FilterPolicy", `{"key":[{"wrong-operator":true}]}`, "Invalid parameter: FilterPolicy: Unrecognized match type wrong-operator"},
			{"FilterPolicy", `{"key":[{"exists":"no"}]}`, "Invalid parameter: FilterPolicy: exists match pattern must be either true or false."},
			{"FilterPolicy", `{"key":[{"numeric":[">",3,"<",1]}]}`, "Invalid parameter: FilterPolicy: Bottom must be less than top"},
			{"FilterPolicy", `{"object":{"key":[{"prefix":"auto-"}]}}`, "Invalid parameter: Filter policy scope MessageAttributes does not support nested filter policy"},
			{"RedrivePolicy", `{"deadLetterTargetArn":"not-an-arn"}`, "Invalid parameter: RedrivePolicy: deadLetterTargetArn is an invalid arn"},
		}
		for _, c := range cases {
			err := set(c.attr, c.value)
			op := fmt.Sprintf("SetSubscriptionAttributes %s=%s", c.attr, c.value)
			errAs[*types.InvalidParameterException](t, err, op)
			wantStatus(t, err, op, 400)
			wantMessage(t, err, op, c.msg)
		}
		tooMany := make([]string, 151)
		for i := range tooMany {
			tooMany[i] = fmt.Sprint(`"v`, i, `"`)
		}
		err := set("FilterPolicy", `{"key":[`+strings.Join(tooMany, ",")+`]}`)
		errAs[*types.InvalidParameterException](t, err, "SetSubscriptionAttributes FilterPolicy with 151 values")
		wantMessage(t, err, "SetSubscriptionAttributes FilterPolicy with 151 values", "Invalid parameter: FilterPolicy: Filter policy is too complex")
	})

	t.Run("http subscription pending confirmation", func(t *testing.T) {
		out, err := client.Subscribe(ctx, &sns.SubscribeInput{TopicArn: aws.String(topicArn), Protocol: aws.String("http"), Endpoint: aws.String("http://127.0.0.1:9/hook")})
		ok(t, err, "Subscribe http")
		if aws.ToString(out.SubscriptionArn) != "pending confirmation" {
			t.Errorf("Subscribe http SubscriptionArn = %q, want the literal \"pending confirmation\"", aws.ToString(out.SubscriptionArn))
		}
		withArn, err := client.Subscribe(ctx, &sns.SubscribeInput{TopicArn: aws.String(topicArn), Protocol: aws.String("http"), Endpoint: aws.String("http://127.0.0.1:9/hook2"), ReturnSubscriptionArn: true})
		ok(t, err, "Subscribe http ReturnSubscriptionArn")
		pending := aws.ToString(withArn.SubscriptionArn)
		if !strings.HasPrefix(pending, topicArn+":") {
			t.Errorf("Subscribe http with ReturnSubscriptionArn = %q, want the real ARN", pending)
		}
		attrs, err := client.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{SubscriptionArn: aws.String(pending)})
		ok(t, err, "GetSubscriptionAttributes of a pending subscription")
		if attrs.Attributes["PendingConfirmation"] != "true" {
			t.Errorf("PendingConfirmation = %q, want true", attrs.Attributes["PendingConfirmation"])
		}
		_, err = client.Unsubscribe(ctx, &sns.UnsubscribeInput{SubscriptionArn: aws.String(pending)})
		errAs[*types.InvalidParameterException](t, err, "Unsubscribe of a pending subscription")
		wantMessageContains(t, err, "Unsubscribe of a pending subscription", "Cannot unsubscribe a subscription that is pending confirmation")
		_, err = client.ConfirmSubscription(ctx, &sns.ConfirmSubscriptionInput{TopicArn: aws.String(topicArn), Token: aws.String("not-a-token")})
		errAs[*types.InvalidParameterException](t, err, "ConfirmSubscription with a bogus token")
	})

	t.Run("Unsubscribe", func(t *testing.T) {
		_, otherSub := subscribeQueue(t, topicArn, name("unsub-q"), nil, nil)
		_, err := client.Unsubscribe(ctx, &sns.UnsubscribeInput{SubscriptionArn: aws.String(otherSub)})
		ok(t, err, "Unsubscribe")
		_, err = client.Unsubscribe(ctx, &sns.UnsubscribeInput{SubscriptionArn: aws.String(otherSub)})
		ok(t, err, "Unsubscribe again (idempotent)")
		_, err = client.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{SubscriptionArn: aws.String(otherSub)})
		errAs[*types.NotFoundException](t, err, "GetSubscriptionAttributes after Unsubscribe")
		wantStatus(t, err, "GetSubscriptionAttributes after Unsubscribe", 404)
		wantMessage(t, err, "GetSubscriptionAttributes after Unsubscribe", "Subscription does not exist")
		_, err = client.SetSubscriptionAttributes(ctx, &sns.SetSubscriptionAttributesInput{SubscriptionArn: aws.String(otherSub), AttributeName: aws.String("RawMessageDelivery"), AttributeValue: aws.String("true")})
		errAs[*types.NotFoundException](t, err, "SetSubscriptionAttributes after Unsubscribe")
		bad := "arn:aws:sns:x"
		_, err = client.Unsubscribe(ctx, &sns.UnsubscribeInput{SubscriptionArn: aws.String(bad)})
		errAs[*types.InvalidParameterException](t, err, "Unsubscribe of a malformed ARN")
		wantMessage(t, err, "Unsubscribe of a malformed ARN", fmt.Sprintf("Invalid parameter: SubscriptionArn Reason: An ARN must have at least 6 elements, not %d", len(strings.Split(bad, ":"))))
	})
}

func TestSNSPublishToSQS(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sns.NewFromConfig(awsCfg)
	topicArn := createTopic(t, name("publish"), nil)
	envQueue, envSub := subscribeQueue(t, topicArn, name("envelope-q"), nil, nil)
	rawQueue, _ := subscribeQueue(t, topicArn, name("raw-q"), nil, map[string]string{"RawMessageDelivery": "true"})

	pub, err := client.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(topicArn), Message: aws.String("Hello world!"), Subject: aws.String("Testing publish"),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"attr1": {DataType: aws.String("Number"), StringValue: aws.String("99.12")},
			"bin":   {DataType: aws.String("Binary"), BinaryValue: []byte{2, 3, 4}},
		},
	})
	ok(t, err, "Publish")
	if !uuidPattern.MatchString(aws.ToString(pub.MessageId)) || pub.SequenceNumber != nil {
		t.Errorf("Publish MessageId %q, SequenceNumber %v; want a UUID and no sequence number", aws.ToString(pub.MessageId), pub.SequenceNumber)
	}

	t.Run("enveloped delivery", func(t *testing.T) {
		msgs := drain(t, envQueue, 5*time.Second)
		if len(msgs) != 1 {
			t.Fatalf("the enveloped subscription received %d messages, want 1", len(msgs))
		}
		if len(msgs[0].MessageAttributes) != 0 {
			t.Errorf("an enveloped delivery carries SQS message attributes %v; the attributes belong inside the envelope", msgs[0].MessageAttributes)
		}
		env := envelope(t, aws.ToString(msgs[0].Body))
		for k, v := range map[string]string{
			"Type": "Notification", "MessageId": aws.ToString(pub.MessageId), "TopicArn": topicArn,
			"Subject": "Testing publish", "Message": "Hello world!", "SignatureVersion": "1",
		} {
			if env[k] != v {
				t.Errorf("envelope %s = %v, want %q", k, env[k], v)
			}
		}
		if ts, _ := env["Timestamp"].(string); !snsTimestamp.MatchString(ts) {
			t.Errorf("envelope Timestamp = %v, want ISO 8601 UTC with milliseconds", env["Timestamp"])
		}
		for _, k := range []string{"Signature", "SigningCertURL"} {
			if s, _ := env[k].(string); s == "" {
				t.Errorf("envelope %s is missing", k)
			}
		}
		if u, _ := env["UnsubscribeURL"].(string); !strings.Contains(u, "Action=Unsubscribe&SubscriptionArn="+envSub) {
			t.Errorf("envelope UnsubscribeURL = %q, want it to unsubscribe %s", u, envSub)
		}
		attrs, _ := env["MessageAttributes"].(map[string]any)
		want := map[string]map[string]any{"attr1": {"Type": "Number", "Value": "99.12"}, "bin": {"Type": "Binary", "Value": "AgME"}}
		for k, w := range want {
			got, _ := attrs[k].(map[string]any)
			if got["Type"] != w["Type"] || got["Value"] != w["Value"] {
				t.Errorf("envelope MessageAttributes[%s] = %v, want %v", k, got, w)
			}
		}
	})

	t.Run("raw delivery", func(t *testing.T) {
		msgs := drain(t, rawQueue, 5*time.Second)
		if len(msgs) != 1 {
			t.Fatalf("the raw subscription received %d messages, want 1", len(msgs))
		}
		m := msgs[0]
		if aws.ToString(m.Body) != "Hello world!" {
			t.Errorf("raw delivery body = %q, want the message itself", aws.ToString(m.Body))
		}
		a := m.MessageAttributes["attr1"]
		b := m.MessageAttributes["bin"]
		if aws.ToString(a.DataType) != "Number" || aws.ToString(a.StringValue) != "99.12" || aws.ToString(b.DataType) != "Binary" || string(b.BinaryValue) != "\x02\x03\x04" {
			t.Errorf("raw delivery SQS message attributes = %+v, want the published attributes", m.MessageAttributes)
		}
	})

	t.Run("message structure json", func(t *testing.T) {
		_, err := client.Publish(ctx, &sns.PublishInput{
			TopicArn: aws.String(topicArn), MessageStructure: aws.String("json"),
			Message: aws.String(`{"default":"for everyone","sqs":"for sqs"}`),
		})
		ok(t, err, "Publish MessageStructure json")
		msgs := drain(t, rawQueue, 5*time.Second)
		if len(msgs) != 1 || aws.ToString(msgs[0].Body) != "for sqs" {
			t.Errorf("MessageStructure json delivered %v to the sqs subscription, want the sqs entry", bodies(msgs))
		}
		drain(t, envQueue, 2*time.Second)
	})

	t.Run("publish validation", func(t *testing.T) {
		cases := []struct {
			op  string
			in  *sns.PublishInput
			msg string
		}{
			{"Publish of an empty message", &sns.PublishInput{TopicArn: aws.String(topicArn), Message: aws.String("")}, "Invalid parameter: Empty message"},
			{"Publish with an empty subject", &sns.PublishInput{TopicArn: aws.String(topicArn), Message: aws.String("x"), Subject: aws.String("")}, "Invalid parameter: Subject"},
			{"Publish of 262145 bytes", &sns.PublishInput{TopicArn: aws.String(topicArn), Message: aws.String(strings.Repeat("a", 262145))}, "Invalid parameter: Message too long"},
			{"Publish json without default", &sns.PublishInput{TopicArn: aws.String(topicArn), MessageStructure: aws.String("json"), Message: aws.String(`{"sqs":"x"}`)}, "Invalid parameter: Message Structure - No default entry in JSON message body"},
			{"Publish json that does not parse", &sns.PublishInput{TopicArn: aws.String(topicArn), MessageStructure: aws.String("json"), Message: aws.String(`{not json`)}, "Invalid parameter: Message Structure - JSON message body failed to parse"},
			{"Publish with MessageDeduplicationId to a standard topic", &sns.PublishInput{TopicArn: aws.String(topicArn), Message: aws.String("x"), MessageDeduplicationId: aws.String("d")}, "Invalid parameter: MessageDeduplicationId Reason: The request includes MessageDeduplicationId parameter that is not valid for this topic type"},
		}
		for _, c := range cases {
			_, err := client.Publish(ctx, c.in)
			errAs[*types.InvalidParameterException](t, err, c.op)
			wantStatus(t, err, c.op, 400)
			wantMessage(t, err, c.op, c.msg)
		}
		_, err := client.Publish(ctx, &sns.PublishInput{
			TopicArn: aws.String(topicArn), Message: aws.String("x"),
			MessageAttributes: map[string]types.MessageAttributeValue{"attr1": {DataType: aws.String("Integer"), StringValue: aws.String("1")}},
		})
		e := typedErr[*types.InvalidParameterValueException](t, err, "Publish with an attribute type outside String, Number and Binary")
		if e.ErrorCode() != "ParameterValueInvalid" {
			t.Errorf("error code = %q, want ParameterValueInvalid", e.ErrorCode())
		}
		wantMessage(t, err, "Publish with an attribute type outside String, Number and Binary",
			"The message attribute 'attr1' has an invalid message attribute type, the set of supported type prefixes is Binary, Number, and String.")
		bad := "arn:aws:sns:" + region()
		_, err = client.Publish(ctx, &sns.PublishInput{TopicArn: aws.String(bad), Message: aws.String("x")})
		errAs[*types.InvalidParameterException](t, err, "Publish to a malformed ARN")
		wantMessage(t, err, "Publish to a malformed ARN", fmt.Sprintf("Invalid parameter: TopicArn Reason: An ARN must have at least 6 elements, not %d", len(strings.Split(bad, ":"))))
	})
}

func TestSNSFilterPolicies(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sns.NewFromConfig(awsCfg)
	topicArn := createTopic(t, name("filter"), nil)
	attrQueue, attrSub := subscribeQueue(t, topicArn, name("filter-attr"), nil, map[string]string{
		"RawMessageDelivery": "true",
		"FilterPolicy":       `{"store":["example_corp"],"price":[{"numeric":[">",100]}]}`,
	})
	bodyQueue, _ := subscribeQueue(t, topicArn, name("filter-body"), nil, map[string]string{
		"RawMessageDelivery": "true",
		"FilterPolicyScope":  "MessageBody",
		"FilterPolicy":       `{"detail":{"status":[{"prefix":"run"}],"region":[{"anything-but":"eu-west-1"}]}}`,
	})

	got, err := client.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{SubscriptionArn: aws.String(attrSub)})
	ok(t, err, "GetSubscriptionAttributes")
	if got.Attributes["FilterPolicyScope"] != "MessageAttributes" {
		t.Errorf("FilterPolicyScope = %q, want the default MessageAttributes", got.Attributes["FilterPolicyScope"])
	}
	wantJSONEqual(t, "FilterPolicy", got.Attributes["FilterPolicy"], `{"store":["example_corp"],"price":[{"numeric":[">",100]}]}`)

	publish := func(body string, attrs map[string]types.MessageAttributeValue) {
		_, err := client.Publish(ctx, &sns.PublishInput{TopicArn: aws.String(topicArn), Message: aws.String(body), MessageAttributes: attrs})
		ok(t, err, "Publish "+body)
	}
	strAttr := func(v string) types.MessageAttributeValue {
		return types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(v)}
	}
	numAttr := func(v string) types.MessageAttributeValue {
		return types.MessageAttributeValue{DataType: aws.String("Number"), StringValue: aws.String(v)}
	}
	publish("attr-match", map[string]types.MessageAttributeValue{"store": strAttr("example_corp"), "price": numAttr("150")})
	publish("attr-cheap", map[string]types.MessageAttributeValue{"store": strAttr("example_corp"), "price": numAttr("50")})
	publish("attr-other-store", map[string]types.MessageAttributeValue{"store": strAttr("other"), "price": numAttr("150")})
	publish("attr-missing-key", map[string]types.MessageAttributeValue{"store": strAttr("example_corp")})
	publish(`{"detail":{"status":"running","region":"us-east-1"}}`, nil)
	publish(`{"detail":{"status":"running","region":"eu-west-1"}}`, nil)
	publish(`{"detail":{"status":"stopped","region":"us-east-1"}}`, nil)
	publish("not json", nil)

	if b := bodies(drain(t, attrQueue, 4*time.Second)); !slices.Equal(b, []string{"attr-match"}) {
		t.Errorf("the attribute-scope policy delivered %v, want only [attr-match]", b)
	}
	if b := bodies(drain(t, bodyQueue, 4*time.Second)); !slices.Equal(b, []string{`{"detail":{"status":"running","region":"us-east-1"}}`}) {
		t.Errorf("the body-scope policy delivered %v, want only the running us-east-1 event", b)
	}

	t.Run("PublishBatch applies policies per entry", func(t *testing.T) {
		out, err := client.PublishBatch(ctx, &sns.PublishBatchInput{TopicArn: aws.String(topicArn), PublishBatchRequestEntries: []types.PublishBatchRequestEntry{
			{Id: aws.String("match"), Message: aws.String("batch-match"), MessageAttributes: map[string]types.MessageAttributeValue{"store": strAttr("example_corp"), "price": numAttr("101")}},
			{Id: aws.String("miss"), Message: aws.String("batch-miss"), MessageAttributes: map[string]types.MessageAttributeValue{"store": strAttr("example_corp"), "price": numAttr("100")}},
		}})
		ok(t, err, "PublishBatch")
		if len(out.Successful) != 2 || len(out.Failed) != 0 {
			t.Errorf("PublishBatch Successful %d, Failed %d", len(out.Successful), len(out.Failed))
		}
		if b := bodies(drain(t, attrQueue, 4*time.Second)); !slices.Equal(b, []string{"batch-match"}) {
			t.Errorf("the attribute-scope policy delivered %v from the batch, want [batch-match]", b)
		}
		drain(t, bodyQueue, time.Second)
	})

	t.Run("clearing the policy", func(t *testing.T) {
		_, err := client.SetSubscriptionAttributes(ctx, &sns.SetSubscriptionAttributesInput{SubscriptionArn: aws.String(attrSub), AttributeName: aws.String("FilterPolicy"), AttributeValue: aws.String("{}")})
		ok(t, err, "SetSubscriptionAttributes FilterPolicy {}")
		publish("unfiltered", nil)
		if b := bodies(drain(t, attrQueue, 4*time.Second)); !slices.Equal(b, []string{"unfiltered"}) {
			t.Errorf("after clearing the policy the subscription received %v, want [unfiltered]", b)
		}
	})
}

func TestSNSPublishBatch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sns.NewFromConfig(awsCfg)
	topicArn := createTopic(t, name("batch"), nil)
	queueURL, _ := subscribeQueue(t, topicArn, name("batch-q"), nil, map[string]string{"RawMessageDelivery": "true"})

	out, err := client.PublishBatch(ctx, &sns.PublishBatchInput{TopicArn: aws.String(topicArn), PublishBatchRequestEntries: []types.PublishBatchRequestEntry{
		{Id: aws.String("one"), Message: aws.String("first")}, {Id: aws.String("two"), Message: aws.String("second")},
	}})
	ok(t, err, "PublishBatch")
	if len(out.Successful) != 2 || len(out.Failed) != 0 {
		t.Fatalf("PublishBatch Successful %d, Failed %d", len(out.Successful), len(out.Failed))
	}
	for _, s := range out.Successful {
		if !uuidPattern.MatchString(aws.ToString(s.MessageId)) {
			t.Errorf("entry %s MessageId = %q", aws.ToString(s.Id), aws.ToString(s.MessageId))
		}
	}
	if b := bodies(drain(t, queueURL, 4*time.Second)); len(b) != 2 {
		t.Errorf("the subscription received %v, want both entries", b)
	}

	entries := func(n int) []types.PublishBatchRequestEntry {
		var e []types.PublishBatchRequestEntry
		for i := range n {
			e = append(e, types.PublishBatchRequestEntry{Id: aws.String(fmt.Sprint("e", i)), Message: aws.String("x")})
		}
		return e
	}
	_, err = client.PublishBatch(ctx, &sns.PublishBatchInput{TopicArn: aws.String(topicArn), PublishBatchRequestEntries: entries(11)})
	errAs[*types.TooManyEntriesInBatchRequestException](t, err, "PublishBatch of 11 entries")
	wantMessage(t, err, "PublishBatch of 11 entries", "The batch request contains more entries than permissible.")
	_, err = client.PublishBatch(ctx, &sns.PublishBatchInput{TopicArn: aws.String(topicArn), PublishBatchRequestEntries: []types.PublishBatchRequestEntry{
		{Id: aws.String("same"), Message: aws.String("a")}, {Id: aws.String("same"), Message: aws.String("b")},
	}})
	errAs[*types.BatchEntryIdsNotDistinctException](t, err, "PublishBatch with duplicate ids")
	wantMessage(t, err, "PublishBatch with duplicate ids", "Two or more batch entries in the request have the same Id.")
	_, err = client.PublishBatch(ctx, &sns.PublishBatchInput{TopicArn: aws.String(topicArn), PublishBatchRequestEntries: []types.PublishBatchRequestEntry{
		{Id: aws.String("message.id"), Message: aws.String("a")},
	}})
	errAs[*types.InvalidBatchEntryIdException](t, err, "PublishBatch with an invalid id")
	_, err = client.PublishBatch(ctx, &sns.PublishBatchInput{TopicArn: aws.String(topicArn), PublishBatchRequestEntries: []types.PublishBatchRequestEntry{}})
	errAs[*types.EmptyBatchRequestException](t, err, "PublishBatch with no entries")
	half := strings.Repeat("a", 131072)
	_, err = client.PublishBatch(ctx, &sns.PublishBatchInput{TopicArn: aws.String(topicArn), PublishBatchRequestEntries: []types.PublishBatchRequestEntry{
		{Id: aws.String("a"), Message: aws.String(half)}, {Id: aws.String("b"), Message: aws.String(half + "a")},
	}})
	errAs[*types.BatchRequestTooLongException](t, err, "PublishBatch over 262144 bytes in total")
	wantMessage(t, err, "PublishBatch over 262144 bytes in total", "The length of all the messages put together is more than the limit.")
}

func TestSNSFifo(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sns.NewFromConfig(awsCfg)
	topicArn := createTopic(t, name("fifo")+".fifo", map[string]string{"FifoTopic": "true"})
	queueURL, _ := subscribeQueue(t, topicArn, name("fifo-q")+".fifo", map[string]string{"FifoQueue": "true"}, nil)

	_, err := client.Publish(ctx, &sns.PublishInput{TopicArn: aws.String(topicArn), Message: aws.String("x"), MessageDeduplicationId: aws.String("d")})
	errAs[*types.InvalidParameterException](t, err, "FIFO Publish without MessageGroupId")
	wantMessage(t, err, "FIFO Publish without MessageGroupId", "Invalid parameter: The MessageGroupId parameter is required for FIFO topics")
	_, err = client.Publish(ctx, &sns.PublishInput{TopicArn: aws.String(topicArn), Message: aws.String("x"), MessageGroupId: aws.String("g")})
	errAs[*types.InvalidParameterException](t, err, "FIFO Publish without a deduplication id")
	wantMessage(t, err, "FIFO Publish without a deduplication id", "Invalid parameter: The topic should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly")

	first, err := client.Publish(ctx, &sns.PublishInput{TopicArn: aws.String(topicArn), Message: aws.String("one"), MessageGroupId: aws.String("g1"), MessageDeduplicationId: aws.String("d1")})
	ok(t, err, "FIFO Publish")
	if aws.ToString(first.SequenceNumber) == "" {
		t.Error("FIFO Publish returned no SequenceNumber")
	}
	_, err = client.Publish(ctx, &sns.PublishInput{TopicArn: aws.String(topicArn), Message: aws.String("one again"), MessageGroupId: aws.String("g1"), MessageDeduplicationId: aws.String("d1")})
	ok(t, err, "FIFO Publish of a duplicate")
	_, err = client.Publish(ctx, &sns.PublishInput{TopicArn: aws.String(topicArn), Message: aws.String("two"), MessageGroupId: aws.String("g1"), MessageDeduplicationId: aws.String("d2")})
	ok(t, err, "FIFO Publish")

	msgs := drain(t, queueURL, 4*time.Second)
	if len(msgs) != 2 {
		t.Fatalf("the FIFO queue received %d messages, want 2 (the duplicate dropped)", len(msgs))
	}
	env := envelope(t, aws.ToString(msgs[0].Body))
	if env["Message"] != "one" || env["Type"] != "Notification" || env["SequenceNumber"] != aws.ToString(first.SequenceNumber) || env["TopicArn"] != topicArn {
		t.Errorf("first FIFO envelope = %v", env)
	}
	for _, k := range []string{"Signature", "SigningCertURL", "SignatureVersion"} {
		if _, present := env[k]; present {
			t.Errorf("a FIFO topic envelope carries %s", k)
		}
	}
	if m := msgs[0].Attributes; m["MessageGroupId"] != "g1" || m["MessageDeduplicationId"] != "d1" {
		t.Errorf("FIFO delivery MessageGroupId %q, MessageDeduplicationId %q; want g1, d1", m["MessageGroupId"], m["MessageDeduplicationId"])
	}
	if second := envelope(t, aws.ToString(msgs[1].Body)); second["Message"] != "two" {
		t.Errorf("second FIFO delivery = %v, want two (group order)", second["Message"])
	}
}

func TestSNSSubscriptionRedrive(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sns.NewFromConfig(awsCfg)
	topicArn := createTopic(t, name("redrive"), nil)
	dlqURL := createQueue(t, name("redrive-dlq"), nil)
	targetURL, subArn := subscribeQueue(t, topicArn, name("redrive-target"), nil, map[string]string{"RawMessageDelivery": "true"})
	_, err := client.SetSubscriptionAttributes(ctx, &sns.SetSubscriptionAttributesInput{
		SubscriptionArn: aws.String(subArn), AttributeName: aws.String("RedrivePolicy"),
		AttributeValue: aws.String(`{"deadLetterTargetArn":"` + queueArn(t, dlqURL) + `"}`),
	})
	ok(t, err, "SetSubscriptionAttributes RedrivePolicy")
	// Deleting the subscribed queue makes every delivery a client error,
	// which SNS sends to the subscription's dead-letter queue.
	_, err = sqs.NewFromConfig(awsCfg).DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(targetURL)})
	ok(t, err, "DeleteQueue of the subscribed queue")
	_, err = client.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(topicArn), Message: aws.String("undeliverable"),
		MessageAttributes: map[string]types.MessageAttributeValue{"k": {DataType: aws.String("String"), StringValue: aws.String("v")}},
	})
	ok(t, err, "Publish")
	msgs := drain(t, dlqURL, 10*time.Second)
	if len(msgs) != 1 || aws.ToString(msgs[0].Body) != "undeliverable" || aws.ToString(msgs[0].MessageAttributes["k"].StringValue) != "v" {
		t.Errorf("the subscription DLQ received %v, want the raw message with its attributes", bodies(msgs))
	}
}

func TestSNSToLambda(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	resultURL := createQueue(t, name("sns-lambda-result"), nil)
	fn := deployFunction(t, name("sns-fn"), "handler.forward", map[string]string{"FORWARD_QUEUE_URL": resultURL})
	topicArn := createTopic(t, name("sns-lambda"), nil)
	_, err := lambda.NewFromConfig(awsCfg).AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName: fn.FunctionName, StatementId: aws.String("sns"), Action: aws.String("lambda:InvokeFunction"),
		Principal: aws.String("sns.amazonaws.com"), SourceArn: aws.String(topicArn),
	})
	ok(t, err, "AddPermission for sns.amazonaws.com")
	sub, err := sns.NewFromConfig(awsCfg).Subscribe(ctx, &sns.SubscribeInput{TopicArn: aws.String(topicArn), Protocol: aws.String("lambda"), Endpoint: fn.FunctionArn, ReturnSubscriptionArn: true})
	ok(t, err, "Subscribe lambda")
	pub, err := sns.NewFromConfig(awsCfg).Publish(ctx, &sns.PublishInput{TopicArn: aws.String(topicArn), Message: aws.String("Hello from SNS!")})
	ok(t, err, "Publish")

	msgs := drain(t, resultURL, 60*time.Second)
	if len(msgs) != 1 {
		t.Fatalf("the function forwarded %d events, want the one SNS event", len(msgs))
	}
	var event struct {
		Records []struct {
			EventSource          string
			EventVersion         string
			EventSubscriptionArn string
			Sns                  map[string]any
		}
	}
	if err := json.Unmarshal([]byte(aws.ToString(msgs[0].Body)), &event); err != nil || len(event.Records) != 1 {
		t.Fatalf("the function received %s, want an SNS event with one record", aws.ToString(msgs[0].Body))
	}
	r := event.Records[0]
	if r.EventSource != "aws:sns" || r.EventVersion != "1.0" || r.EventSubscriptionArn != aws.ToString(sub.SubscriptionArn) {
		t.Errorf("record EventSource %q, EventVersion %q, EventSubscriptionArn %q", r.EventSource, r.EventVersion, r.EventSubscriptionArn)
	}
	s := r.Sns
	if s["Type"] != "Notification" || s["MessageId"] != aws.ToString(pub.MessageId) || s["TopicArn"] != topicArn || s["Message"] != "Hello from SNS!" || s["SignatureVersion"] != "1" {
		t.Errorf("record Sns = %v", s)
	}
	if v, present := s["Subject"]; !present || v != nil {
		t.Errorf("record Sns.Subject = %v (present %t), want JSON null when no subject was published", v, present)
	}
	if attrs, isMap := s["MessageAttributes"].(map[string]any); !isMap || len(attrs) != 0 {
		t.Errorf("record Sns.MessageAttributes = %v, want {}", s["MessageAttributes"])
	}
	for _, k := range []string{"SigningCertUrl", "UnsubscribeUrl", "Signature", "Timestamp"} {
		if v, _ := s[k].(string); v == "" {
			t.Errorf("record Sns.%s is missing (Lambda events spell the URL keys SigningCertUrl and UnsubscribeUrl)", k)
		}
	}
}
