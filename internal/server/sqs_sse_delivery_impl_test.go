package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/server"
)

// SNS and EventBridge deliveries into an SSE-KMS queue. SQS asks KMS for a
// data key on every send (switch sqs.sse-kms.data-key) as the sender, so a
// delivering service is judged against the queue key's policy: AWS
// requires that policy to allow the service principal kms:GenerateDataKey
// and kms:Decrypt, which the aws/sqs managed key's policy cannot, and
// which a customer managed key's default policy does not
// (iam-enforcement-surfaces.md 7.1, 7.5, 8.1, aws-doc). Under iam.enforce
// off nothing is judged and every key delivers. A refused delivery is
// journaled as the service principal's kms:GenerateDataKey denial and
// dead-lettered after one attempt, as a policy refusal is (8.2, 12.4);
// what AWS writes in an EventBridge dead letter's ERROR_CODE for it is
// unrecorded, so the code is not asserted.

const keyedQueueARN = "arn:aws:sqs:us-east-1:000000000000:keyed"

// keyedDelivery is the result of one delivery into the queue keyed: what
// the queue and the delivery's dead-letter queue hold, the journaled
// outcomes of the delivery and the access-denied advisories.
type keyedDelivery struct {
	delivered, letters, outcomes, denied []string
}

// deliverIntoKeyedQueue runs a server under mode, creates the queue keyed
// under the key keyOf returns, admits principal (sns.amazonaws.com or
// events.amazonaws.com) to it and to the dead-letter queue letters by
// queue policy, and has principal's service deliver one message into
// keyed with letters as its dead-letter queue.
func deliverIntoKeyedQueue(t *testing.T, mode server.IAMEnforce, principal string, keyOf keyMaker) keyedDelivery {
	t.Helper()
	c, endpoint, _ := enforced(t, mode)
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"keyed","Attributes":{"KmsMasterKeyId":"`+keyOf(t, endpoint)+`"}}`)
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"letters"}`)
	setQueuePolicy(t, endpoint, "keyed", accountPolicy(principal, "keyed", "000000000000"))
	setQueuePolicy(t, endpoint, "letters", accountPolicy(principal, "letters", "000000000000"))
	const lettersARN = "arn:aws:sqs:us-east-1:000000000000:letters"
	switch principal {
	case "sns.amazonaws.com":
		topic := element(t, mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"orders"}}), "TopicArn")
		mustSNS(t, endpoint, url.Values{
			"Action": {"Subscribe"}, "TopicArn": {topic}, "Protocol": {"sqs"}, "Endpoint": {keyedQueueARN},
			"Attributes.entry.1.key":   {"RedrivePolicy"},
			"Attributes.entry.1.value": {`{"deadLetterTargetArn":"` + lettersARN + `"}`},
		})
		mustSNS(t, endpoint, url.Values{"Action": {"Publish"}, "TopicArn": {topic}, "Message": {"hello"}})
	case "events.amazonaws.com":
		events := func(target, body string) {
			t.Helper()
			if w := jsonCall(t, dummy, endpoint, "events", "1.1", "AWSEvents."+target, body); w.status != http.StatusOK {
				t.Fatalf("%s = %d %s", target, w.status, w.body)
			}
		}
		events("PutRule", `{"Name":"nightly","EventPattern":"{\"source\":[\"app\"]}"}`)
		events("PutTargets", `{"Rule":"nightly","Targets":[{"Id":"q","Arn":"`+keyedQueueARN+`","DeadLetterConfig":{"Arn":"`+lettersARN+`"}}]}`)
		events("PutEvents", `{"Entries":[{"Source":"app","DetailType":"tick","Detail":"{\"hello\":1}"}]}`)
	default:
		t.Fatalf("no delivery set up for %s", principal)
	}
	if _, err := c.Settle(t.Context(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	return keyedDelivery{
		delivered: queueBodies(t, c, endpoint, "keyed"),
		letters:   queueBodies(t, c, endpoint, "letters"),
		outcomes:  outcomesTo(t, c, keyedQueueARN),
		denied:    enforcementAdvisories(t, c, "access-denied"),
	}
}

// keyMaker returns the KmsMasterKeyId a queue is created under.
type keyMaker func(t *testing.T, endpoint string) string

// The keys a queue can be under: the aws/sqs managed key, a customer
// managed key with the default key policy, and one whose policy also
// allows the delivering service principal.
func managedSQSKey(*testing.T, string) string { return "alias/aws/sqs" }

func defaultPolicyKey(t *testing.T, endpoint string) string {
	t.Helper()
	return createKey(t, endpoint)
}

// keyAllowing returns a key maker whose key policy keeps the default
// statement and allows principal kms:GenerateDataKey and kms:Decrypt, the
// statement the SNS and EventBridge guides tell a user to add.
func keyAllowing(principal string) keyMaker {
	return func(t *testing.T, endpoint string) string {
		t.Helper()
		policy := `{"Version":"2012-10-17","Statement":[` +
			`{"Sid":"Enable IAM User Permissions","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"kms:*","Resource":"*"},` +
			`{"Sid":"Deliveries","Effect":"Allow","Principal":{"Service":"` + principal + `"},"Action":["kms:GenerateDataKey","kms:Decrypt"],"Resource":"*"}]}`
		body, err := json.Marshal(map[string]string{"Policy": policy})
		if err != nil {
			t.Fatal(err)
		}
		var created struct{ KeyMetadata struct{ Arn string } }
		if w := jsonCall(t, dummy, endpoint, "kms", "1.1", "TrentService.CreateKey", string(body)); w.status != http.StatusOK || json.Unmarshal([]byte(w.body), &created) != nil {
			t.Fatalf("CreateKey = %d %s", w.status, w.body)
		}
		return created.KeyMetadata.Arn
	}
}

func TestDeliveriesIntoAnSSEKMSQueue(t *testing.T) {
	for _, principal := range []string{"sns.amazonaws.com", "events.amazonaws.com"} {
		for _, c := range []struct {
			name      string
			mode      server.IAMEnforce
			key       keyMaker
			delivered bool
		}{
			{"off, aws/sqs", server.IAMEnforceOff, managedSQSKey, true},
			{"off, customer key", server.IAMEnforceOff, defaultPolicyKey, true},
			{"enforce, aws/sqs", server.IAMEnforceEnforce, managedSQSKey, false},
			{"enforce, customer key with the default policy", server.IAMEnforceEnforce, defaultPolicyKey, false},
			{"enforce, customer key allowing the service", server.IAMEnforceEnforce, keyAllowing(principal), true},
		} {
			t.Run(principal+"/"+c.name, func(t *testing.T) {
				got := deliverIntoKeyedQueue(t, c.mode, principal, c.key)
				if c.delivered {
					if len(got.delivered) != 1 || len(got.letters) != 0 {
						t.Errorf("queue holds %q and dead-letter queue %q; want the one message delivered, no dead letter", got.delivered, got.letters)
					}
					return
				}
				if len(got.delivered) != 0 || len(got.letters) != 1 {
					t.Errorf("queue holds %q and dead-letter queue %q; want nothing delivered and one dead letter", got.delivered, got.letters)
				}
				if len(got.outcomes) != 1 || got.outcomes[0] != "dead-lettered" {
					t.Errorf("delivery outcomes = %q, want one dead-lettered attempt", got.outcomes)
				}
				if !slices.ContainsFunc(got.denied, func(d string) bool {
					return strings.Contains(d, principal) && strings.Contains(d, "kms:GenerateDataKey")
				}) {
					t.Errorf("access-denied advisories = %q, want one for %s calling kms:GenerateDataKey", got.denied, principal)
				}
			})
		}
	}
}
