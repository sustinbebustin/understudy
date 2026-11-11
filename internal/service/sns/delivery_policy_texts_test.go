package sns_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// TestDeliveryPolicyRefusalsAsRecorded replays each DeliveryPolicy refusal
// recorded against AWS (testdata/parity/sns/SNSH/E-*.json, SNSH-18, and
// testdata/parity/sns/R16/x-delivery-policy-unknown-key.json) at the level
// it was recorded: SetSubscriptionAttributes on an https subscription with
// raw delivery off, or SetTopicAttributes. Every refusal is 400
// InvalidParameter with the recorded message. The policies are the
// recorded AttributeValues without the trailing newline the CLI's file
// input added. Malformed JSON keeps understudy's own text, a documented
// deviation (docs/guide/differences.md).
func TestDeliveryPolicyRefusalsAsRecorded(t *testing.T) {
	const prefix = "Invalid parameter: DeliveryPolicy: "
	type level uint8
	const (
		subscription level = iota
		topic
	)
	rows := []struct {
		capture string
		level   level
		policy  string
		message string
	}{
		{
			"SNSH/E-backoff-cubic", subscription, `{"healthyRetryPolicy":{"backoffFunction":"cubic"}}`,
			prefix + "Invalid JSON member value [healthyRetryPolicy.backoffFunction]: the value [cubic] is not a valid backoff function",
		},
		{
			"SNSH/E-backoff-upper", subscription, `{"healthyRetryPolicy":{"backoffFunction":"LINEAR"}}`,
			prefix + "healthyRetryPolicy.numRetries must be specified",
		},
		{
			"SNSH/E-csv-not-raw", subscription, `{"requestPolicy":{"headerContentType":"text/csv"}}`,
			prefix + "requestPolicy: If RawMessageDelivery is disabled the Content-Type can only be set to application/json or text/plain.",
		},
		{
			"SNSH/E-malformed (deviation: understudy's own text)", subscription, `{not json`,
			prefix + "failed to parse JSON.",
		},
		{
			"SNSH/E-max-3601", subscription, `{"healthyRetryPolicy":{"minDelayTarget":1,"maxDelayTarget":3601}}`,
			prefix + "healthyRetryPolicy.numRetries must be specified",
		},
		{
			"SNSH/E-min-0", subscription, `{"healthyRetryPolicy":{"minDelayTarget":0}}`,
			prefix + "healthyRetryPolicy.numRetries must be specified",
		},
		{
			"SNSH/E-min-over-max", subscription, `{"healthyRetryPolicy":{"minDelayTarget":30,"maxDelayTarget":20}}`,
			prefix + "healthyRetryPolicy.numRetries must be specified",
		},
		{
			"SNSH/E-partial", subscription, `{"healthyRetryPolicy":{"numRetries":5}}`,
			prefix + "healthyRetryPolicy.minDelayTarget must be specified",
		},
		{
			"SNSH/E-phases-over", subscription, `{"healthyRetryPolicy":{"numRetries":3,"numNoDelayRetries":2,"numMinDelayRetries":2}}`,
			prefix + "healthyRetryPolicy.minDelayTarget must be specified",
		},
		{
			"SNSH/E-retries-101", subscription, `{"healthyRetryPolicy":{"numRetries":101}}`,
			prefix + "healthyRetryPolicy.minDelayTarget must be specified",
		},
		{
			"SNSH/E-retries-minus-1", subscription, `{"healthyRetryPolicy":{"numRetries":-1}}`,
			prefix + "healthyRetryPolicy.minDelayTarget must be specified",
		},
		{
			"SNSH/E-retries-string", subscription, `{"healthyRetryPolicy":{"numRetries":"3"}}`,
			prefix + "healthyRetryPolicy.numRetries type mismatch, expecting integer, received a string",
		},
		{
			"SNSH/E-rps-0", subscription, `{"throttlePolicy":{"maxReceivesPerSecond":0}}`,
			prefix + "throttlePolicy.maxReceivesPerSecond must be >= 1",
		},
		{
			"SNSH/E-sickly-guaranteed", subscription, `{"sicklyRetryPolicy":{"numRetries":1},"guaranteed":true}`,
			prefix + "sicklyRetryPolicy.minDelayTarget must be specified",
		},
		{
			"SNSH/E-total-over-3600", subscription, `{"healthyRetryPolicy":{"numRetries":100,"numMaxDelayRetries":100,"minDelayTarget":3600,"maxDelayTarget":3600}}`,
			prefix + "healthyRetryPolicy: Total policy retry time cannot be greater than 3600 seconds",
		},
		{
			"SNSH/E-unknown-member", subscription, `{"nope":1}`,
			prefix + "Unexpected JSON member: nope",
		},
		{
			"SNSH/E-xml-not-raw", subscription, `{"requestPolicy":{"headerContentType":"application/xml"}}`,
			prefix + "requestPolicy: If RawMessageDelivery is disabled the Content-Type can only be set to application/json or text/plain.",
		},
		{
			"SNSH/E-topic-csv", topic, `{"http":{"defaultRequestPolicy":{"headerContentType":"text/csv"}}}`,
			prefix + "http.defaultRequestPolicy: Content-Type set at the Topic DeliveryPolicy level can only be application/json or text/plain.",
		},
		{
			"SNSH/E-topic-override", topic, `{"http":{"defaultHealthyRetryPolicy":{"numRetries":7},"disableSubscriptionOverrides":true}}`,
			prefix + "http.defaultHealthyRetryPolicy.minDelayTarget must be specified",
		},
		{
			"SNSH/E-topic-total-over-3600", topic, `{"http":{"defaultHealthyRetryPolicy":{"numRetries":100,"numMaxDelayRetries":100,"minDelayTarget":3600,"maxDelayTarget":3600}}}`,
			prefix + "http.defaultHealthyRetryPolicy: Total policy retry time cannot be greater than 3600 seconds",
		},
		{
			"R16/x-delivery-policy-unknown-key", topic, `{"http":{"nope":1}}`,
			prefix + "Unexpected JSON member: http.nope",
		},
	}
	run(t, func(h *harness) {
		topicARN := h.topic("snsh-e", nil)
		subscriptionARN := h.subscribe(topicARN, "https", "https://127.0.0.1:9000/auto", nil)
		for _, row := range rows {
			var err *awserr.Error
			switch row.level {
			case subscription:
				_, err = call(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{
					SubscriptionArn: subscriptionARN, AttributeName: "DeliveryPolicy", AttributeValue: new(row.policy),
				})
			case topic:
				_, err = call(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{
					TopicArn: topicARN, AttributeName: "DeliveryPolicy", AttributeValue: new(row.policy),
				})
			}
			switch {
			case err == nil:
				h.Errorf("%s: %s accepted, want 400 InvalidParameter %q", row.capture, row.policy, row.message)
			case err.ID.Status != 400 || err.ID.QueryCode != "InvalidParameter" || err.Message != row.message:
				h.Errorf("%s: error = %d %s %q\nwant 400 InvalidParameter %q", row.capture, err.ID.Status, err.ID.QueryCode, err.Message, row.message)
			}
		}
	})
}

// TestDeliveryPolicyJSONCharsetIsStoredVerbatim replays SNSH/E-json-charset
// and the read-back in SNSH/E-attrs-partial (SNSH-18): with raw delivery
// off, a requestPolicy content type of application/json with a charset
// parameter is accepted and reads back as sent.
func TestDeliveryPolicyJSONCharsetIsStoredVerbatim(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("snsh-e", nil)
		arn := h.subscribe(topic, "https", "https://127.0.0.1:9000/auto", nil)
		must(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{
			SubscriptionArn: arn, AttributeName: "DeliveryPolicy", AttributeValue: new(`{"requestPolicy":{"headerContentType":"application/json; charset=UTF-8"}}`),
		})
		const want = `{"healthyRetryPolicy":null,"sicklyRetryPolicy":null,"throttlePolicy":null,"requestPolicy":{"headerContentType":"application/json; charset=UTF-8"},"guaranteed":false}`
		if got := h.subscriptionAttributes(arn)["DeliveryPolicy"]; got != want {
			h.Errorf("DeliveryPolicy = %s\nwant %s", got, want)
		}
	})
}
