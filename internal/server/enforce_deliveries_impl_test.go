package server_test

import (
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/server"
)

// Deliveries under iam.enforce (iam-enforcement-surfaces.md 8, 12.4): each
// delivering service's call carries the aws:SourceAccount it sends, so a
// queue policy that conditions on it admits the delivery; a refused
// delivery is not retried and ends as its switch says; and the setup calls
// (Subscribe, PutTargets) neither check nor change the target's policy.

// setQueuePolicy sets the Policy attribute of the default account's queue.
func setQueuePolicy(t *testing.T, endpoint, queue, policy string) {
	t.Helper()
	attrs, err := json.Marshal(map[string]any{"QueueUrl": endpoint + "/000000000000/" + queue, "Attributes": map[string]string{"Policy": policy}})
	if err != nil {
		t.Fatal(err)
	}
	mustSQS(t, endpoint, "SetQueueAttributes", string(attrs))
}

// queueBodies settles the server and returns the bodies of the messages
// the default account's queue holds.
func queueBodies(t *testing.T, c *admin.Client, endpoint, queue string) []string {
	t.Helper()
	if _, err := c.Settle(t.Context(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	got := mustSQS(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+endpoint+`/000000000000/`+queue+`","MaxNumberOfMessages":10}`)
	var out struct{ Messages []struct{ Body string } }
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("ReceiveMessage = %s: %v", got, err)
	}
	bodies := make([]string, 0, len(out.Messages))
	for _, m := range out.Messages {
		bodies = append(bodies, m.Body)
	}
	return bodies
}

// outcomesTo are the outcome kinds of the journaled deliveries to target,
// in journal order.
func outcomesTo(t *testing.T, c *admin.Client, target string) []string {
	t.Helper()
	page, err := c.JournalEvents(t.Context(), admin.JournalQuery{Kind: "delivery", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := range page.Events {
		var d struct {
			To      string
			Outcome struct{ Kind string }
		}
		if json.Unmarshal(page.Events[i].Detail, &d) == nil && d.To == target {
			out = append(out, d.Outcome.Kind)
		}
	}
	return out
}

// accountPolicy admits service to send to queue only for a delivery whose
// aws:SourceAccount is account.
func accountPolicy(service, queue, account string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"` + service + `"},"Action":"sqs:SendMessage",` +
		`"Resource":"arn:aws:sqs:us-east-1:000000000000:` + queue + `","Condition":{"StringEquals":{"aws:SourceAccount":"` + account + `"}}}]}`
}

// S3 sends the bucket owner's account with every notification
// (iam-enforcement-surfaces.md 8.1, aws-doc), so a queue policy on that
// account admits it and one on another account refuses it. A refused
// notification has no dead-letter queue and is dropped (switch
// s3.notification.delivery-denied, 8.3).
func TestEnforceDeliversS3NotificationsWithTheBucketOwnersAccount(t *testing.T) {
	for _, c := range []struct {
		name, account string
		delivered     bool
	}{
		{"the owner's account", "000000000000", true},
		{"another account", "111111111111", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			client, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
			const queueARN = "arn:aws:sqs:us-east-1:000000000000:uploads"
			mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"uploads"}`)
			setQueuePolicy(t, endpoint, "uploads", accountPolicy("s3.amazonaws.com", "uploads", c.account))
			if got := s3Call(t, dummy, http.MethodPut, endpoint+"/uploads", ""); got.status != http.StatusOK {
				t.Fatalf("CreateBucket = %d %s", got.status, got.body)
			}
			notification := `<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><QueueConfiguration>` +
				`<Queue>` + queueARN + `</Queue><Event>s3:ObjectCreated:*</Event></QueueConfiguration></NotificationConfiguration>`
			if got := s3Call(t, dummy, http.MethodPut, endpoint+"/uploads?notification", notification); got.status != http.StatusOK {
				t.Fatalf("PutBucketNotificationConfiguration = %d %s", got.status, got.body)
			}
			if got := s3Call(t, dummy, http.MethodPut, endpoint+"/uploads/a.txt", "abc"); got.status != http.StatusOK {
				t.Fatalf("PutObject = %d %s", got.status, got.body)
			}
			bodies := queueBodies(t, client, endpoint, "uploads")
			created := slices.ContainsFunc(bodies, func(b string) bool { return strings.Contains(b, "ObjectCreated:Put") })
			if created != c.delivered {
				t.Errorf("queue holds %q; want the ObjectCreated record delivered = %v", bodies, c.delivered)
			}
			outcomes := outcomesTo(t, client, queueARN)
			if !c.delivered && (len(outcomes) == 0 || slices.ContainsFunc(outcomes, func(k string) bool { return k != "dropped" })) {
				t.Errorf("delivery outcomes = %q, want every notification dropped", outcomes)
			}
			if got := wiredSwitch(t, client, "s3.notification.delivery-denied"); got != "drop" {
				t.Errorf("s3.notification.delivery-denied = %q, want drop", got)
			}
		})
	}
}

// EventBridge sends the rule's account with a delivery to a queue
// (events.delivery.source-account-sqs present, iam-enforcement-surfaces.md
// 8.1), so a queue policy on it admits the delivery.
func TestEnforceDeliversEventBridgeToAQueueWithTheRulesAccount(t *testing.T) {
	c, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"target"}`)
	setQueuePolicy(t, endpoint, "target", accountPolicy("events.amazonaws.com", "target", "000000000000"))
	events := func(target, body string) {
		t.Helper()
		if w := jsonCall(t, dummy, endpoint, "events", "1.1", "AWSEvents."+target, body); w.status != http.StatusOK {
			t.Fatalf("%s = %d %s", target, w.status, w.body)
		}
	}
	events("PutRule", `{"Name":"nightly","EventPattern":"{\"source\":[\"app\"]}"}`)
	events("PutTargets", `{"Rule":"nightly","Targets":[{"Id":"q","Arn":"arn:aws:sqs:us-east-1:000000000000:target"}]}`)
	events("PutEvents", `{"Entries":[{"Source":"app","DetailType":"tick","Detail":"{}"}]}`)
	if bodies := queueBodies(t, c, endpoint, "target"); len(bodies) != 1 {
		t.Errorf("queue holds %q, want the one event", bodies)
	}
	for name, want := range map[string]string{"events.delivery.source-account-sqs": "present", "events.delivery.source-account-lambda": "present"} {
		if got := wiredSwitch(t, c, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// PutTargets adds no statement to an SNS topic's policy
// (events.put-targets.sns-policy unchanged, iam-enforcement-surfaces.md
// 8.4), and the default topic policy admits only calls carrying the
// owner's AWS:SourceOwner, which EventBridge does not send (7.2), so the
// delivery is refused, not retried and dropped (12.4).
func TestEnforceRefusesEventBridgeToATopicWithItsDefaultPolicy(t *testing.T) {
	c, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
	topic := element(t, mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"alerts"}}), "TopicArn")
	policy := func() string {
		t.Helper()
		attrs := mustSNS(t, endpoint, url.Values{"Action": {"GetTopicAttributes"}, "TopicArn": {topic}})
		m := regexp.MustCompile(`<key>Policy</key><value>([^<]*)</value>`).FindStringSubmatch(attrs)
		if m == nil {
			t.Fatalf("GetTopicAttributes = %s, want a Policy", attrs)
		}
		return m[1]
	}
	before := policy()
	events := func(target, body string) {
		t.Helper()
		if w := jsonCall(t, dummy, endpoint, "events", "1.1", "AWSEvents."+target, body); w.status != http.StatusOK {
			t.Fatalf("%s = %d %s", target, w.status, w.body)
		}
	}
	events("PutRule", `{"Name":"nightly","EventPattern":"{\"source\":[\"app\"]}"}`)
	events("PutTargets", `{"Rule":"nightly","Targets":[{"Id":"t","Arn":"`+topic+`"}]}`)
	if after := policy(); after != before {
		t.Errorf("GetTopicAttributes after PutTargets = %s, want it unchanged from %s", after, before)
	}
	events("PutEvents", `{"Entries":[{"Source":"app","DetailType":"tick","Detail":"{}"}]}`)
	if _, err := c.Settle(t.Context(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := outcomesTo(t, c, topic); len(got) != 1 || got[0] != "dropped" {
		t.Errorf("delivery outcomes = %q, want one dropped attempt", got)
	}
	denied := enforcementAdvisories(t, c, "access-denied")
	if !slices.ContainsFunc(denied, func(text string) bool {
		return strings.Contains(text, "events.amazonaws.com") && strings.Contains(text, "sns:Publish")
	}) {
		t.Errorf("access-denied advisories = %q, want one for events.amazonaws.com calling sns:Publish", denied)
	}
	if got := wiredSwitch(t, c, "events.put-targets.sns-policy"); got != "unchanged" {
		t.Errorf("events.put-targets.sns-policy = %q, want unchanged", got)
	}
}

// SNS sends the topic's account with each delivery
// (sns.delivery.source-account present, iam-enforcement-surfaces.md 8.1).
// Subscribe checks no queue policy (sns.subscribe.policy-check none, 8.2):
// a subscription to a queue that refuses SNS is made, and its refused
// delivery goes to the subscription's dead-letter queue, whose own policy
// admits SNS, with no retry (8.2, 12.4).
func TestEnforceDeliversSNSWithTheTopicsAccountAndDeadLettersARefusal(t *testing.T) {
	c, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
	topic := element(t, mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"orders"}}), "TopicArn")
	for _, q := range []string{"admits", "refuses", "letters"} {
		mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"`+q+`"}`)
	}
	setQueuePolicy(t, endpoint, "admits", accountPolicy("sns.amazonaws.com", "admits", "000000000000"))
	setQueuePolicy(t, endpoint, "letters", accountPolicy("sns.amazonaws.com", "letters", "000000000000"))
	subscribe := func(queue string, attrs url.Values) {
		t.Helper()
		form := url.Values{"Action": {"Subscribe"}, "TopicArn": {topic}, "Protocol": {"sqs"}, "Endpoint": {"arn:aws:sqs:us-east-1:000000000000:" + queue}}
		maps.Copy(form, attrs)
		if status, body := snsCall(t, endpoint, form); status != http.StatusOK {
			t.Fatalf("Subscribe %s = %d %s, want it made: Subscribe checks no queue policy", queue, status, body)
		}
	}
	subscribe("admits", nil)
	subscribe("refuses", url.Values{
		"Attributes.entry.1.key":   {"RedrivePolicy"},
		"Attributes.entry.1.value": {`{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:letters"}`},
	})
	mustSNS(t, endpoint, url.Values{"Action": {"Publish"}, "TopicArn": {topic}, "Message": {"hello"}})
	if got := queueBodies(t, c, endpoint, "admits"); len(got) != 1 {
		t.Errorf("admitting queue holds %q, want the one notification", got)
	}
	if got := queueBodies(t, c, endpoint, "refuses"); len(got) != 0 {
		t.Errorf("refusing queue holds %q, want nothing", got)
	}
	if got := queueBodies(t, c, endpoint, "letters"); len(got) != 1 || !strings.Contains(got[0], "hello") {
		t.Errorf("dead-letter queue holds %q, want the refused notification", got)
	}
	if got := outcomesTo(t, c, "arn:aws:sqs:us-east-1:000000000000:refuses"); len(got) != 1 || got[0] != "dead-lettered" {
		t.Errorf("delivery outcomes to the refusing queue = %q, want one dead-lettered attempt", got)
	}
	for name, want := range map[string]string{"sns.delivery.source-account": "present", "sns.subscribe.policy-check": "none"} {
		if got := wiredSwitch(t, c, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// A delivery to a deleted queue fails as the missing queue, not as a
// denial: AWS logs it as the client error
// AWS.SimpleQueueService.NonExistentQueue and the message lands in the
// subscription's dead-letter queue (sns.md 7.1, AWS-recorded), which a
// queue with no policy left could not show if SNS were authorized first
// (iam-enforcement-surfaces.md 8.2, 8.4 keep the missing target apart from
// the missing permission). A deleted dead-letter queue is missing the same
// way, while the refusal that sends to it stays a journaled denial (switch
// sqs.enforce.missing-resource.delivery).
func TestEnforceFailsADeliveryToADeletedQueueAsMissing(t *testing.T) {
	c, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
	topic := element(t, mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"orders"}}), "TopicArn")
	for _, q := range []string{"gone", "letters", "refuses", "lost"} {
		mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"`+q+`"}`)
	}
	setQueuePolicy(t, endpoint, "gone", accountPolicy("sns.amazonaws.com", "gone", "000000000000"))
	setQueuePolicy(t, endpoint, "letters", accountPolicy("sns.amazonaws.com", "letters", "000000000000"))
	setQueuePolicy(t, endpoint, "lost", accountPolicy("sns.amazonaws.com", "lost", "000000000000"))
	for queue, dlq := range map[string]string{"gone": "letters", "refuses": "lost"} {
		mustSNS(t, endpoint, url.Values{
			"Action": {"Subscribe"}, "TopicArn": {topic}, "Protocol": {"sqs"}, "Endpoint": {"arn:aws:sqs:us-east-1:000000000000:" + queue},
			"Attributes.entry.1.key":   {"RedrivePolicy"},
			"Attributes.entry.1.value": {`{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:` + dlq + `"}`},
		})
	}
	for _, q := range []string{"gone", "lost"} {
		mustSQS(t, endpoint, "DeleteQueue", `{"QueueUrl":"`+endpoint+`/000000000000/`+q+`"}`)
	}
	mustSNS(t, endpoint, url.Values{"Action": {"Publish"}, "TopicArn": {topic}, "Message": {"hello"}})
	if got := queueBodies(t, c, endpoint, "letters"); len(got) != 1 || !strings.Contains(got[0], "hello") {
		t.Errorf("dead-letter queue holds %q, want the notification to the deleted queue", got)
	}
	if got := outcomesTo(t, c, "arn:aws:sqs:us-east-1:000000000000:gone"); len(got) != 1 || got[0] != "dead-lettered" {
		t.Errorf("delivery outcomes to the deleted queue = %q, want one dead-lettered attempt", got)
	}
	denied := enforcementAdvisories(t, c, "access-denied")
	for _, missing := range []string{":gone", ":lost"} {
		if i := slices.IndexFunc(denied, func(text string) bool { return strings.Contains(text, missing) }); i >= 0 {
			t.Errorf("access-denied advisory %q names the deleted queue %s, want the delivery to fail as the missing queue", denied[i], missing)
		}
	}
	if !slices.ContainsFunc(denied, func(text string) bool { return strings.Contains(text, ":refuses") }) {
		t.Errorf("access-denied advisories = %q, want the refusing queue's denial still journaled", denied)
	}
	if got := wiredSwitch(t, c, "sqs.enforce.missing-resource.delivery"); got != "resolve-first" {
		t.Errorf("sqs.enforce.missing-resource.delivery = %q, want resolve-first", got)
	}
}

// An EventBridge delivery to a deleted function is denied: its dead letter
// carries ERROR_CODE NO_PERMISSIONS, as Lambda refuses events.amazonaws.com
// before it finds the function (switch
// lambda.enforce.missing-resource.delivery deny-first, aws-recorded,
// testdata/parity/sqs/IAM-ENF/22-dlq-gone.json). One to a deleted topic
// fails as the missing target, NO_RESOURCE, since a topic with no policy
// left could not show that if EventBridge were authorized first
// (iam-enforcement-surfaces.md 8.4, aws-doc; switch
// sns.enforce.missing-resource.delivery, inferred: the recording received
// no dead letter for the topic).
func TestEnforceFailsAnEventBridgeDeliveryToADeletedFunctionOrTopic(t *testing.T) {
	c, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
	const (
		ruleARN = "arn:aws:events:us-east-1:000000000000:rule/nightly"
		fn      = "arn:aws:lambda:us-east-1:000000000000:function:gone"
	)
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"events-dlq"}`)
	dlqPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},"Action":"sqs:SendMessage",` +
		`"Resource":"arn:aws:sqs:us-east-1:000000000000:events-dlq","Condition":{"ArnEquals":{"aws:SourceArn":"` + ruleARN + `"}}}]}`
	setQueuePolicy(t, endpoint, "events-dlq", dlqPolicy)
	topic := element(t, mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"gone"}}), "TopicArn")
	mustSNS(t, endpoint, url.Values{"Action": {"DeleteTopic"}, "TopicArn": {topic}})
	pkg := base64.StdEncoding.EncodeToString(zipped(t, "unused"))
	body := `{"FunctionName":"gone","Runtime":"provided.al2023","Handler":"bootstrap","Role":"arn:aws:iam::000000000000:role/fn","Code":{"ZipFile":"` + pkg + `"}}`
	req := newRequest(t, http.MethodPost, endpoint+"/2015-03-31/functions", body)
	req.Header.Set("Content-Type", "application/json")
	if w := signedCall(t, dummy, "lambda", req, body); w.status != http.StatusCreated {
		t.Fatalf("CreateFunction = %d %s", w.status, w.body)
	}
	if w := signedCall(t, dummy, "lambda", newRequest(t, http.MethodDelete, endpoint+"/2015-03-31/functions/gone", ""), ""); w.status != http.StatusNoContent {
		t.Fatalf("DeleteFunction = %d %s", w.status, w.body)
	}
	events := func(target, body string) wire {
		return jsonCall(t, dummy, endpoint, "events", "1.1", "AWSEvents."+target, body)
	}
	if w := events("PutRule", `{"Name":"nightly","EventPattern":"{\"source\":[\"app\"]}"}`); w.status != http.StatusOK {
		t.Fatalf("PutRule = %d %s", w.status, w.body)
	}
	dlq := `"DeadLetterConfig":{"Arn":"arn:aws:sqs:us-east-1:000000000000:events-dlq"}`
	if w := events("PutTargets", `{"Rule":"nightly","Targets":[{"Id":"fn","Arn":"`+fn+`",`+dlq+`},{"Id":"topic","Arn":"`+topic+`",`+dlq+`}]}`); w.status != http.StatusOK {
		t.Fatalf("PutTargets = %d %s", w.status, w.body)
	}
	if w := events("PutEvents", `{"Entries":[{"Source":"app","DetailType":"tick","Detail":"{}"}]}`); w.status != http.StatusOK {
		t.Fatalf("PutEvents = %d %s", w.status, w.body)
	}
	if _, err := c.Settle(t.Context(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	letters := mustSQS(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+endpoint+`/000000000000/events-dlq","MaxNumberOfMessages":10,"MessageAttributeNames":["All"]}`)
	var out struct {
		Messages []struct {
			MessageAttributes map[string]struct{ StringValue string }
		}
	}
	if err := json.Unmarshal([]byte(letters), &out); err != nil {
		t.Fatalf("ReceiveMessage = %s: %v", letters, err)
	}
	codes := map[string]string{}
	for _, m := range out.Messages {
		codes[m.MessageAttributes["TARGET_ARN"].StringValue] = m.MessageAttributes["ERROR_CODE"].StringValue
	}
	if want := map[string]string{fn: "NO_PERMISSIONS", topic: "NO_RESOURCE"}; !maps.Equal(codes, want) {
		t.Errorf("dead letters by TARGET_ARN = %v, want %v", codes, want)
	}
	denials := enforcementAdvisories(t, c, "access-denied")
	if !slices.ContainsFunc(denials, func(d string) bool { return strings.Contains(d, fn) }) {
		t.Errorf("access-denied advisories = %q, want one for the deleted function", denials)
	}
	if slices.ContainsFunc(denials, func(d string) bool { return strings.Contains(d, topic) }) {
		t.Errorf("access-denied advisories = %q, want none for the deleted topic", denials)
	}
	for name, want := range map[string]string{"lambda.enforce.missing-resource.delivery": "deny-first", "sns.enforce.missing-resource.delivery": "resolve-first"} {
		if got := wiredSwitch(t, c, name); got != want {
			t.Errorf("%s = %q, want %s", name, got, want)
		}
	}
}

// Only a missing target is answered before authorization: an EventBridge
// delivery to a function that exists but is Failed, whose policy does not
// admit EventBridge, is still a denial, dead-lettered with NO_PERMISSIONS
// and journaled (iam-enforcement-surfaces.md 8.4 grounds resolve-first for
// "a target resource that no longer exists" alone). Docker is disabled
// here, so the image function settles Failed.
func TestEnforceDeniesAnEventBridgeDeliveryToAFailedFunction(t *testing.T) {
	c, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
	const (
		ruleARN = "arn:aws:events:us-east-1:000000000000:rule/nightly"
		fn      = "arn:aws:lambda:us-east-1:000000000000:function:broken"
	)
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"events-dlq"}`)
	dlqPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},"Action":"sqs:SendMessage",` +
		`"Resource":"arn:aws:sqs:us-east-1:000000000000:events-dlq","Condition":{"ArnEquals":{"aws:SourceArn":"` + ruleARN + `"}}}]}`
	setQueuePolicy(t, endpoint, "events-dlq", dlqPolicy)
	body := `{"FunctionName":"broken","PackageType":"Image","Role":"arn:aws:iam::000000000000:role/fn","Code":{"ImageUri":"local/fn:latest"}}`
	req := newRequest(t, http.MethodPost, endpoint+"/2015-03-31/functions", body)
	req.Header.Set("Content-Type", "application/json")
	if w := signedCall(t, dummy, "lambda", req, body); w.status != http.StatusCreated {
		t.Fatalf("CreateFunction = %d %s", w.status, w.body)
	}
	if _, err := c.Settle(t.Context(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if w := signedCall(t, dummy, "lambda", newRequest(t, http.MethodGet, endpoint+"/2015-03-31/functions/broken/configuration", ""), ""); !strings.Contains(w.body, `"State":"Failed"`) {
		t.Fatalf("GetFunctionConfiguration = %d %s, want the function Failed", w.status, w.body)
	}
	events := func(target, body string) wire {
		return jsonCall(t, dummy, endpoint, "events", "1.1", "AWSEvents."+target, body)
	}
	if w := events("PutRule", `{"Name":"nightly","EventPattern":"{\"source\":[\"app\"]}"}`); w.status != http.StatusOK {
		t.Fatalf("PutRule = %d %s", w.status, w.body)
	}
	if w := events("PutTargets", `{"Rule":"nightly","Targets":[{"Id":"fn","Arn":"`+fn+`","DeadLetterConfig":{"Arn":"arn:aws:sqs:us-east-1:000000000000:events-dlq"}}]}`); w.status != http.StatusOK {
		t.Fatalf("PutTargets = %d %s", w.status, w.body)
	}
	if w := events("PutEvents", `{"Entries":[{"Source":"app","DetailType":"tick","Detail":"{}"}]}`); w.status != http.StatusOK {
		t.Fatalf("PutEvents = %d %s", w.status, w.body)
	}
	if _, err := c.Settle(t.Context(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	letters := mustSQS(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+endpoint+`/000000000000/events-dlq","MaxNumberOfMessages":10,"MessageAttributeNames":["All"]}`)
	var out struct {
		Messages []struct {
			MessageAttributes map[string]struct{ StringValue string }
		}
	}
	if err := json.Unmarshal([]byte(letters), &out); err != nil {
		t.Fatalf("ReceiveMessage = %s: %v", letters, err)
	}
	if len(out.Messages) != 1 || out.Messages[0].MessageAttributes["ERROR_CODE"].StringValue != "NO_PERMISSIONS" {
		t.Errorf("dead letters = %s, want one with ERROR_CODE NO_PERMISSIONS", letters)
	}
	if denied := enforcementAdvisories(t, c, "access-denied"); !slices.ContainsFunc(denied, func(text string) bool { return strings.Contains(text, ":broken") }) {
		t.Errorf("access-denied advisories = %q, want the Failed function's denial journaled", denied)
	}
}

// An event bus target with a RoleArn is delivered as a session of the
// role, which must trust events.amazonaws.com and allow events:PutEvents
// on the target bus (iam-enforcement-surfaces.md 8.4;
// eb-event-bus-perms.html, eventbridge-buses.md 6.2). A role EventBridge
// cannot assume sends the event to the dead-letter queue with ERROR_CODE
// FAILED_TO_ASSUME_ROLE, and a role session the bus refuses with
// NO_PERMISSIONS, neither retried (eb-rule-dlq.html, 8.4). The dead letter
// is sent as events.amazonaws.com, which the queue policy grants (8.1).
func TestEnforceDeliversABusTargetAsItsRole(t *testing.T) {
	const (
		second = "arn:aws:events:us-east-1:000000000000:event-bus/second"
		role   = "arn:aws:iam::000000000000:role/fwd"
		dlq    = "arn:aws:sqs:us-east-1:000000000000:dlq"
	)
	putEvents := `{"Effect":"Allow","Action":"events:PutEvents","Resource":"` + second + `"}`
	for _, c := range []struct {
		name, trust, policy string
		delivered           bool
		code                string
	}{
		{"a role that does not exist", "", "", false, "FAILED_TO_ASSUME_ROLE"},
		{"a role trusting lambda.amazonaws.com", trustOf("lambda.amazonaws.com"), putEvents, false, "FAILED_TO_ASSUME_ROLE"},
		{"a trusted role without events:PutEvents", trustOf("events.amazonaws.com"), "", false, "NO_PERMISSIONS"},
		{"a trusted role allowed events:PutEvents", trustOf("events.amazonaws.com"), putEvents, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			client, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
			events := func(target, body string) {
				t.Helper()
				if w := jsonCall(t, dummy, endpoint, "events", "1.1", "AWSEvents."+target, body); w.status != http.StatusOK {
					t.Fatalf("%s = %d %s", target, w.status, w.body)
				}
			}
			if c.trust != "" {
				iamAsRoot(t, endpoint, url.Values{"Action": {"CreateRole"}, "RoleName": {"fwd"}, "AssumeRolePolicyDocument": {c.trust}})
			}
			if c.policy != "" {
				iamAsRoot(t, endpoint, url.Values{
					"Action": {"PutRolePolicy"}, "RoleName": {"fwd"}, "PolicyName": {"p"},
					"PolicyDocument": {`{"Version":"2012-10-17","Statement":[` + c.policy + `]}`},
				})
			}
			mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"reached"}`)
			setQueuePolicy(t, endpoint, "reached", accountPolicy("events.amazonaws.com", "reached", "000000000000"))
			mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"dlq"}`)
			setQueuePolicy(t, endpoint, "dlq", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},"Action":"sqs:SendMessage","Resource":"`+dlq+`"}]}`)
			events("CreateEventBus", `{"Name":"second"}`)
			events("PutRule", `{"Name":"in","EventBusName":"second","EventPattern":"{\"source\":[\"app\"]}"}`)
			events("PutTargets", `{"Rule":"in","EventBusName":"second","Targets":[{"Id":"q","Arn":"arn:aws:sqs:us-east-1:000000000000:reached"}]}`)
			events("PutRule", `{"Name":"fwd","EventPattern":"{\"source\":[\"app\"]}"}`)
			events("PutTargets", `{"Rule":"fwd","Targets":[{"Id":"bus","Arn":"`+second+`","RoleArn":"`+role+`","DeadLetterConfig":{"Arn":"`+dlq+`"}}]}`)
			events("PutEvents", `{"Entries":[{"Source":"app","DetailType":"tick","Detail":"{}"}]}`)
			if bodies := queueBodies(t, client, endpoint, "reached"); (len(bodies) == 1) != c.delivered {
				t.Errorf("queue behind the second bus holds %q; want the event forwarded = %v", bodies, c.delivered)
			}
			letters := deadLetters(t, endpoint)
			switch {
			case c.delivered && len(letters) != 0:
				t.Errorf("dead letters %q, want none", letters)
			case !c.delivered && (len(letters) != 1 || !strings.HasPrefix(letters[0], c.code+": ")):
				t.Errorf("dead letters %q, want one with ERROR_CODE %s", letters, c.code)
			}
			if got := outcomesTo(t, client, second); len(got) != 1 {
				t.Errorf("delivery outcomes to the second bus = %q, want one attempt, not retried", got)
			}
		})
	}
}

// Across accounts a bus target's role session needs both its own policy
// and the receiving bus's grant to the sender account
// (eb-cross-account.html, eventbridge-buses.md 6.3; iam-evaluation.md
// 2.4): with the grant the event is delivered, without it the bus refuses
// it with NO_PERMISSIONS.
func TestEnforceDeliversABusTargetInAnotherAccountAsItsRole(t *testing.T) {
	const (
		inbox = "arn:aws:events:us-east-1:111122223333:event-bus/inbox"
		dlq   = "arn:aws:sqs:us-east-1:000000000000:dlq"
	)
	theirs := signer{key: "111122223333", secret: "test"}
	for _, granted := range []bool{true, false} {
		t.Run("granted="+strconv.FormatBool(granted), func(t *testing.T) {
			client, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
			events := func(s signer, target, body string) {
				t.Helper()
				if w := jsonCall(t, s, endpoint, "events", "1.1", "AWSEvents."+target, body); w.status != http.StatusOK {
					t.Fatalf("%s = %d %s", target, w.status, w.body)
				}
			}
			iamAsRoot(t, endpoint, url.Values{"Action": {"CreateRole"}, "RoleName": {"fwd"}, "AssumeRolePolicyDocument": {trustOf("events.amazonaws.com")}})
			iamAsRoot(t, endpoint, url.Values{
				"Action": {"PutRolePolicy"}, "RoleName": {"fwd"}, "PolicyName": {"p"},
				"PolicyDocument": {`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"events:PutEvents","Resource":"` + inbox + `"}]}`},
			})
			mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"dlq"}`)
			setQueuePolicy(t, endpoint, "dlq", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},"Action":"sqs:SendMessage","Resource":"`+dlq+`"}]}`)
			events(theirs, "CreateEventBus", `{"Name":"inbox"}`)
			if granted {
				events(theirs, "PutPermission", `{"EventBusName":"inbox","Action":"events:PutEvents","Principal":"000000000000","StatementId":"sender"}`)
			}
			events(dummy, "PutRule", `{"Name":"fwd","EventPattern":"{\"source\":[\"app\"]}"}`)
			events(dummy, "PutTargets", `{"Rule":"fwd","Targets":[{"Id":"bus","Arn":"`+inbox+`","RoleArn":"arn:aws:iam::000000000000:role/fwd","DeadLetterConfig":{"Arn":"`+dlq+`"}}]}`)
			events(dummy, "PutEvents", `{"Entries":[{"Source":"app","DetailType":"tick","Detail":"{}"}]}`)
			if _, err := client.Settle(t.Context(), 10*time.Second); err != nil {
				t.Fatal(err)
			}
			want := []string{"delivered"}
			if !granted {
				want = []string{"dead-lettered"}
			}
			if got := outcomesTo(t, client, inbox); !slices.Equal(got, want) {
				t.Errorf("delivery outcomes to the other account's bus = %q, want %q", got, want)
			}
			letters := deadLetters(t, endpoint)
			if !granted && (len(letters) != 1 || !strings.HasPrefix(letters[0], "NO_PERMISSIONS: ")) {
				t.Errorf("dead letters %q, want one with ERROR_CODE NO_PERMISSIONS", letters)
			}
		})
	}
}

// Under off the outbox's advisory supplies the aws:SourceAccount the
// delivery sends: S3 sends the bucket owner's, which its bucket ARN does
// not name, so a queue policy on the owner's account draws no advisory
// (iam-enforcement-surfaces.md 8.1, 12.4).
func TestOffAdvisesS3NotificationsWithTheBucketOwnersAccount(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	const queueARN = "arn:aws:sqs:us-east-1:000000000000:uploads"
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"uploads"}`)
	setQueuePolicy(t, endpoint, "uploads", accountPolicy("s3.amazonaws.com", "uploads", "000000000000"))
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/uploads", ""); got.status != http.StatusOK {
		t.Fatalf("CreateBucket = %d %s", got.status, got.body)
	}
	notification := `<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><QueueConfiguration>` +
		`<Queue>` + queueARN + `</Queue><Event>s3:ObjectCreated:*</Event></QueueConfiguration></NotificationConfiguration>`
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/uploads?notification", notification); got.status != http.StatusOK {
		t.Fatalf("PutBucketNotificationConfiguration = %d %s", got.status, got.body)
	}
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/uploads/a.txt", "abc"); got.status != http.StatusOK {
		t.Fatalf("PutObject = %d %s", got.status, got.body)
	}
	if bodies := queueBodies(t, c, endpoint, "uploads"); len(bodies) == 0 {
		t.Fatal("the queue received nothing: the notification was not delivered under off")
	}
	if got := advisories(t, c, queueARN); got != 0 {
		t.Errorf("%d policy-would-deny advisories for a policy on the bucket owner's account, want none", got)
	}
}
